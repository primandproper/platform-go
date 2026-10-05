package refreshtokens

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v15/authentication/signin"
	"github.com/primandproper/platform-go/v15/authentication/signin/refreshtokens/internal/signindb"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/mysql"
	"github.com/primandproper/primitives-go/v2/database/postgres"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"
	"github.com/primandproper/primitives-go/v2/testutils/containers/mysqltest"
	"github.com/primandproper/primitives-go/v2/testutils/containers/pgtest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// waitForServer polls until the server accepts a trivial statement.
//
// A container's readiness log precedes it actually accepting connections —
// MySQL's entrypoint in particular logs "ready for connections" and then
// restarts — and NewDatabaseClient does not ping on construction. Without this
// the first DDL statement lands on a socket that is still closing and fails with
// an unhelpful "invalid connection".
func waitForServer(tb testing.TB, ctx context.Context, q database.SQLQueryExecutor) {
	tb.Helper()

	var lastErr error
	for range 30 {
		if _, err := q.ExecContext(ctx, "SELECT 1"); err == nil {
			return
		} else { //nolint:revive // the error is only reported if every attempt fails
			lastErr = err
		}

		time.Sleep(time.Second)
	}

	tb.Fatalf("database never accepted a statement: %v", lastErr)
}

// runDialectSuite is what only a real server can decide.
//
// SQLite covers the logic; what it cannot cover is whether the DDL, the numbered
// placeholders, and the engine's own temporal types are accepted by the server
// they were written for — and, above all, whether the exchange really hands one
// token to exactly one caller when the contenders hold separate transactions on
// separate connections rather than one serialized file.
//
// Every subtest keys on a login of its own, because they share one table: what
// is asserted is which rows survive, never how many the table holds.
func runDialectSuite(t *testing.T, client database.Client, d dialect.Dialect) {
	t.Helper()

	ctx := t.Context()

	waitForServer(t, ctx, client.Writer())
	createTable(t, client, d, DefaultTablePrefix)

	c := newFakeClock()

	store, err := NewSQLStore(&Config{}, client, WithClock(c))
	must.NoError(t, err)

	mint := func(tb testing.TB, familyID, subjectID string, ttl time.Duration) *signin.RefreshTokenIssuance {
		tb.Helper()

		issuance, issueErr := issueFor(tb, store, testScope(), &signin.RefreshTokenRequest{
			TTL:             ttl,
			FamilyID:        familyID,
			SubjectID:       subjectID,
			ActiveAccountID: testAccount,
		})
		must.NoError(tb, issueErr)

		return issuance
	}

	t.Run("round-trips a token through the server's own column types", func(t *testing.T) {
		issuance := mint(t, "family_roundtrip", "user_roundtrip", time.Hour)

		spent, redeemErr := redeem(t, store, testScope(), issuance.Secret)
		must.NoError(t, redeemErr)

		test.EqOp(t, "family_roundtrip", spent.FamilyID)
		test.EqOp(t, "user_roundtrip", spent.SubjectID)
		test.EqOp(t, testAccount, spent.ActiveAccountID)
		test.EqOp(t, testScope(), spent.Scope)
		test.True(t, issuance.Token.ExpiresAt.Equal(spent.ExpiresAt),
			test.Sprintf("issued %v, read %v", issuance.Token.ExpiresAt, spent.ExpiresAt))
		must.NotNil(t, spent.RedeemedAt)
	})

	// The listing is the one statement here with a LIMIT, which is the clause
	// the three engines spell least alike, and it compares the store's clock
	// against real temporal columns rather than SQLite's text.
	t.Run("lists a subject's live logins and ends one for its owner", func(t *testing.T) {
		began := c.Now().UTC()

		phone := mint(t, "family_list_phone", "user_list", time.Hour)
		c.advance(time.Minute)
		mint(t, "family_list_laptop", "user_list", time.Hour)
		c.advance(time.Minute)
		rotate(t, store, testScope(), phone.Secret)

		signIns := listSignIns(t, store, testScope(), "user_list", 10)
		test.Eq(t, []string{"family_list_phone", "family_list_laptop"}, familyIDs(signIns))
		must.SliceLen(t, 2, signIns)
		test.True(t, began.Equal(signIns[0].SignedInAt),
			test.Sprintf("began %v, listed %v", began, signIns[0].SignedInAt))

		test.Eq(t, []string{"family_list_phone"}, familyIDs(listSignIns(t, store, testScope(), "user_list", 1)))

		ended, endErr := endSignIns(t, store, testScope(),
			signin.SignInSelector{SubjectID: "user_other", FamilyID: "family_list_phone"})
		must.NoError(t, endErr)
		test.SliceEmpty(t, ended)

		ended, endErr = endSignIns(t, store, testScope(),
			signin.SignInSelector{SubjectID: "user_list", FamilyID: "family_list_phone"})
		must.NoError(t, endErr)
		test.Eq(t, []string{"family_list_phone"}, endedFamilies(ended))
		test.EqOp(t, 2, endedRevoked(ended))

		test.Eq(t, []string{"family_list_laptop"}, familyIDs(listSignIns(t, store, testScope(), "user_list", 10)))
	})

	// The whole reason the exchange is a guarded update. On SQLite every writer
	// is serialized by the file, so the case proves nothing there; here each
	// contender opens its own transaction on its own connection to a real server,
	// and only the affected-row count stops two of them minting successors into
	// one login.
	//
	// The transactions are the callers' rather than the store's, which is what
	// makes this the case worth running: the guarantee has to survive being
	// handed out.
	t.Run("reads a login's live token by its family", func(t *testing.T) {
		runLiveTokenSuite(t, store, c)
	})

	t.Run("hands one token to exactly one of several concurrent consumers", func(t *testing.T) {
		const contenders = 8

		issuance := mint(t, "family_race", "user_race", time.Hour)

		var (
			start   sync.WaitGroup
			done    sync.WaitGroup
			winners atomic.Int64
		)

		start.Add(1)
		done.Add(contenders)

		for range contenders {
			go func() {
				defer done.Done()

				start.Wait()

				if _, redeemErr := redeem(t, store, testScope(), issuance.Secret); redeemErr == nil {
					winners.Add(1)
				}
			}()
		}

		start.Done()
		done.Wait()

		test.EqOp(t, int64(1), winners.Load())
	})

	// Rotation and reuse detection end to end, against a server that decides the
	// guard rather than a file that serializes everybody.
	t.Run("a replayed token ends the login it belonged to", func(t *testing.T) {
		first := mint(t, "family_reuse", "user_reuse", time.Hour)

		_, redeemErr := redeem(t, store, testScope(), first.Secret)
		must.NoError(t, redeemErr)

		successor := mint(t, "family_reuse", "user_reuse", time.Hour)

		_, redeemErr = redeem(t, store, testScope(), first.Secret)
		test.ErrorIs(t, redeemErr, signin.ErrRefreshTokenReused)

		// And the successor whoever holds the other copy is carrying is gone
		// with it, which is the point of revoking a family rather than a token.
		_, redeemErr = redeem(t, store, testScope(), successor.Secret)
		test.ErrorIs(t, redeemErr, signin.ErrInvalidCredentials)
	})

	// The retry path, whose correctness turns on what :execrows counts. MySQL
	// reports rows *changed* where the other two report rows *matched*, and the
	// claim that bounds this to one re-mint per key reads that count as its
	// answer — so "the second presentation loses" is a claim only a real MySQL
	// server can settle.
	t.Run("honors one retry of an exchange, and only one", func(t *testing.T) {
		first := mint(t, "family_retry", "user_retry", time.Hour)

		_, lost, exchangeErr := exchange(t, store, testScope(), first.Secret, "retry_key_01")
		must.NoError(t, exchangeErr)
		must.NotNil(t, lost)

		// The answer the client never received, retried with the key it minted
		// once for this exchange.
		_, replacement, exchangeErr := exchange(t, store, testScope(), first.Secret, "retry_key_01")
		must.NoError(t, exchangeErr)
		must.NotNil(t, replacement)
		test.NotEqOp(t, lost.Secret, replacement.Secret)

		// The superseded successor is revoked, alone — an ordinary refusal
		// rather than a detected theft, and the family is untouched.
		_, redeemErr := redeem(t, store, testScope(), lost.Secret)
		test.ErrorIs(t, redeemErr, signin.ErrInvalidCredentials)

		// A second retry of the same evidence is a reuse, because the claim
		// spent the key.
		_, _, exchangeErr = exchange(t, store, testScope(), first.Secret, "retry_key_01")
		test.ErrorIs(t, exchangeErr, signin.ErrRefreshTokenReused)

		// And that reuse ended the login, replacement included.
		_, redeemErr = redeem(t, store, testScope(), replacement.Secret)
		test.ErrorIs(t, redeemErr, signin.ErrInvalidCredentials)
	})

	// The clock comparison the grace window rests on is made in Go against the
	// stamp the row carries, so what a server run checks is that the stamp
	// survives the round trip through the engine's own temporal type well enough
	// to be compared at all.
	t.Run("refuses a retry once the grace window has passed", func(t *testing.T) {
		first := mint(t, "family_grace", "user_grace", 48*time.Hour)

		_, _, exchangeErr := exchange(t, store, testScope(), first.Secret, "grace_key_01")
		must.NoError(t, exchangeErr)

		c.advance(RemintGrace + time.Minute)

		_, _, exchangeErr = exchange(t, store, testScope(), first.Secret, "grace_key_01")
		test.ErrorIs(t, exchangeErr, signin.ErrRefreshTokenReused)
	})

	// The primary key, which only a real engine enforces the way this schema says
	// it does.
	t.Run("refuses a second row bearing one digest", func(t *testing.T) {
		repeating, storeErr := NewSQLStore(&Config{}, client,
			WithClock(c), WithGenerator(&constantGenerator{secret: "server-side-collision"}))
		must.NoError(t, storeErr)

		_, issueErr := issueFor(t, repeating, testScope(), &signin.RefreshTokenRequest{
			TTL: time.Hour, FamilyID: "family_collision", SubjectID: "user_collision",
		})
		must.NoError(t, issueErr)

		_, issueErr = issueFor(t, repeating, testScope(), &signin.RefreshTokenRequest{
			TTL: time.Hour, FamilyID: "family_collision", SubjectID: "user_collision",
		})
		test.Error(t, issueErr)
	})

	// The scope predicate, against a server that would happily return another
	// tenant's row if the statement let it.
	t.Run("keeps one tenant's token out of another's reach", func(t *testing.T) {
		issuance, issueErr := issueFor(t, store, tenancy.Of("tenant_scoped"), &signin.RefreshTokenRequest{
			TTL: time.Hour, FamilyID: "family_scoped", SubjectID: "user_scoped",
		})
		must.NoError(t, issueErr)

		_, redeemErr := redeem(t, store, tenancy.Of("tenant_other"), issuance.Secret)
		test.ErrorIs(t, redeemErr, signin.ErrInvalidCredentials)

		_, redeemErr = redeem(t, store, tenancy.Of("tenant_scoped"), issuance.Secret)
		test.NoError(t, redeemErr)
	})

	// The deadline guard is a real temporal comparison here and a string
	// comparison on SQLite, so a server run is the only place the former is
	// checked.
	t.Run("refuses a token past its deadline", func(t *testing.T) {
		short := mint(t, "family_expiry", "user_expiry", time.Minute)
		long := mint(t, "family_expiry_live", "user_expiry", 48*time.Hour)

		c.advance(2 * time.Hour)
		defer c.advance(-2 * time.Hour)

		_, redeemErr := redeem(t, store, testScope(), short.Secret)
		test.ErrorIs(t, redeemErr, signin.ErrInvalidCredentials)

		_, redeemErr = redeem(t, store, testScope(), long.Secret)
		test.NoError(t, redeemErr)
	})

	// The sweeper's comparison, on the column that is not the deadline above.
	//
	// The surviving token outlives the retention window rather than merely the
	// horizon, because what is asserted afterwards is that it can still be
	// exchanged: a row that survived the sweep but lapsed on the way would be
	// refused for the other reason and the assertion would read as a sweep bug.
	t.Run("sweeps only what is past its purge deadline", func(t *testing.T) {
		collectable := mint(t, "family_sweep", "user_sweep", time.Minute)
		surviving := mint(t, "family_sweep_live", "user_sweep", 2*DefaultRetention)

		c.advance(time.Hour + DefaultRetention)
		defer c.advance(-(time.Hour + DefaultRetention))

		swept, sweepErr := store.Sweep(ctx)
		must.NoError(t, sweepErr)
		test.True(t, swept >= 1, test.Sprintf("swept %d", swept))

		_, redeemErr := redeem(t, store, testScope(), collectable.Secret)
		test.ErrorIs(t, redeemErr, signin.ErrInvalidCredentials)

		_, redeemErr = redeem(t, store, testScope(), surviving.Secret)
		test.NoError(t, redeemErr)
	})

	// The revocations, which must not reach each other's rows, and which report
	// only what was live when they ran. The locking reads carry FOR UPDATE on
	// both of these engines, which is a clause sqlc accepts after a LIMIT and the
	// server has to as well.
	t.Run("revokes one login, and one person's every login, reporting what ended", func(t *testing.T) {
		mine := mint(t, "family_revoke_a", "user_revoke", time.Hour)
		alsoMine := mint(t, "family_revoke_b", "user_revoke", time.Hour)
		operated := mint(t, "family_revoke_d", "user_revoke", time.Hour)
		theirs := mint(t, "family_revoke_c", "user_neighbor", time.Hour)

		revoked, revokeErr := revokeFamily(t, store, testScope(), "family_revoke_a")
		must.NoError(t, revokeErr)
		test.EqOp(t, int64(1), revoked)

		_, redeemErr := redeem(t, store, testScope(), mine.Secret)
		test.ErrorIs(t, redeemErr, signin.ErrInvalidCredentials)

		ended, endErr := endSignIns(t, store, testScope(), signin.SignInSelector{FamilyID: "family_revoke_d"})
		must.NoError(t, endErr)
		must.SliceLen(t, 1, ended)
		test.EqOp(t, "user_revoke", ended[0].SubjectID)

		_, redeemErr = redeem(t, store, testScope(), operated.Secret)
		test.ErrorIs(t, redeemErr, signin.ErrInvalidCredentials)

		ended, endErr = endSignIns(t, store, testScope(), signin.SignInSelector{SubjectID: "user_revoke"})
		must.NoError(t, endErr)
		test.Eq(t, []string{"family_revoke_b"}, endedFamilies(ended), test.Sprintf("the already-ended logins are spared"))
		test.EqOp(t, int64(1), endedRevoked(ended))

		_, redeemErr = redeem(t, store, testScope(), alsoMine.Secret)
		test.ErrorIs(t, redeemErr, signin.ErrInvalidCredentials)

		_, redeemErr = redeem(t, store, testScope(), theirs.Secret)
		test.NoError(t, redeemErr)
	})

	// A reuse reports the family it ended, and whether it was live to end.
	t.Run("a reuse names the family it ended", func(t *testing.T) {
		first := mint(t, "family_reuse", "user_reuse", time.Hour)
		rotate(t, store, testScope(), first.Secret)

		_, redeemErr := redeem(t, store, testScope(), first.Secret)

		var reused *signin.RefreshTokenReusedError
		must.True(t, platformerrors.As(redeemErr, &reused))
		test.EqOp(t, "family_reuse", reused.FamilyID)
		test.EqOp(t, "user_reuse", reused.SubjectID)
		test.True(t, reused.Ended)

		_, redeemErr = redeem(t, store, testScope(), first.Secret)
		must.True(t, platformerrors.As(redeemErr, &reused))
		test.False(t, reused.Ended, test.Sprintf("a second replay ends nothing"))
	})

	// A revocation holds the rows it withdraws and nothing beside them. On MySQL
	// one that locked the subject or family index by range held the gap a new
	// login's row is inserted into, and a sign-in that recorded before it
	// minted — holding an audit chain's head the revocation then waited on —
	// deadlocked with it. Here the revocation stays open while somebody else
	// signs in with a family that sorts straight after the one being ended.
	t.Run("a held revocation blocks no sign-in", func(t *testing.T) {
		mint(t, "family_held_a", "user_held", time.Hour)

		var (
			held    = make(chan error, 1)
			release = make(chan struct{})
			done    = make(chan error, 1)
		)

		go func() {
			done <- store.db.WithTransaction(t.Context(), func(tx database.Tx) error {
				_, endErr := store.EndSignIns(t.Context(), tx, testScope(), signin.SignInSelector{SubjectID: "user_held"})
				held <- endErr
				if endErr != nil {
					return endErr
				}

				<-release

				return nil
			})
		}()

		must.NoError(t, <-held)

		// Well inside InnoDB's lock wait timeout, so a blocked insert reads as
		// a failure rather than as a slow pass.
		mintCtx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()

		mintErr := store.db.WithTransaction(mintCtx, func(tx database.Tx) error {
			_, issueErr := store.Issue(mintCtx, tx, testScope(), &signin.RefreshTokenRequest{
				TTL: time.Hour, FamilyID: "family_held_b", SubjectID: "user_held_neighbor", ActiveAccountID: testAccount,
			})

			return issueErr
		})

		close(release)
		must.NoError(t, <-done)
		must.NoError(t, mintErr)
	})

	// The revocation reads a family's rows from a snapshot before it locks them,
	// and a snapshot cannot see a successor an exchange commits after it. That
	// exchange moves the row it spent, so the revocation sees the move once it
	// holds the rows and reads the family again: the successor ends with the
	// login, and the login is reported as ended.
	t.Run("a revocation that races an exchange still ends the successor", func(t *testing.T) {
		first := mint(t, "family_race", "user_race", time.Hour)

		var successor *signin.RefreshTokenIssuance

		racing, storeErr := NewSQLStore(&Config{}, client, WithClock(c))
		must.NoError(t, storeErr)

		racing.q = &interleavedQuerier{
			Querier: racing.q,
			afterFamilyRead: func() {
				successor = rotate(t, store, testScope(), first.Secret)
			},
		}

		ended, endErr := endSignIns(t, racing, testScope(), signin.SignInSelector{SubjectID: "user_race"})
		must.NoError(t, endErr)
		must.NotNil(t, successor)

		test.Eq(t, []string{"family_race"}, endedFamilies(ended))

		_, redeemErr := redeem(t, store, testScope(), successor.Secret)
		test.ErrorIs(t, redeemErr, signin.ErrInvalidCredentials)
	})

	// A prefix is not decoration: it renders a second table, and both the DDL and
	// every statement have to agree about which one they mean.
	t.Run("serves a namespaced table alongside the plain one", func(t *testing.T) {
		createTable(t, client, d, "app")

		namespaced, storeErr := NewSQLStore(&Config{TablePrefix: "app"}, client, WithClock(c))
		must.NoError(t, storeErr)

		issuance, issueErr := issueFor(t, namespaced, testScope(), &signin.RefreshTokenRequest{
			TTL: time.Hour, FamilyID: "family_namespaced", SubjectID: "user_namespaced",
		})
		must.NoError(t, issueErr)

		// The plain store cannot see it, which is what a namespace is for.
		_, redeemErr := redeem(t, store, testScope(), issuance.Secret)
		test.ErrorIs(t, redeemErr, signin.ErrInvalidCredentials)

		_, redeemErr = redeem(t, namespaced, testScope(), issuance.Secret)
		test.NoError(t, redeemErr)
	})
}

func TestRefreshTokens_Postgres(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx, &testClientConfig{connectionString: pg.ConnectionString, maxOpenConns: 8})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runDialectSuite(T, client, dialect.Postgres)
	})
}

func TestRefreshTokens_MySQL(T *testing.T) {
	T.Parallel()

	mysqltest.Run(T, func(ctx context.Context, my *mysqltest.Instance) {
		client, err := mysql.NewDatabaseClient(ctx, &testClientConfig{connectionString: my.ConnectionString, maxOpenConns: 8})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runDialectSuite(T, client, dialect.MySQL)
	}, mysqltest.WithCredentials("refreshtest", "refreshtest", "refreshtest"))
}

// interleavedQuerier runs afterFamilyRead once, on another connection, between
// a revocation's unlocked read of a family's rows and its locked one: the
// window a racing exchange lands in.
type interleavedQuerier struct {
	signindb.Querier

	afterFamilyRead func()
	once            sync.Once
}

func (q *interleavedQuerier) ReadRefreshTokenFamilyRows(
	ctx context.Context,
	db signindb.DBTX,
	arg signindb.ReadRefreshTokenFamilyRowsParams,
) ([]signindb.ReadRefreshTokenFamilyRowsRow, error) {
	rows, err := q.Querier.ReadRefreshTokenFamilyRows(ctx, db, arg)
	if err == nil {
		q.once.Do(q.afterFamilyRead)
	}

	return rows, err
}

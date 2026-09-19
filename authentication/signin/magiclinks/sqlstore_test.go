package magiclinks

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/random"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestNewSQLStore_refusals pins what construction will not accept.
//
// The dialect is read off the client rather than configured, so the only way to
// reach an unsupported one is a client built for it — which is why that arm is
// not tested here and is instead the reason querierDialect names the dialect it
// could not serve.
func TestNewSQLStore_refusals(T *testing.T) {
	T.Parallel()

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(nil, newTestClient(t))

		test.ErrorIs(t, err, ErrNilConfig)
		test.Nil(t, store)
	})

	T.Run("nil client", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(&Config{}, nil)

		test.ErrorIs(t, err, ErrNilDatabaseClient)
		test.Nil(t, store)
	})

	T.Run("a prefix that would not render", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(&Config{TablePrefix: "ends_with_underscore_"}, newTestClient(t))

		test.Error(t, err)
		test.Nil(t, store)
	})
}

// TestIssue_returnsTheSecretOnceAndStoresADigest is the property the hash column
// exists for.
//
// A dump of this table has to be unredeemable, so the secret must be absent from
// every column — not merely absent from the projection, which is why this reads
// the row with raw SQL rather than through the store.
func TestIssue_returnsTheSecretOnceAndStoresADigest(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)

	issuance := issue(t, store)

	must.StrNotEqFold(t, "", issuance.Secret)
	test.EqOp(t, testSubject, issuance.Link.SubjectID)
	test.EqOp(t, testScope(), issuance.Link.Scope)
	test.Nil(t, issuance.Link.RedeemedAt)
	test.Nil(t, issuance.Link.RevokedAt)

	// The whole row, as text, must not contain the secret anywhere.
	var stored string
	must.NoError(t, store.db.Writer().QueryRowContext(t.Context(),
		"SELECT hash || scope || subject_id FROM signin_magic_links").Scan(&stored))

	test.StrNotContains(t, stored, issuance.Secret)
	test.StrContains(t, stored, store.Digest(issuance.Secret))
}

// TestIssue_stampsThePurgeDeadlinePastTheExpiry pins the gap the retention
// window buys, which is the difference between "already used" and "no such
// link" for as long as it lasts.
func TestIssue_stampsThePurgeDeadlinePastTheExpiry(t *testing.T) {
	t.Parallel()

	store, clk := newTestStore(t, WithRetention(time.Hour))

	issuance := issue(t, store)

	test.EqOp(t, clk.Now().UTC().Add(testTTL), issuance.Link.ExpiresAt)
	test.EqOp(t, issuance.Link.ExpiresAt.Add(time.Hour), issuance.Link.PurgeAfter)
}

// TestIssue_refusals pins the argument checks, each of which is a mistake that
// would otherwise land as a row.
func TestIssue_refusals(T *testing.T) {
	T.Parallel()

	T.Run("nil request", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		_, err := issueFor(t, store, testScope(), nil)

		test.ErrorIs(t, err, ErrNilRequest)
	})

	T.Run("no subject", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		_, err := issueFor(t, store, testScope(), &signin.MagicLinkRequest{TTL: testTTL})

		test.ErrorIs(t, err, ErrEmptySubjectID)
	})

	T.Run("no lifetime", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		_, err := issueFor(t, store, testScope(), &signin.MagicLinkRequest{SubjectID: testSubject})

		test.ErrorIs(t, err, ErrNonPositiveLifetime)
	})
}

// TestIssue_leavesOutstandingLinksAlone is the reading passwordreset takes of
// the same situation, asserted here because the opposite is the behavior a
// reasonable person would expect and it is the wrong one.
//
// Somebody who asks twice and opens the first message has a link that works.
func TestIssue_leavesOutstandingLinksAlone(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)

	first := issue(t, store)
	_ = issue(t, store)

	link, err := redeem(t, store, testScope(), first.Secret)

	must.NoError(t, err)
	test.EqOp(t, testSubject, link.SubjectID)
}

// TestRedeem_spendsOnceAndAnswersWithTheSubject is the operation this store
// exists for.
func TestRedeem_spendsOnceAndAnswersWithTheSubject(t *testing.T) {
	t.Parallel()

	store, clk := newTestStore(t)

	issuance := issue(t, store)

	link, err := redeem(t, store, testScope(), issuance.Secret)

	must.NoError(t, err)
	test.EqOp(t, testSubject, link.SubjectID)
	must.NotNil(t, link.RedeemedAt)
	test.EqOp(t, clk.Now().UTC(), *link.RedeemedAt)

	// And the second presentation of the same secret is refused, which is the
	// whole of single use.
	_, err = redeem(t, store, testScope(), issuance.Secret)

	test.ErrorIs(t, err, signin.ErrInvalidMagicLink)
}

// TestRedeem_collapsesEveryRefusal is the posture the sign-in doors take, held
// to at the store: four different facts, one answer.
//
// Told apart they are an oracle for whoever is presenting guesses. What tells
// them apart is the span, which is why refusalReason exists and is tested
// separately.
func TestRedeem_collapsesEveryRefusal(T *testing.T) {
	T.Parallel()

	T.Run("a token nobody minted", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		_, err := redeem(t, store, testScope(), "nothing-was-ever-minted-for-this")

		test.ErrorIs(t, err, signin.ErrInvalidMagicLink)
	})

	T.Run("a link that has expired", func(t *testing.T) {
		t.Parallel()

		store, clk := newTestStore(t)
		issuance := issue(t, store)

		clk.advance(testTTL + time.Second)

		_, err := redeem(t, store, testScope(), issuance.Secret)

		test.ErrorIs(t, err, signin.ErrInvalidMagicLink)
	})

	T.Run("a link that was withdrawn", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		issuance := issue(t, store)

		revoked, err := revokeForSubject(t, store, testScope(), testSubject)
		must.NoError(t, err)
		must.EqOp(t, int64(1), revoked)

		_, err = redeem(t, store, testScope(), issuance.Secret)

		test.ErrorIs(t, err, signin.ErrInvalidMagicLink)
	})

	T.Run("a link presented in another directory", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)
		issuance := issue(t, store)

		_, err := redeem(t, store, tenancy.Of("tenant_b"), issuance.Secret)

		test.ErrorIs(t, err, signin.ErrInvalidMagicLink)
	})
}

// TestRedeem_refusesTheExpiryBoundaryClosed pins which side of its own deadline
// a link dies on.
//
// The guard is `expires_at > now`, so a link presented at the instant it expires
// is refused. Both directions of that comparison are defensible and only one of
// them is what the statement says, so it is asserted rather than left to be
// discovered by somebody debugging a link that worked a second too long.
func TestRedeem_refusesTheExpiryBoundaryClosed(t *testing.T) {
	t.Parallel()

	store, clk := newTestStore(t)
	issuance := issue(t, store)

	clk.advance(testTTL)

	_, err := redeem(t, store, testScope(), issuance.Secret)

	test.ErrorIs(t, err, signin.ErrInvalidMagicLink)
}

// TestRedeem_refusals pins the argument checks a redemption makes before it
// hashes anything.
//
// An empty secret is this store's own refusal rather than the collapsed one,
// because a caller that did not submit is not a guess that missed — and hashing
// the empty string is a perfectly good digest that would cost a round trip to
// find nothing.
func TestRedeem_refusals(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)

	_, err := redeem(t, store, testScope(), "")

	test.ErrorIs(t, err, ErrEmptySecret)
	test.False(t, platformerrors.Is(err, signin.ErrInvalidMagicLink))
}

// TestRedeem_isSingleUseUnderConcurrency is the guarantee the guarded UPDATE
// exists to buy, and the one a read followed by a write would lose.
//
// Two goroutines present one secret at the same instant. Exactly one may be told
// it spent the link; the other must be refused, without either of them
// cooperating.
func TestRedeem_isSingleUseUnderConcurrency(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)
	issuance := issue(t, store)

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		succeeded int
		refused   int
	)

	for range 2 {
		wg.Go(func() {
			_, err := redeem(t, store, testScope(), issuance.Secret)

			mu.Lock()
			defer mu.Unlock()

			if err == nil {
				succeeded++
			} else if platformerrors.Is(err, signin.ErrInvalidMagicLink) {
				refused++
			}
		})
	}

	wg.Wait()

	test.EqOp(t, 1, succeeded)
	test.EqOp(t, 1, refused)
}

// TestRevokeForSubject_withdrawsEveryOutstandingLink pins what an account being
// disabled and an erasure both call.
func TestRevokeForSubject_withdrawsEveryOutstandingLink(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)

	first := issue(t, store)
	second := issue(t, store)

	revoked, err := revokeForSubject(t, store, testScope(), testSubject)

	must.NoError(t, err)
	test.EqOp(t, int64(2), revoked)

	for _, secret := range []string{first.Secret, second.Secret} {
		_, redeemErr := redeem(t, store, testScope(), secret)
		test.ErrorIs(t, redeemErr, signin.ErrInvalidMagicLink)
	}
}

// TestRevokeForSubject_isIdempotent pins the guard that makes the record say
// when the links actually stopped working.
//
// A second call matches nothing and reports zero rather than moving the stamp
// forward, which is what a caller retrying a disable needs and what an operator
// reading the row later depends on.
func TestRevokeForSubject_isIdempotent(t *testing.T) {
	t.Parallel()

	store, clk := newTestStore(t)
	_ = issue(t, store)

	first, err := revokeForSubject(t, store, testScope(), testSubject)
	must.NoError(t, err)
	must.EqOp(t, int64(1), first)

	clk.advance(time.Hour)

	second, err := revokeForSubject(t, store, testScope(), testSubject)

	must.NoError(t, err)
	test.EqOp(t, int64(0), second)
}

// TestRevokeForSubject_staysInsideItsScope is the tenancy obligation, asserted
// rather than assumed.
//
// It is where this parts company with links' subject-wide revoke, which crosses
// tenants deliberately because its subjects are opaque identifiers it cannot
// resolve. A directory's are not.
func TestRevokeForSubject_staysInsideItsScope(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)
	issuance := issue(t, store)

	revoked, err := revokeForSubject(t, store, tenancy.Of("tenant_b"), testSubject)

	must.NoError(t, err)
	test.EqOp(t, int64(0), revoked)

	// And the link it did not reach still works.
	link, err := redeem(t, store, testScope(), issuance.Secret)

	must.NoError(t, err)
	test.EqOp(t, testSubject, link.SubjectID)
}

// TestRevokeForSubject_refusals pins the two argument checks.
func TestRevokeForSubject_refusals(T *testing.T) {
	T.Parallel()

	T.Run("no subject", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		_, err := revokeForSubject(t, store, testScope(), "")

		test.ErrorIs(t, err, ErrEmptySubjectID)
	})

	T.Run("an unset scope", func(t *testing.T) {
		t.Parallel()

		store, _ := newTestStore(t)

		_, err := revokeForSubject(t, store, tenancy.Scope{}, testSubject)

		test.Error(t, err)
	})
}

// TestRefusalReason pins what an operator reads off a span, which is the only
// place the four refusals are told apart.
//
// It is a table over the function rather than four store tests, because what is
// under test is the reading of a row rather than the statement that produced
// one — and the default arm is unreachable through the store by construction.
func TestRefusalReason(T *testing.T) {
	T.Parallel()

	now := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	stamp := now.Add(-time.Minute)

	for name, tc := range map[string]struct {
		link *signin.MagicLink
		want string
	}{
		"already followed": {
			link: &signin.MagicLink{RedeemedAt: &stamp, ExpiresAt: now.Add(time.Hour)},
			want: reasonRedeemed,
		},
		"withdrawn": {
			link: &signin.MagicLink{RevokedAt: &stamp, ExpiresAt: now.Add(time.Hour)},
			want: reasonRevoked,
		},
		"past its deadline": {
			link: &signin.MagicLink{ExpiresAt: now.Add(-time.Second)},
			want: reasonExpired,
		},
		"expiring at this instant": {
			link: &signin.MagicLink{ExpiresAt: now},
			want: reasonExpired,
		},
		// A row that matched none of the three and was still not updated, which
		// the statement's own predicate says cannot happen. It is named so that
		// a fourth guard added to the corpus without a reading here is a span
		// that says so rather than a silent mislabel.
		"none of the three": {
			link: &signin.MagicLink{ExpiresAt: now.Add(time.Hour)},
			want: reasonUnknown,
		},
	} {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			test.EqOp(t, tc.want, refusalReason(tc.link, now))
		})
	}
}

// TestRedeem_reportsRedeemedBeforeRevoked pins the order the two stamps are read
// in, for a row that carries both.
//
// It is reachable: a link is followed and the subject is then disabled, which
// withdraws every unrevoked row and leaves this one alone — so the pair only
// occurs the other way round. The order is asserted anyway, because the reading
// is what an operator acts on and "already used" is the more specific of the two
// true sentences.
func TestRedeem_reportsRedeemedBeforeRevoked(t *testing.T) {
	t.Parallel()

	stamp := time.Date(2026, time.September, 18, 11, 0, 0, 0, time.UTC)
	now := stamp.Add(time.Hour)

	both := &signin.MagicLink{RedeemedAt: &stamp, RevokedAt: &stamp, ExpiresAt: now.Add(time.Hour)}

	test.EqOp(t, reasonRedeemed, refusalReason(both, now))
}

// TestSweep_collectsOnThePurgeDeadline pins the retention ruling end to end.
//
// A row past its expiry is already dead to a redemption and is still there; a
// row past its purge deadline is gone. The gap between the two is what a support
// answer is reconstructed from.
func TestSweep_collectsOnThePurgeDeadline(t *testing.T) {
	t.Parallel()

	store, clk := newTestStore(t, WithRetention(time.Hour))
	_ = issue(t, store)

	// Past the expiry, inside the retention window: refused, but still a row.
	clk.advance(testTTL + time.Minute)

	swept, err := store.Sweep(t.Context())
	must.NoError(t, err)
	test.EqOp(t, int64(0), swept)
	test.EqOp(t, 1, rowsIn(t, store.db, "signin_magic_links"))

	// Past the purge deadline: collected.
	clk.advance(time.Hour)

	swept, err = store.Sweep(t.Context())
	must.NoError(t, err)
	test.EqOp(t, int64(1), swept)
	test.EqOp(t, 0, rowsIn(t, store.db, "signin_magic_links"))
}

// TestSweep_crossesEveryScope pins the one statement in this package that does,
// and the reason it is allowed to: it names no rows, answers with a count, and
// is the store servicing itself on a timer rather than anybody's read.
func TestSweep_crossesEveryScope(t *testing.T) {
	t.Parallel()

	store, clk := newTestStore(t, WithRetention(time.Minute))

	for _, scope := range []tenancy.Scope{testScope(), tenancy.Of("tenant_b"), tenancy.Global()} {
		_, err := issueFor(t, store, scope, &signin.MagicLinkRequest{TTL: testTTL, SubjectID: testSubject})
		must.NoError(t, err)
	}

	clk.advance(testTTL + time.Hour)

	swept, err := store.Sweep(t.Context())

	must.NoError(t, err)
	test.EqOp(t, int64(3), swept)
}

// TestSweep_logsItsOwnFailureInTheBackgroundLoop is the one code path in this
// package whose only effect is a log line: nothing is waiting on the goroutine,
// so a sweep that fails is a table that grows for another interval rather than a
// sign-in that misbehaves.
func TestSweep_logsItsOwnFailureInTheBackgroundLoop(t *testing.T) {
	t.Parallel()

	logger := newRecordingLogger()
	client := newTestClient(t)

	// The table is dropped underneath the store, which is the least contrived
	// way to make a sweep fail against a real database.
	_, err := client.Writer().ExecContext(t.Context(), "DROP TABLE signin_magic_links")
	must.NoError(t, err)

	c := newFakeClock()

	store, err := NewSQLStore(&Config{}, client,
		WithClock(c),
		WithLogger(logger),
		WithSweeper(t.Context(), time.Minute),
	)
	must.NoError(t, err)
	must.NotNil(t, store)

	c.tick()

	// The loop logs asynchronously, so this waits for the line rather than
	// asserting on it immediately.
	for range 100 {
		if logger.count(backgroundSweepFailure) > 0 {
			return
		}

		time.Sleep(10 * time.Millisecond)
	}

	t.Fatalf("the background loop never logged %q", backgroundSweepFailure)
}

// TestSQLStore_addressesTheNamespacedTable pins that a configured prefix reaches
// the statements rather than only the DDL.
//
// The table's name lives in exactly one place — internal/queries' constant, with
// database/ddl supplying the separator — so a namespaced deployment writing to
// the unprefixed table would be two renderings of one name, and the symptom
// would be a store that works until somebody shares a database.
func TestSQLStore_addressesTheNamespacedTable(t *testing.T) {
	t.Parallel()

	client := newTestClient(t)
	createTable(t, client, dialect.SQLite, "ddb")

	c := newFakeClock()

	store, err := NewSQLStore(&Config{TablePrefix: "ddb"}, client, WithClock(c))
	must.NoError(t, err)

	issuance := issue(t, store)

	test.EqOp(t, 1, rowsIn(t, client, "ddb_signin_magic_links"))
	test.EqOp(t, 0, rowsIn(t, client, "signin_magic_links"))

	link, err := redeem(t, store, testScope(), issuance.Secret)

	must.NoError(t, err)
	test.EqOp(t, testSubject, link.SubjectID)
}

// TestDigest_isStableAndIrreversible pins what the exported helper is for and
// what it is not.
//
// It is not a verification — comparing its output to a column by hand is how the
// single-use guarantee gets reimplemented badly — and a caller holding one of
// these holds nothing, which is the property that makes the column safe to back
// up.
func TestDigest_isStableAndIrreversible(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t)

	const secret = "a-secret-somebody-was-mailed"

	digest := store.Digest(secret)

	test.EqOp(t, digest, store.Digest(secret))
	test.StrNotContains(t, digest, secret)
	test.False(t, strings.Contains(secret, digest))
}

// constantGenerator hands back one secret every time, for the case only a real
// engine decides: a second row bearing one digest is a failed write rather than
// a silently replaced row, which is what the primary key is for.
type constantGenerator struct {
	secret string
}

var _ random.Generator = (*constantGenerator)(nil)

func (g *constantGenerator) GenerateHexEncodedString(context.Context, int) (string, error) {
	return g.secret, nil
}

func (g *constantGenerator) GenerateBase32EncodedString(context.Context, int) (string, error) {
	return g.secret, nil
}

func (g *constantGenerator) GenerateBase64EncodedString(context.Context, int) (string, error) {
	return g.secret, nil
}

func (g *constantGenerator) GenerateRawBytes(context.Context, int) ([]byte, error) {
	return []byte(g.secret), nil
}

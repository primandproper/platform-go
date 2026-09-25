package signin_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/recoverycodes"
	recoverymigrations "github.com/primandproper/platform-go/v14/authentication/signin/recoverycodes/migrations"
	"github.com/primandproper/platform-go/v14/identity"
	identitymigrations "github.com/primandproper/platform-go/v14/identity/migrations"

	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/authentication/totp"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/mysql"
	"github.com/primandproper/primitives-go/v2/database/postgres"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/testutils/containers/mysqltest"
	"github.com/primandproper/primitives-go/v2/testutils/containers/pgtest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// serverClientConfig is a client config for a real server: a pool wide enough
// that concurrent sign-ins hold separate transactions on separate connections,
// which is the whole of what the race below needs and what SQLite's single
// serialized writer cannot give it.
type serverClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*serverClientConfig)(nil)

func (c *serverClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *serverClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *serverClientConfig) GetMaxPingAttempts() uint64        { return 30 }
func (c *serverClientConfig) GetPingWaitPeriod() time.Duration  { return time.Second }
func (c *serverClientConfig) GetMaxIdleConns() int              { return 4 }
func (c *serverClientConfig) GetMaxOpenConns() int              { return 16 }
func (c *serverClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// waitForServer polls until the server accepts a trivial statement, because a
// container logs that it is ready slightly before it is.
func waitForServer(tb testing.TB, q database.SQLQueryExecutor) {
	tb.Helper()

	var lastErr error
	for range 30 {
		if _, err := q.ExecContext(tb.Context(), "SELECT 1"); err == nil {
			return
		} else { //nolint:revive // the error is only reported if every attempt fails
			lastErr = err
		}

		time.Sleep(time.Second)
	}

	tb.Fatalf("database never accepted a statement: %v", lastErr)
}

// runConcurrentRecoverySignIns is the guarantee the recovery code doors rest on,
// asserted through the door rather than the store: several sign-ins presenting
// one code at once, each checking it before its transaction and spending it
// inside one, and exactly one of them signed in.
//
// Every contender passes the check — the code is unspent when each of them reads
// it — so what decides the outcome is the spend's guarded write, on a real
// server, with the contenders on separate connections.
func runConcurrentRecoverySignIns(t *testing.T, client database.Client, d dialect.Dialect) {
	t.Helper()

	waitForServer(t, client.Writer())

	const prefix = "rc"

	for _, render := range []func(dialect.Dialect, string) ([]string, error){
		identitymigrations.Statements,
		recoverymigrations.Statements,
	} {
		stmts, err := render(d, prefix)
		must.NoError(t, err)

		for _, stmt := range stmts {
			_, execErr := client.Writer().ExecContext(t.Context(), stmt)
			must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
		}
	}

	store, err := identity.NewSQLStore(client, identity.WithTablePrefix(prefix))
	must.NoError(t, err)

	directory, err := identity.NewService(client, store)
	must.NoError(t, err)

	codes, err := recoverycodes.NewSQLStore(&recoverycodes.Config{TablePrefix: prefix}, client)
	must.NoError(t, err)

	hooks := &recoveryHooks{}

	svc, err := signin.NewService(client, store, argon2.NewArgon2Authenticator(), &fakeIssuer{},
		signin.WithHooks(hooks),
		signin.WithRecoveryCodeStore(codes),
	)
	must.NoError(t, err)

	const password = "correct horse battery staple"

	hashed, err := argon2.NewArgon2Authenticator().HashPassword(t.Context(), password)
	must.NoError(t, err)

	registration, err := directory.Register(t.Context(), testScope,
		&identity.User{
			Username:       "jane",
			EmailAddress:   "jane@example.com",
			HashedPassword: hashed,
			AccountStatus:  identity.StatusGood,
			Scope:          testScope,
		},
		&identity.Account{Name: "Jane's", Scope: testScope},
		[]string{"owner"},
	)
	must.NoError(t, err)

	user := registration.User

	enrollment, err := totp.NewGenerator().Generate(t.Context(), "Example", "jane")
	must.NoError(t, err)

	must.NoError(t, client.WithTransaction(t.Context(), func(tx database.Tx) error {
		if txErr := store.UpdateUserTwoFactorSecret(t.Context(), tx, testScope, user.ID, enrollment.Secret); txErr != nil {
			return txErr
		}

		_, txErr := store.MarkUserTwoFactorSecretVerified(t.Context(), tx, testScope, user.ID)

		return txErr
	}))

	set, err := svc.ReplaceRecoveryCodes(t.Context(), testScope, user.ID, &signin.RecoveryCodeReplacement{
		CurrentPassword: password,
		TOTPCode:        code(t, enrollment.Secret),
	})
	must.NoError(t, err)

	const contenders = 6

	var (
		start   sync.WaitGroup
		done    sync.WaitGroup
		winners atomic.Int64
		refused atomic.Int64
	)

	start.Add(1)
	done.Add(contenders)

	for range contenders {
		go func() {
			defer done.Done()

			start.Wait()

			_, loginErr := svc.LoginForToken(context.WithoutCancel(t.Context()), testScope, &signin.Credentials{
				Username: "jane",
				Password: password,
				TOTPCode: set[0],
			})

			switch {
			case loginErr == nil:
				winners.Add(1)
			case platformerrors.Is(loginErr, signin.ErrInvalidCredentials):
				refused.Add(1)
			default:
				t.Errorf("a contender failed for a reason that is not the refusal: %v", loginErr)
			}
		}()
	}

	start.Done()
	done.Wait()

	test.EqOp(t, int64(1), winners.Load())
	test.EqOp(t, int64(contenders-1), refused.Load())

	remaining, err := svc.RecoveryCodesRemaining(t.Context(), testScope, user.ID)
	must.NoError(t, err)
	test.EqOp(t, signin.DefaultRecoveryCodeCount-1, remaining)

	// Every loser was recorded as a failed sign-in, and exactly one spend was
	// recorded as a spend.
	hooks.mu.Lock()
	defer hooks.mu.Unlock()

	test.SliceLen(t, contenders-1, hooks.failures)
	test.SliceLen(t, 1, hooks.used)
}

func TestRecoveryCodeSignIn_Postgres(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx, &serverClientConfig{connectionString: pg.ConnectionString})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runConcurrentRecoverySignIns(T, client, dialect.Postgres)
	})
}

func TestRecoveryCodeSignIn_MySQL(T *testing.T) {
	T.Parallel()

	mysqltest.Run(T, func(ctx context.Context, my *mysqltest.Instance) {
		client, err := mysql.NewDatabaseClient(ctx, &serverClientConfig{connectionString: my.ConnectionString})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		runConcurrentRecoverySignIns(T, client, dialect.MySQL)
	}, mysqltest.WithCredentials("signinrecovery", "signinrecovery", "signinrecovery"))
}

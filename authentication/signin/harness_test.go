package signin_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/identity"
	identitymigrations "github.com/primandproper/platform-go/v14/identity/migrations"

	"github.com/primandproper/primitives-go/authentication"
	"github.com/primandproper/primitives-go/authentication/argon2"
	"github.com/primandproper/primitives-go/authentication/totp"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/sqlite"
	"github.com/primandproper/primitives-go/tenancy"

	pquernatotp "github.com/pquerna/otp/totp"
	"github.com/shoenig/test/must"
)

// The suite runs against a real SQLite database and a real identity.SQLStore.
// Nothing here mocks the directory: the operations under test are almost
// entirely about which rows are read and in what order, and a store of stubs
// would assert that this package calls the methods it calls rather than that
// signing in works.
//
// What is faked is the token issuer, because a real one would test the tokens
// package, and — in the tests that need a wrong password to be cheap — the
// authenticator.

// testClientConfig is the minimum database.ClientConfig a SQLite client needs.
type testClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// dbTx is database.Tx, spelled once so the tests that reach past the service to
// arrange a row do not each import the database package for one type.
type dbTx = database.Tx

// testScope is the directory the suite registers users in.
var testScope = tenancy.Of("dir_1")

// prefixCounter names a fresh set of tables per subtest. Subtests share one
// database file per test and must not share tables.
var prefixCounter atomic.Uint64

// env is one live database, the identity store over it, and the sign-in service
// over that.
type env struct {
	client    database.Client
	store     *identity.SQLStore
	svc       *signin.Service
	issuer    *fakeIssuer
	hooks     *recordingHooks
	password  string
	user      *identity.User
	accountID string
}

// newEnv builds the whole stack and registers one user with a password, which
// is what almost every test here starts from.
func newEnv(t *testing.T, opts ...signin.ServiceOption) *env {
	t.Helper()

	client, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "signin.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	prefix := fmt.Sprintf("id_%d", prefixCounter.Add(1))

	stmts, err := identitymigrations.Statements(dialect.SQLite, prefix)
	must.NoError(t, err)

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}

	store, err := identity.NewSQLStore(client, identity.WithTablePrefix(prefix))
	must.NoError(t, err)

	e := &env{
		client:   client,
		store:    store,
		issuer:   &fakeIssuer{},
		hooks:    &recordingHooks{},
		password: "correct horse battery staple",
	}

	opts = append([]signin.ServiceOption{
		signin.WithHooks(e.hooks),
		signin.WithTOTPIssuer("Example"),
	}, opts...)

	e.svc, err = signin.NewService(client, store, argon2.NewArgon2Authenticator(), e.issuer, opts...)
	must.NoError(t, err)

	e.register(t)

	return e
}

// register creates the user every test signs in as: one account, one owner
// membership, status good, and a hashed password.
func (e *env) register(t *testing.T) {
	t.Helper()

	hashed, err := argon2.NewArgon2Authenticator().HashPassword(t.Context(), e.password)
	must.NoError(t, err)

	user := &identity.User{
		Username:       "jane",
		EmailAddress:   "jane@example.com",
		HashedPassword: hashed,
		AccountStatus:  identity.StatusGood,
		Scope:          testScope,
	}

	account := &identity.Account{Name: "Jane's", Scope: testScope}

	svc, err := identity.NewService(e.client, e.store)
	must.NoError(t, err)

	registration, err := svc.Register(t.Context(), testScope, user, account, []string{"owner"})
	must.NoError(t, err)

	e.user, e.accountID = registration.User, registration.Account.ID
}

// registerPasswordless registers a user who holds no password credential, which
// identity treats as first-class: a passkey-only or federated registration.
func (e *env) registerPasswordless(t *testing.T, username string) *identity.User {
	t.Helper()

	svc, err := identity.NewService(e.client, e.store)
	must.NoError(t, err)

	registration, err := svc.Register(t.Context(), testScope,
		&identity.User{
			Username:      username,
			EmailAddress:  username + "@example.com",
			AccountStatus: identity.StatusGood,
			Scope:         testScope,
		},
		&identity.Account{Name: username, Scope: testScope},
		[]string{"owner"},
	)
	must.NoError(t, err)

	return registration.User
}

// setStatus moves the registered user's account status.
func (e *env) setStatus(t *testing.T, status identity.AccountStatus, explanation string) {
	t.Helper()

	must.NoError(t, e.client.WithTransaction(t.Context(), func(tx database.Tx) error {
		return e.store.UpdateUserAccountStatus(t.Context(), tx, testScope, e.user.ID, status, explanation)
	}))
}

// enrollTOTP gives the registered user a proven second factor and returns its
// secret.
func (e *env) enrollTOTP(t *testing.T) string {
	t.Helper()

	enrollment, err := totp.NewGenerator().Generate(t.Context(), "Example", "jane")
	must.NoError(t, err)

	must.NoError(t, e.client.WithTransaction(t.Context(), func(tx database.Tx) error {
		if err = e.store.UpdateUserTwoFactorSecret(t.Context(), tx, testScope, e.user.ID, enrollment.Secret); err != nil {
			return err
		}

		return e.store.MarkUserTwoFactorSecretVerified(t.Context(), tx, testScope, e.user.ID)
	}))

	return enrollment.Secret
}

// setServiceRoles grants the registered user service roles.
func (e *env) setServiceRoles(t *testing.T, roles ...string) {
	t.Helper()

	must.NoError(t, e.client.WithTransaction(t.Context(), func(tx database.Tx) error {
		return e.store.SetUserServiceRoles(t.Context(), tx, testScope, e.user.ID, roles)
	}))
}

// code produces a currently-valid TOTP code for a secret.
func code(t *testing.T, secret string) string {
	t.Helper()

	value, err := pquernatotp.GenerateCode(secret, time.Now().UTC())
	must.NoError(t, err)

	return value
}

// credentials is the happy-path credential set, which each test then breaks in
// one specific way.
func (e *env) credentials() *signin.Credentials {
	return &signin.Credentials{Username: "jane", Password: e.password}
}

// fakeIssuer stands in for a real tokens.Issuer.
//
// It records what it was asked for, because the two token lifetimes and the
// claims builder are policy this package's callers configure, and the only way
// to see what was chosen is what reached the issuer.
type fakeIssuer struct {
	err error

	claims map[string]any

	subject string
	expiry  time.Duration
	calls   int
}

func (f *fakeIssuer) IssueToken(
	_ context.Context,
	subject string,
	expiry time.Duration,
	extraClaims map[string]any,
) (token, jti string, err error) {
	f.calls++
	f.subject, f.expiry, f.claims = subject, expiry, extraClaims

	if f.err != nil {
		return "", "", f.err
	}

	return "token-for-" + subject, "jti-" + subject, nil
}

// recordingHooks records every call, and can be made to fail one of them.
//
// calls is the order the hooks ran in, which is the only way to see that a
// token door runs the authentication hook first — the two slices below record
// that both ran, and nothing in them records which was first.
type recordingHooks struct {
	authErr   error
	issueErr  error
	failedErr error

	calls           []string
	authentications []*signin.Authentication
	signIns         []*signin.SignIn
	failures        []*signin.FailedSignIn

	signin.NoopHooks

	passwords,
	refreshes,
	verifications int
}

func (h *recordingHooks) AfterAuthenticate(
	_ context.Context,
	_ database.Tx,
	_ tenancy.Scope,
	a *signin.Authentication,
) error {
	h.calls = append(h.calls, "authenticate")
	h.authentications = append(h.authentications, a)

	return h.authErr
}

func (h *recordingHooks) AfterIssueToken(_ context.Context, _ database.Tx, _ tenancy.Scope, s *signin.SignIn) error {
	h.calls = append(h.calls, "issue")
	h.signIns = append(h.signIns, s)

	return h.issueErr
}

func (h *recordingHooks) AfterFailedSignIn(
	_ context.Context,
	_ database.Tx,
	_ tenancy.Scope,
	attempt *signin.FailedSignIn,
) error {
	h.failures = append(h.failures, attempt)

	return h.failedErr
}

func (h *recordingHooks) AfterUpdatePassword(_ context.Context, _ database.Tx, _ tenancy.Scope, _ *identity.User) error {
	h.passwords++

	return nil
}

func (h *recordingHooks) AfterRefreshTOTPSecret(_ context.Context, _ database.Tx, _ tenancy.Scope, _ *identity.User) error {
	h.refreshes++

	return nil
}

func (h *recordingHooks) AfterVerifyTOTPSecret(_ context.Context, _ database.Tx, _ tenancy.Scope, _ *identity.User) error {
	h.verifications++

	return nil
}

// stubAuthenticator is an Authenticator with no argon2 behind it, for the tests
// that only care whether the comparison was reached.
type stubAuthenticator struct {
	hashErr  error
	matchErr error

	hashes  int
	matches int
	result  bool
}

var _ authentication.Authenticator = (*stubAuthenticator)(nil)

func (s *stubAuthenticator) HashPassword(context.Context, string) (string, error) {
	s.hashes++

	return "hashed", s.hashErr
}

func (s *stubAuthenticator) PasswordMatches(context.Context, string, string) (bool, error) {
	s.matches++

	return s.result, s.matchErr
}

package signin_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/magiclinks"
	magiclinkmigrations "github.com/primandproper/platform-go/v14/authentication/signin/magiclinks/migrations"
	"github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens"
	refreshmigrations "github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens/migrations"
	"github.com/primandproper/platform-go/v14/identity"
	identitymigrations "github.com/primandproper/platform-go/v14/identity/migrations"

	"github.com/primandproper/primitives-go/v2/authentication"
	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/authentication/totp"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/tenancy"

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
	client  database.Client
	store   *identity.SQLStore
	refresh *refreshtokens.SQLStore
	svc     *signin.Service
	issuer  *fakeIssuer
	hooks   *recordingHooks
	user    *identity.User

	// directory is identity's Service over the same store, which is what this
	// package registers through. The suite holds it because the registration
	// tests also arrange rows with it.
	directory *identity.Service

	// magicLinks and mailer are wired only by newMagicLinkEnv. The store is a
	// real one over the same database, for the reason the refresh store is: what
	// is under test is which rows are written and in what order, and a store of
	// stubs would assert that this package calls the methods it calls.
	magicLinks *magiclinks.SQLStore
	mailer     *recordingMailer
	password   string
	accountID  string

	// refreshPrefix is the namespace both schemas were rendered at, kept so the
	// two assertions that count rows can name the table the store writes to.
	refreshPrefix string
}

// newEnv builds the whole stack and registers one user with a password, which
// is what almost every test here starts from.
//
// The service it builds names no refresh token store, which is the default shape
// and the one every test that predates rotation was written against: one token
// per sign-in, and no schema of this package's own.
func newEnv(t *testing.T, opts ...signin.ServiceOption) *env {
	t.Helper()

	return buildEnv(t, false, false, nil, opts...)
}

// newRefreshEnv is newEnv with a live refresh token store wired in, which is
// what a consumer who adopted rotation has.
//
// It is a real refreshtokens.SQLStore over the same SQLite database rather than
// a double, for the reason nothing here mocks the directory either: the
// behavior under test is which rows are written and in what order, and a store
// of stubs would assert that this package calls the methods it calls.
func newRefreshEnv(t *testing.T, opts ...signin.ServiceOption) *env {
	t.Helper()

	return buildEnv(t, true, false, nil, opts...)
}

// newPermissiveRefreshEnv is newRefreshEnv over a Directory that answers with a
// Principal for a user identity.Store.GetPrincipal would refuse.
//
// It is what a consumer who implemented the seven methods themselves has, and it
// is the only way to reach the status check the exchange makes after its re-read:
// on this module's directory that check is unreachable, because the refusal comes
// back instead of the Principal.
func newPermissiveRefreshEnv(t *testing.T, opts ...signin.ServiceOption) *env {
	t.Helper()

	return buildEnv(t, true, false, func(d signin.Directory) signin.Directory {
		return permissiveDirectory{Directory: d}
	}, opts...)
}

// newMagicLinkEnv is newRefreshEnv with the passwordless door wired in: a real
// magiclinks.SQLStore over the same database, and a mailer that records what it
// was handed.
//
// The refresh store comes with it rather than being a second builder, because a
// redemption mints a refresh token where one is configured and that is the shape
// a consumer who adopted either of these has.
func newMagicLinkEnv(t *testing.T, opts ...signin.ServiceOption) *env {
	t.Helper()

	return buildEnv(t, true, true, nil, opts...)
}

// permissiveDirectory is that Directory. Everything but the principal read is
// the real store's; the principal read builds one out of the user row without
// asking whether their status admits a sign-in, which is precisely the thing a
// consumer's own directory might forget to do.
//
// The memberships are left empty on purpose. Nothing downstream of the status
// check runs in the case this exists for, and a Principal assembled with roles
// the test never asserts on would suggest they mattered.
type permissiveDirectory struct {
	signin.Directory
}

func (d permissiveDirectory) GetPrincipal(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID, activeAccountID string,
) (*identity.Principal, error) {
	user, err := d.GetUser(ctx, q, scope, userID)
	if err != nil {
		return nil, err
	}

	return &identity.Principal{User: user.Redacted(), ActiveAccountID: activeAccountID}, nil
}

// buildEnv is all three constructors. The refresh token store has to exist before
// the service that is handed it and after the client it is built over, which is
// the whole reason this is one function with a flag rather than two.
func buildEnv(
	t *testing.T,
	withRefresh, withMagicLinks bool,
	wrapDirectory func(signin.Directory) signin.Directory,
	opts ...signin.ServiceOption,
) *env {
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

	e.directory, err = identity.NewService(client, store)
	must.NoError(t, err)

	// The registrar and the verifications directory are wired for every env
	// rather than only the registration suite's, because the alternative is two
	// shapes of service in one file and a test that reaches the wrong one
	// failing with "not configured" rather than with what it was asserting. The
	// two tests that want a service without them build one of their own.
	opts = append([]signin.ServiceOption{
		signin.WithHooks(e.hooks),
		signin.WithTOTPIssuer("Example"),
		signin.WithRegistrar(e.directory),
		signin.WithVerifications(store),
	}, opts...)

	if withRefresh {
		refreshStmts, stmtErr := refreshmigrations.Statements(dialect.SQLite, prefix)
		must.NoError(t, stmtErr)

		for _, stmt := range refreshStmts {
			_, execErr := client.Writer().ExecContext(t.Context(), stmt)
			must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
		}

		e.refresh, err = refreshtokens.NewSQLStore(&refreshtokens.Config{TablePrefix: prefix}, client)
		must.NoError(t, err)

		e.refreshPrefix = prefix

		opts = append(opts, signin.WithRefreshTokenStore(e.refresh))
	}

	if withMagicLinks {
		linkStmts, stmtErr := magiclinkmigrations.Statements(dialect.SQLite, prefix)
		must.NoError(t, stmtErr)

		for _, stmt := range linkStmts {
			_, execErr := client.Writer().ExecContext(t.Context(), stmt)
			must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
		}

		e.magicLinks, err = magiclinks.NewSQLStore(&magiclinks.Config{TablePrefix: prefix}, client)
		must.NoError(t, err)

		e.mailer = &recordingMailer{}

		// The floor goes in front of everything, including the caller's own
		// options, because it is the one default these tests cannot keep: it
		// holds every request for half a second and the suite makes a lot of
		// them. Prepending rather than appending is what leaves a test free to
		// ask for a real floor — see TestRequestMagicLink_padsItsOwnTiming.
		opts = append([]signin.ServiceOption{
			signin.WithMagicLinkRequestFloor(time.Nanosecond),
		}, opts...)

		opts = append(opts,
			signin.WithMagicLinkStore(e.magicLinks),
			signin.WithMagicLinkMailer(e.mailer),
		)
	}

	var directory signin.Directory = store
	if wrapDirectory != nil {
		directory = wrapDirectory(directory)
	}

	e.svc, err = signin.NewService(client, directory, argon2.NewArgon2Authenticator(), e.issuer, opts...)
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

	registration, err := e.directory.Register(t.Context(), testScope, user, account, []string{"owner"})
	must.NoError(t, err)

	e.user, e.accountID = registration.User, registration.Account.ID
}

// registerPasswordless registers a user who holds no password credential, which
// identity treats as first-class: a passkey-only or federated registration.
func (e *env) registerPasswordless(t *testing.T, username string) *identity.User {
	t.Helper()

	registration, err := e.directory.Register(t.Context(), testScope,
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

// registerAccountless writes a user with a password and no account at all: the
// operator identity/grpc's MembershipAuthorizer doc has in mind, who must sign
// in before anybody can put them in one.
//
// It goes through the store rather than identity.Service.Register because
// Register mints an account — a user with none is a row this suite has to write
// for itself.
func (e *env) registerAccountless(t *testing.T, username string) *identity.User {
	t.Helper()

	hashed, err := argon2.NewArgon2Authenticator().HashPassword(t.Context(), e.password)
	must.NoError(t, err)

	var created *identity.User

	must.NoError(t, e.client.WithTransaction(t.Context(), func(tx database.Tx) error {
		created, err = e.store.CreateUser(t.Context(), tx, testScope, &identity.User{
			Username:       username,
			EmailAddress:   username + "@example.com",
			HashedPassword: hashed,
			AccountStatus:  identity.StatusGood,
			Scope:          testScope,
		})

		return err
	}))

	return created
}

// addAccount puts the registered user in a second account and returns its ID,
// for the case that proves an exchange keeps the account proven at sign-in
// rather than re-resolving to whatever the default has become.
func (e *env) addAccount(t *testing.T, name string) string {
	t.Helper()

	var account *identity.Account

	must.NoError(t, e.client.WithTransaction(t.Context(), func(tx database.Tx) error {
		created, err := e.store.CreateAccount(t.Context(), tx, testScope,
			&identity.Account{Name: name, Scope: testScope, OwnerUserID: e.user.ID})
		if err != nil {
			return err
		}

		account = created

		// Not the default one: the case this exists for is a sign-in that named
		// an account other than the user's default, so a second default would
		// make the assertion pass for the wrong reason.
		_, err = e.store.CreateMembership(t.Context(), tx, testScope, &identity.Membership{
			BelongsToUser:    e.user.ID,
			BelongsToAccount: created.ID,
			Scope:            testScope,
			Roles:            []string{"owner"},
		})

		return err
	}))

	return account.ID
}

// setStatus moves the registered user's account status.
func (e *env) setStatus(t *testing.T, status identity.AccountStatus, explanation string) {
	t.Helper()

	must.NoError(t, e.client.WithTransaction(t.Context(), func(tx database.Tx) error {
		return e.store.UpdateUserAccountStatus(t.Context(), tx, testScope, e.user.ID, status, explanation)
	}))
}

// changeEmailAddress moves a user to another address the way a profile update
// does, and answers with the row it left behind.
//
// It goes through identity.Store.UpdateUser rather than writing the column,
// because what the tests using it turn on is the pair of columns that write
// moves with the address: a changed address is an unproven address with no
// outstanding token, and a test that set the address alone would be asserting
// against a row this module cannot produce.
func (e *env) changeEmailAddress(t *testing.T, user *identity.User, address string) *identity.User {
	t.Helper()

	var updated *identity.User

	must.NoError(t, e.client.WithTransaction(t.Context(), func(tx database.Tx) error {
		moved := *user
		moved.EmailAddress = address

		var err error
		updated, err = e.store.UpdateUser(t.Context(), tx, testScope, &moved)

		return err
	}))

	return updated
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

		_, verifyErr := e.store.MarkUserTwoFactorSecretVerified(t.Context(), tx, testScope, e.user.ID)

		return verifyErr
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
	attachErr error
	verifyErr error

	// verified is the user the second-factor hook was handed: the row the
	// directory's write answered with, rather than the copy read before it.
	verified *identity.User

	calls           []string
	authentications []*signin.Authentication
	signIns         []*signin.SignIn
	failures        []*signin.FailedSignIn
	attached        []*identity.User
	verifieds       []*signin.Verification

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

func (h *recordingHooks) AfterAttachPassword(_ context.Context, _ database.Tx, _ tenancy.Scope, user *identity.User) error {
	h.calls = append(h.calls, "attach")
	h.attached = append(h.attached, user)

	return h.attachErr
}

func (h *recordingHooks) AfterVerify(
	_ context.Context,
	_ database.Tx,
	_ tenancy.Scope,
	verification *signin.Verification,
) error {
	h.calls = append(h.calls, "verify")
	h.verifieds = append(h.verifieds, verification)

	return h.verifyErr
}

func (h *recordingHooks) AfterRefreshTOTPSecret(_ context.Context, _ database.Tx, _ tenancy.Scope, _ *identity.User) error {
	h.refreshes++

	return nil
}

func (h *recordingHooks) AfterVerifyTOTPSecret(
	_ context.Context,
	_ database.Tx,
	_ tenancy.Scope,
	user *identity.User,
) error {
	h.verifications++
	h.verified = user

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

// recordingMailer is the MagicLinkMailer these tests wire in: it keeps what it
// was handed, and can be told to fail.
//
// It records the whole Mail rather than the secret alone, because two of the
// assertions are about who the mail was addressed to and one is about the
// secret never being the digest the row holds.
type recordingMailer struct {
	err  error
	sent []*signin.MagicLinkMail
	mu   sync.Mutex
}

var _ signin.MagicLinkMailer = (*recordingMailer)(nil)

func (m *recordingMailer) SendMagicLink(_ context.Context, mail *signin.MagicLinkMail) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.err != nil {
		return m.err
	}

	m.sent = append(m.sent, mail)

	return nil
}

// count is how many mails were sent, which for the enumeration assertions is the
// whole answer: a request that found nobody must send none, and must say so to
// nobody.
func (m *recordingMailer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.sent)
}

// last is the most recent mail, for the assertions about what it carried.
func (m *recordingMailer) last(tb testing.TB) *signin.MagicLinkMail {
	tb.Helper()

	m.mu.Lock()
	defer m.mu.Unlock()

	must.SliceNotEmpty(tb, m.sent)

	return m.sent[len(m.sent)-1]
}

// fail makes every subsequent send report err, for the one path where a
// committed row and an undelivered mail are the honest outcome.
func (m *recordingMailer) fail(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.err = err
}

// registerUnverified registers somebody through this package's own door, naming
// no password, which is the arrival the passwordless sign-in exists for.
//
// It goes through signin.Service.Register rather than identity's registrar
// because what it needs is the standing that door produces: a registrant lands
// in StatusUnverified, which admits no sign-in, and that is the state the magic
// link promotes them out of.
func (e *env) registerUnverified(t *testing.T, username string) *identity.User {
	t.Helper()

	registered, err := e.svc.Register(t.Context(), testScope, &signin.Registration{
		User: &identity.User{
			Username:     username,
			EmailAddress: username + "@example.com",
			Scope:        testScope,
		},
		Account:    &identity.Account{Name: username, Scope: testScope},
		Credential: signin.NoPassword(),
		OwnerRoles: []string{"owner"},
	})
	must.NoError(t, err)

	return registered.User
}

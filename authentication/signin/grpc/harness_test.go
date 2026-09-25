package grpc_test

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	signingrpc "github.com/primandproper/platform-go/v14/authentication/signin/grpc"
	signinclient "github.com/primandproper/platform-go/v14/authentication/signin/grpc/client"
	"github.com/primandproper/platform-go/v14/authentication/signin/magiclinks"
	magiclinkmigrations "github.com/primandproper/platform-go/v14/authentication/signin/magiclinks/migrations"
	"github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens"
	refreshmigrations "github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens/migrations"
	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/errormappers"
	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/identity/migrations"

	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/authentication/totp"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/random"
	"github.com/primandproper/primitives-go/v2/tenancy"

	pquernatotp "github.com/pquerna/otp/totp"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

// The suite runs against a real SQLite database, a real identity store, a real
// signin.Service and a real gRPC connection on a bufconn.
//
// What these tests are for is the seams: the converters, the two ways a caller
// is resolved, and the error mapping that turns nine refusals into four codes.
// A mocked service would answer for the half of every one of those that is
// hardest to get right.

// TestMain registers the domain tier's error mappers once for the binary.
//
// It is the only honest way to test them: the two registries are process-global,
// and registering inside each test would assert that appending the same mappers
// repeatedly is harmless rather than that appending them once is enough. It is
// also exactly the call a consumer owes — see the package doc — so a suite that
// omitted it would be testing a mounting nobody should perform.
func TestMain(m *testing.M) {
	errormappers.Register()
	m.Run()
}

// testScope is the directory the suite's users live in. The resolver below binds
// it for every request, standing in for whatever a consumer reads off the
// connection.
var testScope = tenancy.Of("dir_1")

// prefixCounter names a fresh set of tables per subtest.
var prefixCounter atomic.Uint64

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

// testPrincipal is the consumer's half of the principal seam, as small as the
// interface allows.
type testPrincipal struct {
	userID          string
	activeAccountID string
	scope           tenancy.Scope
}

var _ callers.Principal = (*testPrincipal)(nil)

func (p *testPrincipal) UserID() string          { return p.userID }
func (p *testPrincipal) Scope() tenancy.Scope    { return p.scope }
func (p *testPrincipal) ActiveAccountID() string { return p.activeAccountID }

// sidPrincipal is a principal whose session type also carries the access
// token's "sid" claim, which is what a consumer whose interceptor parses one
// has.
type sidPrincipal struct {
	*testPrincipal

	familyID string
}

var _ signingrpc.FamilyIdentifier = (*sidPrincipal)(nil)

func (p *sidPrincipal) FamilyID() string { return p.familyID }

// principalKey is where the suite's stand-in for an authentication interceptor
// puts the principal, on the server side.
type principalKey struct{}

// mdUserID is the metadata the suite's "credential" travels in.
//
// Metadata rather than a context value, because a context value does not cross a
// connection: a real consumer's interceptor reads a token off the metadata and
// resolves a principal from it, and this is that with the token replaced by the
// answer.
const mdUserID = "test-user-id"

// mdFamilyID is the metadata the suite's "sid" claim travels in, for the tests
// that need the server to know which login a request came through.
const mdFamilyID = "test-family-id"

// asUser stamps a caller onto an outgoing request. A context built without it
// carries nobody, which is what makes the anonymous tests exercise the real
// path.
func asUser(ctx context.Context, userID string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, mdUserID, userID)
}

// asUserIn is asUser from inside a named login, which is what a request whose
// access token carries a "sid" claim is.
func asUserIn(ctx context.Context, userID, familyID string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, mdUserID, userID, mdFamilyID, familyID)
}

// authenticate is the consumer's authentication interceptor.
func authenticate(
	ctx context.Context,
	req any,
	_ *grpc.UnaryServerInfo,
	handler grpc.UnaryHandler,
) (any, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return handler(ctx, req)
	}

	userIDs := md.Get(mdUserID)
	if len(userIDs) == 0 || userIDs[0] == "" {
		return handler(ctx, req)
	}

	principal := &testPrincipal{userID: userIDs[0], scope: testScope}

	if familyIDs := md.Get(mdFamilyID); len(familyIDs) > 0 {
		return handler(context.WithValue(ctx, principalKey{}, &sidPrincipal{testPrincipal: principal, familyID: familyIDs[0]}), req)
	}

	return handler(context.WithValue(ctx, principalKey{}, principal), req)
}

// extractPrincipal is the callers.PrincipalExtractor the server is built with.
// It reads what the interceptor above resolved and knows nothing about how.
func extractPrincipal(ctx context.Context) (callers.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(callers.Principal)

	return p, ok
}

// harness is one database, one service and one connected client.
type harness struct {
	db    database.Client
	store identity.Store

	// rootCtx carries no credential. Every request context is built from it
	// rather than from the last one, because metadata appends: a context derived
	// from one that already names a caller ends up naming two.
	rootCtx context.Context

	svc       *signin.Service
	directory *identity.Service
	client    *signinclient.Client

	user *identity.User

	// mailer is what the passwordless door was handed, and is the only way to
	// reach the secret it mailed. It is wired for every harness and fed by none
	// but newMagicLinkHarness's.
	mailer *recordingMailer

	password  string
	accountID string
}

// newHarness stands the whole stack up and registers one user with a password.
func newHarness(t *testing.T, svcOpts []signin.ServiceOption, opts ...signingrpc.Option) *harness {
	t.Helper()

	return newHarnessWithIssuer(t, &fakeIssuer{}, svcOpts, opts...)
}

// newHarnessWithIssuer is newHarness with the token issuer named, for the tests
// that need one that fails. Nothing else about a sign-in changes: the password
// is proven and the policy is satisfied, and the only thing that goes wrong is
// the step after both.
func newHarnessWithIssuer(
	t *testing.T,
	issuer signin.TokenIssuer,
	svcOpts []signin.ServiceOption,
	opts ...signingrpc.Option,
) *harness {
	t.Helper()

	return buildHarness(t, issuer, false, false, svcOpts, opts...)
}

// newRefreshHarness is newHarness with a live refresh token store behind the
// service, which is what a consumer who adopted rotation has. The RPC that
// exchanges one is unreachable without it.
func newRefreshHarness(t *testing.T, svcOpts []signin.ServiceOption, opts ...signingrpc.Option) *harness {
	t.Helper()

	return buildHarness(t, &fakeIssuer{}, true, false, svcOpts, opts...)
}

// newMagicLinkHarness is newRefreshHarness with the passwordless door wired in:
// a live magiclinks store and a mailer the test reads the secret out of.
//
// The mailer is the only way to get at that secret, which is the point: it goes
// to the person the account is about and never into a response, so a test plays
// the mail client exactly as TestRegisterThenVerifyThenSignIn does.
func newMagicLinkHarness(t *testing.T, svcOpts []signin.ServiceOption, opts ...signingrpc.Option) *harness {
	t.Helper()

	return buildHarness(t, &fakeIssuer{}, true, true, svcOpts, opts...)
}

// buildHarness is every constructor above. The refresh token store has to exist
// after the client it is built over and before the service that is handed it,
// which is the whole reason this is one function with a flag.
func buildHarness(
	t *testing.T,
	issuer signin.TokenIssuer,
	withRefresh, withMagicLinks bool,
	svcOpts []signin.ServiceOption,
	opts ...signingrpc.Option,
) *harness {
	t.Helper()

	db, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "signin.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	prefix := fmt.Sprintf("id_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(dialect.SQLite, prefix)
	must.NoError(t, err)

	for _, stmt := range stmts {
		_, execErr := db.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr)
	}

	store, err := identity.NewSQLStore(db, identity.WithTablePrefix(prefix))
	must.NoError(t, err)

	authenticator := argon2.NewArgon2Authenticator()

	identitySvc, err := identity.NewService(db, store)
	must.NoError(t, err)

	svcOpts = append([]signin.ServiceOption{
		signin.WithTOTPIssuer("Example"),
		signin.WithRegistrar(identitySvc),
		signin.WithVerifications(store),
	}, svcOpts...)

	if withRefresh {
		refreshStmts, stmtErr := refreshmigrations.Statements(dialect.SQLite, prefix)
		must.NoError(t, stmtErr)

		for _, stmt := range refreshStmts {
			_, execErr := db.Writer().ExecContext(t.Context(), stmt)
			must.NoError(t, execErr)
		}

		refreshStore, storeErr := refreshtokens.NewSQLStore(&refreshtokens.Config{TablePrefix: prefix}, db)
		must.NoError(t, storeErr)

		svcOpts = append(svcOpts, signin.WithRefreshTokenStore(refreshStore))
	}

	mailer := &recordingMailer{}

	if withMagicLinks {
		linkStmts, stmtErr := magiclinkmigrations.Statements(dialect.SQLite, prefix)
		must.NoError(t, stmtErr)

		for _, stmt := range linkStmts {
			_, execErr := db.Writer().ExecContext(t.Context(), stmt)
			must.NoError(t, execErr)
		}

		linkStore, storeErr := magiclinks.NewSQLStore(&magiclinks.Config{TablePrefix: prefix}, db)
		must.NoError(t, storeErr)

		svcOpts = append(svcOpts,
			signin.WithMagicLinkStore(linkStore),
			signin.WithMagicLinkMailer(mailer),
			// The floor holds every request for half a second, which this suite
			// cannot afford across a bufconn round trip. What it protects is
			// asserted in the service's own tests.
			signin.WithMagicLinkRequestFloor(time.Nanosecond),
		)
	}

	svc, err := signin.NewService(db, store, authenticator, issuer, svcOpts...)
	must.NoError(t, err)

	// The scope comes off the connection, which is the seam that exists because
	// a caller signing in has no principal to read it from.
	opts = append([]signingrpc.Option{
		signingrpc.WithScopeResolver(func(context.Context) (tenancy.Scope, error) { return testScope, nil }),
	}, opts...)

	srv, err := signingrpc.NewServer(svc, extractPrincipal, opts...)
	must.NoError(t, err)

	// The error-encoding interceptor is what puts a sentinel into the status
	// details, and the client's decoding one is what takes it out again. Without
	// both, every errors.Is below would fail against a *status.Error.
	grpcServer := grpc.NewServer(grpc.ChainUnaryInterceptor(
		grpcerrors.UnaryErrorEncodingInterceptor(),
		authenticate,
	))
	srv.RegisterOn(grpcServer)

	listener := bufconn.Listen(1 << 20)

	go func() { _ = grpcServer.Serve(listener) }()

	t.Cleanup(grpcServer.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		signinclient.DefaultInterceptors(),
	)
	must.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	h := &harness{
		mailer:    mailer,
		db:        db,
		store:     store,
		svc:       svc,
		directory: identitySvc,
		client:    signinclient.Wrap(conn),
		rootCtx:   t.Context(),
		password:  "correct horse battery staple",
	}

	h.register(t, authenticator)

	return h
}

// register creates the user every test signs in as.
func (h *harness) register(t *testing.T, authenticator *argon2.Argon2Authenticator) {
	t.Helper()

	hashed, err := authenticator.HashPassword(t.Context(), h.password)
	must.NoError(t, err)

	identitySvc, err := identity.NewService(h.db, h.store)
	must.NoError(t, err)

	registration, err := identitySvc.Register(t.Context(), testScope,
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

	h.user, h.accountID = registration.User, registration.Account.ID
}

// enrollTOTP gives the registered user a proven second factor and returns its
// secret.
func (h *harness) enrollTOTP(t *testing.T) string {
	t.Helper()

	enrollment, err := totp.NewGenerator().Generate(t.Context(), "Example", "jane")
	must.NoError(t, err)

	must.NoError(t, h.db.WithTransaction(t.Context(), func(tx database.Tx) error {
		if err = h.store.UpdateUserTwoFactorSecret(t.Context(), tx, testScope, h.user.ID, enrollment.Secret); err != nil {
			return err
		}

		_, err = h.store.MarkUserTwoFactorSecretVerified(t.Context(), tx, testScope, h.user.ID)

		return err
	}))

	return enrollment.Secret
}

// setServiceRoles grants the registered user service roles.
func (h *harness) setServiceRoles(t *testing.T, roles ...string) {
	t.Helper()

	must.NoError(t, h.db.WithTransaction(t.Context(), func(tx database.Tx) error {
		return h.store.SetUserServiceRoles(t.Context(), tx, testScope, h.user.ID, roles)
	}))
}

// totpCode produces a currently-valid TOTP code for a secret.
func totpCode(t *testing.T, secret string) string {
	t.Helper()

	value, err := pquernatotp.GenerateCode(secret, time.Now().UTC())
	must.NoError(t, err)

	return value
}

// asJane is a request context carrying the registered user as the caller.
func (h *harness) asJane() context.Context { return asUser(h.rootCtx, h.user.ID) }

// fakeIssuer stands in for a real tokens.Issuer.
type fakeIssuer struct{}

func (*fakeIssuer) IssueToken(
	_ context.Context,
	subject string,
	_ time.Duration,
	_ map[string]any,
) (token, jti string, err error) {
	return "token-for-" + subject, "jti-" + subject, nil
}

// errIssuerUnavailable is what a token issuer that cannot reach its signing key
// returns. It is an ordinary error naming no sentinel, because that is the
// shape of the failure the doors' default code answers: nothing maps it, so
// whatever the call site passed is what the caller gets.
var errIssuerUnavailable = platformerrors.New("signing key is unavailable")

// failingIssuer is a token issuer that is down.
type failingIssuer struct{}

func (*failingIssuer) IssueToken(
	_ context.Context,
	_ string,
	_ time.Duration,
	_ map[string]any,
) (token, jti string, err error) {
	return "", "", errIssuerUnavailable
}

// mailedSecrets is a random.Generator that hands back a token the test knows.
//
// It is what signin.WithSecretGenerator exists for, and it stands in for the
// one step of a registration that cannot happen over a connection: the
// verification link goes to somebody's inbox, and whoever clicks it is not the
// client that called Register. A test asserting the whole flow has to play the
// mail client, and this is how it reads the mail.
type mailedSecrets struct {
	secret string
}

var _ random.Generator = (*mailedSecrets)(nil)

func (m *mailedSecrets) GenerateBase64EncodedString(context.Context, int) (string, error) {
	return m.secret, nil
}

func (m *mailedSecrets) GenerateHexEncodedString(context.Context, int) (string, error) {
	return m.secret, nil
}

func (m *mailedSecrets) GenerateBase32EncodedString(context.Context, int) (string, error) {
	return m.secret, nil
}

func (m *mailedSecrets) GenerateRawBytes(context.Context, int) ([]byte, error) {
	return []byte(m.secret), nil
}

// recordingMailer keeps what the passwordless door handed it, so a test can play
// the mail client. The secret is never in a response, which is the property the
// door exists under.
type recordingMailer struct {
	sent []*signin.MagicLinkMail
	mu   sync.Mutex
}

var _ signin.MagicLinkMailer = (*recordingMailer)(nil)

func (m *recordingMailer) SendMagicLink(_ context.Context, mail *signin.MagicLinkMail) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.sent = append(m.sent, mail)

	return nil
}

func (m *recordingMailer) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()

	return len(m.sent)
}

// secret is the token most recently mailed, which is what a person clicking a
// link is holding.
func (m *recordingMailer) secret(tb testing.TB) string {
	tb.Helper()

	m.mu.Lock()
	defer m.mu.Unlock()

	must.SliceNotEmpty(tb, m.sent)

	return m.sent[len(m.sent)-1].Issuance.Secret
}

package grpc_test

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	signingrpc "github.com/primandproper/platform-go/v14/authentication/signin/grpc"
	signinclient "github.com/primandproper/platform-go/v14/authentication/signin/grpc/client"
	"github.com/primandproper/platform-go/v14/errormappers"
	"github.com/primandproper/platform-go/v14/identity"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	"github.com/primandproper/platform-go/v14/identity/migrations"

	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/authentication/totp"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
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

var _ identitygrpc.Principal = (*testPrincipal)(nil)

func (p *testPrincipal) UserID() string          { return p.userID }
func (p *testPrincipal) Scope() tenancy.Scope    { return p.scope }
func (p *testPrincipal) ActiveAccountID() string { return p.activeAccountID }

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

// asUser stamps a caller onto an outgoing request. A context built without it
// carries nobody, which is what makes the anonymous tests exercise the real
// path.
func asUser(ctx context.Context, userID string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, mdUserID, userID)
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

	return handler(context.WithValue(ctx, principalKey{}, principal), req)
}

// extractPrincipal is the PrincipalExtractor the server is built with. It reads
// what the interceptor above resolved and knows nothing about how.
func extractPrincipal(ctx context.Context) (identitygrpc.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(identitygrpc.Principal)

	return p, ok
}

// harness is one database, one service and one connected client.
type harness struct {
	db     database.Client
	store  identity.Store
	svc    *signin.Service
	client *signinclient.Client

	// rootCtx carries no credential. Every request context is built from it
	// rather than from the last one, because metadata appends: a context derived
	// from one that already names a caller ends up naming two.
	rootCtx context.Context

	password  string
	user      *identity.User
	accountID string
}

// newHarness stands the whole stack up and registers one user with a password.
func newHarness(t *testing.T, svcOpts []signin.ServiceOption, opts ...signingrpc.Option) *harness {
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

	svcOpts = append([]signin.ServiceOption{signin.WithTOTPIssuer("Example")}, svcOpts...)

	svc, err := signin.NewService(db, store, authenticator, &fakeIssuer{}, svcOpts...)
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
		db:       db,
		store:    store,
		svc:      svc,
		client:   signinclient.Wrap(conn),
		rootCtx:  t.Context(),
		password: "correct horse battery staple",
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

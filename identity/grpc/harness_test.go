package grpc_test

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/errormappers"
	"github.com/primandproper/platform-go/v14/identity"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	identityclient "github.com/primandproper/platform-go/v14/identity/grpc/client"
	"github.com/primandproper/platform-go/v14/identity/migrations"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/sqlite"
	grpcerrors "github.com/primandproper/primitives-go/errors/grpc"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

// The suite runs against a real SQLite database, a real identity.SQLStore and a
// real identity.Service, over a real gRPC connection on a bufconn.
//
// Mocks were the alternative and would have asserted less. What these tests are
// for is the seams between four things that each work on their own — the
// converters, the service's transaction, the store's SQL and the error mapping
// — and a mocked store answers for the store's half of every one of them. The
// cost is a migration per subtest, which SQLite does in microseconds.

// TestMain registers the domain tier's error mappers once for the binary.
//
// It is the only honest way to test them: the two registries are process-global,
// and registering inside each test would assert that appending the same mappers
// repeatedly is harmless rather than that appending them once is enough. It is
// also exactly the call a consumer owes — see identity/grpc's package doc — so a
// suite that omitted it would be testing a mounting nobody should perform.
func TestMain(m *testing.M) {
	errormappers.Register()
	m.Run()
}

// The directories the suite uses. otherScope is the neighbor whose rows must
// never appear in testScope's answers.
var (
	testScope  = tenancy.Of("dir_1")
	otherScope = tenancy.Of("dir_2")
)

// prefixCounter names a fresh set of tables per subtest, since a directory read
// is global to the users table within a scope.
var prefixCounter atomic.Uint64

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

// The metadata the suite's "credential" travels in.
//
// It is metadata rather than a context value because a context value does not
// cross a connection, and a test that set one on the client would be asserting
// nothing about the seam: a real consumer's authentication interceptor reads a
// token off the metadata and resolves a principal from it, and this is that with
// the token replaced by the answer.
const (
	mdUserID    = "test-user-id"
	mdScope     = "test-scope"
	mdAccountID = "test-account-id"
)

// withPrincipal stamps the caller onto an outgoing request, standing in for
// whatever mints a consumer's credential.
func withPrincipal(ctx context.Context, p identitygrpc.Principal) context.Context {
	if p == nil {
		return ctx
	}

	return metadata.AppendToOutgoingContext(ctx,
		mdUserID, p.UserID(),
		mdScope, p.Scope().Owner(),
		mdAccountID, p.ActiveAccountID(),
	)
}

// authenticate is the consumer's authentication interceptor: it reads the
// credential off the metadata and puts a principal on the context.
//
// A request carrying none gets none, which is what makes the anonymous-caller
// test exercise the real path rather than a flag.
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

	principal := &testPrincipal{userID: userIDs[0], scope: tenancy.Global()}

	if owners := md.Get(mdScope); len(owners) > 0 && owners[0] != "" {
		principal.scope = tenancy.Of(owners[0])
	}

	if accounts := md.Get(mdAccountID); len(accounts) > 0 {
		principal.activeAccountID = accounts[0]
	}

	return handler(context.WithValue(ctx, principalKey{}, principal), req)
}

// extractPrincipal is the PrincipalExtractor the server is built with. It reads
// what the interceptor above resolved and knows nothing about how.
func extractPrincipal(ctx context.Context) (identitygrpc.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(identitygrpc.Principal)

	return p, ok
}

// harness is one database, one store, one service and one connected client.
type harness struct {
	store identity.Store
	db    database.Client

	// rootCtx carries no credential. Every request context is built from it
	// rather than from the last one, because metadata appends: a context derived
	// from one that already names a caller ends up naming two, and the server
	// reads the first.
	rootCtx   context.Context
	principal identitygrpc.Principal
	client    *identityclient.Client

	// conn is the same connection the client wraps, for the one test that
	// invokes every RPC off the service descriptor rather than by name.
	conn *grpc.ClientConn

	// listener is the server's, so a test can open a second connection of its
	// own — the one that declines the default interceptors, and has to reach the
	// same server to be asserting anything.
	listener *bufconn.Listener
	svc      *identity.Service
	scope    tenancy.Scope
}

// newHarness stands the whole stack up.
//
// The principal it injects is the same one every request in a test carries
// unless the test overrides it, which is what keeps the tests about the RPCs
// rather than about assembling a caller each time.
func newHarness(t *testing.T, opts ...identitygrpc.Option) *harness {
	t.Helper()

	return newHarnessAs(t, &testPrincipal{userID: "caller", scope: testScope}, opts...)
}

func newHarnessAs(t *testing.T, principal identitygrpc.Principal, opts ...identitygrpc.Option) *harness {
	t.Helper()

	db, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "identity.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	prefix := migrate(t, db)

	store, err := identity.NewSQLStore(db, identity.WithTablePrefix(prefix))
	must.NoError(t, err)

	svc, err := identity.NewService(db, store)
	must.NoError(t, err)

	srv, err := identitygrpc.NewServer(svc, store, db, extractPrincipal, opts...)
	must.NoError(t, err)

	// The error-encoding interceptor is what puts a sentinel into the status
	// details, and the client's decoding one is what takes it out again. Without
	// both, every errors.Is below would fail against a *status.Error and the
	// suite would be asserting the wrong thing about a mapping that works.
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
		identityclient.DefaultInterceptors(),
	)
	must.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	return &harness{
		client:    identityclient.Wrap(conn),
		conn:      conn,
		listener:  listener,
		store:     store,
		svc:       svc,
		db:        db,
		scope:     testScope,
		rootCtx:   t.Context(),
		principal: principal,
	}
}

// dial opens a second connection to the same server, with whatever dial options
// the caller asks for and none of its own.
func (h *harness) dial(t *testing.T, opts ...grpc.DialOption) *grpc.ClientConn {
	t.Helper()

	dialOptions := append([]grpc.DialOption{
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return h.listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}, opts...)

	conn, err := grpc.NewClient("passthrough:///bufnet", dialOptions...)
	must.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	return conn
}

// ctx is the context a request goes out on: the harness's caller on it.
func (h *harness) ctx() context.Context { return withPrincipal(h.rootCtx, h.principal) }

// as returns a context carrying a different caller.
func (h *harness) as(p identitygrpc.Principal) context.Context {
	return withPrincipal(h.rootCtx, p)
}

// seedUser writes a user directly through the store, for the tests whose subject
// is a read rather than a registration.
func (h *harness) seedUser(t *testing.T, scope tenancy.Scope, username string) *identity.User {
	t.Helper()

	var user *identity.User

	must.NoError(t, h.db.WithTransaction(t.Context(), func(tx database.Tx) error {
		created, err := h.store.CreateUser(t.Context(), tx, scope, &identity.User{
			Username:      username,
			EmailAddress:  username + "@example.com",
			AccountStatus: identity.StatusGood,
		})
		user = created

		return err
	}))

	return user
}

// seedAccount registers a user with an account they own, which is the only way
// to get a well-formed one.
func (h *harness) seedAccount(t *testing.T, scope tenancy.Scope, username string) *identity.Registration {
	t.Helper()

	registration, err := h.svc.Register(t.Context(), scope,
		&identity.User{Username: username, EmailAddress: username + "@example.com"},
		&identity.Account{Name: username + "'s account"},
		[]string{"owner"},
	)
	must.NoError(t, err)

	return registration
}

// seedMembership puts a user in an account directly through the store, for the
// tests whose subject is a membership rather than the registration that would
// otherwise be the only way to mint one.
func (h *harness) seedMembership(
	t *testing.T,
	scope tenancy.Scope,
	userID, accountID string,
	roles ...string,
) *identity.Membership {
	t.Helper()

	var membership *identity.Membership

	must.NoError(t, h.db.WithTransaction(t.Context(), func(tx database.Tx) error {
		created, err := h.store.CreateMembership(t.Context(), tx, scope, &identity.Membership{
			BelongsToUser:    userID,
			BelongsToAccount: accountID,
			Roles:            roles,
		})
		membership = created

		return err
	}))

	return membership
}

func migrate(t *testing.T, db database.Client) string {
	t.Helper()

	prefix := fmt.Sprintf("id_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(dialect.SQLite, prefix)
	must.NoError(t, err)
	must.SliceNotEmpty(t, stmts)

	for _, stmt := range stmts {
		_, execErr := db.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}

	return prefix
}

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

// verifyEmail proves a seeded user's address the way the sign-in flow would:
// a verification token is set and then redeemed, through the store. The reads
// keyed by an address refuse a caller whose address is unverified, so a test
// exercising one of those has to do this first.
func (h *harness) verifyEmail(t *testing.T, scope tenancy.Scope, userID string) {
	t.Helper()

	const token = "verification-token-" + "for-the-harness"

	must.NoError(t, h.db.WithTransaction(t.Context(), func(tx database.Tx) error {
		if err := h.store.SetUserEmailAddressVerificationToken(t.Context(), tx, scope, userID, token); err != nil {
			return err
		}

		return h.store.MarkUserEmailAddressVerified(t.Context(), tx, scope, userID, token)
	}))
}

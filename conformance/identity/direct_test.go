package identity_test

import (
	"context"
	"fmt"
	"net"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/conformance"
	conformanceall "github.com/primandproper/platform-go/v14/conformance/all"
	"github.com/primandproper/platform-go/v14/errormappers"
	"github.com/primandproper/platform-go/v14/identity"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	identityclient "github.com/primandproper/platform-go/v14/identity/grpc/client"
	"github.com/primandproper/platform-go/v14/identity/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

// TestMain registers the domain tier's error mappers, which is the call a
// consumer owes at their composition root and the one that decides whether a
// sentinel reaches a client as its mapped code or as Internal.
func TestMain(m *testing.M) {
	errormappers.Register()
	m.Run()
}

// The metadata this harness's stand-in credential travels in.
//
// Metadata rather than a context value, because a context value does not cross
// a connection and a harness that set one on the client would be proving
// nothing about the seam. A real consumer's interceptor reads a token off the
// metadata and resolves a principal from it; this is that, with the token
// replaced by the answer.
const (
	mdUserID  = "conformance-user-id"
	mdScope   = "conformance-scope"
	mdAccount = "conformance-account-id"
)

var prefixCounter atomic.Uint64

// TestConformance_Direct runs every suite against a hand-built identity server
// on a bufconn over SQLite.
//
// It runs conformanceall rather than the identity suite alone, deliberately.
// The cross-cutting suites read whichever surfaces a subject mounted, so a
// subject mounting one surface asserts that surface's share of them — which is
// how the anonymous suite's reading of identity's thirty-one RPCs gets executed
// rather than merely compiled.
func TestConformance_Direct(T *testing.T) {
	T.Parallel()

	db := newDatabase(T)
	prefix := migrate(T, db)

	store, err := identity.NewSQLStore(db, identity.WithTablePrefix(prefix))
	must.NoError(T, err)

	svc, err := identity.NewService(db, store)
	must.NoError(T, err)

	srv, err := identitygrpc.NewServer(svc, store, db, extractPrincipal)
	must.NoError(T, err)

	conn := serve(T, srv)
	client := identityclient.Wrap(conn)

	conformanceall.Run(T, conformance.Seams{
		NewSubject: func(ctx context.Context, opts ...conformance.SubjectOption) (*conformance.Subject, error) {
			req := conformance.NewSubjectRequest(opts...)

			// No notion of an administrative caller in this harness: service
			// roles are a deployment's to define, and a stand-in that invented
			// one would assert against a rule nobody wrote. Declining is what
			// makes those assertions skip rather than pass against a fiction.
			if req.Admin {
				return nil, conformance.ErrSubjectUnsupported
			}

			scope := tenancy.Of(identifiers.New())
			if req.Scope != nil {
				scope = *req.Scope
			}

			// Registered through the service rather than the surface, because
			// every one of identity's RPCs requires a caller — Register
			// included — so there is no client-only way to mint the first one.
			// The service is what a consumer's own registration handler calls.
			reg, registerErr := svc.Register(ctx, scope,
				&identity.User{
					Username:     "conf_" + identifiers.New(),
					EmailAddress: identifiers.New() + "@conformance.invalid",
				},
				&identity.Account{Name: "conf_" + identifiers.New()},
				// The role names are the consumer's, which identity says of
				// them explicitly — so this harness picks one rather than
				// finding a canonical one, and nothing here asserts against it.
				[]string{"account_admin"})
			if registerErr != nil {
				return nil, registerErr
			}

			return &conformance.Subject{
				Scope:    scope,
				UserID:   reg.User.ID,
				Conn:     conn,
				Surfaces: conformance.Surfaces{Identity: client},
				Decorate: func(ctx context.Context) context.Context {
					return metadata.NewOutgoingContext(ctx, metadata.Pairs(
						mdUserID, reg.User.ID,
						mdScope, scope.String(),
						mdAccount, reg.Account.ID,
					))
				},
			}, nil
		},

		// The same connection, called without the metadata that carries a
		// caller. That is legitimate here because this harness's credential is
		// per-call; a deployment carrying one on the connection would open a
		// second connection instead, which is why the seam returns one rather
		// than taking a context.
		Actions: conformance.Actions{
			// Written through the store, which is where this module's own
			// sign-in flow writes one. A consumer implements the same seam by
			// calling their password path; either way the secret in the column
			// is the deployment's, which is what makes searching a response for
			// it meaningful.
			Credentialed: func(ctx context.Context, scope tenancy.Scope, userID string) (string, error) {
				// A real argon2id hash rather than a placeholder, so this
				// asserts against a row that actually holds a secret.
				const hash = "$argon2id$v=19$m=65536,t=3,p=2$Y29uZm9ybWFuY2U$notarealsecret"

				if writeErr := db.WithTransaction(context.WithoutCancel(ctx), func(tx database.Tx) error {
					return store.UpdateUserPassword(ctx, tx, scope, userID, hash)
				}); writeErr != nil {
					return "", writeErr
				}

				return "notarealsecret", nil
			},
		},

		Anonymous: func(context.Context) (grpc.ClientConnInterface, error) {
			return conn, nil
		},

		Dialect:           dialect.SQLite,
		ExclusiveDatabase: true,
	})
}

// authenticate is this harness's stand-in for a consumer's authentication
// interceptor: it turns the metadata a caller sent into a principal on the
// request context, and leaves the context alone when there is none.
//
// Leaving it alone rather than refusing is the important half. Whether an
// anonymous request is refused is each surface's decision to make, and an
// interceptor that refused here would be answering for all of them — which
// would make the anonymous suite assert this function instead of the module.
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

	if accounts := md.Get(mdAccount); len(accounts) > 0 {
		principal.activeAccountID = accounts[0]
	}

	return handler(context.WithValue(ctx, principalKey{}, principal), req)
}

type principalKey struct{}

func extractPrincipal(ctx context.Context) (callers.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(callers.Principal)

	return p, ok
}

type testPrincipal struct {
	userID          string
	activeAccountID string
	scope           tenancy.Scope
}

var _ callers.Principal = (*testPrincipal)(nil)

func (p *testPrincipal) UserID() string          { return p.userID }
func (p *testPrincipal) Scope() tenancy.Scope    { return p.scope }
func (p *testPrincipal) ActiveAccountID() string { return p.activeAccountID }

func serve(t *testing.T, srv *identitygrpc.Server) *grpc.ClientConn {
	t.Helper()

	grpcServer := grpc.NewServer(grpc.ChainUnaryInterceptor(
		grpcerrors.UnaryErrorEncodingInterceptor(), authenticate,
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

	return conn
}

func migrate(t *testing.T, db database.Client) string {
	t.Helper()

	prefix := fmt.Sprintf("conf_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(dialect.SQLite, prefix)
	must.NoError(t, err)
	must.SliceNotEmpty(t, stmts)

	for _, stmt := range stmts {
		_, execErr := db.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}

	return prefix
}

func newDatabase(t *testing.T) database.Client {
	t.Helper()

	db, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "conformance.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	return db
}

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

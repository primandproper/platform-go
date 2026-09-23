package audit_test

import (
	"context"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/audit"
	auditgrpc "github.com/primandproper/platform-go/v14/audit/grpc"
	auditclient "github.com/primandproper/platform-go/v14/audit/grpc/client"
	"github.com/primandproper/platform-go/v14/audit/migrations"
	"github.com/primandproper/platform-go/v14/conformance"
	conformanceaudit "github.com/primandproper/platform-go/v14/conformance/audit"
	"github.com/primandproper/platform-go/v14/errormappers"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

// TestMain registers the domain tier's error mappers, which is the call a
// consumer owes at their composition root and the one that decides whether a
// sentinel reaches a client as its mapped code or as Internal.
func TestMain(m *testing.M) {
	errormappers.Register()
	m.Run()
}

// mdScope is how this harness puts a tenant on a connection. What carries the
// scope is the consumer's — audit/grpc's ScopeResolver says so — so a harness
// picking metadata is picking one of the several right answers, not the answer.
const mdScope = "conformance-scope"

// auditedResourceType is what this harness's auditable action touches. One
// type for every action, which is what lets the paging assertion narrow to the
// rows it produced.
const auditedResourceType = "conformance_audited"

// TestConformance_Direct runs the shared suite against a hand-built server on a
// loopback TCP listener over SQLite.
//
// This is the fast mode, and the point of it is that it is not a reduced one:
// the client is real, the connection is real, and both interceptors are in the
// path, so an assertion that passes here is an assertion about what a client
// sees. What it does not prove is assembly — this server was built by this
// function rather than by service.New — which is the other mode's job.
func TestConformance_Direct(T *testing.T) {
	T.Parallel()

	db := newDatabase(T)

	stmts, err := migrations.Statements(dialect.SQLite, audit.DefaultTablePrefix)
	must.NoError(T, err)

	for _, stmt := range stmts {
		_, execErr := db.Writer().ExecContext(T.Context(), stmt)
		must.NoError(T, execErr)
	}

	reader, err := audit.NewReader(db.Dialect())
	must.NoError(T, err)

	recorder, err := audit.NewRecorder(db.Dialect())
	must.NoError(T, err)

	srv, err := auditgrpc.NewServer(reader, db, auditgrpc.WithScopeResolver(scopeFromMetadata))
	must.NoError(T, err)

	client := serve(T, srv)

	conformance.Run(T, conformance.Seams{
		NewSubject: func(_ context.Context, opts ...conformance.SubjectOption) (*conformance.Subject, error) {
			req := conformance.NewSubjectRequest(opts...)

			// No notion of an administrative caller here: this surface reads a
			// scope off the connection and nothing else, so a caller holding a
			// service role would be the same caller. Declining is what makes
			// the suite skip rather than assert against a difference that does
			// not exist.
			if req.Admin {
				return nil, conformance.ErrSubjectUnsupported
			}

			scope := tenancy.Of(identifiers.New())
			if req.Scope != nil {
				scope = *req.Scope
			}

			return &conformance.Subject{
				Scope:    scope,
				Surfaces: conformance.Surfaces{Audit: client},
				Decorate: func(ctx context.Context) context.Context {
					return metadata.NewOutgoingContext(ctx,
						metadata.Pairs(mdScope, scope.String()))
				},
			}, nil
		},
		Actions: conformance.Actions{
			// This harness's auditable action is a recording, because nothing
			// in this module records one on its own: no gRPC surface here calls
			// audit.Recorder, since a recording belongs inside the transaction
			// of the change it describes and this module does not own that
			// transaction.
			//
			// So this is not a shortcut past the path a consumer exercises — it
			// is the end of that path, which is all of it that lives here. A
			// consumer implements the same seam by creating a row through one
			// of their own handlers, and the assertions do not know or care
			// which of the two happened.
			Auditable: func(ctx context.Context, scope tenancy.Scope) (*conformance.Audited, error) {
				entry := &audit.Entry{
					Scope:        scope,
					EventType:    audit.EventType("conformance.acted"),
					ResourceType: auditedResourceType,
					ResourceID:   identifiers.New(),
					Actor:        audit.Actor{ID: identifiers.New(), Type: audit.ActorUser},
				}

				// Stripped of the call metadata a client would carry: this
				// write is the recorder's, inside a transaction, and not an RPC.
				if recordErr := db.WithTransaction(context.WithoutCancel(ctx), func(tx database.Tx) error {
					return recorder.Record(ctx, tx, scope, entry)
				}); recordErr != nil {
					return nil, recordErr
				}

				return &conformance.Audited{
					ResourceType: entry.ResourceType,
					ResourceID:   entry.ResourceID,
					ActorID:      entry.Actor.ID,
				}, nil
			},
		},
		Dialect: dialect.SQLite,

		// One database, stood up by this function, with nothing else writing
		// to it.
		ExclusiveDatabase: true,
	}, conformanceaudit.Suite())
}

// scopeFromMetadata is this harness's half of "the tenant travels on the
// connection".
func scopeFromMetadata(ctx context.Context) (tenancy.Scope, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return tenancy.Scope{}, errNoScope
	}

	values := md.Get(mdScope)
	if len(values) == 0 || values[0] == "" {
		return tenancy.Scope{}, errNoScope
	}

	return tenancy.Of(values[0]), nil
}

var errNoScope = platformerrors.New("conformance harness: the connection names no scope")

func serve(t *testing.T, srv *auditgrpc.Server) *auditclient.Client {
	t.Helper()

	grpcServer := grpc.NewServer(
		grpc.ChainUnaryInterceptor(grpcerrors.UnaryErrorEncodingInterceptor()))
	srv.RegisterOn(grpcServer)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must.NoError(t, err)

	go func() { _ = grpcServer.Serve(listener) }()

	t.Cleanup(grpcServer.Stop)

	conn, err := grpc.NewClient(listener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		auditclient.DefaultInterceptors(),
	)
	must.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	return auditclient.Wrap(conn)
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

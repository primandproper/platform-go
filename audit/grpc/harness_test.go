package grpc_test

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
	"github.com/primandproper/platform-go/v14/errormappers"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/sqlite"
	platformerrors "github.com/primandproper/primitives-go/errors"
	grpcerrors "github.com/primandproper/primitives-go/errors/grpc"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test/must"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

// The suite runs against a real SQLite database, a real recorder writing a real
// hash chain, a real reader and a real gRPC connection on a bufconn.
//
// What these tests are for is the seams: the converters, the scope binding, and
// the error mapping that turns an entry belonging to somebody else into the same
// answer as one that was never written. A mocked reader would answer for the
// half of every one of those that is hardest to get right — in particular it
// would let a cross-scope read pass, since the mock would return whatever the
// test told it to.

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

// The two tenants the suite writes for, and the scope the resolver hands a
// request that named neither.
var (
	ours   = tenancy.Of("acct_ours")
	theirs = tenancy.Of("acct_theirs")
)

// mdScope is the metadata the suite's stand-in for a connection-borne scope
// travels in.
//
// Metadata rather than a context value, because a context value does not cross
// a connection: a real consumer's resolver reads a host, a certificate or a
// token's claim, and this is that with the lookup replaced by the answer.
const mdScope = "test-scope"

// The two values that make the resolver misbehave on purpose.
const (
	scopeUnplaceable = "unplaceable"
	scopeUnset       = "unset"
)

// asScope stamps the scope a request is against onto an outgoing context.
func asScope(ctx context.Context, scope string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, mdScope, scope)
}

// resolveScope is the consumer's half of the tenancy seam, as small as the
// signature allows: it reads the connection and knows nothing about the request.
func resolveScope(ctx context.Context) (tenancy.Scope, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return tenancy.Scope{}, errNoScopeOnConnection
	}

	values := md.Get(mdScope)
	if len(values) == 0 || values[0] == "" {
		return tenancy.Scope{}, errNoScopeOnConnection
	}

	switch values[0] {
	case scopeUnplaceable:
		return tenancy.Scope{}, errNoScopeOnConnection
	case scopeUnset:
		// A resolver that answers without an error and without a scope, which
		// is the mistake the server validates against rather than trusting.
		return tenancy.Scope{}, nil
	default:
		return tenancy.Of(values[0]), nil
	}
}

// errNoScopeOnConnection is what a resolver returns for a request it cannot
// place. It is a test's error rather than one of this module's: what carries
// the scope is the consumer's, and so is the failure to find it.
var errNoScopeOnConnection = platformerrors.New("no scope on the connection")

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

// harness is one database, one reader and one connected client.
type harness struct {
	db     database.Client
	reader *audit.SQLReader
	client *auditclient.Client

	// rootCtx carries no scope. Every request context is built from it rather
	// than from the last one, because metadata appends: a context derived from
	// one that already names a scope ends up naming two.
	rootCtx context.Context

	// mine and yours are one entry in each tenant's log, recorded in that
	// order, so every test has an id it may read and an id it may not.
	mine  *audit.Entry
	yours *audit.Entry
}

// newHarness stands the whole stack up and records one entry per tenant.
func newHarness(t *testing.T, opts ...auditgrpc.Option) *harness {
	t.Helper()

	db, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "audit.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	stmts, err := migrations.Statements(dialect.SQLite, audit.DefaultTablePrefix)
	must.NoError(t, err)

	for _, stmt := range stmts {
		_, execErr := db.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr)
	}

	recorder, err := audit.NewRecorder(dialect.SQLite)
	must.NoError(t, err)

	reader, err := audit.NewReader(db)
	must.NoError(t, err)

	srv, err := auditgrpc.NewServer(reader, resolveScope, opts...)
	must.NoError(t, err)

	// The error-encoding interceptor is what puts a sentinel into the status
	// details, and the client's decoding one is what takes it out again.
	// Without both, every errors.Is below would fail against a *status.Error.
	grpcServer := grpc.NewServer(grpc.ChainUnaryInterceptor(grpcerrors.UnaryErrorEncodingInterceptor()))
	srv.RegisterOn(grpcServer)

	listener := bufconn.Listen(1 << 20)

	go func() { _ = grpcServer.Serve(listener) }()

	t.Cleanup(grpcServer.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		auditclient.DefaultInterceptors(),
	)
	must.NoError(t, err)
	t.Cleanup(func() { _ = conn.Close() })

	h := &harness{
		db:      db,
		reader:  reader,
		client:  auditclient.Wrap(conn),
		rootCtx: t.Context(),
		mine:    entryFor(ours, "recipe_1"),
		yours:   entryFor(theirs, "recipe_2"),
	}

	must.NoError(t, db.WithTransaction(t.Context(), func(tx database.Tx) error {
		return recorder.Record(t.Context(), tx, h.mine, h.yours)
	}))

	return h
}

// record writes one more entry, for the tests that need a second one.
func (h *harness) record(t *testing.T, entries ...*audit.Entry) {
	t.Helper()

	recorder, err := audit.NewRecorder(dialect.SQLite)
	must.NoError(t, err)

	must.NoError(t, h.db.WithTransaction(t.Context(), func(tx database.Tx) error {
		return recorder.Record(t.Context(), tx, entries...)
	}))
}

// asOurs is a request context carrying the tenant every read below is entitled
// to, and asTheirs one carrying the other.
func (h *harness) asOurs() context.Context   { return asScope(h.rootCtx, ours.Owner()) }
func (h *harness) asTheirs() context.Context { return asScope(h.rootCtx, theirs.Owner()) }

// entryFor builds a minimally valid entry for a scope.
func entryFor(scope tenancy.Scope, resourceID string) *audit.Entry {
	return &audit.Entry{
		EventType:    audit.EventUpdated,
		ResourceType: "recipe",
		ResourceID:   resourceID,
		Scope:        scope.Owner(),
		Actor:        audit.Actor{ID: "user_1", Type: audit.ActorUser, IP: "203.0.113.7"},
		Changes:      map[string]audit.Change{"name": {Old: "Soup", New: "Stew"}},
		Metadata:     map[string]string{"reason": "a typo"},
	}
}

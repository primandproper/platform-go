package grpc_test

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/errormappers"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	"github.com/primandproper/platform-go/v14/webhooks"
	webhooksgrpc "github.com/primandproper/platform-go/v14/webhooks/grpc"
	"github.com/primandproper/platform-go/v14/webhooks/migrations"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test/must"
	"google.golang.org/protobuf/proto"
)

// The suite runs the server's methods in process, against a real SQLite
// database, a real webhooks.SQLStore and a real webhooks.StoreDispatcher.
//
// In process rather than over a bufconn, as the OAuth2 client registry's suite
// runs and for the same reason: what these tests are about is which rows a
// caller reaches and what the handler decides to send back, and a connection
// would add a consumer's interceptor to the things under test without adding
// anything to either decision. The database is real for the opposite reason —
// the scoping here is in the statements, and a mocked store would be answering
// the question the tests are asking.

// TestMain registers the domain tier's error mappers once for the binary.
//
// Without it this suite would assert the codes each handler passes as its
// *default* rather than the ones a client reads. Every handler here hands
// PrepareAndLogGRPCStatus codes.Internal on purpose — the registered mapper is
// what turns a refused URL into InvalidArgument over the preserved chain — so a
// suite that skipped the registration would pin Internal as the answer to "that
// is not an https address" and pass.
//
// It is also exactly the call a consumer owes at their composition root, which
// is the other reason it belongs here rather than inside a test.
func TestMain(m *testing.M) {
	errormappers.Register()
	m.Run()
}

// The tenants these tests register endpoints in, and the neighbor whose rows
// must never appear in the first one's answers.
//
// The second exists because the scope on this surface comes off the principal
// rather than off the request, so the only way to show that a read is keyed on
// it is to ask for the same row from a caller the extractor puts somewhere else.
var (
	testScope  = tenancy.Of("acct_1")
	otherScope = tenancy.Of("acct_2")
)

const (
	testUser  = "user_1"
	otherUser = "user_2"
)

// The catalog these tests publish. It is two entries because one of them has to
// be absent from a subscription request for the catalog gate to be visible.
const (
	orderCreated webhooks.EventType = "order.created"
	orderShipped webhooks.EventType = "order.shipped"
	uncataloged  webhooks.EventType = "order.reticulated"
)

func testCatalog() webhooks.Catalog {
	return webhooks.Catalog{
		orderCreated: {Description: "an order was placed"},
		orderShipped: {Description: "an order left the warehouse"},
	}
}

// testURL is an address the suite's URL checker accepts. It is not resolved:
// see permissiveURLChecker.
const testURL = "https://subscriber.example/hooks"

// testKeys is the smallest keyring a save is accepted with.
func testKeys() *webhookspb.WebhookSigningKeys {
	return &webhookspb.WebhookSigningKeys{Current: []byte("current-signing-key")}
}

// endpointInput is the smallest endpoint the dispatcher accepts.
func endpointInput() *webhookspb.WebhookEndpointInput {
	return &webhookspb.WebhookEndpointInput{
		Name:       "test subscriber",
		Url:        testURL,
		EventTypes: []string{string(orderCreated)},
	}
}

// permissiveURLChecker replaces webhooks.CheckEndpointURL for the suite.
//
// The real one resolves the host and refuses anything that is not publicly
// routable, which would make every test here a DNS test as well and would refuse
// example.test outright. What the suite still wants is a checker that refuses
// something, so the two tests about a rejected URL supply their own.
func permissiveURLChecker(context.Context, string) error { return nil }

// testClientConfig is the minimal database.ClientConfig these tests dial with.
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

// prefixCounter names a fresh set of tables per subtest.
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
// puts the principal.
type principalKey struct{}

// withPrincipal is what a consumer's interceptor does, with the credential
// reading step removed. A context carrying none reaches the server as an
// anonymous request.
func withPrincipal(ctx context.Context, p identitygrpc.Principal) context.Context {
	if p == nil {
		return ctx
	}

	return context.WithValue(ctx, principalKey{}, p)
}

// extractPrincipal is the PrincipalExtractor the server is built with. It reads
// what withPrincipal put there and knows nothing about how.
func extractPrincipal(ctx context.Context) (identitygrpc.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(identitygrpc.Principal)

	return p, ok
}

// harness is one database, one store, one dispatcher and one server over them.
type harness struct {
	db         database.Client
	store      *webhooks.SQLStore
	dispatcher *webhooks.StoreDispatcher
	server     *webhooksgrpc.Server
}

// newHarness migrates a uniquely prefixed set of tables and builds the surface
// over them.
//
// SQLite gets a database of its own per harness rather than a prefix in a shared
// one: DDL invalidates every prepared statement on the whole database, so one
// parallel subtest creating its tables makes another's next read fail with
// "database schema has changed" whatever prefix either is using.
func newHarness(tb testing.TB, opts ...webhooks.DispatcherOption) *harness {
	tb.Helper()

	db, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "webhooks.db")})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = db.Close() })

	prefix := fmt.Sprintf("whg_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(dialect.SQLite, prefix)
	must.NoError(tb, err)
	must.SliceNotEmpty(tb, stmts)

	for _, stmt := range stmts {
		_, execErr := db.Writer().ExecContext(tb.Context(), stmt)
		must.NoError(tb, execErr, must.Sprintf("executing %q", stmt))
	}

	store, err := webhooks.NewSQLStore(db, webhooks.WithTablePrefix(prefix))
	must.NoError(tb, err)

	dispatcher, err := webhooks.NewDispatcher(store, db.Reader(),
		append([]webhooks.DispatcherOption{
			webhooks.WithCatalog(testCatalog()),
			webhooks.WithDispatcherURLChecker(permissiveURLChecker),
		}, opts...)...)
	must.NoError(tb, err)

	server, err := webhooksgrpc.NewServer(dispatcher, store, db, extractPrincipal)
	must.NoError(tb, err)

	return &harness{db: db, store: store, dispatcher: dispatcher, server: server}
}

// ctx is a request context carrying a caller in testScope.
func (h *harness) ctx(tb testing.TB) context.Context {
	tb.Helper()

	return withPrincipal(tb.Context(), &testPrincipal{userID: testUser, scope: testScope})
}

// otherCtx is a request context carrying the neighboring tenant's caller, which
// is how every scoping assertion here is made.
func (h *harness) otherCtx(tb testing.TB) context.Context {
	tb.Helper()

	return withPrincipal(tb.Context(), &testPrincipal{userID: otherUser, scope: otherScope})
}

// seed registers one endpoint directly through the dispatcher, so a test
// asserting what a caller can reach does not reach it through the surface under
// test.
func (h *harness) seed(tb testing.TB, scope tenancy.Scope, events ...webhooks.EventType) *webhooks.Endpoint {
	tb.Helper()

	if len(events) == 0 {
		events = []webhooks.EventType{orderCreated}
	}

	endpoint := &webhooks.Endpoint{
		Name:          "seeded subscriber",
		URL:           testURL,
		Secret:        webhooks.Secret{Current: []byte("seeded-signing-key")},
		Subscriptions: webhooks.SubscribeTo(events...),
	}

	// The registered row rather than the argument: Register settles the
	// identifier and the content type on a copy, so a seed that handed back what
	// it passed in would hand back an endpoint with no ID.
	var registered *webhooks.Endpoint

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		var registerErr error
		registered, registerErr = h.dispatcher.Register(tb.Context(), tx, scope, endpoint)

		return registerErr
	}))

	return registered
}

// messageCarries reports whether the encoded form of a message contains the
// given bytes.
//
// It is the broadest available reading of "not readable through the surface":
// asserting on the fields a converter happens to set would pass a message that
// grew a new one, and this looks at what actually goes on the wire.
func messageCarries(tb testing.TB, message proto.Message, needle []byte) bool {
	tb.Helper()

	if len(needle) == 0 {
		return false
	}

	encoded, err := proto.Marshal(message)
	must.NoError(tb, err)

	return bytes.Contains(encoded, needle)
}

package grpc_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/errormappers"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	"github.com/primandproper/platform-go/v14/notifications"
	notificationsgrpc "github.com/primandproper/platform-go/v14/notifications/grpc"
	"github.com/primandproper/platform-go/v14/notifications/migrations"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/sqlite"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test/must"
)

// The suite runs the server's methods in process, against a real SQLite
// database and a real notifications.SQLStore.
//
// In process rather than over a bufconn, as authentication/oauth2clients/grpc's
// suite is and for the same reason: what these tests are about is which rows a
// handler decides the caller may reach, and a connection would add a consumer's
// interceptor to the things under test without adding anything to the decision.
// The database is real for the opposite reason — the refusals here are about
// which rows a caller reaches, and a mocked store answers that question itself.

// TestMain registers the domain tier's error mappers once for the binary.
//
// Without it this suite would assert the codes each handler passes as its
// *default* rather than the ones a client reads. Every store failure reaches
// PrepareAndLogGRPCStatus as codes.Internal on purpose, and the registered
// mapper is what turns an absent notification into NotFound, so a suite that
// skipped the registration would pin Internal as the answer to "no such
// notification" and pass.
//
// It is also exactly the call a consumer owes at their composition root, which
// is the other reason it belongs here rather than inside a test.
func TestMain(m *testing.M) {
	errormappers.Register()
	m.Run()
}

// The two directories these tests work in, and the two people in the first.
//
// Both pairs exist for the same reason: the scope and the recipient come off the
// principal rather than off a request, so the only way to show a read is keyed
// on either is to ask for the same row as somebody the extractor puts elsewhere.
var (
	testScope  = tenancy.Of("acct_1")
	otherScope = tenancy.Of("acct_2")
)

const (
	testPrincipal  = "user_1"
	otherPrincipal = "user_2"
)

// testToken is a device token long enough to look like one and constant enough
// to assert convergence on.
const testToken = "apns-token-1"

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

// prefixCounter names a fresh pair of tables per subtest, since a device token
// carries a unique index across every directory and subtests share one database.
var prefixCounter atomic.Uint64

// testPrincipalValue is the consumer's half of the principal seam, as small as
// the interface allows. A userID of "" is the whole point of one of these tests:
// it is what a consumer's interceptor resolves for a caller authenticated as
// something other than a person, and such a caller has no inbox.
type testPrincipalValue struct {
	userID          string
	activeAccountID string
	scope           tenancy.Scope
}

var _ identitygrpc.Principal = (*testPrincipalValue)(nil)

func (p *testPrincipalValue) UserID() string          { return p.userID }
func (p *testPrincipalValue) Scope() tenancy.Scope    { return p.scope }
func (p *testPrincipalValue) ActiveAccountID() string { return p.activeAccountID }

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

// harness is one database, one store and one server over both of its seams.
type harness struct {
	db     database.Client
	store  *notifications.SQLStore
	server *notificationsgrpc.Server
}

// newHarness migrates a uniquely prefixed pair of tables and builds the surface
// over them.
//
// The store is passed twice, which is the wiring notifications.SQLStore's own
// documentation describes: two interfaces because their consumers are separate,
// one implementation because they are one schema.
func newHarness(tb testing.TB) *harness {
	tb.Helper()

	db, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "notifications.db")})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = db.Close() })

	prefix := fmt.Sprintf("ng_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(dialect.SQLite, prefix)
	must.NoError(tb, err)
	must.SliceNotEmpty(tb, stmts)

	for _, stmt := range stmts {
		_, execErr := db.Writer().ExecContext(tb.Context(), stmt)
		must.NoError(tb, execErr, must.Sprintf("executing %q", stmt))
	}

	store, err := notifications.NewSQLStore(db, notifications.WithTablePrefix(prefix))
	must.NoError(tb, err)

	server, err := notificationsgrpc.NewServer(store, store, db, extractPrincipal)
	must.NoError(tb, err)

	return &harness{db: db, store: store, server: server}
}

// ctx is a request context carrying the named caller, in the first directory.
func (h *harness) ctx(tb testing.TB, principal string) context.Context {
	tb.Helper()

	return withPrincipal(tb.Context(), &testPrincipalValue{userID: principal, scope: testScope})
}

// ctxIn is the same, in whichever directory is named.
func (h *harness) ctxIn(tb testing.TB, principal string, scope tenancy.Scope) context.Context {
	tb.Helper()

	return withPrincipal(tb.Context(), &testPrincipalValue{userID: principal, scope: scope})
}

// seedNotification files one notification directly through the store, so a test
// asserting what a caller can reach does not reach it through the surface under
// test.
func (h *harness) seedNotification(tb testing.TB, scope tenancy.Scope, principal, topic string) *notifications.Notification {
	tb.Helper()

	notification := &notifications.Notification{
		Principal: principal,
		Topic:     topic,
		Title:     "something happened",
		Body:      "the long version",
		Link:      "/things/1",
	}

	var filed *notifications.Notification

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		var err error

		filed, err = h.store.CreateNotification(tb.Context(), tx, scope, notification)

		return err
	}))

	return filed
}

// seedDevice registers one handset directly through the store, for the same
// reason seedNotification writes directly.
func (h *harness) seedDevice(tb testing.TB, scope tenancy.Scope, principal, token string) *notifications.Device {
	tb.Helper()

	device := &notifications.Device{
		Principal: principal,
		Token:     token,
		Platform:  notifications.PlatformIOS,
	}

	var registered *notifications.Device

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		var err error

		registered, err = h.store.RegisterDevice(tb.Context(), tx, scope, device)

		return err
	}))

	return registered
}

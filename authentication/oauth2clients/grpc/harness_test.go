package grpc_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	oauth2clientsgrpc "github.com/primandproper/platform-go/v14/authentication/oauth2clients/grpc"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/migrations"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
	"github.com/primandproper/platform-go/v14/database"
	"github.com/primandproper/platform-go/v14/database/dialect"
	"github.com/primandproper/platform-go/v14/database/sqlite"
	"github.com/primandproper/platform-go/v14/errormappers"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	"github.com/primandproper/platform-go/v14/tenancy"

	"github.com/shoenig/test/must"
)

// The suite runs the server's methods in process, against a real SQLite
// database, a real oauth2clients.SQLStore and a real oauth2clients.Service.
//
// In process rather than over a bufconn, unlike identity/grpc's suite, because
// what these tests are about is who the handler decides the caller is — and a
// connection would add a consumer's interceptor to the things under test without
// adding anything to the decision. The database is real for the opposite reason:
// the refusals here are about which rows a caller reaches, and a mocked store
// answers that question itself.

// TestMain registers the domain tier's error mappers once for the binary.
//
// Without it this suite would assert the codes each handler passes as its
// *default* rather than the ones a client reads. The administered half hands
// PrepareAndLogGRPCStatus codes.Internal for every store failure on purpose —
// the registered mapper is what turns a refused registration into NotFound or
// AlreadyExists over the preserved chain — so a suite that skipped the
// registration would pin Internal as the answer to "no such client" and pass.
//
// It is also exactly the call a consumer owes at their composition root, which
// is the other reason it belongs here rather than inside a test.
func TestMain(m *testing.M) {
	errormappers.Register()
	m.Run()
}

// The registries these tests work in, and the two people in the first.
//
// The second exists because the scope on this surface comes off the principal
// rather than off the request, so the only way to show that a read is keyed on
// it is to ask for the same row from a caller the extractor puts somewhere else.
var (
	testScope  = tenancy.Of("acct_1")
	otherScope = tenancy.Of("acct_2")
)

const (
	testOwner  = "user_1"
	otherOwner = "user_2"
)

// testRedirect is a redirect URI oauth2server.ValidateRedirectURI accepts, so
// that a test about authorization is not also a test about URI validation.
const testRedirect = "https://example.test/callback"

// creationInput is the smallest registration the store accepts.
func creationInput() *oauth2clientspb.OAuth2ClientCreationInput {
	return &oauth2clientspb.OAuth2ClientCreationInput{
		Name:         "test client",
		RedirectUris: []string{testRedirect},
	}
}

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

// prefixCounter names a fresh table per subtest, since client_id carries a
// unique index across every registry and subtests share one database.
var prefixCounter atomic.Uint64

// testPrincipal is the consumer's half of the principal seam, as small as the
// interface allows. A userID of "" is the whole point of several of these tests:
// it is what a consumer's interceptor resolves for a caller authenticated as
// something other than a person.
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

// harness is one database, one store, one service and one server over them.
type harness struct {
	db     database.Client
	store  *oauth2clients.SQLStore
	svc    *oauth2clients.Service
	server *oauth2clientsgrpc.Server
}

// newHarness migrates a uniquely prefixed table and builds the surface over it.
func newHarness(tb testing.TB) *harness {
	tb.Helper()

	db, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "oauth2clients.db")})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = db.Close() })

	prefix := fmt.Sprintf("ocg_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(dialect.SQLite, prefix)
	must.NoError(tb, err)
	must.SliceNotEmpty(tb, stmts)

	for _, stmt := range stmts {
		_, execErr := db.Writer().ExecContext(tb.Context(), stmt)
		must.NoError(tb, execErr, must.Sprintf("executing %q", stmt))
	}

	store, err := oauth2clients.NewSQLStore(db, oauth2clients.WithTablePrefix(prefix))
	must.NoError(tb, err)

	svc, err := oauth2clients.NewService(db, store)
	must.NoError(tb, err)

	server, err := oauth2clientsgrpc.NewServer(svc, store, db, extractPrincipal)
	must.NoError(tb, err)

	return &harness{db: db, store: store, svc: svc, server: server}
}

// ctx is a request context carrying the named caller.
func (h *harness) ctx(tb testing.TB, userID string) context.Context {
	tb.Helper()

	return withPrincipal(tb.Context(), &testPrincipal{userID: userID, scope: testScope})
}

// seed writes one registration directly through the service, so a test asserting
// what a caller can reach does not reach it through the surface under test.
//
// An owner of "" is the administered arrangement, which is what the administered
// CreateOAuth2Client writes and what several of these tests are about.
func (h *harness) seed(tb testing.TB, owner string) *oauth2clients.Client {
	tb.Helper()

	issued, err := h.svc.CreateClient(tb.Context(), testScope, owner, &oauth2clients.CreationInput{
		Name:         "test client",
		RedirectURIs: []string{testRedirect},
	})
	must.NoError(tb, err)
	must.NotNil(tb, issued)

	return issued.Client
}

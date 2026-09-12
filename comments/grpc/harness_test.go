package grpc_test

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/comments"
	commentsgrpc "github.com/primandproper/platform-go/v14/comments/grpc"
	"github.com/primandproper/platform-go/v14/comments/migrations"
	"github.com/primandproper/platform-go/v14/errormappers"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test/must"
)

// The suite runs the server's methods in process, against a real SQLite
// database and a real comments.SQLStore.
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
// what turns a reply to a reply into InvalidArgument over the preserved chain —
// so a suite that skipped the registration would pin Internal as the answer to
// "that comment cannot be replied to" and pass.
//
// It is also exactly the call a consumer owes at their composition root, which
// is the other reason it belongs here rather than inside a test.
func TestMain(m *testing.M) {
	errormappers.Register()
	m.Run()
}

// The tenants these tests write comments in, and the neighbor whose rows must
// never appear in the first one's answers.
//
// The second exists because the scope on this surface comes off the principal
// rather than off the request, so the only way to show that a read is keyed on
// it is to ask for the same row from a caller the extractor puts somewhere else.
var (
	testScope  = tenancy.Of("acct_1")
	otherScope = tenancy.Of("acct_2")
)

// The two people. One writes, and the other is who a moderation rule is asked
// about.
const (
	testUser  = "user_1"
	otherUser = "user_2"
)

// The catalog these tests publish, and one type deliberately outside it so the
// write gate is visible.
const (
	recipeType  comments.TargetType = "recipe"
	mealType    comments.TargetType = "meal"
	unknownType comments.TargetType = "unregistered"
)

// testTargets is the catalog the harness builds stores against. Neither entry
// carries an existence check: "no check" is the ordinary case, and the one test
// about a registered check supplies its own.
var testTargets = comments.Targets{
	recipeType: {Description: "a recipe"},
	mealType:   {Description: "a meal"},
}

// testTarget is the thing most of these comments are about.
var testTarget = comments.Target{Type: recipeType, ID: "recipe_1"}

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

// harness is one database, one store and one server over them.
type harness struct {
	db     database.Client
	store  *comments.SQLStore
	server *commentsgrpc.Server
}

// newHarness migrates a uniquely prefixed table and builds the surface over it.
//
// SQLite gets a database of its own per harness rather than a prefix in a shared
// one: DDL invalidates every prepared statement on the whole database, so one
// parallel subtest creating its table makes another's next read fail with
// "database schema has changed" whatever prefix either is using.
func newHarness(tb testing.TB, opts ...commentsgrpc.Option) *harness {
	tb.Helper()

	return newHarnessWithTargets(tb, testTargets, opts...)
}

// newHarnessWithTargets is newHarness over a catalog the caller chose, for the
// cases about what the write gate does and does not stop.
func newHarnessWithTargets(tb testing.TB, targets comments.Targets, opts ...commentsgrpc.Option) *harness {
	tb.Helper()

	db, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "comments.db")})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = db.Close() })

	prefix := fmt.Sprintf("cmg_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(dialect.SQLite, prefix)
	must.NoError(tb, err)
	must.SliceNotEmpty(tb, stmts)

	for _, stmt := range stmts {
		_, execErr := db.Writer().ExecContext(tb.Context(), stmt)
		must.NoError(tb, execErr, must.Sprintf("executing %q", stmt))
	}

	store, err := comments.NewSQLStore(db,
		comments.WithTablePrefix(prefix), comments.WithTargets(targets))
	must.NoError(tb, err)

	server, err := commentsgrpc.NewServer(store, db, extractPrincipal, opts...)
	must.NoError(tb, err)

	return &harness{db: db, store: store, server: server}
}

// ctx is a request context carrying a caller in testScope.
func (h *harness) ctx(tb testing.TB) context.Context {
	tb.Helper()

	return withPrincipal(tb.Context(), &testPrincipal{userID: testUser, scope: testScope})
}

// ctxAs is a request context carrying a named caller in testScope, for the cases
// about acting on somebody else's words.
func (h *harness) ctxAs(tb testing.TB, userID string) context.Context {
	tb.Helper()

	return withPrincipal(tb.Context(), &testPrincipal{userID: userID, scope: testScope})
}

// otherCtx is a request context carrying the neighboring tenant's caller, which
// is how every scoping assertion here is made.
func (h *harness) otherCtx(tb testing.TB) context.Context {
	tb.Helper()

	return withPrincipal(tb.Context(), &testPrincipal{userID: otherUser, scope: otherScope})
}

// seed writes one comment directly through the store, so a test asserting what a
// caller can reach does not reach it through the surface under test.
//
// It hands back the stored row rather than the value it was given: the create
// does not touch its argument, so the id and the creation time are on what the
// write answered with alone.
func (h *harness) seed(tb testing.TB, scope tenancy.Scope, comment *comments.Comment) *comments.Comment {
	tb.Helper()

	if comment.Author == "" {
		comment.Author = testUser
	}

	if comment.Body == "" {
		comment.Body = "seeded"
	}

	if comment.Target.Zero() && comment.ParentID == "" {
		comment.Target = testTarget
	}

	var stored *comments.Comment

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		written, err := h.store.CreateComment(tb.Context(), tx, scope, comment)
		if err != nil {
			return err
		}

		stored = written

		return nil
	}))

	return stored
}

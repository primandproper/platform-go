package grpc_test

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/errormappers"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	"github.com/primandproper/platform-go/v14/waitlists"
	waitlistsgrpc "github.com/primandproper/platform-go/v14/waitlists/grpc"
	"github.com/primandproper/platform-go/v14/waitlists/migrations"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"

	"github.com/primandproper/primitives-go/v2/clock"
	clockmock "github.com/primandproper/primitives-go/v2/clock/mock"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test/must"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// The suite runs the server's methods in process, against a real SQLite
// database and a real waitlists.SQLStore.
//
// In process rather than over a bufconn, as the OAuth2 client registry's and
// webhooks' suites run and for the same reason: what these tests are about is
// which rows a caller reaches and what the handler decides to send back, and a
// connection would add a consumer's interceptor to the things under test without
// adding anything to either decision. The database is real for the opposite
// reason — the scoping here is in the statements, and a mocked store would be
// answering the question the tests are asking.

// TestMain registers the domain tier's error mappers once for the binary.
//
// Without it this suite would assert the codes each handler passes as its
// *default* rather than the ones a client reads. Every handler here hands
// PrepareAndLogGRPCStatus codes.Internal on purpose — the registered mapper is
// what turns a closed list into FailedPrecondition over the preserved chain — so
// a suite that skipped the registration would pin Internal as the answer to "we
// have stopped taking signups" and pass.
//
// It is also exactly the call a consumer owes at their composition root, which
// is the other reason it belongs here rather than inside a test.
func TestMain(m *testing.M) {
	errormappers.Register()
	m.Run()
}

// The tenants these tests work in, and the neighbor whose rows must never
// appear in the first one's answers.
//
// Neither is global, because tenancy.Global() is the scope a bug defaults to — a
// predicate that lost its binding matches it — so a suite that worked entirely
// in it would pass with the scope dropped from every statement.
var (
	testScope  = tenancy.Of("acme")
	otherScope = tenancy.Of("other")
)

const (
	testUser  = "user_1"
	otherUser = "user_2"
)

// testNow is the instant the suite's clock is parked at. Every closing time
// below is relative to it, so a list is open or closed because the test said so
// rather than because the wall clock happened to agree.
var testNow = time.Date(2026, time.March, 1, 12, 0, 0, 0, time.UTC)

// prefixCounter names a fresh set of tables per harness.
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
// anonymous request — which on this service is three RPCs working as intended
// rather than a failure.
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

// permitWithdrawals is the SignupAuthorizer most of this suite is built with:
// one that permits everything.
//
// It is not a default the package ships and could not be — see
// waitlistsgrpc.SignupAuthorizer — so the suite names it, which is the point.
// The tests about what a refusal looks like supply one that refuses.
func permitWithdrawals() waitlistsgrpc.SignupAuthorizer {
	return waitlistsgrpc.SignupAuthorizerFunc(
		func(context.Context, waitlistsgrpc.Principal, tenancy.Scope, string, string) error {
			return nil
		})
}

// harness is one database, one store and one server over them.
type harness struct {
	db     database.Client
	store  *waitlists.SQLStore
	server *waitlistsgrpc.Server
}

// newHarness migrates a uniquely prefixed set of tables and builds the surface
// over them, with a clock parked at testNow.
//
// SQLite gets a database of its own per harness rather than a prefix in a shared
// one: DDL invalidates every prepared statement on the whole database, so one
// parallel subtest creating its tables makes another's next read fail with
// "database schema has changed" whatever prefix either is using.
func newHarness(tb testing.TB, opts ...waitlistsgrpc.Option) *harness {
	tb.Helper()

	db, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "waitlists.db")})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = db.Close() })

	return newHarnessOn(tb, db, dialect.SQLite, permitWithdrawals(),
		append([]waitlistsgrpc.Option{waitlistsScopeResolver()}, opts...)...)
}

// waitlistsScopeResolver places an anonymous caller in testScope, which is what
// a multi-tenant deployment's resolver does.
//
// The suite uses it almost everywhere, because it is what makes the public three
// testable against the same rows the administrative fourteen see. The package's
// GlobalScope default is the subject of a test of its own — see
// newHarnessWithScope.
func waitlistsScopeResolver() waitlistsgrpc.Option {
	return waitlistsgrpc.WithScopeResolver(func(context.Context) (tenancy.Scope, error) {
		return testScope, nil
	})
}

// newHarnessWithAuthorizer is newHarness with a rule of its own about who may
// withdraw a signup.
func newHarnessWithAuthorizer(
	tb testing.TB,
	authorizer waitlistsgrpc.SignupAuthorizer,
	opts ...waitlistsgrpc.Option,
) *harness {
	tb.Helper()

	db, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "waitlists.db")})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = db.Close() })

	return newHarnessOn(tb, db, dialect.SQLite, authorizer,
		append([]waitlistsgrpc.Option{waitlistsScopeResolver()}, opts...)...)
}

// newHarnessWithScope is a harness with no resolver of the suite's own, so that
// a test can name one — or name none, and get the package's GlobalScope default.
func newHarnessWithScope(tb testing.TB, resolve waitlistsgrpc.ScopeResolver) *harness {
	tb.Helper()

	db, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "waitlists.db")})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = db.Close() })

	var opts []waitlistsgrpc.Option
	if resolve != nil {
		opts = append(opts, waitlistsgrpc.WithScopeResolver(resolve))
	}

	return newHarnessOn(tb, db, dialect.SQLite, permitWithdrawals(), opts...)
}

// newHarnessOn is the half that does not know where the database came from,
// which is what lets containers_test.go run the same suite against Postgres and
// MySQL.
func newHarnessOn(
	tb testing.TB,
	db database.Client,
	d dialect.Dialect,
	authorizer waitlistsgrpc.SignupAuthorizer,
	opts ...waitlistsgrpc.Option,
) *harness {
	tb.Helper()

	prefix := fmt.Sprintf("wlg_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(d, prefix)
	must.NoError(tb, err)
	must.SliceNotEmpty(tb, stmts)

	for _, stmt := range stmts {
		_, execErr := db.Writer().ExecContext(tb.Context(), stmt)
		must.NoError(tb, execErr, must.Sprintf("executing %q", stmt))
	}

	store, err := waitlists.NewSQLStore(db,
		waitlists.WithTablePrefix(prefix), waitlists.WithClock(newStubClock()))
	must.NoError(tb, err)

	server, err := waitlistsgrpc.NewServer(store, db, extractPrincipal, authorizer, opts...)
	must.NoError(tb, err)

	return &harness{db: db, store: store, server: server}
}

// ctx is a request context carrying an administrative caller in testScope.
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

// anonCtx is a request context carrying nobody, which is what the signup page
// looks like.
func (h *harness) anonCtx(tb testing.TB) context.Context {
	tb.Helper()

	return tb.Context()
}

// seedList opens a list directly through the store, so a test asserting what a
// caller can reach does not reach it through the surface under test.
func (h *harness) seedList(tb testing.TB, scope tenancy.Scope, closesAt time.Time) *waitlists.List {
	tb.Helper()

	var list *waitlists.List

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		created, err := h.store.CreateList(tb.Context(), tx, scope, &waitlists.List{
			Name:        "Launch",
			Description: "early access to the beta",
			ClosesAt:    closesAt,
		})
		if err != nil {
			return err
		}

		list = created

		return nil
	}))

	return list
}

// seedOpenList opens a list that is taking signups at testNow.
func (h *harness) seedOpenList(tb testing.TB, scope tenancy.Scope) *waitlists.List {
	tb.Helper()

	return h.seedList(tb, scope, testNow.Add(720*time.Hour))
}

// seedSignup joins somebody directly through the store.
func (h *harness) seedSignup(
	tb testing.TB,
	scope tenancy.Scope,
	listID, contact string,
) *waitlists.Signup {
	tb.Helper()

	var signup *waitlists.Signup

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		joined, err := h.store.Join(tb.Context(), tx, scope, listID, &waitlists.Signup{Contact: contact})
		if err != nil {
			return err
		}

		signup = joined

		return nil
	}))

	return signup
}

// invite moves a seeded signup to invited directly through the store, for the
// tests whose subject is what happens next.
func (h *harness) invite(tb testing.TB, scope tenancy.Scope, listID, signupID string) {
	tb.Helper()

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		return h.store.Invite(tb.Context(), tx, scope, listID, signupID)
	}))
}

// listInput is the smallest input CreateList accepts.
func listInput(name string, closesAt time.Time) *waitlistspb.WaitlistInput {
	return &waitlistspb.WaitlistInput{
		Name:        name,
		Description: "early access to the beta",
		ClosesAt:    timestamppb.New(closesAt),
	}
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

// stubClock is a clock parked at testNow, so "is this list open" is decided by
// the suite rather than by the wall clock.
type stubClock struct {
	now time.Time
	*clockmock.ClockMock

	mu sync.Mutex
}

var _ clock.Clock = (*stubClock)(nil)

func newStubClock() *stubClock {
	c := &stubClock{now: testNow}

	c.ClockMock = &clockmock.ClockMock{
		NowFunc:   c.read,
		SinceFunc: func(t time.Time) time.Duration { return c.read().Sub(t) },
	}

	return c
}

func (c *stubClock) read() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

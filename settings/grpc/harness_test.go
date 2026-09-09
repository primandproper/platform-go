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
	"github.com/primandproper/platform-go/v14/settings"
	settingsgrpc "github.com/primandproper/platform-go/v14/settings/grpc"
	"github.com/primandproper/platform-go/v14/settings/migrations"
	"github.com/primandproper/platform-go/v14/settings/settingspb"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/sqlite"
	"github.com/primandproper/primitives-go/pointer"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test/must"
)

// The suite runs the server's methods in process, against a real SQLite
// database and a real settings.SQLStore.
//
// In process rather than over a bufconn, as the OAuth2 client registry's suite
// runs and for the same reason: what these tests are about is which rows a
// caller reaches and what the handler decides to send back, and a connection
// would add a consumer's interceptor to the things under test without adding
// anything to either decision. The database is real for the opposite reason —
// the scoping here is in the statements, the resolution is a join between two
// tables, and a mocked store would be answering the questions the tests are
// asking.

// TestMain registers the domain tier's error mappers once for the binary.
//
// Without it this suite would assert the codes each handler passes as its
// *default* rather than the ones a client reads. Every handler here hands
// PrepareAndLogGRPCStatus codes.Internal on purpose — the registered mapper is
// what turns an edit that strands values into FailedPrecondition over the
// preserved chain — so a suite that skipped the registration would pin Internal
// as the answer to "clear these values first" and pass.
//
// It is also exactly the call a consumer owes at their composition root, which
// is the other reason it belongs here rather than inside a test.
func TestMain(m *testing.M) {
	errormappers.Register()
	m.Run()
}

// The scopes these tests work in. Neither is global, because tenancy.Global()
// is the scope a bug defaults to — a predicate that lost its binding matches it
// — so a suite that worked entirely in it would pass with the scope dropped
// from every statement.
var (
	testScope  = tenancy.Of("acme")
	otherScope = tenancy.Of("other")
)

const (
	testUser  = "user-1"
	otherUser = "user-2"
)

// The subjects this suite is about: the caller themselves, and a second person
// whose settings the harness's authorizer refuses them.
var (
	testSubject    = settings.Subject{Type: settings.SubjectUser, ID: testUser}
	strangeSubject = settings.Subject{Type: settings.SubjectUser, ID: otherUser}
	accountSubject = settings.Subject{Type: settings.SubjectAccount, ID: "account-1"}
)

// The settings this suite defines. One of each kind, because the typed value is
// the decision under test and the four cases are the whole of it.
const (
	digestSetting    = "notifications.digest"
	compactSetting   = "display.compact"
	retentionSetting = "privacy.retention_days"
	ratioSetting     = "display.density"
	channelSetting   = "notifications.channel"
)

// selfOnly is the harness's SubjectAuthorizer, and it is the two-line rule
// [settingsgrpc.SubjectAuthorizer] documents: a person may act on themselves
// and on nobody else.
//
// It is a real rule rather than a permit-everything stub, because six of the
// thirteen RPCs are gated by it and a stub would make every one of those tests
// assert the handler's behavior with the gate switched off.
func selfOnly(_ context.Context, caller settingsgrpc.Principal, subject settings.Subject) error {
	if caller != nil && subject.Type == settings.SubjectUser && subject.ID == caller.UserID() {
		return nil
	}

	return settingsgrpc.ErrTargetNotPermitted
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
	store  *settings.SQLStore
	server *settingsgrpc.Server
}

// newHarness migrates a uniquely prefixed set of tables and builds the surface
// over them.
//
// SQLite gets a database of its own per harness rather than a prefix in a
// shared one: DDL invalidates every prepared statement on the whole database,
// so one parallel subtest creating its tables makes another's next read fail
// with "database schema has changed" whatever prefix either is using.
func newHarness(tb testing.TB, authorizer settingsgrpc.SubjectAuthorizer) *harness {
	tb.Helper()

	db, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "settings.db")})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = db.Close() })

	prefix := fmt.Sprintf("stg_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(dialect.SQLite, prefix)
	must.NoError(tb, err)
	must.SliceNotEmpty(tb, stmts)

	for _, stmt := range stmts {
		_, execErr := db.Writer().ExecContext(tb.Context(), stmt)
		must.NoError(tb, execErr, must.Sprintf("executing %q", stmt))
	}

	store, err := settings.NewSQLStore(db, settings.WithTablePrefix(prefix))
	must.NoError(tb, err)

	if authorizer == nil {
		authorizer = settingsgrpc.SubjectAuthorizerFunc(selfOnly)
	}

	server, err := settingsgrpc.NewServer(store, db, extractPrincipal, authorizer)
	must.NoError(tb, err)

	return &harness{db: db, store: store, server: server}
}

// newSeededHarness is newHarness with the four kinds defined in the caller's
// scope, which is what most of these tests need before they can ask anything.
func newSeededHarness(tb testing.TB) *harness {
	tb.Helper()

	h := newHarness(tb, nil)
	h.seedCatalog(tb, testScope)

	return h
}

// ctx is a request context carrying a caller in testScope.
func (h *harness) ctx(tb testing.TB) context.Context {
	tb.Helper()

	return withPrincipal(tb.Context(), &testPrincipal{
		userID:          testUser,
		scope:           testScope,
		activeAccountID: accountSubject.ID,
	})
}

// otherCtx is a request context carrying the neighboring scope's caller, which
// is how every isolation assertion here is made.
func (h *harness) otherCtx(tb testing.TB) context.Context {
	tb.Helper()

	return withPrincipal(tb.Context(), &testPrincipal{userID: otherUser, scope: otherScope})
}

// seedCatalog defines one setting of each kind, directly through the store, so
// a test asserting what a caller can reach does not reach it through the
// surface under test.
//
// The five are deliberately not uniform: the string setting enumerates its
// values and has a default, the boolean has a default, the integer has none —
// which is the third resolution case — the float has a default outside any
// enumeration, and the second string setting has neither, which is the only way
// to store the empty string and therefore the only way to tell "chose nothing"
// from "chose the empty string".
func (h *harness) seedCatalog(tb testing.TB, scope tenancy.Scope) {
	tb.Helper()

	h.seed(tb, scope, &settings.Definition{
		Name:        digestSetting,
		Kind:        settings.KindString,
		Default:     pointer.To("weekly"),
		Enumeration: []string{"daily", "never", "weekly"},
	})

	h.seed(tb, scope, &settings.Definition{
		Name:    compactSetting,
		Kind:    settings.KindBool,
		Default: pointer.To("false"),
	})

	h.seed(tb, scope, &settings.Definition{
		Name: retentionSetting,
		Kind: settings.KindInt,
	})

	h.seed(tb, scope, &settings.Definition{
		Name:    ratioSetting,
		Kind:    settings.KindFloat,
		Default: pointer.To("1.5"),
	})

	h.seed(tb, scope, &settings.Definition{
		Name: channelSetting,
		Kind: settings.KindString,
	})
}

// seed writes one definition through the store and hands back what was stored.
func (h *harness) seed(tb testing.TB, scope tenancy.Scope, definition *settings.Definition) *settings.Definition {
	tb.Helper()

	var stored *settings.Definition

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		created, err := h.store.CreateDefinition(tb.Context(), tx, scope, definition)
		if err != nil {
			return err
		}

		stored = created

		return nil
	}))

	return stored
}

// seedValue stores one subject's answer through the store.
func (h *harness) seedValue(tb testing.TB, scope tenancy.Scope, subject settings.Subject, name, raw string) {
	tb.Helper()

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		_, err := h.store.SetValue(tb.Context(), tx, scope, subject, name, raw)

		return err
	}))
}

// subjectOf renders a subject for a request.
func subjectOf(subject settings.Subject) *settingspb.SettingSubject {
	return &settingspb.SettingSubject{Type: subject.Type.String(), Id: subject.ID}
}

// stringValue, boolValue, intValue and floatValue are the four cases of the
// oneof, spelled once each so a test reads as the value it is sending.
func stringValue(v string) *settingspb.TypedValue {
	return &settingspb.TypedValue{Value: &settingspb.TypedValue_StringValue{StringValue: v}}
}

func boolValue(v bool) *settingspb.TypedValue {
	return &settingspb.TypedValue{Value: &settingspb.TypedValue_BoolValue{BoolValue: v}}
}

func intValue(v int64) *settingspb.TypedValue {
	return &settingspb.TypedValue{Value: &settingspb.TypedValue_IntValue{IntValue: v}}
}

func floatValue(v float64) *settingspb.TypedValue {
	return &settingspb.TypedValue{Value: &settingspb.TypedValue_FloatValue{FloatValue: v}}
}

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
	"github.com/primandproper/platform-go/v14/issuereports"
	issuereportsgrpc "github.com/primandproper/platform-go/v14/issuereports/grpc"
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"
	"github.com/primandproper/platform-go/v14/issuereports/migrations"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/sqlite"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/filtering/filteringpb"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test/must"
)

// The suite runs the server's methods in process, against a real SQLite database
// and a real issuereports.SQLStore.
//
// In process rather than over a bufconn, as authentication/oauth2clients/grpc's
// suite is and for the same reason: what these tests are about is which rows a
// caller reaches, and a connection would add a consumer's interceptor to the
// things under test without adding anything to the decision. The database is
// real for the opposite reason — the refusals here are the store's, the guarded
// move most of all, and a mocked store answers that question itself.

// TestMain registers the domain tier's error mappers once for the binary.
//
// Without it this suite would assert the codes each handler passes as its
// *default* rather than the ones a client reads. Every handler here hands
// PrepareAndLogGRPCStatus codes.Internal for a store failure on purpose — the
// registered mapper is what turns a report somebody else moved into
// codes.Aborted and a missing one into codes.NotFound — so a suite that skipped
// the registration would pin Internal as the answer to "that report already
// moved" and pass.
//
// It is also exactly the call a consumer owes at their composition root.
func TestMain(m *testing.M) {
	errormappers.Register()
	m.Run()
}

// The tenants these tests work in, and the people in the first.
//
// otherScope exists because the scope on this surface comes off the principal
// rather than off the request, so the only way to show a read is keyed on it is
// to ask for the same row from a caller the extractor puts somewhere else.
var (
	testScope  = tenancy.Of("tenant_1")
	otherScope = tenancy.Of("tenant_2")
)

const (
	testReporter  = "user_1"
	otherReporter = "user_2"
	triager       = "user_3"
)

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

// prefixCounter names a fresh table per subtest. Subtests share one database and
// must not share a table: a tenant's whole list is keyed on the scope and
// nothing else, so one subtest's queue would be another's.
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

// extractPrincipal is the PrincipalExtractor the server is built with.
func extractPrincipal(ctx context.Context) (identitygrpc.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(identitygrpc.Principal)

	return p, ok
}

// triageAuthorizer is the suite's ReportAuthorizer, and it is the composition
// the exported ReporterAuthorizer's documentation describes: a person reaches
// their own reports, and the one caller this deployment calls a triager reaches
// everybody's.
//
// It is deliberately not what a deployment would ship — a real one asks the
// consumer's own grants — but it is a rule with two sides, which is what these
// tests need in order to show the seam is consulted and that a refusal is
// answered the two ways this surface answers one.
type triageAuthorizer struct {
	issuereportsgrpc.ReporterAuthorizer
}

func (a triageAuthorizer) AuthorizeReport(
	ctx context.Context,
	caller issuereportsgrpc.Principal,
	report *issuereports.Report,
) error {
	if caller != nil && caller.UserID() == triager {
		return nil
	}

	return a.ReporterAuthorizer.AuthorizeReport(ctx, caller, report)
}

func (a triageAuthorizer) AuthorizeReporter(
	ctx context.Context,
	caller issuereportsgrpc.Principal,
	reporter string,
) error {
	if caller != nil && caller.UserID() == triager {
		return nil
	}

	return a.ReporterAuthorizer.AuthorizeReporter(ctx, caller, reporter)
}

// brokenAuthorizer is an authorizer that cannot decide, which is a different
// answer from a refusal and reaches a client as codes.Internal.
type brokenAuthorizer struct {
	err error
}

var _ issuereportsgrpc.ReportAuthorizer = (*brokenAuthorizer)(nil)

func (a *brokenAuthorizer) AuthorizeReport(
	context.Context, issuereportsgrpc.Principal, *issuereports.Report,
) error {
	return a.err
}

func (a *brokenAuthorizer) AuthorizeReporter(
	context.Context, issuereportsgrpc.Principal, string,
) error {
	return a.err
}

// harness is one database, one store and one server over them.
type harness struct {
	db     database.Client
	store  *issuereports.SQLStore
	server *issuereportsgrpc.Server
}

// newHarness migrates a uniquely prefixed table and builds the surface over it,
// with the suite's own two-sided rule.
func newHarness(tb testing.TB) *harness {
	tb.Helper()

	return newHarnessWithAuthorizer(tb, triageAuthorizer{})
}

// newHarnessWithAuthorizer is newHarness with the seam supplied, for the tests
// that are about what this surface does with each of the three answers a
// ReportAuthorizer may give.
func newHarnessWithAuthorizer(tb testing.TB, targets issuereportsgrpc.ReportAuthorizer) *harness {
	tb.Helper()

	db, err := sqlite.NewDatabaseClient(tb.Context(),
		&testClientConfig{connectionString: filepath.Join(tb.TempDir(), "issuereports.db")})
	must.NoError(tb, err)
	tb.Cleanup(func() { _ = db.Close() })

	prefix := fmt.Sprintf("irg_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(dialect.SQLite, prefix)
	must.NoError(tb, err)
	must.SliceNotEmpty(tb, stmts)

	for _, stmt := range stmts {
		_, execErr := db.Writer().ExecContext(tb.Context(), stmt)
		must.NoError(tb, execErr, must.Sprintf("executing %q", stmt))
	}

	store, err := issuereports.NewSQLStore(db, issuereports.WithTablePrefix(prefix))
	must.NoError(tb, err)

	server, err := issuereportsgrpc.NewServer(store, db, extractPrincipal, targets)
	must.NoError(tb, err)

	return &harness{db: db, store: store, server: server}
}

// ctx is a request context carrying the named caller, in testScope.
func (h *harness) ctx(tb testing.TB, userID string) context.Context {
	tb.Helper()

	return withPrincipal(tb.Context(), &testPrincipal{userID: userID, scope: testScope})
}

// otherScopeCtx is a caller the extractor puts in a different tenant entirely,
// with the same user identifier — so a read that comes back empty came back
// empty for the scope rather than for the person.
func (h *harness) otherScopeCtx(tb testing.TB, userID string) context.Context {
	tb.Helper()

	return withPrincipal(tb.Context(), &testPrincipal{userID: userID, scope: otherScope})
}

// seedReport files one report directly through the store, so a test asserting
// what a caller can reach does not reach it through the surface under test.
func (h *harness) seedReport(tb testing.TB, scope tenancy.Scope, reporter string) *issuereports.Report {
	tb.Helper()

	return h.seedReportAbout(tb, scope, reporter, "recipe", "recipe_1")
}

// seedReportAbout is seedReport with the subject named, for the two lists that
// page by one.
func (h *harness) seedReportAbout(
	tb testing.TB,
	scope tenancy.Scope,
	reporter, subjectType, subjectID string,
) *issuereports.Report {
	tb.Helper()

	report := &issuereports.Report{
		Reporter:    reporter,
		Kind:        "bug",
		Details:     "the thing did not work",
		SubjectType: subjectType,
		SubjectID:   subjectID,
	}

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		return h.store.CreateReport(tb.Context(), tx, scope, report)
	}))

	return report
}

// move transitions a seeded report directly through the store, for the tests
// that need a row already somewhere in the lifecycle.
func (h *harness) move(
	tb testing.TB,
	scope tenancy.Scope,
	reportID string,
	from, to issuereports.Status,
	resolution string,
) *issuereports.Report {
	tb.Helper()

	var moved *issuereports.Report

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		var err error
		moved, err = h.store.TransitionReport(tb.Context(), tx, scope, reportID, from, to, resolution)

		return err
	}))
	must.NotNil(tb, moved)

	return moved
}

// creationInput is the smallest report the store accepts.
func creationInput() *issuereportspb.IssueReportCreationInput {
	return &issuereportspb.IssueReportCreationInput{
		Kind:        "bug",
		Details:     "the thing did not work",
		SubjectType: "recipe",
		SubjectId:   "recipe_1",
	}
}

// errAuthorizerUnavailable stands in for a rule that could not be evaluated —
// the database behind a consumer's own membership read being down.
var errAuthorizerUnavailable = platformerrors.New("the authorizer could not decide")

// zeroTime is what a timestamp carries when nobody assigned one, and the value
// the assertions about database-assigned stamps are made against.
func zeroTime() time.Time { return time.Time{} }

// badFilter is a page request no converter can read, for the tests that assert a
// malformed request is answered as malformed before anything is gated or read.
//
// The sort direction is the field with a closed set of spellings, so an
// unrecognized one is refused by filtering/grpc rather than by this package.
func badFilter() *filteringpb.QueryFilter {
	sortBy := "sideways"

	return &filteringpb.QueryFilter{SortBy: &sortBy}
}

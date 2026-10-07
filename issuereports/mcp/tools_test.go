package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v15/callers"
	"github.com/primandproper/platform-go/v15/issuereports"
	issuereportsgrpc "github.com/primandproper/platform-go/v15/issuereports/grpc"
	issuereportsmcp "github.com/primandproper/platform-go/v15/issuereports/mcp"
	"github.com/primandproper/platform-go/v15/issuereports/migrations"
	"github.com/primandproper/platform-go/v15/mcptool"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/google/jsonschema-go/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var (
	testScope  = tenancy.Of("tenant_1")
	otherScope = tenancy.Of("tenant_2")
)

const (
	reporter      = "user_1"
	otherReporter = "user_2"
	triager       = "user_3"
	archivist     = "user_4"
)

// grantsByUser is the suite's policy: a reporter reads, a triager pages the
// queue, and the archivist pages it archived rows included.
var grantsByUser = map[string][]authorization.Permission{
	reporter:      {issuereportsgrpc.PermissionReadReports},
	otherReporter: {issuereportsgrpc.PermissionReadReports},
	triager:       {issuereportsgrpc.PermissionReadReports, issuereportsgrpc.PermissionTriageReports},
	archivist: {
		issuereportsgrpc.PermissionReadReports, issuereportsgrpc.PermissionTriageReports,
		issuereportsgrpc.PermissionArchiveReports,
	},
}

type testClientConfig struct{ connectionString string }

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 2 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 1 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

var prefixCounter atomic.Uint64

type testPrincipal struct {
	userID string
	scope  tenancy.Scope
}

func (p *testPrincipal) UserID() string          { return p.userID }
func (p *testPrincipal) Scope() tenancy.Scope    { return p.scope }
func (p *testPrincipal) ActiveAccountID() string { return "" }

type principalKey struct{}

func extractPrincipal(ctx context.Context) (callers.Principal, bool) {
	p, ok := ctx.Value(principalKey{}).(callers.Principal)

	return p, ok
}

func extractGrants(ctx context.Context) (authorization.Grants, bool) {
	p, ok := extractPrincipal(ctx)
	if !ok {
		return authorization.Grants{}, false
	}

	return authorization.NewGrants(authorization.NewPermissionSet(grantsByUser[p.UserID()]...)), true
}

// triageAuthorizer lets a person read their own reports and the triager read
// everybody's.
type triageAuthorizer struct {
	issuereportsgrpc.ReporterAuthorizer
}

func (a triageAuthorizer) AuthorizeReport(ctx context.Context, caller callers.Principal, report *issuereports.Report) error {
	if caller != nil && (caller.UserID() == triager || caller.UserID() == archivist) {
		return nil
	}

	return a.ReporterAuthorizer.AuthorizeReport(ctx, caller, report)
}

func (a triageAuthorizer) AuthorizeReporter(ctx context.Context, caller callers.Principal, named string) error {
	if caller != nil && (caller.UserID() == triager || caller.UserID() == archivist) {
		return nil
	}

	return a.ReporterAuthorizer.AuthorizeReporter(ctx, caller, named)
}

type brokenAuthorizer struct {
	issuereportsgrpc.ReporterAuthorizer
}

var errUndecided = platformerrors.New("the issue reports MCP suite's authorizer could not decide")

func (brokenAuthorizer) AuthorizeReport(context.Context, callers.Principal, *issuereports.Report) error {
	return errUndecided
}

func (brokenAuthorizer) AuthorizeReporter(context.Context, callers.Principal, string) error {
	return errUndecided
}

// harness is one database, one store, and one MCP session over the tools.
type harness struct {
	db      database.Client
	store   *issuereports.SQLStore
	session *sdkmcp.ClientSession

	// caller is who the next call is made as; nil is nobody.
	caller atomic.Pointer[testPrincipal]
}

func newHarness(t *testing.T, targets issuereportsgrpc.ReportAuthorizer) *harness {
	t.Helper()

	db, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "issuereports.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	prefix := fmt.Sprintf("irm_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(dialect.SQLite, prefix)
	must.NoError(t, err)

	for _, stmt := range stmts {
		_, execErr := db.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr)
	}

	store, err := issuereports.NewSQLStore(db, issuereports.WithTablePrefix(prefix))
	must.NoError(t, err)

	h := &harness{db: db, store: store}

	// The suite's authenticator stands in for one reading req.Extra.TokenInfo:
	// it puts whoever the test is calling as onto the context.
	authenticate := func(ctx context.Context, _ *sdkmcp.CallToolRequest) (context.Context, error) {
		if p := h.caller.Load(); p != nil {
			return context.WithValue(ctx, principalKey{}, callers.Principal(p)), nil
		}

		return ctx, nil
	}

	tools, err := issuereportsmcp.NewTools(store, db, authenticate, extractPrincipal, extractGrants, targets)
	must.NoError(t, err)

	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "issuereports", Version: "test"}, nil)
	tools.RegisterOn(server)

	clientTransport, serverTransport := sdkmcp.NewInMemoryTransports()

	serverSession, err := server.Connect(t.Context(), serverTransport, nil)
	must.NoError(t, err)
	t.Cleanup(func() { _ = serverSession.Close() })

	session, err := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "suite", Version: "test"}, nil).
		Connect(t.Context(), clientTransport, nil)
	must.NoError(t, err)
	t.Cleanup(func() { _ = session.Close() })

	h.session = session

	return h
}

func (h *harness) as(userID string, scope tenancy.Scope) {
	h.caller.Store(&testPrincipal{userID: userID, scope: scope})
}

func (h *harness) seed(t *testing.T, scope tenancy.Scope, filedBy string) *issuereports.Report {
	t.Helper()

	var filed *issuereports.Report

	must.NoError(t, h.db.WithTransaction(t.Context(), func(tx database.Tx) error {
		var err error
		filed, err = h.store.CreateReport(t.Context(), tx, scope, &issuereports.Report{
			Reporter: filedBy, Kind: "bug", Details: "the thing did not work",
		})

		return err
	}))

	return filed
}

// call invokes a tool and returns its result, failing the test on a protocol
// error — a tool's own refusal is a result with IsError set, not an error.
func (h *harness) call(t *testing.T, tool string, args map[string]any) *sdkmcp.CallToolResult {
	t.Helper()

	result, err := h.session.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: tool, Arguments: args})
	must.NoError(t, err)

	return result
}

func text(t *testing.T, result *sdkmcp.CallToolResult) string {
	t.Helper()

	must.SliceLen(t, 1, result.Content)

	content, ok := result.Content[0].(*sdkmcp.TextContent)
	must.True(t, ok)

	return content.Text
}

func decode[T any](t *testing.T, result *sdkmcp.CallToolResult) *T {
	t.Helper()

	must.False(t, result.IsError, must.Sprintf("tool failed: %s", text(t, result)))

	raw, err := json.Marshal(result.StructuredContent)
	must.NoError(t, err)

	var out T
	must.NoError(t, json.Unmarshal(raw, &out))

	return &out
}

func TestNewTools(T *testing.T) {
	T.Parallel()

	authenticate := func(ctx context.Context, _ *sdkmcp.CallToolRequest) (context.Context, error) { return ctx, nil }

	T.Run("refuses a nil store", func(t *testing.T) {
		t.Parallel()

		_, err := issuereportsmcp.NewTools(nil, nil, authenticate, extractPrincipal, extractGrants, triageAuthorizer{})
		test.ErrorIs(t, err, issuereportsmcp.ErrNilStore)
	})

	T.Run("refuses a nil authorizer", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, triageAuthorizer{})

		_, err := issuereportsmcp.NewTools(h.store, h.db, authenticate, extractPrincipal, extractGrants, nil)
		test.ErrorIs(t, err, issuereportsmcp.ErrNilReportAuthorizer)
	})

	T.Run("refuses a nil authenticator, principal extractor or grants extractor", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, triageAuthorizer{})

		_, err := issuereportsmcp.NewTools(h.store, h.db, nil, extractPrincipal, extractGrants, triageAuthorizer{})
		test.ErrorIs(t, err, mcptool.ErrNilAuthenticator)

		_, err = issuereportsmcp.NewTools(h.store, h.db, authenticate, nil, extractGrants, triageAuthorizer{})
		test.ErrorIs(t, err, mcptool.ErrNilPrincipalExtractor)

		_, err = issuereportsmcp.NewTools(h.store, h.db, authenticate, extractPrincipal, nil, triageAuthorizer{})
		test.ErrorIs(t, err, mcptool.ErrNilGrantsExtractor)
	})
}

func TestTools_Schemas(T *testing.T) {
	T.Parallel()

	h := newHarness(T, triageAuthorizer{})

	listed, err := h.session.ListTools(T.Context(), nil)
	must.NoError(T, err)

	byName := map[string]*sdkmcp.Tool{}
	for _, tool := range listed.Tools {
		byName[tool.Name] = tool
	}

	must.MapLen(T, 4, byName)

	// schemaOf decodes a listed schema, which is what a model is handed.
	schemaOf := func(t *testing.T, raw any) map[string]any {
		t.Helper()

		encoded, marshalErr := json.Marshal(raw)
		must.NoError(t, marshalErr)

		var schema map[string]any
		must.NoError(t, json.Unmarshal(encoded, &schema))

		return schema
	}

	T.Run("every tool is read-only", func(t *testing.T) {
		t.Parallel()

		for name, tool := range byName {
			must.NotNil(t, tool.Annotations, must.Sprint(name))
			test.True(t, tool.Annotations.ReadOnlyHint, test.Sprint(name))
		}
	})

	T.Run("the input properties are the json tags the handlers decode", func(t *testing.T) {
		t.Parallel()

		get := schemaOf(t, byName[issuereportsmcp.ToolGetReport].InputSchema)
		test.MapContainsKey(t, get["properties"].(map[string]any), "reportID")
		test.Eq(t, []any{"reportID"}, get["required"].([]any))

		byStatus := schemaOf(t, byName[issuereportsmcp.ToolListReportsByStatus].InputSchema)
		properties := byStatus["properties"].(map[string]any)
		test.MapContainsKey(t, properties, "filter")

		status := properties["status"].(map[string]any)
		test.Eq(t, []any{"open", "acknowledged", "resolved", "declined"}, status["enum"].([]any))

		filter := properties["filter"].(map[string]any)["properties"].(map[string]any)
		test.MapContainsKey(t, filter, "maxResponseSize")
		test.MapContainsKey(t, filter, "sortBy")

		// The reporter is optional: an unnamed one is the caller.
		byReporter := schemaOf(t, byName[issuereportsmcp.ToolListReportsByReporter].InputSchema)
		reporterProperties := byReporter["properties"].(map[string]any)
		test.MapContainsKey(t, reporterProperties, "reporter")
		test.MapContainsKey(t, reporterProperties, "filter")
		test.MapNotContainsKey(t, byReporter, "required")
	})

	T.Run("the output describes the row by its json tags, without the fields it never marshals", func(t *testing.T) {
		t.Parallel()

		get := schemaOf(t, byName[issuereportsmcp.ToolGetReport].OutputSchema)
		properties := get["properties"].(map[string]any)

		for _, name := range []string{"id", "reporter", "kind", "details", "status", "closedAt", "scope"} {
			test.MapContainsKey(t, properties, name)
		}

		list := schemaOf(t, byName[issuereportsmcp.ToolListReports].OutputSchema)
		listed := list["properties"].(map[string]any)
		test.MapContainsKey(t, listed, "data")
		test.MapContainsKey(t, listed, "cursor")
		test.MapContainsKey(t, listed, "countsKnown")
	})
}

func TestTools_EveryPropertyIsDescribed(T *testing.T) {
	T.Parallel()

	h := newHarness(T, triageAuthorizer{})

	listed, err := h.session.ListTools(T.Context(), nil)
	must.NoError(T, err)

	for _, tool := range listed.Tools {
		for kind, raw := range map[string]any{"input": tool.InputSchema, "output": tool.OutputSchema} {
			encoded, marshalErr := json.Marshal(raw)
			must.NoError(T, marshalErr)

			var schema jsonschema.Schema
			must.NoError(T, json.Unmarshal(encoded, &schema))

			test.SliceEmpty(T, mcptool.Undescribed(&schema), test.Sprintf("%s %s", tool.Name, kind))
		}
	}
}

func TestFieldDocs_AreCurrent(T *testing.T) {
	T.Parallel()

	specs, err := mcptool.DirectiveSpecs("tools.go")
	must.NoError(T, err)

	extracted, err := mcptool.Extract(specs...)
	must.NoError(T, err)

	must.Eq(T, extracted, issuereportsmcp.FieldDocs, must.Sprint("the field doc comments changed; run go generate ./issuereports/mcp"))
}

func TestTools_GetReport(T *testing.T) {
	T.Parallel()

	T.Run("reads the caller's own report", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, triageAuthorizer{})
		filed := h.seed(t, testScope, reporter)

		h.as(reporter, testScope)
		got := decode[issuereports.Report](t, h.call(t, issuereportsmcp.ToolGetReport, map[string]any{"reportID": filed.ID}))

		test.EqOp(t, filed.ID, got.ID)
		test.EqOp(t, issuereports.StatusOpen, got.Status)
		test.EqOp(t, reporter, got.Reporter)
	})

	T.Run("somebody else's report reads as absent", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, triageAuthorizer{})
		filed := h.seed(t, testScope, reporter)

		h.as(otherReporter, testScope)
		result := h.call(t, issuereportsmcp.ToolGetReport, map[string]any{"reportID": filed.ID})

		test.True(t, result.IsError)
		test.EqOp(t, issuereports.ErrReportNotFound.Error(), text(t, result))
	})

	T.Run("another tenant's report reads as absent", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, triageAuthorizer{})
		filed := h.seed(t, testScope, reporter)

		h.as(reporter, otherScope)
		result := h.call(t, issuereportsmcp.ToolGetReport, map[string]any{"reportID": filed.ID})

		test.True(t, result.IsError)
		test.EqOp(t, issuereports.ErrReportNotFound.Error(), text(t, result))
	})

	T.Run("a call carrying nobody is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, triageAuthorizer{})
		filed := h.seed(t, testScope, reporter)

		result := h.call(t, issuereportsmcp.ToolGetReport, map[string]any{"reportID": filed.ID})

		test.True(t, result.IsError)
		test.EqOp(t, mcptool.ErrNoPrincipal.Error(), text(t, result))
	})

	T.Run("a caller without the read grant is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, triageAuthorizer{})
		filed := h.seed(t, testScope, reporter)

		h.as("user_without_grants", testScope)
		result := h.call(t, issuereportsmcp.ToolGetReport, map[string]any{"reportID": filed.ID})

		test.True(t, result.IsError)
		test.EqOp(t, mcptool.ErrPermissionDenied.Error(), text(t, result))
	})

	T.Run("an authorizer that cannot decide is a failure, and its words are not repeated", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, brokenAuthorizer{})
		filed := h.seed(t, testScope, reporter)

		h.as(reporter, testScope)
		result := h.call(t, issuereportsmcp.ToolGetReport, map[string]any{"reportID": filed.ID})

		test.True(t, result.IsError)
		test.EqOp(t, mcptool.ErrToolFailed.Error(), text(t, result))
	})

	T.Run("an argument the schema does not name is refused before the handler runs", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, triageAuthorizer{})
		h.as(reporter, testScope)

		result := h.call(t, issuereportsmcp.ToolGetReport, map[string]any{"ReportID": "anything"})

		test.True(t, result.IsError)
	})
}

func TestTools_ListReports(T *testing.T) {
	T.Parallel()

	T.Run("pages the tenant's queue, honoring a filter keyed as the schema says", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, triageAuthorizer{})
		h.seed(t, testScope, reporter)
		h.seed(t, testScope, otherReporter)
		h.seed(t, otherScope, reporter)

		h.as(triager, testScope)
		page := decode[filtering.QueryFilteredResult[issuereports.Report]](t,
			h.call(t, issuereportsmcp.ToolListReports, map[string]any{"filter": map[string]any{"maxResponseSize": 1}}))

		test.SliceLen(t, 1, page.Data)
		test.EqOp(t, uint16(1), page.MaxResponseSize)

		whole := decode[filtering.QueryFilteredResult[issuereports.Report]](t,
			h.call(t, issuereportsmcp.ToolListReports, map[string]any{}))
		test.SliceLen(t, 2, whole.Data)
	})

	T.Run("a reporter without the triage grant is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, triageAuthorizer{})
		h.as(reporter, testScope)

		result := h.call(t, issuereportsmcp.ToolListReports, map[string]any{})

		test.True(t, result.IsError)
		test.EqOp(t, mcptool.ErrPermissionDenied.Error(), text(t, result))
	})

	T.Run("archived reports are honored only for the archive grant", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, triageAuthorizer{})
		filed := h.seed(t, testScope, reporter)
		h.seed(t, testScope, reporter)

		must.NoError(t, h.db.WithTransaction(t.Context(), func(tx database.Tx) error {
			_, err := h.store.ArchiveReport(t.Context(), tx, testScope, filed.ID)

			return err
		}))

		args := map[string]any{"filter": map[string]any{"includeArchived": true}}

		h.as(triager, testScope)
		cleared := decode[filtering.QueryFilteredResult[issuereports.Report]](t, h.call(t, issuereportsmcp.ToolListReports, args))
		test.SliceLen(t, 1, cleared.Data)

		h.as(archivist, testScope)
		honored := decode[filtering.QueryFilteredResult[issuereports.Report]](t, h.call(t, issuereportsmcp.ToolListReports, args))
		test.SliceLen(t, 2, honored.Data)
	})

	T.Run("a sort direction that is not one is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, triageAuthorizer{})
		h.as(triager, testScope)

		result := h.call(t, issuereportsmcp.ToolListReports, map[string]any{"filter": map[string]any{"sortBy": "sideways"}})

		test.True(t, result.IsError)
	})
}

func TestTools_ListReportsByStatus(T *testing.T) {
	T.Parallel()

	T.Run("pages one status", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, triageAuthorizer{})
		filed := h.seed(t, testScope, reporter)
		h.seed(t, testScope, reporter)

		must.NoError(t, h.db.WithTransaction(t.Context(), func(tx database.Tx) error {
			_, err := h.store.TransitionReport(t.Context(), tx, testScope, filed.ID,
				issuereports.StatusOpen, issuereports.StatusResolved, "fixed")

			return err
		}))

		h.as(triager, testScope)
		page := decode[filtering.QueryFilteredResult[issuereports.Report]](t,
			h.call(t, issuereportsmcp.ToolListReportsByStatus, map[string]any{"status": "resolved"}))

		must.SliceLen(t, 1, page.Data)
		test.EqOp(t, filed.ID, page.Data[0].ID)
		test.EqOp(t, "fixed", page.Data[0].Resolution)
	})

	T.Run("a status the queue does not have is refused by the schema", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, triageAuthorizer{})
		h.as(triager, testScope)

		result := h.call(t, issuereportsmcp.ToolListReportsByStatus, map[string]any{"status": "closed"})

		test.True(t, result.IsError)
	})
}

func TestTools_ListReportsByReporter(T *testing.T) {
	T.Parallel()

	T.Run("an unnamed reporter is the caller, in the caller's tenant", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, triageAuthorizer{})
		h.seed(t, testScope, reporter)
		h.seed(t, testScope, reporter)
		h.seed(t, testScope, otherReporter)
		h.seed(t, otherScope, reporter)

		h.as(reporter, testScope)
		page := decode[filtering.QueryFilteredResult[issuereports.Report]](t,
			h.call(t, issuereportsmcp.ToolListReportsByReporter, map[string]any{}))

		must.SliceLen(t, 2, page.Data)
		for _, report := range page.Data {
			test.EqOp(t, reporter, report.Reporter)
			test.EqOp(t, testScope, report.Scope)
		}
	})

	T.Run("naming somebody else is refused before the read, whether or not they filed anything", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, triageAuthorizer{})
		h.seed(t, testScope, otherReporter)

		h.as(reporter, testScope)

		for _, named := range []string{otherReporter, "user_who_never_filed"} {
			result := h.call(t, issuereportsmcp.ToolListReportsByReporter, map[string]any{"reporter": named})

			test.True(t, result.IsError, test.Sprint(named))
			test.EqOp(t, issuereportsmcp.ErrReporterNotPermitted.Error(), text(t, result), test.Sprint(named))
		}
	})

	T.Run("a rule that lets the caller name somebody reads their reports", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, triageAuthorizer{})
		h.seed(t, testScope, reporter)
		h.seed(t, testScope, otherReporter)

		h.as(triager, testScope)
		page := decode[filtering.QueryFilteredResult[issuereports.Report]](t,
			h.call(t, issuereportsmcp.ToolListReportsByReporter, map[string]any{"reporter": reporter}))

		must.SliceLen(t, 1, page.Data)
		test.EqOp(t, reporter, page.Data[0].Reporter)
	})

	T.Run("a caller without the read grant is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, triageAuthorizer{})
		h.as("user_without_grants", testScope)

		result := h.call(t, issuereportsmcp.ToolListReportsByReporter, map[string]any{})

		test.True(t, result.IsError)
		test.EqOp(t, mcptool.ErrPermissionDenied.Error(), text(t, result))
	})

	T.Run("archived reports are honored only for the archive grant", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, triageAuthorizer{})
		filed := h.seed(t, testScope, reporter)
		h.seed(t, testScope, reporter)

		must.NoError(t, h.db.WithTransaction(t.Context(), func(tx database.Tx) error {
			_, err := h.store.ArchiveReport(t.Context(), tx, testScope, filed.ID)

			return err
		}))

		args := map[string]any{"reporter": reporter, "filter": map[string]any{"includeArchived": true}}

		h.as(reporter, testScope)
		cleared := decode[filtering.QueryFilteredResult[issuereports.Report]](t,
			h.call(t, issuereportsmcp.ToolListReportsByReporter, args))
		test.SliceLen(t, 1, cleared.Data)

		h.as(archivist, testScope)
		honored := decode[filtering.QueryFilteredResult[issuereports.Report]](t,
			h.call(t, issuereportsmcp.ToolListReportsByReporter, args))
		test.SliceLen(t, 2, honored.Data)
	})

	T.Run("an authorizer that cannot decide is a failure, and its words are not repeated", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, brokenAuthorizer{})
		h.seed(t, testScope, reporter)

		h.as(reporter, testScope)
		result := h.call(t, issuereportsmcp.ToolListReportsByReporter, map[string]any{})

		test.True(t, result.IsError)
		test.EqOp(t, mcptool.ErrToolFailed.Error(), text(t, result))
	})
}

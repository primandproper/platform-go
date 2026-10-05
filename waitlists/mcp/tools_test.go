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
	"github.com/primandproper/platform-go/v15/internal/mcptool"
	"github.com/primandproper/platform-go/v15/waitlists"
	waitlistsgrpc "github.com/primandproper/platform-go/v15/waitlists/grpc"
	waitlistsmcp "github.com/primandproper/platform-go/v15/waitlists/mcp"
	"github.com/primandproper/platform-go/v15/waitlists/migrations"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/sqlite"
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
	administrator = "user_1"
	archivist     = "user_2"
	visitor       = "user_3"
)

var grantsByUser = map[string][]authorization.Permission{
	administrator: {waitlistsgrpc.PermissionReadLists},
	archivist:     {waitlistsgrpc.PermissionReadLists, waitlistsgrpc.PermissionArchiveLists},
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

type harness struct {
	db      database.Client
	store   *waitlists.SQLStore
	session *sdkmcp.ClientSession
	caller  atomic.Pointer[testPrincipal]
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	db, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "waitlists.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	prefix := fmt.Sprintf("wlm_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(dialect.SQLite, prefix)
	must.NoError(t, err)

	for _, stmt := range stmts {
		_, execErr := db.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr)
	}

	store, err := waitlists.NewSQLStore(db, waitlists.WithTablePrefix(prefix))
	must.NoError(t, err)

	h := &harness{db: db, store: store}

	authenticate := func(ctx context.Context, _ *sdkmcp.CallToolRequest) (context.Context, error) {
		if p := h.caller.Load(); p != nil {
			return context.WithValue(ctx, principalKey{}, callers.Principal(p)), nil
		}

		return ctx, nil
	}

	tools, err := waitlistsmcp.NewTools(store, db, authenticate, extractPrincipal, extractGrants)
	must.NoError(t, err)

	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "waitlists", Version: "test"}, nil)
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

func (h *harness) seed(t *testing.T, scope tenancy.Scope, name string, closesAt time.Time) *waitlists.List {
	t.Helper()

	var created *waitlists.List

	must.NoError(t, h.db.WithTransaction(t.Context(), func(tx database.Tx) error {
		var err error
		created, err = h.store.CreateList(t.Context(), tx, scope, &waitlists.List{
			Name: name, Description: "early access", ClosesAt: closesAt,
		})

		return err
	}))

	return created
}

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

func later() time.Time   { return time.Now().Add(24 * time.Hour).Truncate(time.Second) }
func earlier() time.Time { return time.Now().Add(-24 * time.Hour).Truncate(time.Second) }

func TestNewTools(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil store or client", func(t *testing.T) {
		t.Parallel()

		authenticate := func(ctx context.Context, _ *sdkmcp.CallToolRequest) (context.Context, error) { return ctx, nil }

		_, err := waitlistsmcp.NewTools(nil, nil, authenticate, extractPrincipal, extractGrants)
		test.ErrorIs(t, err, waitlistsmcp.ErrNilStore)

		h := newHarness(t)

		_, err = waitlistsmcp.NewTools(h.store, nil, authenticate, extractPrincipal, extractGrants)
		test.ErrorIs(t, err, waitlistsmcp.ErrNilDatabaseClient)
	})
}

func TestTools_Schemas(T *testing.T) {
	T.Parallel()

	h := newHarness(T)

	listed, err := h.session.ListTools(T.Context(), nil)
	must.NoError(T, err)
	must.SliceLen(T, 3, listed.Tools)

	for _, tool := range listed.Tools {
		must.NotNil(T, tool.Annotations)
		test.True(T, tool.Annotations.ReadOnlyHint, test.Sprint(tool.Name))

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

	must.Eq(T, extracted, waitlistsmcp.FieldDocs, must.Sprint("the field doc comments changed; run go generate ./waitlists/mcp"))
}

func TestTools_GetList(T *testing.T) {
	T.Parallel()

	T.Run("reads the tenant's list", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		seeded := h.seed(t, testScope, "beta", later())

		h.as(administrator, testScope)
		got := decode[waitlists.List](t, h.call(t, waitlistsmcp.ToolGetList, map[string]any{"listID": seeded.ID}))

		test.EqOp(t, seeded.ID, got.ID)
		test.EqOp(t, "beta", got.Name)
	})

	T.Run("another tenant's list reads as absent", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		seeded := h.seed(t, testScope, "beta", later())

		h.as(administrator, otherScope)
		result := h.call(t, waitlistsmcp.ToolGetList, map[string]any{"listID": seeded.ID})

		test.True(t, result.IsError)
		test.EqOp(t, waitlists.ErrListNotFound.Error(), text(t, result))
	})

	T.Run("a caller without the read grant is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		seeded := h.seed(t, testScope, "beta", later())

		h.as(visitor, testScope)
		result := h.call(t, waitlistsmcp.ToolGetList, map[string]any{"listID": seeded.ID})

		test.True(t, result.IsError)
		test.EqOp(t, mcptool.ErrPermissionDenied.Error(), text(t, result))
	})
}

func TestTools_ListLists(T *testing.T) {
	T.Parallel()

	T.Run("pages the whole catalog, closed lists included", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.seed(t, testScope, "open", later())
		h.seed(t, testScope, "closed", earlier())
		h.seed(t, otherScope, "elsewhere", later())

		h.as(administrator, testScope)
		page := decode[filtering.QueryFilteredResult[waitlists.List]](t, h.call(t, waitlistsmcp.ToolListLists, map[string]any{}))

		test.SliceLen(t, 2, page.Data)
	})

	T.Run("archived lists are honored only for the archive grant", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		archived := h.seed(t, testScope, "retired", later())
		h.seed(t, testScope, "live", later())

		must.NoError(t, h.db.WithTransaction(t.Context(), func(tx database.Tx) error {
			_, err := h.store.ArchiveList(t.Context(), tx, testScope, archived.ID)

			return err
		}))

		args := map[string]any{"filter": map[string]any{"includeArchived": true}}

		h.as(administrator, testScope)
		test.SliceLen(t, 1, decode[filtering.QueryFilteredResult[waitlists.List]](t,
			h.call(t, waitlistsmcp.ToolListLists, args)).Data)

		h.as(archivist, testScope)
		test.SliceLen(t, 2, decode[filtering.QueryFilteredResult[waitlists.List]](t,
			h.call(t, waitlistsmcp.ToolListLists, args)).Data)
	})
}

func TestTools_ListOpenLists(T *testing.T) {
	T.Parallel()

	T.Run("pages the open lists for a caller holding no grants", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		open := h.seed(t, testScope, "open", later())
		h.seed(t, testScope, "closed", earlier())

		h.as(visitor, testScope)
		page := decode[filtering.QueryFilteredResult[waitlists.List]](t, h.call(t, waitlistsmcp.ToolListOpenLists, map[string]any{}))

		must.SliceLen(t, 1, page.Data)
		test.EqOp(t, open.ID, page.Data[0].ID)
	})

	T.Run("still needs a caller", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		result := h.call(t, waitlistsmcp.ToolListOpenLists, map[string]any{})

		test.True(t, result.IsError)
		test.EqOp(t, mcptool.ErrNoPrincipal.Error(), text(t, result))
	})
}

package mcp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/internal/mcptool"
	"github.com/primandproper/platform-go/v14/webhooks"
	webhooksgrpc "github.com/primandproper/platform-go/v14/webhooks/grpc"
	webhooksmcp "github.com/primandproper/platform-go/v14/webhooks/mcp"
	"github.com/primandproper/platform-go/v14/webhooks/migrations"

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
	orderCreated webhooks.EventType = "order.created"
	orderShipped webhooks.EventType = "order.shipped"

	testURL = "https://subscriber.example/hooks"

	reader    = "user_1"
	archivist = "user_2"
	nobody    = "user_3"

	// routingToken is a static header value, which the tools never answer with.
	routingToken = "routing-token-that-is-a-credential"
)

var grantsByUser = map[string][]authorization.Permission{
	reader: {webhooksgrpc.PermissionReadEndpoints, webhooksgrpc.PermissionReadEventTypes},
	archivist: {
		webhooksgrpc.PermissionReadEndpoints, webhooksgrpc.PermissionArchiveEndpoints,
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

type harness struct {
	db         database.Client
	store      *webhooks.SQLStore
	dispatcher *webhooks.StoreDispatcher
	session    *sdkmcp.ClientSession
	caller     atomic.Pointer[testPrincipal]
}

func newHarness(t *testing.T) *harness {
	t.Helper()

	db, err := sqlite.NewDatabaseClient(t.Context(),
		&testClientConfig{connectionString: filepath.Join(t.TempDir(), "webhooks.db")})
	must.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	prefix := fmt.Sprintf("whm_%d", prefixCounter.Add(1))

	stmts, err := migrations.Statements(dialect.SQLite, prefix)
	must.NoError(t, err)

	for _, stmt := range stmts {
		_, execErr := db.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr)
	}

	store, err := webhooks.NewSQLStore(db, webhooks.WithTablePrefix(prefix))
	must.NoError(t, err)

	dispatcher, err := webhooks.NewDispatcher(store, db.Reader(),
		webhooks.WithCatalog(webhooks.Catalog{
			orderCreated: {Description: "an order was placed"},
			orderShipped: {Description: "an order left the warehouse"},
		}),
		webhooks.WithDispatcherURLChecker(func(context.Context, string) error { return nil }),
	)
	must.NoError(t, err)

	h := &harness{db: db, store: store, dispatcher: dispatcher}

	authenticate := func(ctx context.Context, _ *sdkmcp.CallToolRequest) (context.Context, error) {
		if p := h.caller.Load(); p != nil {
			return context.WithValue(ctx, principalKey{}, callers.Principal(p)), nil
		}

		return ctx, nil
	}

	tools, err := webhooksmcp.NewTools(dispatcher, store, db, authenticate, extractPrincipal, extractGrants)
	must.NoError(t, err)

	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "webhooks", Version: "test"}, nil)
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

func (h *harness) seed(t *testing.T, scope tenancy.Scope) *webhooks.Endpoint {
	t.Helper()

	var registered *webhooks.Endpoint

	must.NoError(t, h.db.WithTransaction(t.Context(), func(tx database.Tx) error {
		var err error
		registered, err = h.dispatcher.Register(t.Context(), tx, scope, &webhooks.Endpoint{
			Name:          "seeded subscriber",
			URL:           testURL,
			Headers:       map[string]string{"X-Routing": routingToken},
			Secret:        webhooks.Secret{Current: []byte("seeded-signing-key")},
			Subscriptions: webhooks.SubscribeTo(orderCreated),
		})

		return err
	}))

	return registered
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

func TestNewTools(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil dispatcher, store or client", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		authenticate := func(ctx context.Context, _ *sdkmcp.CallToolRequest) (context.Context, error) { return ctx, nil }

		_, err := webhooksmcp.NewTools(nil, h.store, h.db, authenticate, extractPrincipal, extractGrants)
		test.ErrorIs(t, err, webhooksmcp.ErrNilDispatcher)

		_, err = webhooksmcp.NewTools(h.dispatcher, nil, h.db, authenticate, extractPrincipal, extractGrants)
		test.ErrorIs(t, err, webhooksmcp.ErrNilStore)

		_, err = webhooksmcp.NewTools(h.dispatcher, h.store, nil, authenticate, extractPrincipal, extractGrants)
		test.ErrorIs(t, err, webhooksmcp.ErrNilDatabaseClient)

		_, err = webhooksmcp.NewTools(h.dispatcher, h.store, h.db, nil, extractPrincipal, extractGrants)
		test.ErrorIs(t, err, mcptool.ErrNilAuthenticator)
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

			test.StrNotContains(T, string(encoded), `"headers"`, test.Sprintf("%s %s", tool.Name, kind))

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

	must.Eq(T, extracted, webhooksmcp.FieldDocs, must.Sprint("the field doc comments changed; run go generate ./webhooks/mcp"))
}

func TestTools_GetEndpoint(T *testing.T) {
	T.Parallel()

	T.Run("reads the tenant's endpoint, without its headers or its secret", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		seeded := h.seed(t, testScope)

		h.as(reader, testScope)
		result := h.call(t, webhooksmcp.ToolGetEndpoint, map[string]any{"endpointID": seeded.ID})

		got := decode[webhooks.Endpoint](t, result)
		test.EqOp(t, seeded.ID, got.ID)
		test.EqOp(t, testURL, got.URL)
		must.SliceLen(t, 1, got.Subscriptions)
		test.EqOp(t, orderCreated, got.Subscriptions[0].EventType)
		test.MapEmpty(t, got.Headers)

		test.StrNotContains(t, text(t, result), routingToken)
		test.StrNotContains(t, text(t, result), "seeded-signing-key")
	})

	T.Run("another tenant's endpoint reads as absent", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		seeded := h.seed(t, testScope)

		h.as(reader, otherScope)
		result := h.call(t, webhooksmcp.ToolGetEndpoint, map[string]any{"endpointID": seeded.ID})

		test.True(t, result.IsError)
		test.EqOp(t, webhooksmcp.ErrEndpointNotFound.Error(), text(t, result))
	})

	T.Run("a caller without the read grant is refused", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		seeded := h.seed(t, testScope)

		h.as(nobody, testScope)
		result := h.call(t, webhooksmcp.ToolGetEndpoint, map[string]any{"endpointID": seeded.ID})

		test.True(t, result.IsError)
		test.EqOp(t, mcptool.ErrPermissionDenied.Error(), text(t, result))
	})
}

func TestTools_ListEndpoints(T *testing.T) {
	T.Parallel()

	T.Run("pages the tenant's endpoints without their headers", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.seed(t, testScope)
		h.seed(t, testScope)
		h.seed(t, otherScope)

		h.as(reader, testScope)
		result := h.call(t, webhooksmcp.ToolListEndpoints, map[string]any{})

		page := decode[filtering.QueryFilteredResult[webhooks.Endpoint]](t, result)
		test.SliceLen(t, 2, page.Data)
		test.False(t, strings.Contains(text(t, result), routingToken))

		one := decode[filtering.QueryFilteredResult[webhooks.Endpoint]](t,
			h.call(t, webhooksmcp.ToolListEndpoints, map[string]any{"filter": map[string]any{"maxResponseSize": 1}}))
		test.SliceLen(t, 1, one.Data)
	})

	T.Run("archived endpoints are honored only for the archive grant", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		archived := h.seed(t, testScope)
		h.seed(t, testScope)

		must.NoError(t, h.db.WithTransaction(t.Context(), func(tx database.Tx) error {
			_, err := h.store.ArchiveEndpoint(t.Context(), tx, testScope, archived.ID)

			return err
		}))

		args := map[string]any{"filter": map[string]any{"includeArchived": true}}

		h.as(reader, testScope)
		test.SliceLen(t, 1, decode[filtering.QueryFilteredResult[webhooks.Endpoint]](t,
			h.call(t, webhooksmcp.ToolListEndpoints, args)).Data)

		h.as(archivist, testScope)
		test.SliceLen(t, 2, decode[filtering.QueryFilteredResult[webhooks.Endpoint]](t,
			h.call(t, webhooksmcp.ToolListEndpoints, args)).Data)
	})
}

func TestTools_ListEventTypes(T *testing.T) {
	T.Parallel()

	T.Run("answers the catalog, sorted, with its prose", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.as(reader, testScope)

		got := decode[webhooksmcp.EventTypes](t, h.call(t, webhooksmcp.ToolListEventTypes, map[string]any{}))

		must.SliceLen(t, 2, got.Results)
		test.EqOp(t, orderCreated, got.Results[0].EventType)
		test.EqOp(t, "an order was placed", got.Results[0].Description)
		test.EqOp(t, orderShipped, got.Results[1].EventType)
	})

	T.Run("is behind its own grant", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		h.as(archivist, testScope)

		result := h.call(t, webhooksmcp.ToolListEventTypes, map[string]any{})

		test.True(t, result.IsError)
		test.EqOp(t, mcptool.ErrPermissionDenied.Error(), text(t, result))
	})
}

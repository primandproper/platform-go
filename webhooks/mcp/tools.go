package mcp

import (
	"context"
	"database/sql"
	"errors"

	"github.com/primandproper/platform-go/v15/callers"
	"github.com/primandproper/platform-go/v15/mcptool"
	"github.com/primandproper/platform-go/v15/webhooks"
	webhooksgrpc "github.com/primandproper/platform-go/v15/webhooks/grpc"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:generate go run ../../mcptool/mcpdocs -pkg mcp -out fielddocs_gen.go github.com/primandproper/platform-go/v15/webhooks.Endpoint github.com/primandproper/platform-go/v15/webhooks.Subscription github.com/primandproper/platform-go/v15/webhooks/mcp.GetEndpointInput github.com/primandproper/platform-go/v15/webhooks/mcp.ListEndpointsInput github.com/primandproper/platform-go/v15/webhooks/mcp.ListEventTypesInput github.com/primandproper/platform-go/v15/webhooks/mcp.EventTypes github.com/primandproper/platform-go/v15/webhooks/mcp.EventTypeDefinition

// surfaceName scopes this surface's spans, logger and instruments.
const surfaceName = "webhooks_mcp"

// archivedClearedKey records that a read asked for archived endpoints and did
// not hold the grant that reaches them.
const archivedClearedKey = "webhooks_mcp.include_archived_cleared"

// headersProperty is the endpoint property these tools never answer with.
const headersProperty = "headers"

// The tools this surface serves, by the name a model calls them.
const (
	ToolGetEndpoint    = "get_webhook_endpoint"
	ToolListEndpoints  = "list_webhook_endpoints"
	ToolListEventTypes = "list_webhook_event_types"
)

var (
	// ErrNilDispatcher is a tool surface built with no dispatcher to read the
	// catalog from.
	ErrNilDispatcher = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil webhook dispatcher for the MCP tools")

	// ErrNilStore is a tool surface built over no store.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil webhooks store for the MCP tools")

	// ErrNilDatabaseClient is a tool surface built with no handle to read on.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client for the webhooks MCP tools")

	// ErrEndpointNotFound is an endpoint that is not in the caller's scope:
	// absent and somebody else's are one answer. The store answers it as
	// database/sql.ErrNoRows, whose text names the driver rather than the
	// endpoint, so this is what a model is told instead.
	ErrEndpointNotFound = platformerrors.New("no such webhook endpoint in the caller's scope")
)

// Authenticator turns one tool call into the context the principal and grants
// extractors read. See [NewTools].
type Authenticator = mcptool.Authenticator

// GetEndpointInput is what get_webhook_endpoint takes.
type GetEndpointInput struct {
	// EndpointID is the identifier of the endpoint to read.
	EndpointID string `json:"endpointID"`
}

// ListEndpointsInput is what list_webhook_endpoints takes.
type ListEndpointsInput struct {
	// Filter is the page to read. Absent reads the first page, oldest first.
	Filter *filtering.QueryFilter `json:"filter,omitempty"`
}

// ListEventTypesInput is what list_webhook_event_types takes: nothing. The
// catalog is the same for every tenant and is never paged.
type ListEventTypesInput struct{}

// EventTypes is what list_webhook_event_types answers with.
type EventTypes struct {
	// Results is every event type in the application's catalog, sorted.
	Results []EventTypeDefinition `json:"results"`
}

// EventTypeDefinition is one event type a subscription may name.
type EventTypeDefinition struct {
	// EventType is the event type, as a subscription names it.
	EventType webhooks.EventType `json:"eventType"`
	// Description is prose explaining when the event fires.
	Description string `json:"description"`
}

// Tools is the read-only MCP tool surface over webhook endpoints and the event
// catalog.
type Tools struct {
	dispatcher webhooks.Dispatcher
	store      webhooks.Store
	client     database.Client
	surface    *mcptool.Surface

	getEndpoint    *sdkmcp.Tool
	listEndpoints  *sdkmcp.Tool
	listEventTypes *sdkmcp.Tool
}

// NewTools builds the tool surface.
//
// It takes what webhooks/grpc's server takes, plus the authenticator and the
// grants extractor a tool needs in place of the interceptor a gRPC method has
// in front of it. See mcptool.
func NewTools(
	dispatcher webhooks.Dispatcher,
	store webhooks.Store,
	client database.Client,
	authenticate Authenticator,
	principals callers.PrincipalExtractor,
	grants authorization.GrantsExtractor,
	opts ...Option,
) (*Tools, error) {
	if dispatcher == nil {
		return nil, ErrNilDispatcher
	}

	if store == nil {
		return nil, ErrNilStore
	}

	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	var o options
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}

	surface, err := mcptool.NewSurface(surfaceName, authenticate, principals, grants,
		mcptool.WithClientSafeSentinels(ErrEndpointNotFound),
		mcptool.WithLogger(o.logger),
		mcptool.WithTracerProvider(o.tracerProvider),
		mcptool.WithMetricsProvider(o.metricsProvider),
	)
	if err != nil {
		return nil, err
	}

	endpoint := mcptool.Output[webhooks.Endpoint](fieldDocs, nil)
	delete(endpoint.Properties, headersProperty)

	page := mcptool.Output[filtering.QueryFilteredResult[webhooks.Endpoint]](fieldDocs, nil)
	delete(page.Properties["data"].Items.Properties, headersProperty)

	return &Tools{
		dispatcher: dispatcher,
		store:      store,
		client:     client,
		surface:    surface,
		getEndpoint: &sdkmcp.Tool{
			Name:         ToolGetEndpoint,
			Description:  "Read one of the tenant's webhook endpoints by its identifier: where deliveries go, and which events it is subscribed to.",
			InputSchema:  mcptool.Input[GetEndpointInput](fieldDocs, nil),
			OutputSchema: endpoint,
			Annotations:  readOnly(),
		},
		listEndpoints: &sdkmcp.Tool{
			Name:         ToolListEndpoints,
			Description:  "Page the tenant's webhook endpoints.",
			InputSchema:  mcptool.Input[ListEndpointsInput](fieldDocs, nil),
			OutputSchema: page,
			Annotations:  readOnly(),
		},
		listEventTypes: &sdkmcp.Tool{
			Name:         ToolListEventTypes,
			Description:  "List the event types a webhook endpoint may subscribe to, with when each one fires.",
			InputSchema:  mcptool.Input[ListEventTypesInput](fieldDocs, nil),
			OutputSchema: mcptool.Output[EventTypes](fieldDocs, nil),
			Annotations:  readOnly(),
		},
	}, nil
}

// RegisterOn adds this surface's tools to an MCP server.
func (t *Tools) RegisterOn(srv *sdkmcp.Server) {
	sdkmcp.AddTool(srv, t.getEndpoint, t.GetEndpoint)
	sdkmcp.AddTool(srv, t.listEndpoints, t.ListEndpoints)
	sdkmcp.AddTool(srv, t.listEventTypes, t.ListEventTypes)
}

// GetEndpoint reads one of the tenant's endpoints.
//
// The signing secret never leaves the store's value — it does not marshal —
// and the static headers are cleared before the answer is built. A gRPC client
// managing its endpoints reads them back; a model has no use for them, and a
// routing token is as often a credential, which is why webhooks keeps them out
// of audit diffs too. A model's context is a log somebody else keeps.
func (t *Tools) GetEndpoint(
	ctx context.Context,
	req *sdkmcp.CallToolRequest,
	in GetEndpointInput,
) (*sdkmcp.CallToolResult, *webhooks.Endpoint, error) {
	ctx, call, err := t.surface.Begin(ctx, req, ToolGetEndpoint, webhooksgrpc.PermissionReadEndpoints)
	if err != nil {
		return nil, nil, err
	}

	call.Op.Set("webhooks_mcp.endpoint_id", in.EndpointID)

	endpoint, err := t.store.GetEndpoint(ctx, t.client.Reader(), call.Scope, in.EndpointID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			err = platformerrors.Wrap(ErrEndpointNotFound, err.Error())
		}

		return nil, nil, call.End(err)
	}

	endpoint.Headers = nil

	return nil, endpoint, call.End(nil)
}

// ListEndpoints pages the tenant's endpoints. include_archived is honored only
// for a caller holding the archive grant.
func (t *Tools) ListEndpoints(
	ctx context.Context,
	req *sdkmcp.CallToolRequest,
	in ListEndpointsInput,
) (*sdkmcp.CallToolResult, *filtering.QueryFilteredResult[webhooks.Endpoint], error) {
	ctx, call, err := t.surface.Begin(ctx, req, ToolListEndpoints, webhooksgrpc.PermissionReadEndpoints)
	if err != nil {
		return nil, nil, err
	}

	filter, err := call.Filter(ctx, in.Filter, webhooksgrpc.PermissionArchiveEndpoints, archivedClearedKey)
	if err != nil {
		return nil, nil, call.End(err)
	}

	page, err := t.store.ListEndpoints(ctx, t.client.Reader(), call.Scope, filter)
	if err != nil {
		return nil, nil, call.End(err)
	}

	for _, endpoint := range page.Data {
		endpoint.Headers = nil
	}

	return nil, page, call.End(nil)
}

// ListEventTypes answers what a subscription may name: the application's
// catalog, which is the same for every tenant and so binds no scope, less the
// events it marks Internal, which no subscription may name. It is
// webhooks/grpc's ListEventTypes over another transport, behind the same
// grant.
func (t *Tools) ListEventTypes(
	ctx context.Context,
	req *sdkmcp.CallToolRequest,
	_ ListEventTypesInput,
) (*sdkmcp.CallToolResult, *EventTypes, error) {
	_, call, err := t.surface.Begin(ctx, req, ToolListEventTypes, webhooksgrpc.PermissionReadEventTypes)
	if err != nil {
		return nil, nil, err
	}

	catalog := t.dispatcher.Catalog()

	out := &EventTypes{Results: make([]EventTypeDefinition, 0, len(catalog))}
	for _, eventType := range catalog.SubscribableEventTypes() {
		out.Results = append(out.Results, EventTypeDefinition{
			EventType:   eventType,
			Description: catalog[eventType].Description,
		})
	}

	return nil, out, call.End(nil)
}

func readOnly() *sdkmcp.ToolAnnotations {
	return &sdkmcp.ToolAnnotations{ReadOnlyHint: true}
}

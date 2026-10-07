package mcp

import (
	"context"

	"github.com/primandproper/platform-go/v15/callers"
	"github.com/primandproper/platform-go/v15/mcptool"
	"github.com/primandproper/platform-go/v15/waitlists"
	waitlistsgrpc "github.com/primandproper/platform-go/v15/waitlists/grpc"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:generate go run ../../mcptool/mcpdocs -pkg mcp -out fielddocs_gen.go github.com/primandproper/platform-go/v15/waitlists.List github.com/primandproper/platform-go/v15/waitlists/mcp.GetListInput github.com/primandproper/platform-go/v15/waitlists/mcp.ListListsInput

// surfaceName scopes this surface's spans, logger and instruments.
const surfaceName = "waitlists_mcp"

// archivedClearedKey records that a read asked for archived lists and did not
// hold the grant that reaches them.
const archivedClearedKey = "waitlists_mcp.include_archived_cleared"

// The tools this surface serves, by the name a model calls them.
const (
	ToolGetList       = "get_waitlist"
	ToolListLists     = "list_waitlists"
	ToolListOpenLists = "list_open_waitlists"
)

var (
	// ErrNilStore is a tool surface built over no store.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil waitlists store for the MCP tools")

	// ErrNilDatabaseClient is a tool surface built with no handle to read on.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client for the waitlists MCP tools")
)

// Authenticator turns one tool call into the context the principal and grants
// extractors read. See [NewTools].
type Authenticator = mcptool.Authenticator

// GetListInput is what get_waitlist takes.
type GetListInput struct {
	// ListID is the identifier of the waitlist to read.
	ListID string `json:"listID"`
}

// ListListsInput is what list_waitlists and list_open_waitlists take.
type ListListsInput struct {
	// Filter is the page to read. Absent reads the first page, oldest first.
	Filter *filtering.QueryFilter `json:"filter,omitempty"`
}

// Tools is the read-only MCP tool surface over the waitlist catalog.
//
// It is the catalog and nothing else. A signup holds the address its signatory
// typed, and over gRPC every read of one is behind its own grant and, for the
// public half, a [waitlistsgrpc.SignupAuthorizer]; a tool that paged a list's
// signups would put every signatory's address in a model's context. The
// signups are not reachable from here, and that is the ruling rather than a
// gap a later tool fills.
type Tools struct {
	store   waitlists.Store
	client  database.Client
	surface *mcptool.Surface

	getList       *sdkmcp.Tool
	listLists     *sdkmcp.Tool
	listOpenLists *sdkmcp.Tool
}

// NewTools builds the tool surface over a store.
//
// It takes the authenticator and the grants extractor a tool needs in place of
// the interceptor a gRPC method has in front of it. See mcptool.
func NewTools(
	store waitlists.Store,
	client database.Client,
	authenticate Authenticator,
	principals callers.PrincipalExtractor,
	grants authorization.GrantsExtractor,
	opts ...Option,
) (*Tools, error) {
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
		mcptool.WithClientSafeSentinels(waitlists.ErrListNotFound),
		mcptool.WithLogger(o.logger),
		mcptool.WithTracerProvider(o.tracerProvider),
		mcptool.WithMetricsProvider(o.metricsProvider),
	)
	if err != nil {
		return nil, err
	}

	page := mcptool.Output[filtering.QueryFilteredResult[waitlists.List]](fieldDocs, nil)

	return &Tools{
		store:   store,
		client:  client,
		surface: surface,
		getList: &sdkmcp.Tool{
			Name:         ToolGetList,
			Description:  "Read one of the tenant's waitlists by its identifier: what people are queueing for, and when it stops taking signups.",
			InputSchema:  mcptool.Input[GetListInput](fieldDocs, nil),
			OutputSchema: mcptool.Output[waitlists.List](fieldDocs, nil),
			Annotations:  readOnly(),
		},
		listLists: &sdkmcp.Tool{
			Name:         ToolListLists,
			Description:  "Page the tenant's whole waitlist catalog, open and closed alike.",
			InputSchema:  mcptool.Input[ListListsInput](fieldDocs, nil),
			OutputSchema: page,
			Annotations:  readOnly(),
		},
		listOpenLists: &sdkmcp.Tool{
			Name:         ToolListOpenLists,
			Description:  "Page the tenant's waitlists that are still taking signups.",
			InputSchema:  mcptool.Input[ListListsInput](fieldDocs, nil),
			OutputSchema: page,
			Annotations:  readOnly(),
		},
	}, nil
}

// RegisterOn adds this surface's tools to an MCP server.
func (t *Tools) RegisterOn(srv *sdkmcp.Server) {
	sdkmcp.AddTool(srv, t.getList, t.GetList)
	sdkmcp.AddTool(srv, t.listLists, t.ListLists)
	sdkmcp.AddTool(srv, t.listOpenLists, t.ListOpenLists)
}

// GetList reads one of the tenant's live lists, behind the read grant.
func (t *Tools) GetList(
	ctx context.Context,
	req *sdkmcp.CallToolRequest,
	in GetListInput,
) (*sdkmcp.CallToolResult, *waitlists.List, error) {
	ctx, call, err := t.surface.Begin(ctx, req, ToolGetList, waitlistsgrpc.PermissionReadLists)
	if err != nil {
		return nil, nil, err
	}

	call.Op.Set("waitlists_mcp.list_id", in.ListID)

	list, err := t.store.GetList(ctx, t.client.Reader(), call.Scope, in.ListID)
	if err != nil {
		return nil, nil, call.End(err)
	}

	return nil, list, call.End(nil)
}

// ListLists pages the tenant's whole catalog, behind the read grant: the
// administrative read. include_archived is honored only for a caller holding
// the archive grant.
func (t *Tools) ListLists(
	ctx context.Context,
	req *sdkmcp.CallToolRequest,
	in ListListsInput,
) (*sdkmcp.CallToolResult, *filtering.QueryFilteredResult[waitlists.List], error) {
	ctx, call, err := t.surface.Begin(ctx, req, ToolListLists, waitlistsgrpc.PermissionReadLists)
	if err != nil {
		return nil, nil, err
	}

	filter, err := call.Filter(ctx, in.Filter, waitlistsgrpc.PermissionArchiveLists, archivedClearedKey)
	if err != nil {
		return nil, nil, call.End(err)
	}

	page, err := t.store.ListLists(ctx, t.client.Reader(), call.Scope, filter)
	if err != nil {
		return nil, nil, call.End(err)
	}

	return nil, page, call.End(nil)
}

// ListOpenLists pages the lists still taking signups. It needs no grant,
// because its gRPC counterpart is public: it is what a signup page renders. It
// still needs a caller, since the tenant comes off the principal and a tool
// call has no connection to resolve one from.
func (t *Tools) ListOpenLists(
	ctx context.Context,
	req *sdkmcp.CallToolRequest,
	in ListListsInput,
) (*sdkmcp.CallToolResult, *filtering.QueryFilteredResult[waitlists.List], error) {
	ctx, call, err := t.surface.Begin(ctx, req, ToolListOpenLists, mcptool.NoGrant)
	if err != nil {
		return nil, nil, err
	}

	filter, err := call.Filter(ctx, in.Filter, waitlistsgrpc.PermissionArchiveLists, archivedClearedKey)
	if err != nil {
		return nil, nil, call.End(err)
	}

	page, err := t.store.ListOpenLists(ctx, t.client.Reader(), call.Scope, filter)
	if err != nil {
		return nil, nil, call.End(err)
	}

	return nil, page, call.End(nil)
}

func readOnly() *sdkmcp.ToolAnnotations {
	return &sdkmcp.ToolAnnotations{ReadOnlyHint: true}
}

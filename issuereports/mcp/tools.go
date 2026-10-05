package mcp

import (
	"context"
	"errors"
	"reflect"

	"github.com/primandproper/platform-go/v15/callers"
	"github.com/primandproper/platform-go/v15/internal/mcptool"
	"github.com/primandproper/platform-go/v15/issuereports"
	issuereportsgrpc "github.com/primandproper/platform-go/v15/issuereports/grpc"

	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/filtering"

	"github.com/google/jsonschema-go/jsonschema"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

//go:generate go run ../../internal/cmd/mcpdocs -pkg mcp -out fielddocs_gen.go github.com/primandproper/platform-go/v15/issuereports.Report github.com/primandproper/platform-go/v15/issuereports/mcp.GetReportInput github.com/primandproper/platform-go/v15/issuereports/mcp.ListReportsInput github.com/primandproper/platform-go/v15/issuereports/mcp.ListReportsByStatusInput

// surfaceName scopes this surface's spans, logger and instruments.
const surfaceName = "issuereports_mcp"

// archivedClearedKey records that a read asked for archived reports and did not
// hold the grant that reaches them.
const archivedClearedKey = "issuereports_mcp.include_archived_cleared"

// The tools this surface serves, by the name a model calls them.
const (
	ToolGetReport           = "get_issue_report"
	ToolListReports         = "list_issue_reports"
	ToolListReportsByStatus = "list_issue_reports_by_status"
)

var (
	// ErrNilStore is a tool surface built over no store.
	ErrNilStore = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil issue reports store for the MCP tools")

	// ErrNilDatabaseClient is a tool surface built with no handle to read on.
	ErrNilDatabaseClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client for the issue reports MCP tools")

	// ErrNilReportAuthorizer is a tool surface built with no rule about whose
	// reports a caller may read. See issuereports/grpc's ReportAuthorizer for
	// why there is no default.
	ErrNilReportAuthorizer = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil issue reports authorizer for the MCP tools")
)

// Authenticator turns one tool call into the context the principal and grants
// extractors read. See [NewTools].
type Authenticator = mcptool.Authenticator

// GetReportInput is what get_issue_report takes.
type GetReportInput struct {
	// ReportID is the identifier of the report to read.
	ReportID string `json:"reportID"`
}

// ListReportsInput is what list_issue_reports takes.
type ListReportsInput struct {
	// Filter is the page to read. Absent reads the first page, oldest first.
	Filter *filtering.QueryFilter `json:"filter,omitempty"`
}

// ListReportsByStatusInput is what list_issue_reports_by_status takes.
type ListReportsByStatusInput struct {
	// Filter is the page to read. Absent reads the first page, oldest first.
	Filter *filtering.QueryFilter `json:"filter,omitempty"`
	// Status is the triage queue to page: the status every report on the page
	// is in.
	Status issuereports.Status `json:"status"`
}

// Tools is the read-only MCP tool surface over the report queue.
type Tools struct {
	store   issuereports.Store
	client  database.Client
	targets issuereportsgrpc.ReportAuthorizer
	surface *mcptool.Surface

	getReport           *sdkmcp.Tool
	listReports         *sdkmcp.Tool
	listReportsByStatus *sdkmcp.Tool
}

// NewTools builds the tool surface over a store.
//
// It takes what issuereports/grpc's server takes, and for the same reasons,
// plus the grants extractor that server takes as an option: a gRPC method's
// grant is checked by an interceptor in front of it, and a tool has nothing in
// front of it but the bearer check, so the grant is checked here and the
// extractor is required. See internal/mcptool.
func NewTools(
	store issuereports.Store,
	client database.Client,
	authenticate Authenticator,
	principals callers.PrincipalExtractor,
	grants authorization.GrantsExtractor,
	targets issuereportsgrpc.ReportAuthorizer,
	opts ...Option,
) (*Tools, error) {
	if store == nil {
		return nil, ErrNilStore
	}

	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	if targets == nil {
		return nil, ErrNilReportAuthorizer
	}

	var o options
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}

	surface, err := mcptool.NewSurface(surfaceName, authenticate, principals, grants, issuereports.ClientSafeSentinels,
		o.logger, o.tracerProvider, o.metricsProvider)
	if err != nil {
		return nil, err
	}

	types := map[reflect.Type]*jsonschema.Schema{reflect.TypeFor[issuereports.Status](): statusSchema()}

	return &Tools{
		store:   store,
		client:  client,
		targets: targets,
		surface: surface,
		getReport: &sdkmcp.Tool{
			Name:         ToolGetReport,
			Description:  "Read one of the tenant's issue reports by its identifier: what somebody told you about your product, and where the report stands.",
			InputSchema:  mcptool.Input[GetReportInput](fieldDocs, types),
			OutputSchema: mcptool.Output[issuereports.Report](fieldDocs, types),
			Annotations:  readOnly(),
		},
		listReports: &sdkmcp.Tool{
			Name:         ToolListReports,
			Description:  "Page the tenant's issue reports in every status: the whole triage queue.",
			InputSchema:  mcptool.Input[ListReportsInput](fieldDocs, types),
			OutputSchema: mcptool.Output[filtering.QueryFilteredResult[issuereports.Report]](fieldDocs, types),
			Annotations:  readOnly(),
		},
		listReportsByStatus: &sdkmcp.Tool{
			Name:         ToolListReportsByStatus,
			Description:  "Page the tenant's issue reports in one status: open, acknowledged, resolved or declined.",
			InputSchema:  mcptool.Input[ListReportsByStatusInput](fieldDocs, types),
			OutputSchema: mcptool.Output[filtering.QueryFilteredResult[issuereports.Report]](fieldDocs, types),
			Annotations:  readOnly(),
		},
	}, nil
}

// RegisterOn adds this surface's tools to an MCP server.
func (t *Tools) RegisterOn(srv *sdkmcp.Server) {
	sdkmcp.AddTool(srv, t.getReport, t.GetReport)
	sdkmcp.AddTool(srv, t.listReports, t.ListReports)
	sdkmcp.AddTool(srv, t.listReportsByStatus, t.ListReportsByStatus)
}

// GetReport reads one of the tenant's live reports.
//
// It is issuereports/grpc's GetReport over another transport: the same grant,
// and the same [issuereportsgrpc.ReportAuthorizer] asked once the row is in
// hand. A refusal from the authorizer reads as an absent report, as it does
// there, so a model walking identifiers cannot tell which of them are real.
func (t *Tools) GetReport(
	ctx context.Context,
	req *sdkmcp.CallToolRequest,
	in GetReportInput,
) (*sdkmcp.CallToolResult, *issuereports.Report, error) {
	ctx, call, err := t.surface.Begin(ctx, req, ToolGetReport, issuereportsgrpc.PermissionReadReports)
	if err != nil {
		return nil, nil, err
	}

	call.Op.Set("issuereports_mcp.report_id", in.ReportID)

	report, err := t.store.GetReport(ctx, t.client.Reader(), call.Scope, in.ReportID)
	if err != nil {
		return nil, nil, call.End(err)
	}

	if err = t.targets.AuthorizeReport(ctx, call.Principal, report); err != nil {
		if errors.Is(err, callers.ErrTargetNotPermitted) {
			return nil, nil, call.End(issuereports.ErrReportNotFound)
		}

		return nil, nil, call.End(err)
	}

	return nil, report, call.End(nil)
}

// ListReports pages the tenant's queue, behind the triage grant.
// include_archived is honored only for a caller holding the archive grant.
func (t *Tools) ListReports(
	ctx context.Context,
	req *sdkmcp.CallToolRequest,
	in ListReportsInput,
) (*sdkmcp.CallToolResult, *filtering.QueryFilteredResult[issuereports.Report], error) {
	ctx, call, err := t.surface.Begin(ctx, req, ToolListReports, issuereportsgrpc.PermissionTriageReports)
	if err != nil {
		return nil, nil, err
	}

	filter, err := call.Filter(ctx, in.Filter, issuereportsgrpc.PermissionArchiveReports, archivedClearedKey)
	if err != nil {
		return nil, nil, call.End(err)
	}

	page, err := t.store.ListReports(ctx, t.client.Reader(), call.Scope, filter)
	if err != nil {
		return nil, nil, call.End(err)
	}

	return nil, page, call.End(nil)
}

// ListReportsByStatus pages one status's queue, behind the triage grant.
func (t *Tools) ListReportsByStatus(
	ctx context.Context,
	req *sdkmcp.CallToolRequest,
	in ListReportsByStatusInput,
) (*sdkmcp.CallToolResult, *filtering.QueryFilteredResult[issuereports.Report], error) {
	ctx, call, err := t.surface.Begin(ctx, req, ToolListReportsByStatus, issuereportsgrpc.PermissionTriageReports)
	if err != nil {
		return nil, nil, err
	}

	call.Op.Set("issuereports_mcp.status", in.Status.String())

	filter, err := call.Filter(ctx, in.Filter, issuereportsgrpc.PermissionArchiveReports, archivedClearedKey)
	if err != nil {
		return nil, nil, call.End(err)
	}

	page, err := t.store.ListReportsByStatus(ctx, t.client.Reader(), call.Scope, in.Status, filter)
	if err != nil {
		return nil, nil, call.End(err)
	}

	return nil, page, call.End(nil)
}

// statusSchema names the statuses as an enum, read off issuereports.Statuses so
// a status added there is one a model is told about.
func statusSchema() *jsonschema.Schema {
	enum := make([]any, 0, len(issuereports.Statuses))
	for _, status := range issuereports.Statuses {
		enum = append(enum, status.String())
	}

	return &jsonschema.Schema{Type: "string", Enum: enum}
}

func readOnly() *sdkmcp.ToolAnnotations {
	return &sdkmcp.ToolAnnotations{ReadOnlyHint: true}
}

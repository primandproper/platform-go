/*
Package mcp serves the issue report queue to a model, as read-only Model Context
Protocol tools.

It is imported as issuereportsmcp, and the MCP Go SDK beside it as sdkmcp:

	tools, _ := issuereportsmcp.NewTools(store, client, authenticate, extractPrincipal, grants, targets)
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "reports"}, nil)
	tools.RegisterOn(server)

The tools are get_issue_report, list_issue_reports,
list_issue_reports_by_status and list_issue_reports_by_reporter. Mounting the
server behind a bearer check is primitives-go's authentication/oauth2server/mcp,
whose Protect puts the verified token on each call's req.Extra.TokenInfo —
which is what the authenticator here reads.

# The same rules as issuereports/grpc, over another transport

Each tool requires the permission its gRPC counterpart requires —
PermissionReadReports for the keyed read and for one person's reports,
PermissionTriageReports for the two queue reads — checked against the same
authorization.GrantsExtractor, so a policy written for one transport governs
both. The keyed read asks the same issuereports/grpc.ReportAuthorizer once the
row is in hand, and a refusal reads as an absent report, as it does there. The
reporter read asks it before the read, about the person named — the caller,
when the call names nobody — and a refusal is [ErrReporterNotPermitted], which
says nothing about whether that person ever filed one. include_archived is
honored only for a caller holding PermissionArchiveReports. The tenant comes off
the principal and never off an argument.

# The schema is the row's

What a model is told about a report is reflected off issuereports.Report: the
property names are its `json` tags, and each description is the field's own doc
comment, generated into fielddocs_gen.go by `go generate` and checked against
the source by this package's tests. A field added to Report reaches a model the
next time the package is built, and a doc comment edited without regenerating
fails a test. internal/mcptool says why the schema is never written by hand.

# Read-only, and why

There is no write tool, and that is a ruling rather than a gap. A write through
a model is a different authorization conversation from a read — who is
answerable for a report resolved by an agent is a question this module does not
answer for its consumers — and a consumer that has answered it builds the write
over the store or the gRPC surface it already has.

# Errors

A tool's error text goes to the model verbatim. A refusal is answered in its
own words — no principal, no grant, an argument the schema refused, a reporter
the caller may not name, or one of issuereports.ClientSafeSentinels — and every
other failure is internal/mcptool's ErrToolFailed, with the cause on the call's
log line and span.
*/
package mcp

//platform:transport resource surface: read-only MCP tools — one report, the queue, the queue by status, one person's reports — over `issuereports.Store`

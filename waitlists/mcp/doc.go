/*
Package mcp serves the waitlist catalog to a model, as read-only Model Context
Protocol tools.

It is imported as waitlistsmcp, and the MCP Go SDK beside it as sdkmcp:

	tools, _ := waitlistsmcp.NewTools(store, client, authenticate, extractPrincipal, grants)
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "waitlists"}, nil)
	tools.RegisterOn(server)

The tools are get_waitlist, list_waitlists and list_open_waitlists. Mounting the
server behind a bearer check is primitives-go's
authentication/oauth2server/mcp, whose Protect puts the verified token on each
call's req.Extra.TokenInfo — which is what the authenticator here reads.

# The catalog, and not the signups

A signup holds the address its signatory typed. Over gRPC every read of one is
behind a grant of its own, and these tools serve none of them: a tool paging a
list's signups would put every signatory's address in a model's context. See
[Tools].

# The same rules as waitlists/grpc, over another transport

get_waitlist and list_waitlists require PermissionReadLists, checked against the
same authorization.GrantsExtractor the gRPC enforcer reads. list_open_waitlists
requires no grant, because ListOpenLists is public — but it still requires a
caller, because the tenant comes off the principal and a tool call has no
connection to resolve one from. include_archived is honored only for a caller
holding PermissionArchiveLists, on both paged reads.

# The schema is the row's

What a model is told about a list is reflected off waitlists.List, with each
field's own doc comment as its description, generated into fielddocs_gen.go by
`go generate` and checked against the source by this package's tests.
internal/mcptool says why.

# Errors

A refusal is answered in its own words — no principal, no grant, an argument the
schema refused, or waitlists.ErrListNotFound — and every other failure is
internal/mcptool's ErrToolFailed, with the cause on the call's log line and span.
*/
package mcp

//platform:transport resource surface: read-only MCP tools — the waitlist catalog, whole and open, never the signups — over `waitlists.Store`

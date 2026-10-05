/*
Package mcp serves webhook endpoints and the event catalog to a model, as
read-only Model Context Protocol tools.

It is imported as webhooksmcp, and the MCP Go SDK beside it as sdkmcp:

	tools, _ := webhooksmcp.NewTools(dispatcher, store, client, authenticate, extractPrincipal, grants)
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "webhooks"}, nil)
	tools.RegisterOn(server)

The tools are get_webhook_endpoint, list_webhook_endpoints and
list_webhook_event_types. Mounting the server behind a bearer check is
primitives-go's authentication/oauth2server/mcp, whose Protect puts the verified
token on each call's req.Extra.TokenInfo — which is what the authenticator here
reads.

# The same rules as webhooks/grpc, over another transport

Each tool requires the permission its gRPC counterpart requires, checked
against the same authorization.GrantsExtractor: PermissionReadEndpoints for the
two endpoint reads and PermissionReadEventTypes for the catalog. include_archived
is honored only for a caller holding PermissionArchiveEndpoints. The tenant
comes off the principal and never off an argument; the catalog binds none,
because it is the same for every tenant.

# What an endpoint is answered without

The signing secret never marshals, so no tool can answer with it. The static
headers do marshal, and these tools clear them and leave them out of the
schema: a routing token is as often a credential — which is why webhooks keeps
them out of audit diffs — and a model's context is a log somebody else keeps.
A console managing its endpoints reads them over gRPC.

# The schema is the row's

What a model is told about an endpoint and its subscriptions is reflected off
webhooks.Endpoint and webhooks.Subscription, with each field's own doc comment
as its description, generated into fielddocs_gen.go by `go generate` and
checked against the source by this package's tests. internal/mcptool says why.

# Read-only

There is no write tool: registering an endpoint through a model is a request
that a delivery go somewhere, decided by whoever is talking to the model, and
that is a different authorization conversation from a read.

# Errors

A refusal is answered in its own words — no principal, no grant, an argument the
schema refused, or [ErrEndpointNotFound] — and every other failure is
internal/mcptool's ErrToolFailed, with the cause on the call's log line and span.
*/
package mcp

//platform:transport resource surface: read-only MCP tools — endpoints without their headers, and the event catalog — over `webhooks.Store` and `webhooks.Dispatcher`

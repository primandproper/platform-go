// Package mcptool is what a Model Context Protocol tool surface is built on:
// the schema a tool's input and output are described with, and the gate every
// call passes before it reaches a store. This module's own surfaces —
// issuereports/mcp, waitlists/mcp and webhooks/mcp — are built on it, and a
// consumer's tools over its own rows are built on it the same way.
//
// # The schema is derived, never written
//
// A tool definition tells a model the shape of a row. A shape written out by
// hand beside the type it describes is a second copy that drifts the day a
// field is added upstream, and the failure is quiet: a model told last year's
// shape of a row answers with last year's fields, and a filter keyed on Go field
// names against camelCase tags is dropped in full and the list comes back
// unfiltered and plausible. So [Output] and [Input] reflect the Go type itself —
// property names from the `json` tags, types from the field types — and the
// descriptions are the fields' own doc comments, carried as a generated [Docs]
// map because a running binary has no source to read them from. [Extract] is
// what generates that map and what each surface's test re-runs against the
// source, so a doc comment edited without regenerating fails a test rather than
// telling a model something the code no longer says. A paged read's filter is
// filtering.QueryFilterSchema, which primitives-go reflects off the struct for
// the same reason.
//
// # The gate is the whole of the authorization
//
// A gRPC surface here has an interceptor in front of it that checks the
// method's grant before a handler runs. An MCP tool has nothing in front of it
// but the bearer check on the transport, which says the token is good and not
// what its holder may read. [Surface.Begin] is therefore where a tool's grant
// is checked, against the same authorization.GrantsExtractor the gRPC enforcer
// reads and the same permission the gRPC method declares, so a caller who may
// not call ListReports over one transport may not call it over the other. A
// tool that needs no grant says so with [NoGrant] rather than by passing
// nothing.
//
// # What a model is told when a call fails
//
// A tool's error text goes to the model verbatim, and a model repeats it. So
// [Call.End] returns a refusal's own words only for an error the surface named
// as safe — a sentinel whose text was written for whoever reads it, or a
// refusal of the arguments themselves — and replaces every other failure with
// [ErrToolFailed], logging the cause. A database error's text is the schema of
// the database.
//
// # Building a surface
//
// A surface names the structs its tools describe in a go:generate directive,
// and the generator writes their doc comments into the package:
//
//	//go:generate go run github.com/primandproper/platform-go/v15/mcptool/mcpdocs -pkg tools -out fielddocs_gen.go example.com/app/recipes.Recipe example.com/app/tools.GetRecipeInput
//
// Its test keeps that file honest by extracting the same structs again, read
// off the same directive so the list is written once:
//
//	specs, err := mcptool.DirectiveSpecs("tools.go")
//	extracted, err := mcptool.Extract(specs...)
//	// extracted must equal fieldDocs
//
// The tools take their schemas from [Input] and [Output] over that map, and
// every handler opens with [Surface.Begin] and returns through [Call.End]:
//
//	ctx, call, err := surface.Begin(ctx, req, "get_recipe", recipes.PermissionRead)
//	if err != nil {
//		return nil, nil, err
//	}
//
//	recipe, err := store.GetRecipe(ctx, db, call.Scope, in.RecipeID)
//	if err != nil {
//		return nil, nil, call.End(err)
//	}
//
//	return nil, recipe, call.End(nil)
//
// call.Scope is the caller's, off their principal, and is the only scope a
// handler reads with: a tenant a model could name in its arguments would be a
// cross-tenant read hiding behind an argument.
package mcptool

//go:generate go run ./mcpdocs -pkg mcptool -out fielddocs_gen.go github.com/primandproper/primitives-go/v2/filtering.QueryFilteredResult github.com/primandproper/primitives-go/v2/filtering.Pagination

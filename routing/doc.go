/*
Package routing provides a declarative, type-safe HTTP router that generates an
OpenAPI 3 specification as routes are registered.

The primary type is Router: a concrete router that owns request decoding,
validation, response encoding, error mapping, and OpenAPI accumulation. Routes
are declared with typed handlers of the form func(ctx, In) (Out, error) via the
package-level generic functions (Get, Post, Put, Patch, Delete, Head). Because Go
interface methods cannot be generic, registration is done with these functions
rather than methods on the Router.

A Router is backed by a Backend — the pluggable seam that a concrete mux library
implements. Implementations ship for chi, the net/http.ServeMux standard library
(stdlib), julienschmidt/httprouter, and gin-gonic/gin. The Router builds
everything on top of the Backend's primitives and never depends on the library
directly, so the underlying router is swappable without touching route code:

	backend := chi.NewBackend(logger, tracerProvider, metricProvider, cfg)
	r := routing.New(backend, encoder, logger, tracerProvider, routing.WithTitle("My API"))

	routing.Post(r, "/orgs/{orgID:uint64}/users", createUser, routing.WithSummary("Create user"))
	routing.Get(r, "/orgs/{orgID:uint64}", getOrg)

	r.MountOpenAPI("/openapi.json", "/docs")

Path parameters use an inline typed syntax — "/users/{id:uint64}" — which drives
both runtime binding and the generated parameter schema. Query, header, cookie,
and body values are bound from struct tags on the typed input.
*/
package routing

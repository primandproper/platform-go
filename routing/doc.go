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

	backend := chi.NewBackend(cfg, chi.WithLogger(logger), chi.WithTracerProvider(tracerProvider))
	r := routing.New(backend, encoder, routing.WithLogger(logger), routing.WithTitle("My API"))

	routing.Post(r, "/orgs/{orgID:uint64}/users", createUser, routing.WithSummary("Create user"))
	routing.Get(r, "/orgs/{orgID:uint64}", getOrg)

	r.MountOpenAPI("/openapi.json", "/docs")

A returned error becomes the platform APIError envelope, with the status derived
from the error's platform code. A service with an error wire format of its own
replaces that rendering wholesale with WithErrorEncoder, which decides the status
and the body while leaving serialization to the route's encoder:

	routing.WithErrorEncoder(func(ctx context.Context, err error) (int, any) {
		if errors.Is(err, platformerrors.ErrResourceInUse) {
			return http.StatusConflict, legacyError{Error: "resource is in use"}
		}

		return http.StatusInternalServerError, legacyError{Error: err.Error()}
	})

Path parameters use an inline typed syntax — "/users/{id:uint64}" — which drives
both runtime binding and the generated parameter schema. Query, header, cookie,
and body values are bound from struct tags on the typed input.

A route can also be stated as a value rather than as arguments, with Endpoint and
Register:

	var GetOrder = routing.Endpoint[GetOrderRequest, Order]{
		Method:  http.MethodGet,
		Pattern: "/orders/{orderID:uuid}",
	}

	routing.Register(r, GetOrder, getOrderHandler, routing.WithSummary("Fetch an order"))

The verb helpers are sugar over Register, so the two are interchangeable on this
side. What a descriptor adds is a second reader: routing/client calls an Endpoint
over HTTP, inverting the same binding this package applies, so a Go consumer gets
a compile-checked client with no generated code and no second statement of the
path or the field names. That does not replace the OpenAPI document, which still
serves external and polyglot consumers; both come from the same registration.
*/
package routing

package routing

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/swaggest/openapi-go"
)

// Get registers a typed GET handler.
func Get[In, Out any](r *Router, pattern string, h Handler[In, Out], opts ...Option) *Route {
	return Register(r, Endpoint[In, Out]{Method: http.MethodGet, Pattern: pattern}, h, opts...)
}

// Post registers a typed POST handler.
func Post[In, Out any](r *Router, pattern string, h Handler[In, Out], opts ...Option) *Route {
	return Register(r, Endpoint[In, Out]{Method: http.MethodPost, Pattern: pattern}, h, opts...)
}

// Put registers a typed PUT handler.
func Put[In, Out any](r *Router, pattern string, h Handler[In, Out], opts ...Option) *Route {
	return Register(r, Endpoint[In, Out]{Method: http.MethodPut, Pattern: pattern}, h, opts...)
}

// Patch registers a typed PATCH handler.
func Patch[In, Out any](r *Router, pattern string, h Handler[In, Out], opts ...Option) *Route {
	return Register(r, Endpoint[In, Out]{Method: http.MethodPatch, Pattern: pattern}, h, opts...)
}

// Delete registers a typed DELETE handler.
func Delete[In, Out any](r *Router, pattern string, h Handler[In, Out], opts ...Option) *Route {
	return Register(r, Endpoint[In, Out]{Method: http.MethodDelete, Pattern: pattern}, h, opts...)
}

// Head registers a typed HEAD handler.
func Head[In, Out any](r *Router, pattern string, h Handler[In, Out], opts ...Option) *Route {
	return Register(r, Endpoint[In, Out]{Method: http.MethodHead, Pattern: pattern}, h, opts...)
}

// Register serves an Endpoint with a typed handler. It parses the typed path,
// builds and validates the binding plan, records the OpenAPI operation, and
// registers the adapted handler on the backend.
//
// It is the general form; the verb helpers are sugar that mint the descriptor
// inline. Reach for it when the descriptor is a value the service publishes —
// so that a Go consumer can call the same route through routing/client without
// restating the method, the path, or the types.
func Register[In, Out any](r *Router, ep Endpoint[In, Out], h Handler[In, Out], opts ...Option) *Route {
	method := ep.Method
	plain, pathParams := parsePath(r.prefix + ep.Pattern)

	plan := newBindPlan[In](pathParams, method)

	rc := newRouteConfig(method, r)
	for _, o := range opts {
		o(rc)
	}
	if rc.operationID == "" {
		rc.operationID = defaultOperationID(method, plain)
	}

	r.recordOperation(method, plain, rc, new(In), responseStructure[Out](rc.envelope))

	handler := applyMiddleware(buildHTTPHandler(r, plan, rc, h), rc.middleware)
	r.backend.Handle(method, plain, handler)

	return &Route{Method: method, Path: plain, OperationID: rc.operationID}
}

// applyMiddleware wraps handler with the given middleware, outermost first.
func applyMiddleware(handler http.Handler, middleware []Middleware) http.Handler {
	for i := len(middleware) - 1; i >= 0; i-- {
		if middleware[i] != nil {
			handler = middleware[i](handler)
		}
	}

	return handler
}

// recordOperation feeds one registration into the OpenAPI reflector.
func (r *Router) recordOperation(method, plain string, rc *routeConfig, reqStructure, respStructure any) {
	oc, err := r.reflector.NewOperationContext(method, plain)
	if err != nil {
		r.errs.add(fmt.Errorf("building operation %s %s: %w", method, plain, err))

		return
	}

	oc.SetID(rc.operationID)
	if rc.summary != "" {
		oc.SetSummary(rc.summary)
	}
	if rc.description != "" {
		oc.SetDescription(rc.description)
	}
	if len(rc.tags) > 0 {
		oc.SetTags(rc.tags...)
	}
	if rc.deprecated {
		oc.SetIsDeprecated(true)
	}
	for i := range rc.security {
		oc.AddSecurity(rc.security[i].name, rc.security[i].scopes...)
	}

	oc.AddReqStructure(reqStructure)

	if respStructure != nil {
		oc.AddRespStructure(respStructure, openapi.WithHTTPStatus(rc.successStatus))
	}
	for i := range rc.additionalResponses {
		ar := &rc.additionalResponses[i]
		oc.AddRespStructure(ar.body,
			openapi.WithHTTPStatus(ar.status),
			func(cu *openapi.ContentUnit) { cu.Description = ar.description },
		)
	}

	if addErr := r.reflector.AddOperation(oc); addErr != nil {
		r.errs.add(fmt.Errorf("adding operation %s %s: %w", method, plain, addErr))
	}
}

// Handle registers a raw http.Handler on the backend — an escape hatch for routes
// that do not fit the typed model (static files, streaming, websockets). It
// records no OpenAPI operation.
func (r *Router) Handle(method, pattern string, handler http.Handler, middleware ...Middleware) {
	plain, _ := parsePath(r.prefix + pattern)
	r.backend.Handle(method, plain, applyMiddleware(handler, middleware))
}

// Group creates a sub-Router that shares the backend, reflector, and error
// accumulator, but applies an additional path prefix and default tags to routes
// registered through it.
func (r *Router) Group(prefix string, fn func(sub *Router), tags ...string) {
	sub := *r
	sub.prefix = r.prefix + prefix
	sub.tags = append(append([]string(nil), r.tags...), tags...)
	fn(&sub)
}

// defaultOperationID derives a stable operation ID from the method and path, e.g.
// GET /orgs/{orgID}/users -> "get_orgs_orgID_users".
func defaultOperationID(method, plain string) string {
	replacer := strings.NewReplacer("/", "_", "{", "", "}", "", ":", "_")
	id := strings.Trim(replacer.Replace(plain), "_")
	if id == "" {
		id = "root"
	}

	return strings.ToLower(method) + "_" + id
}

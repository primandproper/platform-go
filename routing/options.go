package routing

import (
	"github.com/primandproper/platform-go/v13/encoding"
)

type (
	// Option customizes a single route's registration and its generated OpenAPI
	// operation.
	Option func(*routeConfig)

	additionalResponse struct {
		body        any
		description string
		status      int
	}

	// routeConfig is the resolved per-route configuration.
	routeConfig struct {
		contentType         encoding.ContentType
		operationID         string
		summary             string
		description         string
		requestContentType  string
		tags                []string
		additionalResponses []additionalResponse
		middleware          []Middleware
		maxRequestBody      int64
		successStatus       int
		deprecated          bool
		envelope            bool
		// maxRequestBodySet separates "bounded at zero bytes, which is no bound"
		// from "nobody said", which is the difference between a route that opted
		// out of the limit and one that never had an opinion.
		maxRequestBodySet bool
	}
)

// DefaultRawBodyLimit is the request-body bound a route with a RawBody field
// gets when neither it nor its Router sets one.
//
// Only such a route: a decoded body is bounded by whatever the Router or the
// route says and otherwise not at all, which is what it has always been. A raw
// body is different in kind — nothing between the socket and the handler's
// []byte forms an opinion about how much of it to read — so the default is a
// number rather than "as much as arrives". Routes that carry larger documents
// say so with WithMaxRequestBody.
const DefaultRawBodyLimit int64 = 1 << 20 // 1 MiB

// newRouteConfig builds the default per-route config for a method, inheriting the
// Router's default tags, envelope setting, and request-body bound.
func newRouteConfig(method string, r *Router) *routeConfig {
	return &routeConfig{
		tags:              append([]string(nil), r.tags...),
		successStatus:     defaultSuccessStatus(method),
		envelope:          r.envelopeDefault,
		maxRequestBody:    r.maxRequestBody,
		maxRequestBodySet: r.maxRequestBody > 0,
	}
}

// WithSummary sets the operation's short summary.
func WithSummary(summary string) Option {
	return func(rc *routeConfig) { rc.summary = summary }
}

// WithDescription sets the operation's long description.
func WithDescription(description string) Option {
	return func(rc *routeConfig) { rc.description = description }
}

// WithOperationID overrides the generated operation ID.
func WithOperationID(id string) Option {
	return func(rc *routeConfig) { rc.operationID = id }
}

// WithTags adds OpenAPI tags to the operation (in addition to any Router defaults).
func WithTags(tags ...string) Option {
	return func(rc *routeConfig) { rc.tags = append(rc.tags, tags...) }
}

// WithDeprecated marks the operation as deprecated.
func WithDeprecated() Option {
	return func(rc *routeConfig) { rc.deprecated = true }
}

// WithResponseStatus overrides the success HTTP status (default 200, or 201 for POST).
func WithResponseStatus(status int) Option {
	return func(rc *routeConfig) { rc.successStatus = status }
}

// WithContentType overrides the response content type for this route.
func WithContentType(contentType encoding.ContentType) Option {
	return func(rc *routeConfig) { rc.contentType = contentType }
}

// WithRequestContentType sets the media type the route's request body is
// documented under.
//
// It is documentation only: nothing checks an incoming request against it, and
// the body is decoded by content-type negotiation exactly as before. What it
// changes is the generated operation, which is the reason a route with a RawBody
// field wants it — a GeoJSON document arrives as "application/geo+json", and a
// spec that calls it "application/json" sends every generated client the wrong
// Content-Type header.
//
// Unset, a decoded body is documented as the reflector's default
// (application/json) and a raw one as application/octet-stream, which is what an
// unparsed body with no declared media type is.
func WithRequestContentType(contentType string) Option {
	return func(rc *routeConfig) { rc.requestContentType = contentType }
}

// WithMaxRequestBody bounds this route's request body, in bytes, overriding any
// Router-wide default from WithDefaultMaxRequestBody.
//
// The bound applies to whichever body the route has, decoded or raw. A request
// over it is answered 413 without the handler running, and the connection is not
// left reading a body that has already been refused.
//
// It is per-route because the alternative is one number for every endpoint a
// service has, which has to be the largest one any endpoint needs: the route
// that accepts a 10 MiB import sets the ceiling that the login route then also
// runs under.
//
// A value of zero or less is no bound — including on a RawBody route, which is
// how one opts out of DefaultRawBodyLimit.
func WithMaxRequestBody(n int64) Option {
	return func(rc *routeConfig) {
		rc.maxRequestBody = n
		rc.maxRequestBodySet = true
	}
}

// WithEnvelope toggles wrapping the response body in errors/http.APIResponse[Out].
// Enveloping is on by default (configurable at the Router level).
func WithEnvelope(enabled bool) Option {
	return func(rc *routeConfig) { rc.envelope = enabled }
}

// WithAdditionalResponse documents an additional response (e.g. a 404 with an error body).
func WithAdditionalResponse(status int, body any, description string) Option {
	return func(rc *routeConfig) {
		rc.additionalResponses = append(rc.additionalResponses, additionalResponse{
			status:      status,
			body:        body,
			description: description,
		})
	}
}

// WithMiddleware applies middleware to this route only.
func WithMiddleware(middleware ...Middleware) Option {
	return func(rc *routeConfig) { rc.middleware = append(rc.middleware, middleware...) }
}

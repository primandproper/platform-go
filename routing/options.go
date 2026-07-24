package routing

import (
	"github.com/primandproper/platform-go/v6/encoding"
)

type (
	// Option customizes a single route's registration and its generated OpenAPI
	// operation.
	Option func(*routeConfig)

	securityRequirement struct {
		name   string
		scopes []string
	}

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
		tags                []string
		security            []securityRequirement
		additionalResponses []additionalResponse
		middleware          []Middleware
		successStatus       int
		deprecated          bool
		envelope            bool
	}
)

// newRouteConfig builds the default per-route config for a method, inheriting the
// Router's default tags and envelope setting.
func newRouteConfig(method string, r *Router) *routeConfig {
	return &routeConfig{
		tags:          append([]string(nil), r.tags...),
		successStatus: defaultSuccessStatus(method),
		envelope:      r.envelopeDefault,
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

// WithEnvelope toggles wrapping the response body in errors/http.APIResponse[Out].
// Enveloping is on by default (configurable at the Router level).
func WithEnvelope(enabled bool) Option {
	return func(rc *routeConfig) { rc.envelope = enabled }
}

// WithSecurity adds a security requirement (scheme name + optional scopes) to the operation.
func WithSecurity(scheme string, scopes ...string) Option {
	return func(rc *routeConfig) {
		rc.security = append(rc.security, securityRequirement{name: scheme, scopes: scopes})
	}
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

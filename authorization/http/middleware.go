package http

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/primandproper/platform-go/v13/authorization"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	httpx "github.com/primandproper/platform-go/v13/errors/http"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/routing"

	oteltrace "go.opentelemetry.io/otel/trace"
)

// serviceName names the Enforcer's logger and metrics.
const serviceName = "authorization"

const (
	decisionAllowed = "allowed"
	decisionDenied  = "denied"
	decisionAudited = "audited"
)

// DenyHandler writes the response for a denied request. Replace the default
// when a service has its own error envelope.
type DenyHandler func(res http.ResponseWriter, req *http.Request, err error)

// Enforcer builds authorization middleware for HTTP routes.
//
// Unlike the gRPC Enforcer, this one holds no table of requirements. A route's
// permissions are declared at its registration site instead:
//
//	routing.Get(r, "/things/{id}", handler,
//		routing.WithMiddleware(authz.Require(ReadThingsPermission)))
//
// That is not a stylistic preference. Global middleware runs before the mux
// matches, so the route pattern is not yet known there — a central table keyed
// by pattern would require platform-go to re-implement path matching, or
// routing.Backend to grow a per-backend hook. Declaring at registration keeps
// the requirement next to the handler it guards, which is also where a reader
// is most likely to notice a missing one.
//
// The consequence is that HTTP cannot fail closed on an undeclared route the
// way gRPC does: a route registered with no Require middleware is simply
// unguarded. Assert coverage with a test over your registered routes — the
// package doc explains why that is the answer rather than a stopgap.
type Enforcer struct {
	extract     authorization.GrantsExtractor
	logger      logging.Logger
	denyHandler DenyHandler

	checksCounter   metrics.Int64Counter
	denialsCounter  metrics.Int64Counter
	noGrantsCounter metrics.Int64Counter

	metricsProvider metrics.Provider
	auditOnly       bool
}

// Option configures an Enforcer.
type Option func(*Enforcer)

// WithLogger attaches a logger, which the Enforcer uses for the denials that
// mean something is misconfigured: a route registered with an empty permission
// list, and a request that requires authorization but carries no grants at all.
// It also carries the audit-only announcement at construction, and a failure to
// write the denial response.
//
// An ordinary denial, where the caller simply lacks a permission, is counted
// and attached to the span and stops there. It is enforcement working as
// designed, and a log line per occurrence is the wrong volume for that: the
// number worth alerting on is the counter.
func WithLogger(logger logging.Logger) Option {
	return func(e *Enforcer) {
		e.logger = logger
	}
}

// WithMetricsProvider attaches a metrics provider, enabling the authorization
// counters.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(e *Enforcer) {
		e.metricsProvider = metricsProvider
	}
}

// WithDenyHandler replaces the response written on denial. The default encodes
// the platform's APIResponse envelope with code E110 at HTTP 403.
func WithDenyHandler(h DenyHandler) Option {
	return func(e *Enforcer) {
		if h != nil {
			e.denyHandler = h
		}
	}
}

// WithAuditOnly evaluates and records every decision but denies nothing. See
// the gRPC package's WithAuditOnly for why this exists and how to use it.
func WithAuditOnly() Option {
	return func(e *Enforcer) {
		e.auditOnly = true
	}
}

// NewEnforcer builds an HTTP authorization Enforcer.
func NewEnforcer(extract authorization.GrantsExtractor, opts ...Option) (*Enforcer, error) {
	if extract == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "grants extractor")
	}

	e := &Enforcer{extract: extract}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}

	e.logger = logging.EnsureLogger(e.logger).WithName(serviceName)
	if e.denyHandler == nil {
		e.denyHandler = e.writeDenial
	}

	mp := metrics.EnsureMetricsProvider(e.metricsProvider)

	var err error
	if e.checksCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_http_checks", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating authorization checks counter")
	}
	if e.denialsCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_http_denials", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating authorization denials counter")
	}
	if e.noGrantsCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_http_missing_grants", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating authorization missing grants counter")
	}

	if e.auditOnly {
		e.logger.Info("http authorization enforcer running in audit-only mode; denials will be recorded but not enforced")
	}

	return e, nil
}

// Require returns middleware admitting only requests whose grants include every
// permission in perms.
//
// Require with no permissions denies everything rather than allowing it. The
// set-algebra answer would be a vacuous allow, but a middleware installed with
// an empty list is far more likely to be a bug — a slice that came back empty
// from configuration — than an intent to admit everyone, and a route that
// needs no authorization simply omits the middleware.
func (e *Enforcer) Require(perms ...authorization.Permission) routing.Middleware {
	required := make([]authorization.Permission, len(perms))
	copy(required, perms)

	requiredStrings := make([]string, len(required))
	for i, p := range required {
		requiredStrings[i] = string(p)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			ctx := req.Context()
			span := oteltrace.SpanFromContext(ctx)

			// The path goes on the span, never on the counters. This package
			// sees the request after routing has already matched it but has no
			// portable way to recover the pattern that matched — routing.Router
			// exposes none — so the only label available here is the raw path,
			// and a raw path carries an identifier per resource. As a span
			// attribute that is one value on one trace; as a metric attribute it
			// is a new time series per URL.
			tracing.AttachToSpan(span, keys.AuthorizationMethodKey, req.URL.Path)

			e.checksCounter.Add(ctx, 1)
			tracing.AttachToSpan(span, keys.AuthorizationRequiredKey, requiredStrings)

			if len(required) == 0 {
				e.deny(res, req, span, next, "route requires an empty permission list")

				return
			}

			grants, ok := e.extract(ctx)
			if !ok {
				e.noGrantsCounter.Add(ctx, 1)
				e.deny(res, req, span, next, "no grants available for request requiring authorization")

				return
			}

			if !grants.HasAll(required...) {
				e.deny(res, req, span, next, "")

				return
			}

			tracing.AttachToSpan(span, keys.AuthorizationDecisionKey, decisionAllowed)
			next.ServeHTTP(res, req)
		})
	}
}

// deny records a denial and either writes it or, in audit-only mode, proceeds.
func (e *Enforcer) deny(
	res http.ResponseWriter,
	req *http.Request,
	span oteltrace.Span,
	next http.Handler,
	logMessage string,
) {
	e.denialsCounter.Add(req.Context(), 1)

	if logMessage != "" {
		e.logger.WithSpan(span).WithRequest(req).Error(logMessage, authorization.ErrPermissionDenied)
	}

	if e.auditOnly {
		tracing.AttachToSpan(span, keys.AuthorizationDecisionKey, decisionAudited)
		next.ServeHTTP(res, req)

		return
	}

	tracing.AttachToSpan(span, keys.AuthorizationDecisionKey, decisionDenied)
	e.denyHandler(res, req, authorization.ErrPermissionDenied)
}

// writeDenial is the default DenyHandler. It emits the same envelope the router
// produces for a handler that returned ErrPermissionDenied, so a denial looks
// identical whether it came from middleware or from inside a handler.
func (e *Enforcer) writeDenial(res http.ResponseWriter, req *http.Request, _ error) {
	details := httpx.ResponseDetails{}
	if sc := oteltrace.SpanContextFromContext(req.Context()); sc.HasTraceID() {
		details.TraceID = sc.TraceID().String()
	}

	res.Header().Set("Content-Type", "application/json")
	res.WriteHeader(httpx.HTTPStatusForCode(httpx.ErrUserIsNotAuthorized))

	body := httpx.NewAPIErrorResponse("permission denied", httpx.ErrUserIsNotAuthorized, details)
	if err := json.NewEncoder(res).Encode(body); err != nil {
		e.logger.WithRequest(req).Error("writing authorization denial response", err)
	}
}

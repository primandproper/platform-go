package grpc

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/v13/authorization"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// serviceName names the Enforcer's logger and metrics.
const serviceName = "authorization"

// decision values recorded on spans and logs.
const (
	decisionAllowed = "allowed"
	decisionDenied  = "denied"
	decisionAudited = "audited"
)

// errDenied is the single denial value the interceptors return.
//
// It satisfies three requirements at once that no off-the-shelf error does:
// status.FromError recognizes GRPCStatus, so the wire code is right even when
// no error-encoding interceptor is installed; errors.Is matches the platform
// sentinel, so in-process callers can branch on it; and the client-visible
// message is a constant, so a caller that just failed to authorize learns
// nothing about the permission it was missing.
var errDenied error = deniedError{}

type deniedError struct{}

func (deniedError) Error() string { return "permission denied" }

func (deniedError) Unwrap() error { return platformerrors.ErrPermissionDenied }

func (deniedError) GRPCStatus() *status.Status {
	return status.New(codes.PermissionDenied, "permission denied")
}

// Enforcer authorizes RPCs against a Requirements table.
//
// One Enforcer serves every method, which is why every measurement it records
// carries a method attribute: a single mis-declared method is invisible in the
// total.
type Enforcer struct {
	reqs    *Requirements
	extract authorization.GrantsExtractor
	logger  logging.Logger

	checksCounter     metrics.Int64Counter
	denialsCounter    metrics.Int64Counter
	undeclaredCounter metrics.Int64Counter
	noGrantsCounter   metrics.Int64Counter

	metricsProvider metrics.Provider
	auditOnly       bool
}

// Option configures an Enforcer.
type Option func(*Enforcer)

// WithLogger attaches a logger, which the Enforcer uses for the denials that
// mean something is misconfigured: an undeclared method, and an RPC that
// requires authorization but carries no grants at all. It also carries the
// audit-only announcement at construction.
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
// counters. Without it the counters are no-ops and an ordinary denial is
// visible only on the span, since nothing logs one.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(e *Enforcer) {
		e.metricsProvider = metricsProvider
	}
}

// WithAuditOnly evaluates and records every decision but denies nothing.
//
// This is the rollout tool. Turning enforcement on across a service that has
// never had it is otherwise a coin flip: the table is large, hand-written, and
// a single missing entry becomes an outage on deploy. Run audit-only, watch
// authorization_grpc_denials and authorization_grpc_undeclared_methods settle to
// zero, then remove the option. (Exporters that suffix counters, Prometheus
// among them, will show these as _total.)
//
// It is the only mode in which an unauthorized request proceeds, which is why
// it is a code-level option rather than configuration, and why it announces
// itself in the log at construction.
func WithAuditOnly() Option {
	return func(e *Enforcer) {
		e.auditOnly = true
	}
}

// NewEnforcer builds an Enforcer over a frozen Requirements table.
func NewEnforcer(reqs *Requirements, extract authorization.GrantsExtractor, opts ...Option) (*Enforcer, error) {
	if reqs == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "requirements")
	}
	if extract == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "grants extractor")
	}

	e := &Enforcer{reqs: reqs, extract: extract}
	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}

	e.logger = logging.EnsureLogger(e.logger).WithName(serviceName)

	mp := metrics.EnsureMetricsProvider(e.metricsProvider)

	var err error
	if e.checksCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_grpc_checks", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating authorization checks counter")
	}
	if e.denialsCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_grpc_denials", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating authorization denials counter")
	}
	if e.undeclaredCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_grpc_undeclared_methods", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating authorization undeclared methods counter")
	}
	if e.noGrantsCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_grpc_missing_grants", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating authorization missing grants counter")
	}

	if e.auditOnly {
		e.logger.Info("authorization enforcer running in audit-only mode; denials will be recorded but not enforced")
	}

	return e, nil
}

// UnaryServerInterceptor authorizes unary RPCs.
func (e *Enforcer) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := e.check(ctx, info.FullMethod); err != nil {
			return nil, err
		}

		return handler(ctx, req)
	}
}

// StreamServerInterceptor authorizes streaming RPCs.
//
// Streams are authorized once, when the stream opens. Nothing re-checks
// mid-stream, so a long-lived stream outlives a revocation of the authority
// that opened it — the same property the unary path has for the duration of a
// call, just over a longer window.
func (e *Enforcer) StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := e.check(ss.Context(), info.FullMethod); err != nil {
			return err
		}

		return handler(srv, ss)
	}
}

// check is the single copy of the authorization decision. Both interceptors are
// three lines around it, which is the point: the unary and stream paths cannot
// drift apart into subtly different rules.
func (e *Enforcer) check(ctx context.Context, fullMethod string) error {
	// Attributes land on the ambient RPC span rather than a child. An
	// authorization check is a few map lookups; a span per RPC to describe it
	// would double the trace volume of every service that installs this.
	span := oteltrace.SpanFromContext(ctx)
	tracing.AttachToSpan(span, keys.AuthorizationMethodKey, fullMethod)

	methodAttr := metric.WithAttributes(attribute.String(keys.AuthorizationMethodKey, fullMethod))
	e.checksCounter.Add(ctx, 1, methodAttr)

	perms, public, declared := e.reqs.lookup(fullMethod)

	if !declared {
		// An undeclared method is a different bug from a denied request — it
		// means the table is incomplete, not that a caller overreached — so it
		// is counted separately and logged at a level that gets noticed.
		e.undeclaredCounter.Add(ctx, 1, methodAttr)
		e.logger.WithSpan(span).WithValue(keys.AuthorizationMethodKey, fullMethod).
			Error("denying rpc with no declared authorization requirements", errDenied)

		return e.resolve(ctx, span, methodAttr, false)
	}

	if public {
		tracing.AttachToSpan(span, keys.AuthorizationDecisionKey, decisionAllowed)

		return nil
	}

	tracing.AttachToSpan(span, keys.AuthorizationRequiredKey, permissionStrings(perms))

	grants, ok := e.extract(ctx)
	if !ok {
		// No authority could be determined for a method that requires some.
		// That usually means authentication did not run, or ran after this
		// interceptor, so it is worth its own counter.
		e.noGrantsCounter.Add(ctx, 1, methodAttr)
		e.logger.WithSpan(span).WithValue(keys.AuthorizationMethodKey, fullMethod).
			Error("no grants available for rpc requiring authorization", errDenied)

		return e.resolve(ctx, span, methodAttr, false)
	}

	return e.resolve(ctx, span, methodAttr, grants.HasAll(perms...))
}

// resolve records the decision and returns the error the caller should see,
// honoring audit-only mode.
func (e *Enforcer) resolve(ctx context.Context, span oteltrace.Span, methodAttr metric.MeasurementOption, allowed bool) error {
	if allowed {
		tracing.AttachToSpan(span, keys.AuthorizationDecisionKey, decisionAllowed)

		return nil
	}

	e.denialsCounter.Add(ctx, 1, methodAttr)

	if e.auditOnly {
		tracing.AttachToSpan(span, keys.AuthorizationDecisionKey, decisionAudited)

		return nil
	}

	tracing.AttachToSpan(span, keys.AuthorizationDecisionKey, decisionDenied)

	return errDenied
}

// permissionStrings converts permissions for span attachment. Required
// permissions describe the policy, not the requester, and they go to telemetry
// only — never to the client.
func permissionStrings(perms []authorization.Permission) []string {
	out := make([]string, len(perms))
	for i, p := range perms {
		out[i] = string(p)
	}

	return out
}

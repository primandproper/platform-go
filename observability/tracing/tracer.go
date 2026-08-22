package tracing

import (
	"context"

	"github.com/primandproper/platform-go/v13/observability/logging"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

var _ otel.ErrorHandler = (*ErrorHandler)(nil)

// ErrorHandler reports OpenTelemetry's own internal errors — a dropped batch, an
// exporter that cannot reach its collector — through a logging.Logger.
type ErrorHandler struct {
	logger logging.Logger
}

// NewErrorHandler builds an ErrorHandler reporting through the given logger.
// An absent logger resolves to the noop, so the handler is safe to install
// regardless.
func NewErrorHandler(logger logging.Logger) *ErrorHandler {
	return &ErrorHandler{logger: logging.EnsureLogger(logger).WithName("otel_errors")}
}

// Handle satisfies otel.ErrorHandler.
func (h *ErrorHandler) Handle(err error) {
	if err != nil {
		h.logger.Error("tracer reported issue", err)
	}
}

// SetGlobalErrorHandler installs a logger-backed handler as OpenTelemetry's
// process-global error handler, and reports whether it did.
//
// This package used to do it in init(), which made importing anything under
// observability/tracing enough to reassign a process-global — hard-wired to the
// slog backend no matter which backend the application had configured, and
// clobbering any handler the application had installed itself. The choice to own
// that global belongs to whoever owns main, so it is a call now rather than a
// link-time side effect.
//
// Passing a nil logger is a no-op returning false, rather than quietly
// installing a handler that discards everything OTel reports.
func SetGlobalErrorHandler(logger logging.Logger) bool {
	if logger == nil {
		return false
	}

	otel.SetErrorHandler(NewErrorHandler(logger))

	return true
}

// Tracer describes a tracer.
type Tracer interface {
	StartSpan(ctx context.Context) (context.Context, Span)
	StartCustomSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, Span)
}

// Provider is trace.TracerProvider plus the two lifecycle methods a
// process needs at exit.
//
// ForceFlush drains what is buffered; Shutdown drains and then releases — it
// stops the span processor's goroutine and closes the exporter's connection.
// Flushing alone leaves both running, which is why Shutdown is part of the
// interface rather than something callers type-assert for: an implementation
// that cannot be shut down is one this package should not accept.
type Provider interface {
	trace.TracerProvider
	ForceFlush(context.Context) error
	Shutdown(context.Context) error
}

type noopProvider struct {
	noop.TracerProvider
}

func (n *noopProvider) Tracer(instrumentationName string, opts ...trace.TracerOption) trace.Tracer {
	return noop.NewTracerProvider().Tracer(instrumentationName, opts...)
}

func (n *noopProvider) ForceFlush(context.Context) error {
	return nil
}

func (n *noopProvider) Shutdown(context.Context) error {
	return nil
}

func EnsureTracerProvider(tracerProvider Provider) Provider {
	if tracerProvider != nil {
		return tracerProvider
	}

	return &noopProvider{}
}

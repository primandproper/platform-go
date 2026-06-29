package tracing

import (
	"context"

	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/logging/slog"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"
)

type errorHandler struct {
	logger logging.Logger
}

func (h errorHandler) Handle(err error) {
	h.logger.Error("tracer reported issue", err)
}

func init() {
	// set PackagePrefix to a noop error handler just so one is set
	otel.SetErrorHandler(errorHandler{logger: slog.NewSlogLogger(logging.ErrorLevel).WithName("otel_errors")})
}

// Tracer describes a tracer.
type Tracer interface {
	StartSpan(ctx context.Context) (context.Context, Span)
	StartCustomSpan(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, Span)
}

// TracerProvider is a simple alias for trace.TracerProvider.
type TracerProvider interface {
	trace.TracerProvider
	ForceFlush(context.Context) error
}

type noopTracerProvider struct {
	noop.TracerProvider
}

func (n *noopTracerProvider) Tracer(instrumentationName string, opts ...trace.TracerOption) trace.Tracer {
	return noop.NewTracerProvider().Tracer(instrumentationName, opts...)
}

func (n *noopTracerProvider) ForceFlush(context.Context) error {
	return nil
}

func EnsureTracerProvider(tracerProvider TracerProvider) TracerProvider {
	if tracerProvider != nil {
		return tracerProvider
	}

	return &noopTracerProvider{}
}

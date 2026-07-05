package noop

import (
	"context"

	"github.com/primandproper/platform-go/v4/observability/tracing"

	"go.opentelemetry.io/otel/trace"
	otelnoop "go.opentelemetry.io/otel/trace/noop"
)

var _ tracing.TracerProvider = (*TracerProvider)(nil)

// TracerProvider is a no-op TracerProvider.
type TracerProvider struct {
	otelnoop.TracerProvider
}

// NewTracerProvider returns a no-op TracerProvider.
func NewTracerProvider() tracing.TracerProvider {
	return &TracerProvider{}
}

// Tracer returns a no-op Tracer.
func (*TracerProvider) Tracer(instrumentationName string, opts ...trace.TracerOption) trace.Tracer {
	return otelnoop.NewTracerProvider().Tracer(instrumentationName, opts...)
}

// ForceFlush is a no-op.
func (*TracerProvider) ForceFlush(context.Context) error {
	return nil
}

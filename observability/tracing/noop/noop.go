// Package noop is the tracing.Provider that records no spans, and the detail
// worth knowing about it is that it still propagates.
//
// Its tracers are OTel's own noop tracers, which return the span context they
// found on the incoming context rather than an empty one. A traceparent
// arriving from upstream therefore survives this process and is emitted on the
// requests it makes: the resulting trace has a gap where this service should
// be, not a break, and the services either side of it still stitch together.
//
// Nothing is sampled, ForceFlush and Shutdown have nothing to flush, and spans
// report IsRecording as false — so a caller that guards expensive attribute
// computation on that check pays for none of it. It is what
// tracing.EnsureTracerProvider resolves to when a constructor is handed no
// provider, and what observability/tracing/config builds for the "noop"
// provider name or the empty string.
package noop

import (
	"context"

	"github.com/primandproper/platform-go/v13/observability/tracing"

	"go.opentelemetry.io/otel/trace"
	otelnoop "go.opentelemetry.io/otel/trace/noop"
)

var _ tracing.Provider = (*TracerProvider)(nil)

// TracerProvider is a no-op TracerProvider.
type TracerProvider struct {
	otelnoop.TracerProvider
}

// NewTracerProvider returns a no-op TracerProvider.
func NewTracerProvider() tracing.Provider {
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

// Shutdown is a no-op.
func (*TracerProvider) Shutdown(context.Context) error {
	return nil
}

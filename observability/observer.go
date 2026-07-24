package observability

import (
	"context"

	"github.com/primandproper/platform-go/v6/observability/logging"
	"github.com/primandproper/platform-go/v6/observability/tracing"

	"go.opentelemetry.io/otel/trace"
)

var _ Observer = (*observer)(nil)

// Observer bundles a named logger and tracer for a single component, so that a
// component holds one observability field instead of a logger/tracer pair. Each
// traced operation begins via Begin, which returns an Operation that records
// selected values to the active span and a span-linked logger simultaneously.
//
// It is an interface so that unit tests can substitute a recording
// implementation (see NewRecordingObserver) and assert which fields a unit
// observed.
type Observer interface {
	Begin(ctx context.Context) (context.Context, Operation)
	BeginCustom(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, Operation)
	Logger() logging.Logger
	Tracer() tracing.Tracer
}

type observer struct {
	logger logging.Logger
	tracer tracing.Tracer
}

// NewObserver builds the production Observer from the standard DI dependencies.
// The name is applied to both the logger and the tracer, mirroring the prior
// logging.NewNamedLogger / tracing.NewNamedTracer pair.
func NewObserver(name string, logger logging.Logger, tracerProvider tracing.TracerProvider) Observer {
	return NewObserverWithTracer(name, logger, tracing.NewNamedTracer(tracerProvider, name))
}

// NewObserverWithTracer builds an Observer from an already-constructed Tracer,
// for the few constructors that receive a tracing.Tracer rather than a
// TracerProvider (e.g. the embeddings providers). The name is applied to the
// logger; the tracer is used as given (a nil tracer becomes a noop).
func NewObserverWithTracer(name string, logger logging.Logger, tracer tracing.Tracer) Observer {
	if tracer == nil {
		tracer = tracing.NewTracerForTest(name)
	}

	return &observer{
		logger: logging.NewNamedLogger(logger, name),
		tracer: tracer,
	}
}

// NewObserverForTest builds an Observer backed by a noop logger and tracer, for
// code that just needs a functioning Observer in tests. To assert which values a
// unit attaches, use NewRecordingObserver instead.
func NewObserverForTest(name string) Observer {
	return &observer{
		logger: logging.NewNamedLogger(nil, name),
		tracer: tracing.NewTracerForTest(name),
	}
}

// Begin starts a span named for the calling function and returns an Operation
// carrying a span-linked logger. It resolves the span name via
// tracing.GetCallerName, which depends on Begin sitting exactly one frame below
// the instrumented method (the same frame-depth contract Tracer.StartSpan
// relies on); the span-name test guards that contract.
func (o *observer) Begin(ctx context.Context) (context.Context, Operation) {
	ctx, span := o.tracer.StartCustomSpan(ctx, tracing.GetCallerName())

	return ctx, newOperation(span, o.logger)
}

// BeginCustom starts an explicitly named span and returns an Operation.
func (o *observer) BeginCustom(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, Operation) {
	ctx, span := o.tracer.StartCustomSpan(ctx, name, opts...)

	return ctx, newOperation(span, o.logger)
}

// Logger returns the component's span-less named logger, for use outside of a
// traced operation (constructors, background goroutines).
func (o *observer) Logger() logging.Logger {
	return o.logger
}

// Tracer returns the component's underlying tracer.
func (o *observer) Tracer() tracing.Tracer {
	return o.tracer
}

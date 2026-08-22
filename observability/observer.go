package observability

import (
	"context"

	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/tracing"

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
	Begin(ctx context.Context, opts ...BeginOption) (context.Context, Operation)
	BeginCustom(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, Operation)
	Logger() logging.Logger
	Tracer() tracing.Tracer
}

type observer struct {
	logger logging.Logger
	tracer tracing.Tracer

	// values are seeded onto every Operation this observer begins. They are
	// held rather than baked into the logger alone because an Operation records
	// to both pillars, and a field worth having on every log line is worth
	// having on every span.
	values map[string]any
}

// NewObserver builds the production Observer from the standard DI dependencies.
// The name is applied to both the logger and the tracer, mirroring the prior
// logging.NewNamedLogger / tracing.NewNamedTracer pair.
//
// Every constructor in this module reaches observability through here, which is
// what makes a span's instrumentation scope predictable: it is always the
// component's own name, never whatever the caller happened to name a tracer it
// built itself. A constructor that accepted a ready-made tracing.Tracer would
// scope its spans by accident, so none of them do.
// A nil tracerProvider is safe: NewNamedTracer substitutes a noop provider, so
// an unconfigured component traces nowhere rather than panicking.
func NewObserver(name string, logger logging.Logger, tracerProvider tracing.Provider) Observer {
	return NewObserverWithValues(name, logger, tracerProvider, nil)
}

// NewObserverWithValues is NewObserver for a component whose every operation
// describes the same thing: a listener bound to one channel, a worker bound to
// one queue. The values land on the component's logger and are seeded onto
// every Operation Begin returns, so a field that is constant for the
// component's lifetime is stated once at construction instead of at every call
// site — and, more to the point, cannot be stated at some of them and forgotten
// at the rest.
//
// They are seeded before the caller's own BeginOptions, so an operation may
// still override one.
//
// It is a constructor rather than a method on Observer because Observer is an
// exported interface: a method would break every implementation outside this
// module, and the values are known where the observer is built anyway.
func NewObserverWithValues(
	name string,
	logger logging.Logger,
	tracerProvider tracing.Provider,
	values map[string]any,
) Observer {
	named := logging.NewNamedLogger(logger, name)
	if len(values) > 0 {
		named = named.WithValues(values)
	}

	return &observer{
		logger: named,
		tracer: tracing.NewNamedTracer(tracerProvider, name),
		values: values,
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
// carrying a span-linked logger, seeded with opts. It resolves the span name via
// tracing.GetCallerName, which depends on Begin sitting exactly one frame below
// the instrumented method (the same frame-depth contract Tracer.StartSpan
// relies on); the span-name test guards that contract. The variadic parameter
// does not disturb it — options are applied after the name is resolved, and
// GetCallerName counts frames rather than arguments.
func (o *observer) Begin(ctx context.Context, opts ...BeginOption) (context.Context, Operation) {
	ctx, span := o.tracer.StartCustomSpan(ctx, tracing.GetCallerName())

	return ctx, applyBeginOptions(o.seed(span), opts)
}

// BeginCustom starts an explicitly named span and returns an Operation.
func (o *observer) BeginCustom(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, Operation) {
	ctx, span := o.tracer.StartCustomSpan(ctx, name, opts...)

	return ctx, o.seed(span)
}

// seed builds the Operation for a span and records whatever this observer was
// constructed with. The values are applied through the Operation rather than
// onto the span directly, so they land on exactly the pillars an equivalent
// Set would have put them on.
func (o *observer) seed(span tracing.Span) Operation {
	op := newOperation(span, o.logger)

	if len(o.values) > 0 {
		// The logger already carries them from construction; this is the span's
		// half. LogOnly's counterpart would double them up on every line.
		for key, value := range o.values {
			op.SpanOnly(key, value)
		}
	}

	return op
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

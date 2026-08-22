package observability

import (
	"context"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"

	"go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc/codes"
)

var _ Operation = (*operation)(nil)

// Operation is the per-call observability bag returned by Observer.Begin. A value
// recorded via Set lands on both the active span and the running logger, so a
// value selected once is available to either pillar later. SpanOnly and LogOnly
// are escape hatches for the occasions where a value belongs to just one.
//
// It is an interface so that a recording Observer can hand back an Operation a
// test can read values off of (see RecordingOperation).
type Operation interface {
	Set(key string, value any) Operation
	SetValues(values map[string]any) Operation
	SpanOnly(key string, value any) Operation
	LogOnly(key string, value any) Operation
	Logger() logging.Logger
	Span() tracing.Span
	Time(ctx context.Context, c clock.Clock, hist metrics.Float64Histogram, opts ...metric.RecordOption) func()
	Error(err error, descriptionFmt string, descriptionArgs ...any) error
	Acknowledge(err error, descriptionFmt string, descriptionArgs ...any)
	GRPCStatus(err error, code codes.Code, descriptionFmt string, descriptionArgs ...any) error
	End()
}

type operation struct {
	span   tracing.Span
	logger logging.Logger
}

// newOperation links the span into the logger exactly once, so every line
// emitted via Logger carries the trace and span IDs.
func newOperation(span tracing.Span, logger logging.Logger) *operation {
	return &operation{
		span:   span,
		logger: logging.EnsureLogger(logger).WithSpan(span),
	}
}

// Set records a value to both the active span and the running logger.
func (op *operation) Set(key string, value any) Operation {
	tracing.AttachToSpan(op.span, key, value)
	op.logger = op.logger.WithValue(key, value)

	return op
}

// SetValues records multiple values to both pillars.
func (op *operation) SetValues(values map[string]any) Operation {
	for k, v := range values {
		op.Set(k, v)
	}

	return op
}

// SpanOnly records a value to the active span only.
func (op *operation) SpanOnly(key string, value any) Operation {
	tracing.AttachToSpan(op.span, key, value)

	return op
}

// LogOnly records a value to the running logger only.
func (op *operation) LogOnly(key string, value any) Operation {
	op.logger = op.logger.WithValue(key, value)

	return op
}

// Logger returns the running logger, carrying every Set/LogOnly value and the
// span link.
func (op *operation) Logger() logging.Logger {
	return op.logger
}

// Span returns the active span for independent use.
func (op *operation) Span() tracing.Span {
	return op.span
}

// Error logs and traces err, then returns it wrapped with the given description.
func (op *operation) Error(err error, descriptionFmt string, descriptionArgs ...any) error {
	return PrepareAndLogError(err, op.logger, op.span, descriptionFmt, descriptionArgs...)
}

// Acknowledge logs and traces err without wrapping or returning it.
func (op *operation) Acknowledge(err error, descriptionFmt string, descriptionArgs ...any) {
	AcknowledgeError(err, op.logger, op.span, descriptionFmt, descriptionArgs...)
}

// GRPCStatus logs and traces err, then returns it as a gRPC status error.
func (op *operation) GRPCStatus(err error, code codes.Code, descriptionFmt string, descriptionArgs ...any) error {
	return PrepareAndLogGRPCStatus(err, op.logger, op.span, code, descriptionFmt, descriptionArgs...)
}

// End ends the active span.
func (op *operation) End() {
	if op.span != nil {
		op.span.End()
	}
}

// millisPerSecond converts a duration to the milliseconds every histogram in
// this module reports, keeping the sub-millisecond part rather than truncating
// it: an operation that takes 400µs is not an operation that takes zero.
const millisPerSecond = 1000.0

// Time starts a timer and returns the function that records how long the
// operation took into hist, in milliseconds.
//
// Call it deferred, at the top of the work it measures:
//
//	defer op.Time(c.clock, c.latencyHist)()
//
// The doubled parentheses are the point: the outer call starts the clock now
// and the deferred inner one stops it, so there is no `startTime` local for a
// later edit to move, shadow, or leave behind when the block it belonged to is
// restructured. Twenty-five sites had written the three-line closure by hand,
// and the ones that had drifted had drifted in the direction this fixes.
//
// c is the component's clock, and is a parameter rather than something resolved
// here because that is the mistake this exists to stop repeating. A component
// that holds an injected clock and reads time.Now() for its latency is a
// component whose tests cannot control what its histogram records; passing nil
// resolves to the wall clock, and is the honest spelling for a component that
// has no clock to inject.
//
// The recording runs on the context the work ran under, so an exporter that
// reads baggage off it sees what the operation saw. A nil histogram records
// nothing, so an unmetered component needs no branch.
func (op *operation) Time(ctx context.Context, c clock.Clock, hist metrics.Float64Histogram, opts ...metric.RecordOption) func() {
	return timing(ctx, c, hist, opts...)
}

// timing is Time's body, shared with RecordingOperation so that a test reading
// an Operation off a RecordingObserver measures through the same code the real
// one does.
func timing(ctx context.Context, c clock.Clock, hist metrics.Float64Histogram, opts ...metric.RecordOption) func() {
	if hist == nil {
		return func() {}
	}

	if c == nil {
		c = clock.NewClock()
	}

	startTime := c.Now()

	// Now at both ends rather than Now-then-Since: one method for a caller's
	// clock to answer, and a stub clock pinned to an instant reports a duration
	// of zero, which is what pinning it means.
	return func() {
		hist.Record(ctx, c.Now().Sub(startTime).Seconds()*millisPerSecond, opts...)
	}
}

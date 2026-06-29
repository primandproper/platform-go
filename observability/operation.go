package observability

import (
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/tracing"

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

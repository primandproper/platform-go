package tracing

import (
	"context"

	"github.com/luna-duclos/instrumentedsql"
)

// NewInstrumentedSQLTracer wraps a Tracer for instrumentedsql.
func NewInstrumentedSQLTracer(tracerProvider TracerProvider, name string) instrumentedsql.Tracer {
	return &instrumentedSQLTracerWrapper{tracer: NewTracer(tracerProvider.Tracer(name))}
}

var _ instrumentedsql.Tracer = (*instrumentedSQLTracerWrapper)(nil)

type instrumentedSQLTracerWrapper struct {
	tracer Tracer
}

// GetSpan wraps tracer.GetSpan. It does not start a span itself: instrumentedsql
// always follows GetSpan with NewChild, which starts the actual SQL span parented
// to whatever span already lives in ctx. Starting one here would leak an unfinished
// phantom parent and orphan the trace tree.
func (t *instrumentedSQLTracerWrapper) GetSpan(ctx context.Context) instrumentedsql.Span {
	return &instrumentedSQLSpanWrapper{
		ctx:    ctx,
		tracer: t.tracer,
	}
}

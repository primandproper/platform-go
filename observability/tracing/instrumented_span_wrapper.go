package tracing

import (
	"context"

	"github.com/luna-duclos/instrumentedsql"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

var _ instrumentedsql.Span = (*instrumentedSQLSpanWrapper)(nil)

type instrumentedSQLSpanWrapper struct {
	ctx    context.Context
	tracer Tracer
	span   trace.Span
}

func (w *instrumentedSQLSpanWrapper) NewChild(s string) instrumentedsql.Span {
	w.ctx, w.span = w.tracer.StartCustomSpan(w.ctx, s)

	return w
}

func (w *instrumentedSQLSpanWrapper) SetLabel(k, v string) {
	if w.span != nil {
		w.span.SetAttributes(attribute.String(k, v))
	}
}

func (w *instrumentedSQLSpanWrapper) SetError(err error) {
	if w.span != nil {
		w.span.RecordError(err)
	}
}

func (w *instrumentedSQLSpanWrapper) Finish() {
	if w.span != nil {
		w.span.End()
	}
}

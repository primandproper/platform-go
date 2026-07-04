package tracing

import (
	"context"
	"sync"
	"testing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

type spanRecord struct {
	name     string
	spanID   trace.SpanID
	parentID trace.SpanID
}

// recordingSpanProcessor captures span start/end events for assertions.
type recordingSpanProcessor struct {
	starts []spanRecord
	ends   []spanRecord
	mu     sync.Mutex
}

func (p *recordingSpanProcessor) OnStart(_ context.Context, s sdktrace.ReadWriteSpan) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.starts = append(p.starts, spanRecord{name: s.Name(), spanID: s.SpanContext().SpanID(), parentID: s.Parent().SpanID()})
}

func (p *recordingSpanProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.ends = append(p.ends, spanRecord{name: s.Name(), spanID: s.SpanContext().SpanID(), parentID: s.Parent().SpanID()})
}

func (p *recordingSpanProcessor) Shutdown(context.Context) error   { return nil }
func (p *recordingSpanProcessor) ForceFlush(context.Context) error { return nil }

func Test_instrumentedSQLTracerWrapper_parentsToContextSpanWithoutLeakingPhantom(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		rec := &recordingSpanProcessor{}
		tp := sdktrace.NewTracerProvider(
			sdktrace.WithSpanProcessor(rec),
			sdktrace.WithSampler(sdktrace.AlwaysSample()),
		)

		ctx, parent := tp.Tracer("test").Start(t.Context(), "parent")

		sqlTracer := NewInstrumentedSQLTracer(tp, "test")
		child := sqlTracer.GetSpan(ctx).NewChild("op-sql")
		child.Finish()
		parent.End()

		rec.mu.Lock()
		defer rec.mu.Unlock()

		// GetSpan must not start a phantom parent: only the parent and the SQL span.
		test.SliceLen(t, 2, rec.starts)
		test.SliceLen(t, 2, rec.ends)

		var sqlSpan *spanRecord
		for i := range rec.starts {
			if rec.starts[i].name == "op-sql" {
				sqlSpan = &rec.starts[i]
			}
		}
		must.NotNil(t, sqlSpan)

		// The SQL span must be parented to the real span living in ctx.
		test.EqOp(t, parent.SpanContext().SpanID(), sqlSpan.parentID)

		// The SQL span must have been ended (no leak).
		var ended bool
		for i := range rec.ends {
			if rec.ends[i].spanID == sqlSpan.spanID {
				ended = true
			}
		}
		test.True(t, ended)
	})
}

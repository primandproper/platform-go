package observability

import (
	"context"
	"maps"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/primandproper/platform-go/v4/observability/logging"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// recordingLogger is a logging.Logger that records the values and span links
// folded into it, so tests can assert the logger half of the dual-write
// contract. Every With* method returns the same instance, sharing one record.
type loggerRecord struct {
	values    map[string]any
	spanCalls int
}

type recordingLogger struct {
	rec *loggerRecord
}

func newRecordingLogger() *recordingLogger {
	return &recordingLogger{rec: &loggerRecord{values: map[string]any{}}}
}

func (l *recordingLogger) WithValue(key string, value any) logging.Logger {
	l.rec.values[key] = value
	return l
}

func (l *recordingLogger) WithValues(values map[string]any) logging.Logger {
	maps.Copy(l.rec.values, values)
	return l
}

func (l *recordingLogger) WithSpan(trace.Span) logging.Logger {
	l.rec.spanCalls++
	return l
}

func (l *recordingLogger) WithName(string) logging.Logger             { return l }
func (l *recordingLogger) Clone() logging.Logger                      { return l }
func (l *recordingLogger) WithRequest(*http.Request) logging.Logger   { return l }
func (l *recordingLogger) WithResponse(*http.Response) logging.Logger { return l }
func (l *recordingLogger) WithError(error) logging.Logger             { return l }
func (l *recordingLogger) Info(string)                                {}
func (l *recordingLogger) Debug(string)                               {}
func (l *recordingLogger) Error(string, error)                        {}
func (l *recordingLogger) SetRequestIDFunc(logging.RequestIDFunc)     {}

// recordingExporter captures finished spans so tests can assert the span half of
// the dual-write contract.
type recordingExporter struct {
	spans []sdktrace.ReadOnlySpan
	mu    sync.Mutex
}

func (e *recordingExporter) ExportSpans(_ context.Context, spans []sdktrace.ReadOnlySpan) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.spans = append(e.spans, spans...)
	return nil
}

func (e *recordingExporter) Shutdown(context.Context) error { return nil }

func (e *recordingExporter) recorded() []sdktrace.ReadOnlySpan {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.spans
}

func spanAttr(s sdktrace.ReadOnlySpan, key string) (attribute.Value, bool) {
	attrs := s.Attributes()
	for i := range attrs {
		if string(attrs[i].Key) == key {
			return attrs[i].Value, true
		}
	}
	return attribute.Value{}, false
}

// newTestObserver wires a real SDK tracer provider feeding a recording exporter
// plus a recording logger, so both pillars can be inspected after an Operation.
func newTestObserver(t *testing.T) (Observer, *loggerRecord, *recordingExporter) {
	t.Helper()

	rl := newRecordingLogger()
	exp := &recordingExporter{}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	return NewObserver("test_observer", rl, tp), rl.rec, exp
}

func TestObserver_Begin(T *testing.T) {
	T.Parallel()

	T.Run("names the span after the calling function", func(t *testing.T) {
		t.Parallel()

		o, _, exp := newTestObserver(t)

		_, op := o.Begin(t.Context())
		op.End()

		spans := exp.recorded()
		test.SliceLen(t, 1, spans)
		// Guards the caller-skip contract: the name must be the test closure, not Begin.
		name := spans[0].Name()
		test.True(t, strings.Contains(name, "TestObserver_Begin"))
		test.False(t, strings.Contains(name, "Begin)"))
	})

	T.Run("links the span into the logger exactly once", func(t *testing.T) {
		t.Parallel()

		o, rec, _ := newTestObserver(t)

		_, op := o.Begin(t.Context())
		op.End()

		test.EqOp(t, 1, rec.spanCalls)
	})
}

func TestObserver_accessors(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		o, _, _ := newTestObserver(t)

		test.NotNil(t, o.Logger())
		test.NotNil(t, o.Tracer())
	})
}

func TestNewObserverForTest(T *testing.T) {
	T.Parallel()

	T.Run("yields a usable noop-backed observer", func(t *testing.T) {
		t.Parallel()

		o := NewObserverForTest("test_observer")
		must.NotNil(t, o)

		ctx, op := o.Begin(t.Context())
		test.NotNil(t, ctx)

		op.Set("key", "value")
		test.NotNil(t, op.Logger())
		op.End()
	})
}

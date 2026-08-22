package observability

import (
	"context"
	"maps"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/primandproper/platform-go/v13/observability/logging"

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
func (l *recordingLogger) Warn(string)                                {}
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

func TestNewObserverWithValues(T *testing.T) {
	T.Parallel()

	// newSeededObserver mirrors newTestObserver, with values a component would
	// know at construction and never change.
	newSeededObserver := func(t *testing.T, values map[string]any) (Observer, *loggerRecord, *recordingExporter) {
		t.Helper()

		rl := newRecordingLogger()
		exp := &recordingExporter{}
		tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exp))
		t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

		return NewObserverWithValues("test_observer", rl, tp, values), rl.rec, exp
	}

	T.Run("records the values on the component's logger", func(t *testing.T) {
		t.Parallel()

		_, rec, _ := newSeededObserver(t, map[string]any{"channel": "outbox"})

		test.EqOp(t, "outbox", rec.values["channel"])
	})

	T.Run("seeds every span the observer begins", func(t *testing.T) {
		t.Parallel()

		o, _, exp := newSeededObserver(t, map[string]any{"channel": "outbox"})

		for range 2 {
			_, op := o.Begin(t.Context())
			op.End()
		}

		spans := exp.recorded()
		must.SliceLen(t, 2, spans)

		for i := range spans {
			value, ok := spanAttr(spans[i], "channel")
			must.True(t, ok, must.Sprintf("span %d carried no seeded value", i))
			test.EqOp(t, "outbox", value.AsString())
		}
	})

	T.Run("seeds an explicitly named span too", func(t *testing.T) {
		t.Parallel()

		o, _, exp := newSeededObserver(t, map[string]any{"channel": "outbox"})

		_, op := o.BeginCustom(t.Context(), "custom")
		op.End()

		spans := exp.recorded()
		must.SliceLen(t, 1, spans)

		value, ok := spanAttr(spans[0], "channel")
		must.True(t, ok)
		test.EqOp(t, "outbox", value.AsString())
	})

	// The logger already carries them from construction, so seeding them onto
	// the operation's logger as well would double every line's fields.
	T.Run("does not re-record the values on the operation's logger", func(t *testing.T) {
		t.Parallel()

		o, rec, _ := newSeededObserver(t, map[string]any{"channel": "outbox"})

		_, op := o.Begin(t.Context())
		op.Set("other", "value")
		op.End()

		test.EqOp(t, "outbox", rec.values["channel"])
		test.EqOp(t, "value", rec.values["other"])
	})

	T.Run("an operation may still record its own value for a seeded key", func(t *testing.T) {
		t.Parallel()

		o, _, exp := newSeededObserver(t, map[string]any{"channel": "outbox"})

		_, op := o.Begin(t.Context(), WithValue("channel", "work"))
		op.End()

		spans := exp.recorded()
		must.SliceLen(t, 1, spans)

		value, ok := spanAttr(spans[0], "channel")
		must.True(t, ok)
		test.EqOp(t, "work", value.AsString())
	})

	// NewObserver is this with no values, so the absent case has to stay
	// exactly what it was.
	T.Run("no values behaves as NewObserver does", func(t *testing.T) {
		t.Parallel()

		for _, values := range []map[string]any{nil, {}} {
			o, rec, exp := newSeededObserver(t, values)

			_, op := o.Begin(t.Context())
			op.End()

			spans := exp.recorded()
			must.SliceLen(t, 1, spans)

			test.MapEmpty(t, rec.values)
		}
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

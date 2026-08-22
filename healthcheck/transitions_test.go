package healthcheck

import (
	"context"
	"maps"
	"net/http"
	"sync"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/logging"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/trace"
)

// line is one thing a logger was told.
type line struct {
	err     error
	message string
	values  map[string]any
	level   logging.Level
}

// recordingLogger keeps what it was told, so a test can assert that a component
// going down produced a line naming the component and carrying the reason.
//
// Derived loggers share the parent's slice — WithValue returns a new logger, and
// what a test wants to see is everything that reached the root.
type recordingLogger struct {
	lines  *[]line
	values map[string]any
	mu     *sync.Mutex
}

func newRecordingLogger() *recordingLogger {
	return &recordingLogger{lines: &[]line{}, values: map[string]any{}, mu: &sync.Mutex{}}
}

func (l *recordingLogger) record(level logging.Level, message string, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	values := make(map[string]any, len(l.values))
	maps.Copy(values, l.values)

	*l.lines = append(*l.lines, line{level: level, message: message, err: err, values: values})
}

func (l *recordingLogger) recorded() []line {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]line(nil), *l.lines...)
}

func (l *recordingLogger) with(values map[string]any) logging.Logger {
	merged := make(map[string]any, len(l.values)+len(values))
	maps.Copy(merged, l.values)
	maps.Copy(merged, values)

	return &recordingLogger{lines: l.lines, values: merged, mu: l.mu}
}

func (l *recordingLogger) Info(message string)  { l.record(logging.InfoLevel, message, nil) }
func (l *recordingLogger) Debug(message string) { l.record(logging.DebugLevel, message, nil) }
func (l *recordingLogger) Warn(message string)  { l.record(logging.WarnLevel, message, nil) }
func (l *recordingLogger) Error(message string, err error) {
	l.record(logging.ErrorLevel, message, err)
}

func (l *recordingLogger) SetRequestIDFunc(logging.RequestIDFunc) {}

func (l *recordingLogger) Clone() logging.Logger { return l.with(nil) }
func (l *recordingLogger) WithName(name string) logging.Logger {
	return l.with(map[string]any{logging.LoggerNameKey: name})
}
func (l *recordingLogger) WithValues(v map[string]any) logging.Logger { return l.with(v) }
func (l *recordingLogger) WithValue(k string, v any) logging.Logger {
	return l.with(map[string]any{k: v})
}
func (l *recordingLogger) WithRequest(*http.Request) logging.Logger   { return l.with(nil) }
func (l *recordingLogger) WithResponse(*http.Response) logging.Logger { return l.with(nil) }
func (l *recordingLogger) WithError(err error) logging.Logger {
	return l.with(map[string]any{"error": err})
}
func (l *recordingLogger) WithSpan(trace.Span) logging.Logger { return l.with(nil) }

func TestCheckerRegistry_LogsTransitions(T *testing.T) {
	T.Parallel()

	T.Run("a component going down is logged once, with the reason", func(t *testing.T) {
		t.Parallel()

		logger := newRecordingLogger()

		registry, err := NewRegistry(WithLogger(logger))
		must.NoError(t, err)

		failure := platformerrors.New("connection refused")
		component := &mockChecker{name: "database", checkFn: func(context.Context) error { return failure }}
		registry.Register(component)

		registry.CheckAll(t.Context())
		registry.CheckAll(t.Context())

		failures := linesAt(logger.recorded(), logging.ErrorLevel)
		must.SliceLen(t, 1, failures)
		test.ErrorIs(t, failures[0].err, failure)
		test.EqOp(t, "database", stringValue(t, failures[0], componentKey))
		test.EqOp(t, string(StatusDown), stringValue(t, failures[0], statusKey))

		// Recovering is the other half of the signal, and says what it recovered
		// from.
		component.checkFn = nil
		registry.CheckAll(t.Context())

		recoveries := linesAt(logger.recorded(), logging.InfoLevel)
		must.SliceLen(t, 1, recoveries)
		test.EqOp(t, "database", stringValue(t, recoveries[0], componentKey))
		test.EqOp(t, string(StatusUp), stringValue(t, recoveries[0], statusKey))
		test.EqOp(t, string(StatusDown), stringValue(t, recoveries[0], previousStatusKey))
	})
}

// stringValue reads one value off a recorded line, failing the test when the
// line does not carry it.
func stringValue(t *testing.T, l line, key string) string {
	t.Helper()

	s, ok := l.values[key].(string)
	must.True(t, ok, must.Sprintf("line %q carries no string %q", l.message, key))

	return s
}

func linesAt(lines []line, level logging.Level) []line {
	var matched []line

	for i := range lines {
		if lines[i].level == level {
			matched = append(matched, lines[i])
		}
	}

	return matched
}

func TestCheck(T *testing.T) {
	T.Parallel()

	T.Run("a nil registry has nothing to check and is up", func(t *testing.T) {
		t.Parallel()

		result := Check(t.Context(), nil)

		must.NotNil(t, result)
		test.EqOp(t, StatusUp, result.Status)
		test.MapEmpty(t, result.Components)
	})

	T.Run("a registry is asked", func(t *testing.T) {
		t.Parallel()

		registry := newTestRegistry(t)
		registry.Register(&mockChecker{name: "database"})

		result := Check(t.Context(), registry)

		must.NotNil(t, result)
		test.EqOp(t, StatusUp, result.Components["database"].Status)
	})
}

func TestWithPillars(T *testing.T) {
	T.Parallel()

	T.Run("a nil Pillars attaches nothing", func(t *testing.T) {
		t.Parallel()

		registry, err := NewRegistry(WithPillars(nil))
		must.NoError(t, err)
		must.NotNil(t, registry)

		test.EqOp(t, StatusUp, registry.CheckAll(t.Context()).Status)
	})
}

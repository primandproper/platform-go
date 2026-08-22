package oauth2server_test

import (
	"context"
	"maps"
	"net/http"
	"sync"
	"testing"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	"github.com/primandproper/platform-go/v13/observability/logging"

	otelcodes "go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// line is one thing a logger was told.
type line struct {
	err     error
	message string
	values  map[string]any
	level   logging.Level
}

// recordingLogger keeps what it was told, so a test can assert at what level a
// refusal reached the logs.
//
// It exists because the severity split is the one thing about this server's
// error handling that has no visible effect on a response: a client that sent a
// bad request and a client that met a broken store are both told something went
// wrong, and only the log line says which of those is worth waking somebody for.
//
// Derived loggers share the parent's slice — WithValue returns a new logger, and
// what a test wants to see is everything that reached the root.
type recordingLogger struct {
	lines  *[]line
	values map[string]any
	mu     *sync.Mutex
}

var _ logging.Logger = (*recordingLogger)(nil)

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

// at returns the lines recorded at one level.
func (l *recordingLogger) at(level logging.Level) []line {
	var found []line

	recorded := l.recorded()
	for i := range recorded {
		if recorded[i].level == level {
			found = append(found, recorded[i])
		}
	}

	return found
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

// errorSpans records which spans were marked as having failed.
type errorSpans struct {
	names []string
	mu    sync.Mutex
}

var _ sdktrace.SpanProcessor = (*errorSpans)(nil)

func (p *errorSpans) OnStart(context.Context, sdktrace.ReadWriteSpan) {}

func (p *errorSpans) OnEnd(span sdktrace.ReadOnlySpan) {
	if span.Status().Code != otelcodes.Error {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	p.names = append(p.names, span.Name())
}

func (p *errorSpans) Shutdown(context.Context) error   { return nil }
func (p *errorSpans) ForceFlush(context.Context) error { return nil }

func (p *errorSpans) errored() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.names...)
}

// newObservedHarness builds a server over a store that can be broken on demand,
// with everything it logs and every span it fails recorded.
func newObservedHarness(t *testing.T, faults *faultStore, opts ...oauth2server.Option) (*harness, *recordingLogger, *errorSpans) {
	t.Helper()

	logger, spans := newRecordingLogger(), &errorSpans{}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans), sdktrace.WithSampler(sdktrace.AlwaysSample()))
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	h := &harness{t: t, authenticator: &passwordAuthenticator{}, store: faults}

	return h.build(t, h.authenticator, append([]oauth2server.Option{
		oauth2server.WithLogger(logger),
		oauth2server.WithTracerProvider(tp),
	}, opts...)), logger, spans
}

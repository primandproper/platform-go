// Package slog implements logging.Logger over the standard library's log/slog,
// emitting JSON to stdout.
//
// It is the backend with no third-party dependency behind it, which is most of
// the reason to choose it: the handler is the standard library's, so its output
// format and its performance are whatever the Go release provides. It is also
// the one with nothing to shut down — records go to stdout as they are written,
// so there is no buffer to flush and nothing is lost when a process exits
// abruptly. Collection is the deployment's problem rather than the process's.
//
// Source locations are attached only at debug level, and are resolved by walking
// the stack in this package so they point at the caller of Info, Debug, Warn, or
// Error rather than at this file — which is what a wrapper around slog otherwise
// reports for every line it emits.
//
// log/slog has no named loggers, so logging.Logger's WithName is implemented as
// an attribute on the derived logger rather than as a name slog itself knows
// about.
package slog

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/logging"

	"go.opentelemetry.io/otel/trace"
)

var _ logging.Logger = (*Logger)(nil)

// Logger is the log/slog logging.Logger implementation. It is exported, and
// returned by NewSlogLogger, so a caller who has chosen slog can depend on that
// choice rather than on the interface every backend shares.
type Logger struct {
	requestIDFunc logging.RequestIDFunc
	logger        *slog.Logger
}

// NewSlogLogger builds a log/slog-backed Logger.
func NewSlogLogger(lvl logging.Level) *Logger {
	var level slog.Leveler
	switch lvl {
	case logging.DebugLevel:
		level = slog.LevelDebug
	case logging.InfoLevel:
		level = slog.LevelInfo
	case logging.WarnLevel:
		level = slog.LevelWarn
	case logging.ErrorLevel:
		level = slog.LevelError
	}

	handlerOptions := &slog.HandlerOptions{
		AddSource: lvl == logging.DebugLevel,
		Level:     level,
	}

	return &Logger{
		logger: slog.New(slog.NewJSONHandler(os.Stdout, handlerOptions)),
	}
}

// WithName is our obligatory contract fulfillment function.
// Slog doesn't support named loggers :( so we have this workaround.
func (l *Logger) WithName(name string) logging.Logger {
	l2 := l.logger.With(slog.String(logging.LoggerNameKey, name))
	return &Logger{requestIDFunc: l.requestIDFunc, logger: l2}
}

// SetRequestIDFunc sets the request ID retrieval function.
func (l *Logger) SetRequestIDFunc(f logging.RequestIDFunc) {
	if f != nil {
		l.requestIDFunc = f
	}
}

// logAt emits a record whose source PC points at the caller of the exported
// logging method rather than at this wrapper. Calling
// l.logger.Info/Debug/Warn/Error directly makes slog's AddSource attribute every
// line to this file.
func (l *Logger) logAt(level slog.Level, msg string, attrs ...slog.Attr) {
	ctx := context.Background()
	if !l.logger.Enabled(ctx, level) {
		return
	}

	var pcs [1]uintptr
	// Skip [runtime.Callers, logAt, the exported Info/Debug/Warn/Error method] so
	// the captured PC is the caller of that exported method.
	runtime.Callers(3, pcs[:])
	r := slog.NewRecord(time.Now(), level, msg, pcs[0])
	r.AddAttrs(attrs...)
	_ = l.logger.Handler().Handle(ctx, r) //nolint:errcheck // logging is best-effort
}

// Info satisfies our contract for the logging.Logger Info method.
func (l *Logger) Info(input string) {
	l.logAt(slog.LevelInfo, input)
}

// Debug satisfies our contract for the logging.Logger Debug method.
func (l *Logger) Debug(input string) {
	l.logAt(slog.LevelDebug, input)
}

// Warn satisfies our contract for the logging.Logger Warn method.
func (l *Logger) Warn(input string) {
	l.logAt(slog.LevelWarn, input)
}

// Error satisfies our contract for the logging.Logger Error method.
func (l *Logger) Error(whatWasHappeningWhenErrorOccurred string, err error) {
	if err != nil {
		l.logAt(slog.LevelError, whatWasHappeningWhenErrorOccurred, slog.Any("error", err))
		return
	}
	l.logAt(slog.LevelError, whatWasHappeningWhenErrorOccurred)
}

// Clone satisfies our contract for the logging.Logger WithValue method.
func (l *Logger) Clone() logging.Logger {
	l2 := l.logger.With()
	return &Logger{requestIDFunc: l.requestIDFunc, logger: l2}
}

// WithValue satisfies our contract for the logging.Logger WithValue method.
func (l *Logger) WithValue(key string, value any) logging.Logger {
	l2 := l.logger.With(slog.Any(key, value))
	return &Logger{requestIDFunc: l.requestIDFunc, logger: l2}
}

// WithValues satisfies our contract for the logging.Logger WithValues method.
func (l *Logger) WithValues(values map[string]any) logging.Logger {
	var l2 = l.logger.With()

	for key, val := range values {
		l2 = l2.With(slog.Any(key, val))
	}

	return &Logger{requestIDFunc: l.requestIDFunc, logger: l2}
}

// WithError satisfies our contract for the logging.Logger WithError method.
func (l *Logger) WithError(err error) logging.Logger {
	l2 := l.logger.With(slog.Any("error", err))
	return &Logger{requestIDFunc: l.requestIDFunc, logger: l2}
}

// WithSpan satisfies our contract for the logging.Logger WithSpan method.
func (l *Logger) WithSpan(span trace.Span) logging.Logger {
	si := logging.ExtractSpanInfo(span)

	l2 := l.logger.With(slog.String(keys.SpanIDKey, si.SpanID), slog.String(keys.TraceIDKey, si.TraceID))

	return &Logger{requestIDFunc: l.requestIDFunc, logger: l2}
}

func (l *Logger) attachRequestToLog(req *http.Request) *slog.Logger {
	ri := logging.ExtractRequestInfo(req, l.requestIDFunc)
	if req == nil {
		return l.logger
	}

	l2 := l.logger.With(slog.String(keys.RequestMethodKey, ri.Method))

	if ri.Path != "" {
		l2 = l2.With(slog.String("path", ri.Path))
	}
	if ri.Query != "" {
		l2 = l2.With(slog.String(keys.URLQueryKey, ri.Query))
	}
	if ri.RequestID != "" {
		l2 = l2.With(slog.String(keys.RequestIDKey, ri.RequestID))
	}

	return l2
}

// WithRequest satisfies our contract for the logging.Logger WithRequest method.
func (l *Logger) WithRequest(req *http.Request) logging.Logger {
	return &Logger{requestIDFunc: l.requestIDFunc, logger: l.attachRequestToLog(req)}
}

// WithResponse satisfies our contract for the logging.Logger WithResponse method.
func (l *Logger) WithResponse(res *http.Response) logging.Logger {
	l2 := l.logger.With()
	if res != nil {
		l2 = l.attachRequestToLog(res.Request).With(slog.Int(keys.ResponseStatusKey, res.StatusCode))
	}

	return &Logger{requestIDFunc: l.requestIDFunc, logger: l2}
}

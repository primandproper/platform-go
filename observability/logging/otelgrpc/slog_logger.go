// Package otelgrpc implements logging.Logger over log/slog, fanning every record
// out to both stdout and an OTLP collector reached over gRPC.
//
// It is the backend for a deployment that wants its logs correlated with its
// traces in one system rather than scraped from container output. Both
// destinations always receive the record — stdout is not a fallback for a
// collector that is down — so a collector outage costs the correlated copy and
// nothing else.
//
// An empty CollectorEndpoint builds a logger that writes only to stdout, which
// makes this backend usable in a local run without a collector standing by. Note
// that this is the one thing the config's own validation rejects, so reaching
// that state means constructing the Config directly rather than loading it.
//
// # Shutting down
//
// Records reach the collector through a batch processor, so they are buffered:
// a process that exits without calling Shutdown loses whatever had not been
// flushed, which on a crash path is usually the records explaining the crash.
// The DI container calls it, and so does the observability package's
// Pillars.Shutdown. Shutdown on a logger derived through With* is a no-op —
// those do not own the provider — so the value to keep is the one the
// constructor returned.
//
// Construction reports nothing about itself, deliberately. Announcing the
// collector endpoint through log/slog's global default would route it to
// whatever else in the process claimed that global, and routing it through the
// logger being built makes the collector's first record one about having
// configured the collector — which, on an unreachable endpoint, is a record
// Shutdown then blocks trying to flush.
//
// Building this logger sets the OTel global logger provider, which is
// process-wide state: it is a composition-root thing to construct, not something
// to build twice.
package otelgrpc

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"time"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/logging"
	o11yutils "github.com/primandproper/platform-go/v13/observability/utils"
	"github.com/primandproper/platform-go/v13/version"

	slogmulti "github.com/samber/slog-multi"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

var _ logging.Logger = (*Logger)(nil)

// Logger is the OTLP-over-gRPC logging.Logger implementation, built on
// log/slog. It is exported, and returned by NewOtelSlogLogger, so a caller who
// has chosen it can depend on that choice rather than on the interface every
// backend shares.
type Logger struct {
	requestIDFunc  logging.RequestIDFunc
	logger         *slog.Logger
	loggerProvider *log.LoggerProvider
}

// NewOtelSlogLogger builds an OTLP-over-gRPC Logger.
//
// It reports nothing about its own construction. It used to announce the
// collector endpoint through log/slog's global default — a destination, format
// and level belonging to whatever else in the process had claimed that global,
// which is exactly what a caller choosing this backend is choosing not to use.
// Routing the line through the logger being built is no better: the first record
// the collector receives is then one about having configured the collector, and
// on an unreachable endpoint it is a record Shutdown has to block trying to
// flush.
func NewOtelSlogLogger(ctx context.Context, lvl logging.Level, serviceName string, cfg *Config) (*Logger, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

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

	logHandlers := []slog.Handler{
		slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			AddSource: lvl == logging.DebugLevel,
			Level:     level,
		}),
	}

	var loggerProvider *log.LoggerProvider

	if cfg.CollectorEndpoint != "" {
		options := []otlploggrpc.Option{
			otlploggrpc.WithEndpoint(cfg.CollectorEndpoint),
		}

		// Only override the library's default timeout when one is actually configured;
		// passing a zero Timeout disables the timeout entirely.
		if cfg.Timeout > 0 {
			options = append(options, otlploggrpc.WithTimeout(cfg.Timeout))
		}

		if cfg.Insecure {
			options = append(options, otlploggrpc.WithInsecure())
		}

		// Create the OTLP log exporter that sends logs to configured destination
		logExporter, err := otlploggrpc.New(ctx, options...)
		if err != nil {
			return nil, errors.Wrap(err, "instantiating otlploggrpc exporter")
		}

		res, resErr := o11yutils.OtelResource(ctx, serviceName)
		if resErr != nil {
			return nil, resErr
		}

		// Create the logger provider
		loggerProvider = log.NewLoggerProvider(
			log.WithProcessor(log.NewBatchProcessor(logExporter)),
			log.WithResource(res),
			log.WithAttributeCountLimit(128),
			log.WithAttributeValueLengthLimit(-1),
		)

		// Set the logger provider globally
		global.SetLoggerProvider(loggerProvider)

		logHandlers = append(logHandlers, otelslog.NewHandler(
			serviceName,
			otelslog.WithLoggerProvider(loggerProvider),
			otelslog.WithVersion(version.Get().Version),
			otelslog.WithSource(true),
		))
	}

	logger := &Logger{
		logger:         slog.New(slogmulti.Fanout(logHandlers...)),
		loggerProvider: loggerProvider,
	}

	return logger, nil
}

// Shutdown flushes buffered log records and stops the batch processor's exporter
// goroutine. It is a no-op for loggers configured without a collector endpoint, and
// for loggers derived via With* (which do not own the provider). The DI container
// (samber/do) invokes this automatically on shutdown; Pillars.Shutdown calls it too.
func (l *Logger) Shutdown(ctx context.Context) error {
	if l.loggerProvider == nil {
		return nil
	}

	return errors.Wrap(l.loggerProvider.Shutdown(ctx), "shutting down otelslog logger provider")
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
// logging method rather than at this wrapper.
//
// This is the same fix the log/slog backend carries, and this backend — a copy
// of that one — was made before it. Calling l.logger.Info/Debug/Warn/Error
// directly leaves slog to walk the stack itself, and it stops at this file, so
// AddSource and the otelslog bridge's WithSource(true) both attribute every
// record in the process to whichever of the four methods below emitted it.
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

package otelgrpc

import (
	"context"
	"log/slog"
	"net/http"
	"os"

	"github.com/primandproper/platform-go/v6/errors"
	"github.com/primandproper/platform-go/v6/observability/keys"
	"github.com/primandproper/platform-go/v6/observability/logging"
	o11yutils "github.com/primandproper/platform-go/v6/observability/utils"
	"github.com/primandproper/platform-go/v6/version"

	slogmulti "github.com/samber/slog-multi"
	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel/exporters/otlp/otlplog/otlploggrpc"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/trace"
)

// logger is our log wrapper.
type otelSlogLogger struct {
	requestIDFunc  logging.RequestIDFunc
	logger         *slog.Logger
	loggerProvider *log.LoggerProvider
}

// NewOtelSlogLogger builds a new otelSlogLogger.
func NewOtelSlogLogger(ctx context.Context, lvl logging.Level, serviceName string, cfg *Config) (logging.Logger, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	var level slog.Leveler
	switch {
	case logging.LevelsEqual(lvl, logging.DebugLevel):
		level = slog.LevelDebug
	case logging.LevelsEqual(lvl, logging.InfoLevel):
		level = slog.LevelInfo
	case logging.LevelsEqual(lvl, logging.WarnLevel):
		level = slog.LevelWarn
	case logging.LevelsEqual(lvl, logging.ErrorLevel):
		level = slog.LevelError
	}

	logHandlers := []slog.Handler{
		slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
			AddSource: logging.LevelsEqual(lvl, logging.DebugLevel),
			Level:     level,
		}),
	}

	var loggerProvider *log.LoggerProvider

	if cfg.CollectorEndpoint != "" {
		slog.Info("configuring otelgprc collector handler", slog.String("endpoint", cfg.CollectorEndpoint))

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

		// Create the logger provider
		loggerProvider = log.NewLoggerProvider(
			log.WithProcessor(log.NewBatchProcessor(logExporter)),
			log.WithResource(o11yutils.MustOtelResource(ctx, serviceName)),
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

	logger := &otelSlogLogger{
		logger:         slog.New(slogmulti.Fanout(logHandlers...)),
		loggerProvider: loggerProvider,
	}

	return logger, nil
}

// Shutdown flushes buffered log records and stops the batch processor's exporter
// goroutine. It is a no-op for loggers configured without a collector endpoint, and
// for loggers derived via With* (which do not own the provider). The DI container
// (samber/do) invokes this automatically on shutdown; Pillars.Shutdown calls it too.
func (l *otelSlogLogger) Shutdown(ctx context.Context) error {
	if l.loggerProvider == nil {
		return nil
	}

	return errors.Wrap(l.loggerProvider.Shutdown(ctx), "shutting down otelslog logger provider")
}

// WithName is our obligatory contract fulfillment function.
// Slog doesn't support named loggers :( so we have this workaround.
func (l *otelSlogLogger) WithName(name string) logging.Logger {
	l2 := l.logger.With(slog.String(logging.LoggerNameKey, name))
	return &otelSlogLogger{requestIDFunc: l.requestIDFunc, logger: l2}
}

// SetRequestIDFunc sets the request ID retrieval function.
func (l *otelSlogLogger) SetRequestIDFunc(f logging.RequestIDFunc) {
	if f != nil {
		l.requestIDFunc = f
	}
}

// Info satisfies our contract for the logging.Logger Info method.
func (l *otelSlogLogger) Info(input string) {
	l.logger.Info(input)
}

// Debug satisfies our contract for the logging.Logger Debug method.
func (l *otelSlogLogger) Debug(input string) {
	l.logger.Debug(input)
}

// Error satisfies our contract for the logging.Logger Error method.
func (l *otelSlogLogger) Error(whatWasHappeningWhenErrorOccurred string, err error) {
	if err != nil {
		l.logger.Error(whatWasHappeningWhenErrorOccurred, slog.Any("error", err))
		return
	}
	l.logger.Error(whatWasHappeningWhenErrorOccurred)
}

// Clone satisfies our contract for the logging.Logger WithValue method.
func (l *otelSlogLogger) Clone() logging.Logger {
	l2 := l.logger.With()
	return &otelSlogLogger{requestIDFunc: l.requestIDFunc, logger: l2}
}

// WithValue satisfies our contract for the logging.Logger WithValue method.
func (l *otelSlogLogger) WithValue(key string, value any) logging.Logger {
	l2 := l.logger.With(slog.Any(key, value))
	return &otelSlogLogger{requestIDFunc: l.requestIDFunc, logger: l2}
}

// WithValues satisfies our contract for the logging.Logger WithValues method.
func (l *otelSlogLogger) WithValues(values map[string]any) logging.Logger {
	var l2 = l.logger.With()

	for key, val := range values {
		l2 = l2.With(slog.Any(key, val))
	}

	return &otelSlogLogger{requestIDFunc: l.requestIDFunc, logger: l2}
}

// WithError satisfies our contract for the logging.Logger WithError method.
func (l *otelSlogLogger) WithError(err error) logging.Logger {
	l2 := l.logger.With(slog.Any("error", err))
	return &otelSlogLogger{requestIDFunc: l.requestIDFunc, logger: l2}
}

// WithSpan satisfies our contract for the logging.Logger WithSpan method.
func (l *otelSlogLogger) WithSpan(span trace.Span) logging.Logger {
	si := logging.ExtractSpanInfo(span)

	l2 := l.logger.With(slog.String(keys.SpanIDKey, si.SpanID), slog.String(keys.TraceIDKey, si.TraceID))

	return &otelSlogLogger{requestIDFunc: l.requestIDFunc, logger: l2}
}

func (l *otelSlogLogger) attachRequestToLog(req *http.Request) *slog.Logger {
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
func (l *otelSlogLogger) WithRequest(req *http.Request) logging.Logger {
	return &otelSlogLogger{requestIDFunc: l.requestIDFunc, logger: l.attachRequestToLog(req)}
}

// WithResponse satisfies our contract for the logging.Logger WithResponse method.
func (l *otelSlogLogger) WithResponse(res *http.Response) logging.Logger {
	l2 := l.logger.With()
	if res != nil {
		l2 = l.attachRequestToLog(res.Request).With(slog.Int(keys.ResponseStatusKey, res.StatusCode))
	}

	return &otelSlogLogger{requestIDFunc: l.requestIDFunc, logger: l2}
}

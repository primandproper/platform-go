package zap

import (
	"fmt"
	"net/http"
	"os"

	"github.com/primandproper/platform-go/v5/observability/keys"
	"github.com/primandproper/platform-go/v5/observability/logging"
	loggingnoop "github.com/primandproper/platform-go/v5/observability/logging/noop"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// logger is our log wrapper.
type zapLogger struct {
	requestIDFunc logging.RequestIDFunc
	logger        *zap.Logger
	atomicLevel   zap.AtomicLevel
}

// NewZapLogger builds a new zapLogger.
func NewZapLogger(lvl logging.Level) logging.Logger {
	atomicLevel := zap.NewAtomicLevel()

	var cfg zap.Config
	switch {
	case logging.LevelsEqual(lvl, logging.DebugLevel):
		atomicLevel.SetLevel(zap.DebugLevel)
		cfg = zap.NewDevelopmentConfig()
	case logging.LevelsEqual(lvl, logging.WarnLevel):
		atomicLevel.SetLevel(zap.WarnLevel)
		cfg = zap.NewProductionConfig()
	case logging.LevelsEqual(lvl, logging.ErrorLevel):
		atomicLevel.SetLevel(zap.ErrorLevel)
		cfg = zap.NewProductionConfig()
	default:
		atomicLevel.SetLevel(zap.InfoLevel)
		cfg = zap.NewProductionConfig()
	}

	// Wire our AtomicLevel into the config so SetLevel affects the running logger.
	cfg.Level = atomicLevel

	l, err := cfg.Build(zap.AddCallerSkip(1))
	if err != nil {
		fmt.Fprintf(os.Stderr, "WARNING: failed to create zap logger, falling back to noop: %v\n", err)
		return loggingnoop.NewLogger()
	}

	return &zapLogger{logger: l, atomicLevel: atomicLevel}
}

// WithName is our obligatory contract fulfillment function.
func (l *zapLogger) WithName(name string) logging.Logger {
	l2 := l.logger.With(zap.String(logging.LoggerNameKey, name))
	return &zapLogger{requestIDFunc: l.requestIDFunc, logger: l2, atomicLevel: l.atomicLevel}
}

// SetLevel sets the log level for our zap logger.
func (l *zapLogger) SetLevel(level logging.Level) {
	var lvl zapcore.Level

	switch {
	case logging.LevelsEqual(level, logging.DebugLevel):
		lvl = zap.DebugLevel
	case logging.LevelsEqual(level, logging.WarnLevel):
		lvl = zap.WarnLevel
	case logging.LevelsEqual(level, logging.ErrorLevel):
		lvl = zap.ErrorLevel
	default:
		lvl = zap.InfoLevel
	}

	l.atomicLevel.SetLevel(lvl)
}

// SetRequestIDFunc sets the request ID retrieval function.
func (l *zapLogger) SetRequestIDFunc(f logging.RequestIDFunc) {
	if f != nil {
		l.requestIDFunc = f
	}
}

// Info satisfies our contract for the logging.Logger Info method.
func (l *zapLogger) Info(input string) {
	l.logger.Info(input)
}

// Debug satisfies our contract for the logging.Logger Debug method.
func (l *zapLogger) Debug(input string) {
	l.logger.Debug(input)
}

// Error satisfies our contract for the logging.Logger Error method.
func (l *zapLogger) Error(whatWasHappeningWhenErrorOccurred string, err error) {
	if err != nil {
		l.logger.Error(whatWasHappeningWhenErrorOccurred, zap.Error(err))
		return
	}
	l.logger.Error(whatWasHappeningWhenErrorOccurred)
}

// Clone satisfies our contract for the logging.Logger WithValue method.
func (l *zapLogger) Clone() logging.Logger {
	l2 := l.logger.With()
	return &zapLogger{requestIDFunc: l.requestIDFunc, logger: l2, atomicLevel: l.atomicLevel}
}

// WithValue satisfies our contract for the logging.Logger WithValue method.
func (l *zapLogger) WithValue(key string, value any) logging.Logger {
	l2 := l.logger.With(zap.Any(key, value))
	return &zapLogger{requestIDFunc: l.requestIDFunc, logger: l2, atomicLevel: l.atomicLevel}
}

// WithValues satisfies our contract for the logging.Logger WithValues method.
func (l *zapLogger) WithValues(values map[string]any) logging.Logger {
	var l2 = l.logger.With()

	for key, val := range values {
		l2 = l2.With(zap.Any(key, val))
	}

	return &zapLogger{requestIDFunc: l.requestIDFunc, logger: l2, atomicLevel: l.atomicLevel}
}

// WithError satisfies our contract for the logging.Logger WithError method.
func (l *zapLogger) WithError(err error) logging.Logger {
	l2 := l.logger.With(zap.Error(err))
	return &zapLogger{requestIDFunc: l.requestIDFunc, logger: l2, atomicLevel: l.atomicLevel}
}

// WithSpan satisfies our contract for the logging.Logger WithSpan method.
func (l *zapLogger) WithSpan(span trace.Span) logging.Logger {
	si := logging.ExtractSpanInfo(span)

	l2 := l.logger.With(zap.String(keys.SpanIDKey, si.SpanID), zap.String(keys.TraceIDKey, si.TraceID))

	return &zapLogger{requestIDFunc: l.requestIDFunc, logger: l2, atomicLevel: l.atomicLevel}
}

func (l *zapLogger) attachRequestToLog(req *http.Request) *zap.Logger {
	ri := logging.ExtractRequestInfo(req, l.requestIDFunc)
	if req == nil {
		return l.logger
	}

	l2 := l.logger.With(zap.String(keys.RequestMethodKey, ri.Method))

	if ri.Path != "" {
		l2 = l2.With(zap.String("path", ri.Path))
	}
	if ri.Query != "" {
		l2 = l2.With(zap.String(keys.URLQueryKey, ri.Query))
	}
	if ri.RequestID != "" {
		l2 = l2.With(zap.String(keys.RequestIDKey, ri.RequestID))
	}

	return l2
}

// WithRequest satisfies our contract for the logging.Logger WithRequest method.
func (l *zapLogger) WithRequest(req *http.Request) logging.Logger {
	return &zapLogger{requestIDFunc: l.requestIDFunc, logger: l.attachRequestToLog(req), atomicLevel: l.atomicLevel}
}

// WithResponse satisfies our contract for the logging.Logger WithResponse method.
func (l *zapLogger) WithResponse(res *http.Response) logging.Logger {
	l2 := l.logger.With()
	if res != nil {
		l2 = l.attachRequestToLog(res.Request).With(zap.Int(keys.ResponseStatusKey, res.StatusCode))
	}

	return &zapLogger{requestIDFunc: l.requestIDFunc, logger: l2, atomicLevel: l.atomicLevel}
}

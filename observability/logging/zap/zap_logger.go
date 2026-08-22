// Package zap implements logging.Logger over uber-go/zap.
//
// It is the backend to choose when the level has to move without a restart.
// SetLevel is not on logging.Logger, so it is reachable only through the
// *Logger the constructor returned — and because every logger derived by
// WithName or WithValue shares the same underlying atomic level, one call
// re-levels the whole tree. Narrowing the constructor's result to the interface
// at the point of construction is what makes that unreachable.
//
// The requested level also selects zap's configuration preset, which decides
// rather more than the threshold: debug builds the development config, with its
// console encoding and stack traces on warnings, and every other level builds
// the production config, which is JSON. A later SetLevel moves the threshold and
// not the encoding, so a process started at info and lowered to debug logs debug
// records in production's format.
//
// Construction reports an error rather than degrading to a noop. A service that
// asked for zap and silently got nothing logs nothing for its whole life, and
// the one line saying so scrolls past in the first second of startup.
package zap

import (
	"net/http"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/logging"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

var _ logging.Logger = (*Logger)(nil)

// Logger is the zap logging.Logger implementation. It is exported, and
// returned by NewZapLogger, so a caller who has chosen zap can depend on that
// choice rather than on the interface every backend shares — most concretely
// SetLevel, which logging.Logger does not carry and which was unreachable for
// as long as this constructor returned the interface.
//
// The AtomicLevel is shared with every logger WithName derives from this one,
// so a SetLevel here re-levels the whole tree. Those derived loggers are
// logging.Logger values, because that is what the interface's method returns:
// hold on to the Logger this constructor handed back if you intend to re-level
// at runtime.
type Logger struct {
	requestIDFunc logging.RequestIDFunc
	logger        *zap.Logger
	atomicLevel   zap.AtomicLevel
}

// NewZapLogger builds a zap-backed Logger.
//
// It returns an error rather than degrading to a noop logger with a warning on
// stderr. A service that asked for zap and silently got nothing logs nothing
// for its whole life, and the one line saying so scrolls past in the first
// second of startup — which is the same failure the config packages refuse when
// they will not substitute a provider nobody named.
func NewZapLogger(lvl logging.Level) (*Logger, error) {
	atomicLevel := zap.NewAtomicLevel()

	var cfg zap.Config
	switch lvl {
	case logging.DebugLevel:
		atomicLevel.SetLevel(zap.DebugLevel)
		cfg = zap.NewDevelopmentConfig()
	case logging.WarnLevel:
		atomicLevel.SetLevel(zap.WarnLevel)
		cfg = zap.NewProductionConfig()
	case logging.ErrorLevel:
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
		return nil, errors.Wrap(err, "building zap logger")
	}

	return &Logger{logger: l, atomicLevel: atomicLevel}, nil
}

// WithName is our obligatory contract fulfillment function.
func (l *Logger) WithName(name string) logging.Logger {
	l2 := l.logger.With(zap.String(logging.LoggerNameKey, name))
	return &Logger{requestIDFunc: l.requestIDFunc, logger: l2, atomicLevel: l.atomicLevel}
}

// SetLevel sets the log level for our zap logger.
func (l *Logger) SetLevel(level logging.Level) {
	var lvl zapcore.Level

	switch level {
	case logging.DebugLevel:
		lvl = zap.DebugLevel
	case logging.WarnLevel:
		lvl = zap.WarnLevel
	case logging.ErrorLevel:
		lvl = zap.ErrorLevel
	default:
		lvl = zap.InfoLevel
	}

	l.atomicLevel.SetLevel(lvl)
}

// SetRequestIDFunc sets the request ID retrieval function.
func (l *Logger) SetRequestIDFunc(f logging.RequestIDFunc) {
	if f != nil {
		l.requestIDFunc = f
	}
}

// Info satisfies our contract for the logging.Logger Info method.
func (l *Logger) Info(input string) {
	l.logger.Info(input)
}

// Debug satisfies our contract for the logging.Logger Debug method.
func (l *Logger) Debug(input string) {
	l.logger.Debug(input)
}

// Warn satisfies our contract for the logging.Logger Warn method.
func (l *Logger) Warn(input string) {
	l.logger.Warn(input)
}

// Error satisfies our contract for the logging.Logger Error method.
func (l *Logger) Error(whatWasHappeningWhenErrorOccurred string, err error) {
	if err != nil {
		l.logger.Error(whatWasHappeningWhenErrorOccurred, zap.Error(err))
		return
	}
	l.logger.Error(whatWasHappeningWhenErrorOccurred)
}

// Clone satisfies our contract for the logging.Logger WithValue method.
func (l *Logger) Clone() logging.Logger {
	l2 := l.logger.With()
	return &Logger{requestIDFunc: l.requestIDFunc, logger: l2, atomicLevel: l.atomicLevel}
}

// WithValue satisfies our contract for the logging.Logger WithValue method.
func (l *Logger) WithValue(key string, value any) logging.Logger {
	l2 := l.logger.With(zap.Any(key, value))
	return &Logger{requestIDFunc: l.requestIDFunc, logger: l2, atomicLevel: l.atomicLevel}
}

// WithValues satisfies our contract for the logging.Logger WithValues method.
func (l *Logger) WithValues(values map[string]any) logging.Logger {
	var l2 = l.logger.With()

	for key, val := range values {
		l2 = l2.With(zap.Any(key, val))
	}

	return &Logger{requestIDFunc: l.requestIDFunc, logger: l2, atomicLevel: l.atomicLevel}
}

// WithError satisfies our contract for the logging.Logger WithError method.
func (l *Logger) WithError(err error) logging.Logger {
	l2 := l.logger.With(zap.Error(err))
	return &Logger{requestIDFunc: l.requestIDFunc, logger: l2, atomicLevel: l.atomicLevel}
}

// WithSpan satisfies our contract for the logging.Logger WithSpan method.
func (l *Logger) WithSpan(span trace.Span) logging.Logger {
	si := logging.ExtractSpanInfo(span)

	l2 := l.logger.With(zap.String(keys.SpanIDKey, si.SpanID), zap.String(keys.TraceIDKey, si.TraceID))

	return &Logger{requestIDFunc: l.requestIDFunc, logger: l2, atomicLevel: l.atomicLevel}
}

func (l *Logger) attachRequestToLog(req *http.Request) *zap.Logger {
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
func (l *Logger) WithRequest(req *http.Request) logging.Logger {
	return &Logger{requestIDFunc: l.requestIDFunc, logger: l.attachRequestToLog(req), atomicLevel: l.atomicLevel}
}

// WithResponse satisfies our contract for the logging.Logger WithResponse method.
func (l *Logger) WithResponse(res *http.Response) logging.Logger {
	l2 := l.logger.With()
	if res != nil {
		l2 = l.attachRequestToLog(res.Request).With(zap.Int(keys.ResponseStatusKey, res.StatusCode))
	}

	return &Logger{requestIDFunc: l.requestIDFunc, logger: l2, atomicLevel: l.atomicLevel}
}

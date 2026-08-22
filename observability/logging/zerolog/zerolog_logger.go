// Package zerolog implements logging.Logger over rs/zerolog.
//
// What it takes care to avoid is zerolog's process-global configuration.
// zerolog's own Timestamp and Caller read package-level variables —
// TimestampFunc, TimeFieldFormat, CallerSkipFrameCount, CallerMarshalFunc —
// which belong to whichever library in the process set them last. Setting those
// at import time made this package's formatting choices everybody's. Timestamps
// are written by a per-logger hook instead, in UTC and RFC 3339 with nanoseconds,
// and call sites are resolved here with a local skip count, so what this logger
// emits is decided by this logger and nothing else in the binary is affected.
//
// Caller paths are trimmed of this module's own prefix, which shows up only in
// -trimpath builds; an untrimmed build reports the absolute path the runtime
// gives it. A stack that cannot be walked yields "unknown" rather than an absent
// field, so the caller key is always present.
package zerolog

import (
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/logging"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/trace"
)

const here = "github.com/primandproper/platform-go/v13/"

// utcTimestampHook writes each event's timestamp as RFC3339Nano in UTC.
//
// zerolog's own Timestamp() reads the package-global TimestampFunc and
// TimeFieldFormat, which this package used to set at import time — process-wide
// state that belonged to whichever application happened to link it. A hook is
// per-logger, so the format travels with the logger that wants it.
type utcTimestampHook struct{}

func (utcTimestampHook) Run(e *zerolog.Event, _ zerolog.Level, _ string) {
	e.Str(zerolog.TimestampFieldName, time.Now().UTC().Format(time.RFC3339Nano))
}

// callerAt resolves the call site skip frames above its own caller, formatted
// the way this package has always formatted callers.
//
// It stands in for zerolog's Caller(), which reads the global
// CallerSkipFrameCount and CallerMarshalFunc. Resolving the frame here means the
// skip count is a local constant that can be read against the method using it,
// rather than a global increment that any other zerolog user in the process
// could also apply.
//
// The TrimPrefix matters only in -trimpath builds, where runtime reports
// module-relative paths; an untrimmed build keeps its absolute path.
func callerAt(skip int) string {
	_, file, line, ok := runtime.Caller(skip + 1)
	if !ok {
		return unknownCaller
	}

	return strings.TrimPrefix(file, here) + ", line " + strconv.Itoa(line)
}

// unknownCaller stands in when the stack cannot be walked, so the caller field
// is always present rather than sometimes absent and sometimes empty.
const unknownCaller = "unknown"

var _ logging.Logger = (*Logger)(nil)

// Logger is the zerolog logging.Logger implementation. It is exported, and
// returned by NewZerologLogger, so a caller who has chosen zerolog can depend on
// that choice rather than on the interface every backend shares.
type Logger struct {
	requestIDFunc logging.RequestIDFunc
	logger        zerolog.Logger
}

// buildZerologger builds a new zerologger.
//
// The caller field is attached by the methods that emit, not here. Binding
// Caller() into the logger's context added it to every event and left Error's
// own Caller() to add a second copy, so an error logged at the warn or error
// threshold carried two of them.
func buildZerologger(level logging.Level) zerolog.Logger {
	var lvl zerolog.Level
	logger := zerolog.New(os.Stdout).Hook(utcTimestampHook{})

	switch level {
	case logging.DebugLevel:
		lvl = zerolog.DebugLevel
	case logging.WarnLevel:
		lvl = zerolog.WarnLevel
	case logging.ErrorLevel:
		lvl = zerolog.ErrorLevel
	default:
		lvl = zerolog.InfoLevel
	}

	return logger.Level(lvl)
}

// NewZerologLogger builds a zerolog-backed Logger.
func NewZerologLogger(lvl logging.Level) *Logger {
	return &Logger{logger: buildZerologger(lvl)}
}

// WithName is our obligatory contract fulfillment function.
// Zerolog doesn't support named loggers :( so we have this workaround.
func (l *Logger) WithName(name string) logging.Logger {
	l2 := l.logger.With().Str(logging.LoggerNameKey, name).Logger()
	return &Logger{requestIDFunc: l.requestIDFunc, logger: l2}
}

// SetRequestIDFunc sets the request ID retrieval function.
func (l *Logger) SetRequestIDFunc(f logging.RequestIDFunc) {
	if f != nil {
		l.requestIDFunc = f
	}
}

// Info satisfies our contract for the logging.Logger Info method.
func (l *Logger) Info(input string) {
	l.logger.Info().Msg(input)
}

// Debug satisfies our contract for the logging.Logger Debug method.
func (l *Logger) Debug(input string) {
	l.logger.Debug().Msg(input)
}

// Warn satisfies our contract for the logging.Logger Warn method.
func (l *Logger) Warn(input string) {
	l.logger.Warn().Msg(input)
}

// Error satisfies our contract for the logging.Logger Error method.
func (l *Logger) Error(whatWasHappeningWhenErrorOccurred string, err error) {
	// Resolved here rather than in a helper, because the frame count has to be
	// counted from a fixed depth below this method: 1 is whoever called it.
	caller := callerAt(1)

	if err != nil {
		l.logger.Error().Stack().Str(zerolog.CallerFieldName, caller).Err(err).Msg(whatWasHappeningWhenErrorOccurred)
		return
	}

	l.logger.Error().Str(zerolog.CallerFieldName, caller).Msg(whatWasHappeningWhenErrorOccurred)
}

// Clone satisfies our contract for the logging.Logger WithValue method.
func (l *Logger) Clone() logging.Logger {
	l2 := l.logger.With().Logger()
	return &Logger{requestIDFunc: l.requestIDFunc, logger: l2}
}

// WithValue satisfies our contract for the logging.Logger WithValue method.
func (l *Logger) WithValue(key string, value any) logging.Logger {
	l2 := l.logger.With().Interface(key, value).Logger()
	return &Logger{requestIDFunc: l.requestIDFunc, logger: l2}
}

// WithValues satisfies our contract for the logging.Logger WithValues method.
func (l *Logger) WithValues(values map[string]any) logging.Logger {
	var l2 = l.logger.With().Logger()

	for key, val := range values {
		l2 = l2.With().Interface(key, val).Logger()
	}

	return &Logger{requestIDFunc: l.requestIDFunc, logger: l2}
}

// WithError satisfies our contract for the logging.Logger WithError method.
func (l *Logger) WithError(err error) logging.Logger {
	l2 := l.logger.With().Err(err).Logger()
	return &Logger{requestIDFunc: l.requestIDFunc, logger: l2}
}

// WithSpan satisfies our contract for the logging.Logger WithSpan method.
func (l *Logger) WithSpan(span trace.Span) logging.Logger {
	si := logging.ExtractSpanInfo(span)

	l2 := l.logger.With().Str(keys.SpanIDKey, si.SpanID).Str(keys.TraceIDKey, si.TraceID).Logger()

	return &Logger{requestIDFunc: l.requestIDFunc, logger: l2}
}

func (l *Logger) attachRequestToLog(req *http.Request) zerolog.Logger {
	ri := logging.ExtractRequestInfo(req, l.requestIDFunc)
	if req == nil {
		return l.logger
	}

	l2 := l.logger.With().
		Str(keys.RequestMethodKey, ri.Method).
		Logger()

	if ri.Path != "" {
		l2 = l2.With().Str("path", ri.Path).Logger()
	}
	if ri.Query != "" {
		l2 = l2.With().Str(keys.URLQueryKey, ri.Query).Logger()
	}
	if ri.RequestID != "" {
		l2 = l2.With().Str(keys.RequestIDKey, ri.RequestID).Logger()
	}

	return l2
}

// WithRequest satisfies our contract for the logging.Logger WithRequest method.
func (l *Logger) WithRequest(req *http.Request) logging.Logger {
	return &Logger{requestIDFunc: l.requestIDFunc, logger: l.attachRequestToLog(req)}
}

// WithResponse satisfies our contract for the logging.Logger WithResponse method.
func (l *Logger) WithResponse(res *http.Response) logging.Logger {
	l2 := l.logger.With().Logger()
	if res != nil {
		l2 = l.attachRequestToLog(res.Request).With().Int(keys.ResponseStatusKey, res.StatusCode).Logger()
	}

	return &Logger{requestIDFunc: l.requestIDFunc, logger: l2}
}

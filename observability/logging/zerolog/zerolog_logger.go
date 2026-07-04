package zerolog

import (
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v3/observability/keys"
	"github.com/primandproper/platform-go/v3/observability/logging"

	"github.com/rs/zerolog"
	"go.opentelemetry.io/otel/trace"
)

const here = "github.com/primandproper/platform-go/v3/"

func init() {
	// The wrapper's logging methods add exactly one frame between the caller and
	// zerolog's Caller() capture, so skip one — not two, which pointed one frame
	// above the actual call site.
	zerolog.CallerSkipFrameCount++
	zerolog.DisableSampling(true)
	zerolog.TimeFieldFormat = time.RFC3339Nano
	// Emit timestamps in UTC rather than a hardcoded America/Chicago local time.
	zerolog.TimestampFunc = func() time.Time {
		return time.Now().UTC()
	}
	zerolog.CallerMarshalFunc = func(_ uintptr, file string, line int) string {
		return strings.TrimPrefix(file, here) + ", line " + strconv.Itoa(line)
	}
	zerolog.LevelFieldName = "severity"
}

// logger is our log wrapper.
type zerologLogger struct {
	requestIDFunc logging.RequestIDFunc
	logger        zerolog.Logger
}

// buildZerologger builds a new zerologger.
func buildZerologger(level logging.Level) zerolog.Logger {
	var lvl zerolog.Level
	logger := zerolog.New(os.Stdout).With().Timestamp().Logger()

	switch {
	case logging.LevelsEqual(level, logging.DebugLevel):
		logger = logger.With().Logger()
		lvl = zerolog.DebugLevel
	case logging.LevelsEqual(level, logging.WarnLevel):
		logger = logger.With().Caller().Logger()
		lvl = zerolog.WarnLevel
	case logging.LevelsEqual(level, logging.ErrorLevel):
		logger = logger.With().Caller().Logger()
		lvl = zerolog.ErrorLevel
	default:
		lvl = zerolog.InfoLevel
	}

	return logger.Level(lvl)
}

// NewZerologLogger builds a new zerologLogger.
func NewZerologLogger(lvl logging.Level) logging.Logger {
	return &zerologLogger{logger: buildZerologger(lvl)}
}

// WithName is our obligatory contract fulfillment function.
// Zerolog doesn't support named loggers :( so we have this workaround.
func (l *zerologLogger) WithName(name string) logging.Logger {
	l2 := l.logger.With().Str(logging.LoggerNameKey, name).Logger()
	return &zerologLogger{requestIDFunc: l.requestIDFunc, logger: l2}
}

// SetRequestIDFunc sets the request ID retrieval function.
func (l *zerologLogger) SetRequestIDFunc(f logging.RequestIDFunc) {
	if f != nil {
		l.requestIDFunc = f
	}
}

// Info satisfies our contract for the logging.Logger Info method.
func (l *zerologLogger) Info(input string) {
	l.logger.Info().Msg(input)
}

// Debug satisfies our contract for the logging.Logger Debug method.
func (l *zerologLogger) Debug(input string) {
	l.logger.Debug().Msg(input)
}

// Error satisfies our contract for the logging.Logger Error method.
func (l *zerologLogger) Error(whatWasHappeningWhenErrorOccurred string, err error) {
	if err != nil {
		l.logger.Error().Stack().Caller().Err(err).Msg(whatWasHappeningWhenErrorOccurred)
		return
	}
	l.logger.Error().Caller().Msg(whatWasHappeningWhenErrorOccurred)
}

// Clone satisfies our contract for the logging.Logger WithValue method.
func (l *zerologLogger) Clone() logging.Logger {
	l2 := l.logger.With().Logger()
	return &zerologLogger{requestIDFunc: l.requestIDFunc, logger: l2}
}

// WithValue satisfies our contract for the logging.Logger WithValue method.
func (l *zerologLogger) WithValue(key string, value any) logging.Logger {
	l2 := l.logger.With().Interface(key, value).Logger()
	return &zerologLogger{requestIDFunc: l.requestIDFunc, logger: l2}
}

// WithValues satisfies our contract for the logging.Logger WithValues method.
func (l *zerologLogger) WithValues(values map[string]any) logging.Logger {
	var l2 = l.logger.With().Logger()

	for key, val := range values {
		l2 = l2.With().Interface(key, val).Logger()
	}

	return &zerologLogger{requestIDFunc: l.requestIDFunc, logger: l2}
}

// WithError satisfies our contract for the logging.Logger WithError method.
func (l *zerologLogger) WithError(err error) logging.Logger {
	l2 := l.logger.With().Err(err).Logger()
	return &zerologLogger{requestIDFunc: l.requestIDFunc, logger: l2}
}

// WithSpan satisfies our contract for the logging.Logger WithSpan method.
func (l *zerologLogger) WithSpan(span trace.Span) logging.Logger {
	si := logging.ExtractSpanInfo(span)

	l2 := l.logger.With().Str(keys.SpanIDKey, si.SpanID).Str(keys.TraceIDKey, si.TraceID).Logger()

	return &zerologLogger{requestIDFunc: l.requestIDFunc, logger: l2}
}

func (l *zerologLogger) attachRequestToLog(req *http.Request) zerolog.Logger {
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
func (l *zerologLogger) WithRequest(req *http.Request) logging.Logger {
	return &zerologLogger{requestIDFunc: l.requestIDFunc, logger: l.attachRequestToLog(req)}
}

// WithResponse satisfies our contract for the logging.Logger WithResponse method.
func (l *zerologLogger) WithResponse(res *http.Response) logging.Logger {
	l2 := l.logger.With().Logger()
	if res != nil {
		l2 = l.attachRequestToLog(res.Request).With().Int(keys.ResponseStatusKey, res.StatusCode).Logger()
	}

	return &zerologLogger{requestIDFunc: l.requestIDFunc, logger: l2}
}

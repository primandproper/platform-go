package routing_test

import (
	"context"
	"database/sql"
	"errors"
	"maps"
	"net/http"
	"sync"
	"testing"

	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/routing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
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
// returned error reached the logs, and with what on the line.
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

// observedRouter builds a Router that records everything it logs and every span
// it marks as failed.
func observedRouter(t *testing.T, opts ...routing.RouterOption) (*routing.Router, *recordingLogger, *errorSpanProcessor) {
	t.Helper()

	logger := newRecordingLogger()
	spans := &errorSpanProcessor{}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans), sdktrace.WithSampler(sdktrace.AlwaysSample()))

	return buildTestRouter(t, append(opts, routing.WithLogger(logger), routing.WithTracerProvider(tp))...), logger, spans
}

func TestRouter_ErrorSeverity(T *testing.T) {
	T.Parallel()

	T.Run("a 5xx is logged at ERROR and marks the span", func(t *testing.T) {
		t.Parallel()

		r, logger, spans := observedRouter(t)
		routing.Get(r, "/orgs/{orgID:uint64}", func(_ context.Context, _ getUserInput) (userOutput, error) {
			return userOutput{}, errors.New("the database is on fire")
		})

		test.EqOp(t, http.StatusInternalServerError, doRequest(t, r, http.MethodGet, "/orgs/1", "").Code)

		must.SliceLen(t, 1, logger.at(logging.ErrorLevel))
		test.SliceContains(t, spans.errored(), "get_orgs_orgID")
	})

	T.Run("a 4xx from the handler is logged at WARN and leaves the span alone", func(t *testing.T) {
		t.Parallel()

		r, logger, spans := observedRouter(t)
		routing.Get(r, "/orgs/{orgID:uint64}", func(_ context.Context, _ getUserInput) (userOutput, error) {
			return userOutput{}, sql.ErrNoRows
		})

		test.EqOp(t, http.StatusNotFound, doRequest(t, r, http.MethodGet, "/orgs/1", "").Code)

		test.SliceEmpty(t, logger.at(logging.ErrorLevel))
		test.SliceEmpty(t, spans.errored())

		warned := logger.at(logging.WarnLevel)
		must.SliceLen(t, 1, warned)

		// The error itself and the status are both on the line: a WARN that does
		// not say what went wrong, or why it was only a WARN, is a line nobody can
		// act on.
		test.NotNil(t, warned[0].values["error"])
		test.EqOp(t, http.StatusNotFound, warned[0].values["response.status"])
	})

	T.Run("a malformed request is a client mistake, not a service fault", func(t *testing.T) {
		t.Parallel()

		// The case the escape hatch existed for: an unauthenticated route where a
		// caller sending nonsense could otherwise write ERROR lines and failed
		// spans into the service's telemetry at will.
		r, logger, spans := observedRouter(t)
		routing.Get(r, "/orgs/{orgID:uint64}", func(_ context.Context, in getUserInput) (userOutput, error) {
			return userOutput{ID: in.OrgID}, nil
		})

		test.EqOp(t, http.StatusBadRequest, doRequest(t, r, http.MethodGet, "/orgs/not-a-number", "").Code)

		test.SliceEmpty(t, logger.at(logging.ErrorLevel))
		test.SliceEmpty(t, spans.errored())
		test.SliceLen(t, 1, logger.at(logging.WarnLevel))
	})

	T.Run("a custom classifier decides, and sees the status", func(t *testing.T) {
		t.Parallel()

		var seen []int

		r, logger, spans := observedRouter(t, routing.WithErrorClassifier(
			func(_ context.Context, _ error, status int) routing.ErrorSeverity {
				seen = append(seen, status)

				return routing.SeverityInfo
			},
		))
		routing.Get(r, "/orgs/{orgID:uint64}", func(_ context.Context, _ getUserInput) (userOutput, error) {
			return userOutput{}, errors.New("the database is on fire")
		})

		test.EqOp(t, http.StatusInternalServerError, doRequest(t, r, http.MethodGet, "/orgs/1", "").Code)

		test.Eq(t, []int{http.StatusInternalServerError}, seen)
		test.SliceLen(t, 1, logger.at(logging.InfoLevel))
		test.SliceEmpty(t, logger.at(logging.ErrorLevel))
		test.SliceEmpty(t, spans.errored())
	})

	T.Run("a classifier sees the status a custom encoder chose", func(t *testing.T) {
		t.Parallel()

		var seen []int

		r, _, _ := observedRouter(t,
			routing.WithErrorEncoder(func(context.Context, error) (int, any) {
				return http.StatusTeapot, flatError{Error: "i am a teapot"}
			}),
			routing.WithErrorClassifier(func(_ context.Context, _ error, status int) routing.ErrorSeverity {
				seen = append(seen, status)

				return routing.SeverityNone
			}),
		)
		routing.Get(r, "/orgs/{orgID:uint64}", func(_ context.Context, _ getUserInput) (userOutput, error) {
			return userOutput{}, errors.New("the database is on fire")
		})

		test.EqOp(t, http.StatusTeapot, doRequest(t, r, http.MethodGet, "/orgs/1", "").Code)
		test.Eq(t, []int{http.StatusTeapot}, seen)
	})

	T.Run("SeverityNone records nothing", func(t *testing.T) {
		t.Parallel()

		r, logger, spans := observedRouter(t, routing.WithErrorClassifier(
			func(context.Context, error, int) routing.ErrorSeverity { return routing.SeverityNone },
		))
		routing.Get(r, "/orgs/{orgID:uint64}", func(_ context.Context, _ getUserInput) (userOutput, error) {
			return userOutput{}, errors.New("the database is on fire")
		})

		test.EqOp(t, http.StatusInternalServerError, doRequest(t, r, http.MethodGet, "/orgs/1", "").Code)

		test.SliceEmpty(t, logger.recorded())
		test.SliceEmpty(t, spans.errored())
	})

	T.Run("a severity this package does not know is recorded rather than dropped", func(t *testing.T) {
		t.Parallel()

		r, logger, spans := observedRouter(t, routing.WithErrorClassifier(
			func(context.Context, error, int) routing.ErrorSeverity { return routing.ErrorSeverity(200) },
		))
		routing.Get(r, "/orgs/{orgID:uint64}", func(_ context.Context, _ getUserInput) (userOutput, error) {
			return userOutput{}, sql.ErrNoRows
		})

		test.EqOp(t, http.StatusNotFound, doRequest(t, r, http.MethodGet, "/orgs/1", "").Code)

		test.SliceLen(t, 1, logger.at(logging.ErrorLevel))
		test.SliceContains(t, spans.errored(), "get_orgs_orgID")
	})

	T.Run("a nil classifier leaves the default in place", func(t *testing.T) {
		t.Parallel()

		r, logger, _ := observedRouter(t, routing.WithErrorClassifier(nil))
		routing.Get(r, "/orgs/{orgID:uint64}", func(_ context.Context, _ getUserInput) (userOutput, error) {
			return userOutput{}, sql.ErrNoRows
		})

		test.EqOp(t, http.StatusNotFound, doRequest(t, r, http.MethodGet, "/orgs/1", "").Code)
		test.SliceLen(t, 1, logger.at(logging.WarnLevel))
	})
}

func TestDefaultErrorSeverity(T *testing.T) {
	T.Parallel()

	T.Run("severity follows the status", func(t *testing.T) {
		t.Parallel()

		for status, expected := range map[int]routing.ErrorSeverity{
			http.StatusOK:                  routing.SeverityInfo,
			http.StatusMovedPermanently:    routing.SeverityInfo,
			http.StatusBadRequest:          routing.SeverityWarn,
			http.StatusNotFound:            routing.SeverityWarn,
			http.StatusTooManyRequests:     routing.SeverityWarn,
			http.StatusInternalServerError: routing.SeverityError,
			http.StatusBadGateway:          routing.SeverityError,
		} {
			test.EqOp(t, expected, routing.DefaultErrorSeverity(t.Context(), errors.New("blah"), status),
				test.Sprintf("status %d", status))
		}
	})

	T.Run("the zero value is the one that reports", func(t *testing.T) {
		t.Parallel()

		// A classifier that falls through its own cases returns the zero value.
		// It has to be the severity that keeps the error, not the one that drops
		// it.
		var zero routing.ErrorSeverity

		test.EqOp(t, routing.SeverityError, zero)
		test.EqOp(t, "error", zero.String())
	})

	T.Run("every severity names itself", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "warn", routing.SeverityWarn.String())
		test.EqOp(t, "info", routing.SeverityInfo.String())
		test.EqOp(t, "none", routing.SeverityNone.String())
		test.EqOp(t, "unknown", routing.ErrorSeverity(200).String())
	})
}

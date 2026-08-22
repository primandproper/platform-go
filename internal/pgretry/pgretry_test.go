package pgretry

import (
	"context"
	stderrors "errors"
	"maps"
	"net/http"
	"strings"
	"sync"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/logging"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// IsRetryable decides which failures a Retrier is allowed to re-run. Getting it
// wrong in either direction is expensive: too narrow and a retryable deadlock
// becomes a failed request, too wide and a permanent error is re-run until the
// attempt ceiling.
func TestIsRetryable(T *testing.T) {
	T.Parallel()

	cases := map[string]struct {
		err      error
		expected bool
	}{
		"a deadlock":                 {&pgconn.PgError{Code: pgDeadlockDetected}, true},
		"a serialization failure":    {&pgconn.PgError{Code: pgSerializationFailure}, true},
		"a wrapped deadlock":         {platformerrors.Wrap(&pgconn.PgError{Code: pgDeadlockDetected}, "writing"), true},
		"another Postgres condition": {&pgconn.PgError{Code: "23505"}, false},
		"an unrelated error":         {stderrors.New("boom"), false},
		"no error at all":            {nil, false},
	}

	for name, tc := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			test.EqOp(t, tc.expected, IsRetryable(tc.err))
		})
	}
}

func TestTruncateError(T *testing.T) {
	T.Parallel()

	// The column is nullable and the row distinguishes "has not failed" from
	// "failed"; rendering nil as "" would collapse the two.
	T.Run("a nil cause stores nothing", func(t *testing.T) {
		t.Parallel()

		test.Nil(t, TruncateError(nil))
	})

	T.Run("a short cause reaches the column intact", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "boom", TruncateError(platformerrors.New("boom")))
	})

	T.Run("bounds what reaches the column", func(t *testing.T) {
		t.Parallel()

		stored, ok := TruncateError(stderrors.New(strings.Repeat("e", MaxStoredErrLen*2))).(string)

		must.True(t, ok)
		test.EqOp(t, MaxStoredErrLen, len(stored))
	})
}

func TestRetrier_Do(T *testing.T) {
	T.Parallel()

	deadlock := &pgconn.PgError{Code: pgDeadlockDetected}

	T.Run("runs the write once when it succeeds", func(t *testing.T) {
		t.Parallel()

		var calls int

		r := &Retrier{Attempts: 5}

		err := r.Do(t.Context(), "writing", func() error {
			calls++

			return nil
		})

		must.NoError(t, err)
		test.EqOp(t, 1, calls)
	})

	// The point of the classification: a permanent failure must not be re-run,
	// because re-asking an answered question only delays the error.
	T.Run("returns a non-retryable failure without re-running it", func(t *testing.T) {
		t.Parallel()

		var calls int

		sentinel := stderrors.New("constraint violated")
		r := &Retrier{Attempts: 5}

		err := r.Do(t.Context(), "writing", func() error {
			calls++

			return sentinel
		})

		test.ErrorIs(t, err, sentinel)
		test.EqOp(t, 1, calls)
	})

	T.Run("re-runs a deadlock until it succeeds", func(t *testing.T) {
		t.Parallel()

		var calls int

		r := &Retrier{Attempts: 5}

		err := r.Do(t.Context(), "writing", func() error {
			calls++
			if calls == 1 {
				return deadlock
			}

			return nil
		})

		must.NoError(t, err)
		test.EqOp(t, 2, calls)
	})

	// Attempts is a ceiling, not a suggestion — a deadlock that never clears has
	// to stop rather than spin.
	T.Run("gives up at the attempt ceiling and returns the last failure", func(t *testing.T) {
		t.Parallel()

		var calls int

		r := &Retrier{Attempts: 3}

		err := r.Do(t.Context(), "writing", func() error {
			calls++

			return deadlock
		})

		test.ErrorIs(t, err, deadlock)
		test.EqOp(t, 3, calls)
	})

	// The attempt number is the loop's counter made observable, and the log line
	// is the only place it surfaces: a reader of a retry line uses it to tell a
	// first retry from a fifth, and a queue that has started contending from one
	// that deadlocked once. Counting the calls alone cannot see it — a counter
	// that ran backwards would produce the same three calls and label them 1, 0.
	T.Run("names the attempt it is retrying, and counts one retry per re-run", func(t *testing.T) {
		t.Parallel()

		var calls int

		logger := newRecordingLogger()
		counter := &metricsmock.Int64CounterMock{AddFunc: func(context.Context, int64, ...metric.AddOption) {}}

		r := &Retrier{
			Logger:     logger,
			Counter:    counter,
			AttemptKey: "work_queue.attempt",
			Subject:    "work queue",
			Attempts:   3,
		}

		err := r.Do(t.Context(), "writing", func() error {
			calls++

			return deadlock
		})

		test.ErrorIs(t, err, deadlock)
		test.EqOp(t, 3, calls)

		// Two retries for three attempts, labeled with the attempt that had
		// just failed rather than with the one about to run.
		test.Eq(t, []any{uint(1), uint(2)}, logger.valuesFor("work_queue.attempt"))
		test.Eq(t, []any{"writing", "writing"}, logger.valuesFor("operation"))
		test.SliceLen(t, 2, counter.AddCalls())
	})

	// The zero value has to be usable rather than panic on the absent logger and
	// counter, because "no attempt budget was configured" is a legible state.
	T.Run("the zero value runs the write once and retries nothing", func(t *testing.T) {
		t.Parallel()

		var calls int

		var r Retrier

		err := r.Do(t.Context(), "writing", func() error {
			calls++

			return deadlock
		})

		test.ErrorIs(t, err, deadlock)
		test.EqOp(t, 1, calls)
	})
}

// recordingLogger keeps the values it was handed, in the order they arrived, so
// a test can assert what a retry line said rather than only that one was
// written. Only WithValues is recorded, because that is the only way this
// package attaches anything.
type recordingLogger struct {
	values []map[string]any
	mu     sync.Mutex
}

func newRecordingLogger() *recordingLogger { return &recordingLogger{} }

// valuesFor returns what each recorded line carried under key, in order.
func (l *recordingLogger) valuesFor(key string) []any {
	l.mu.Lock()
	defer l.mu.Unlock()

	found := make([]any, 0, len(l.values))
	for _, values := range l.values {
		if value, ok := values[key]; ok {
			found = append(found, value)
		}
	}

	return found
}

func (l *recordingLogger) WithValues(values map[string]any) logging.Logger {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.values = append(l.values, maps.Clone(values))

	return l
}

func (l *recordingLogger) Info(string)                                {}
func (l *recordingLogger) Debug(string)                               {}
func (l *recordingLogger) Warn(string)                                {}
func (l *recordingLogger) Error(string, error)                        {}
func (l *recordingLogger) SetRequestIDFunc(logging.RequestIDFunc)     {}
func (l *recordingLogger) Clone() logging.Logger                      { return l }
func (l *recordingLogger) WithName(string) logging.Logger             { return l }
func (l *recordingLogger) WithValue(string, any) logging.Logger       { return l }
func (l *recordingLogger) WithRequest(*http.Request) logging.Logger   { return l }
func (l *recordingLogger) WithResponse(*http.Response) logging.Logger { return l }
func (l *recordingLogger) WithError(error) logging.Logger             { return l }
func (l *recordingLogger) WithSpan(trace.Span) logging.Logger         { return l }

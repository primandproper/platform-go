package sqlguard

import (
	"maps"
	"net/http"
	"sync"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/trace"
)

var errNotFound = platformerrors.New("thing not found")

// testGuard is the guard these tests exercise: everything named, so that a
// missing field shows up as a difference rather than as silence.
func testGuard() *Guard {
	return &Guard{
		NotFound:  errNotFound,
		Namespace: "things",
		IDKey:     "things.id",
		Message:   "thing left the active set before its outcome could be recorded",
		Reason:    "thing %q is no longer active",
	}
}

// beginOp starts an Operation against no configured pillars, which is the
// no-op path every one of these assertions runs through.
func beginOp(t *testing.T) observability.Operation {
	t.Helper()

	return beginOpWith(t, nil)
}

// beginOpWith starts an Operation logging to logger, for the assertions about
// what a missed guard writes.
func beginOpWith(t *testing.T, logger logging.Logger) observability.Operation {
	t.Helper()

	_, op := observability.NewObserver("sqlguard_test", logger, nil).Begin(t.Context())
	t.Cleanup(op.End)

	return op
}

// line is one logged message and the values the logger carried when it was
// written.
type line struct {
	values  map[string]any
	message string
}

// recordingLogger keeps what it was told. Derived loggers share the parent's
// slice — Operation.Set replaces its logger with a derived one on every value it
// records, and what a test wants to see is the line that eventually reached the
// root.
type recordingLogger struct {
	lines  *[]line
	values map[string]any
	mu     *sync.Mutex
}

func newRecordingLogger() *recordingLogger {
	return &recordingLogger{lines: &[]line{}, values: map[string]any{}, mu: &sync.Mutex{}}
}

func (l *recordingLogger) recorded() []line {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]line(nil), *l.lines...)
}

func (l *recordingLogger) with(values map[string]any) logging.Logger {
	merged := make(map[string]any, len(l.values)+len(values))
	maps.Copy(merged, l.values)
	maps.Copy(merged, values)

	return &recordingLogger{lines: l.lines, values: merged, mu: l.mu}
}

func (l *recordingLogger) Info(message string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	*l.lines = append(*l.lines, line{message: message, values: maps.Clone(l.values)})
}

func (l *recordingLogger) WithValue(key string, value any) logging.Logger {
	return l.with(map[string]any{key: value})
}

func (l *recordingLogger) WithValues(values map[string]any) logging.Logger { return l.with(values) }

func (l *recordingLogger) Debug(string)                               {}
func (l *recordingLogger) Warn(string)                                {}
func (l *recordingLogger) Error(string, error)                        {}
func (l *recordingLogger) SetRequestIDFunc(logging.RequestIDFunc)     {}
func (l *recordingLogger) Clone() logging.Logger                      { return l.with(nil) }
func (l *recordingLogger) WithName(string) logging.Logger             { return l.with(nil) }
func (l *recordingLogger) WithRequest(*http.Request) logging.Logger   { return l.with(nil) }
func (l *recordingLogger) WithResponse(*http.Response) logging.Logger { return l.with(nil) }
func (l *recordingLogger) WithError(error) logging.Logger             { return l.with(nil) }
func (l *recordingLogger) WithSpan(trace.Span) logging.Logger         { return l.with(nil) }

func TestGuard_Exec(T *testing.T) {
	T.Parallel()

	const query = "UPDATE things SET state = 'done' WHERE id = :1 AND state = 'running'"

	T.Run("a guard that matched a row succeeds", func(t *testing.T) {
		t.Parallel()

		db, mock, dbErr := sqlmock.New()
		must.NoError(t, dbErr)

		t.Cleanup(func() { _ = db.Close() })

		mock.ExpectExec(query).WillReturnResult(sqlmock.NewResult(0, 1))

		test.NoError(t, testGuard().Exec(t.Context(), beginOp(t), db, query, nil, "id-1", "finish", "finishing thing"))
	})

	// The whole reason the guard is in the statement: zero rows is not an error
	// the driver reports, and treating it as success has the caller report an
	// outcome the database never recorded.
	T.Run("a guard that matched nothing is the sentinel", func(t *testing.T) {
		t.Parallel()

		db, mock, dbErr := sqlmock.New()
		must.NoError(t, dbErr)

		t.Cleanup(func() { _ = db.Close() })

		mock.ExpectExec(query).WillReturnResult(sqlmock.NewResult(0, 0))

		err := testGuard().Exec(t.Context(), beginOp(t), db, query, nil, "id-1", "finish", "finishing thing")

		test.ErrorIs(t, err, errNotFound)
		test.StrContains(t, err.Error(), `thing "id-1" is no longer active`)
	})

	T.Run("reports what the driver said", func(t *testing.T) {
		t.Parallel()

		sentinel := platformerrors.New("connection refused")

		db, mock, dbErr := sqlmock.New()
		must.NoError(t, dbErr)

		t.Cleanup(func() { _ = db.Close() })

		mock.ExpectExec(query).WillReturnError(sentinel)

		err := testGuard().Exec(t.Context(), beginOp(t), db, query, nil, "id-1", "finish", "finishing thing")

		test.ErrorIs(t, err, sentinel)
	})

	T.Run("reports a driver that could not count the rows it changed", func(t *testing.T) {
		t.Parallel()

		sentinel := platformerrors.New("rows affected unsupported")

		db, mock, dbErr := sqlmock.New()
		must.NoError(t, dbErr)

		t.Cleanup(func() { _ = db.Close() })

		mock.ExpectExec(query).WillReturnResult(sqlmock.NewErrorResult(sentinel))

		err := testGuard().Exec(t.Context(), beginOp(t), db, query, nil, "id-1", "finish", "finishing thing")

		test.ErrorIs(t, err, sentinel)
	})

	// The identifier is the whole value of the line. "a thing left the active
	// set" with nothing naming which thing is a line nobody can act on, and the
	// work it reports has already run — the export is written, the charge is
	// posted — so the row it names is where the reconciliation starts.
	T.Run("a missed guard names the row it could not find", func(t *testing.T) {
		t.Parallel()

		db, mock, dbErr := sqlmock.New()
		must.NoError(t, dbErr)

		t.Cleanup(func() { _ = db.Close() })

		mock.ExpectExec(query).WillReturnResult(sqlmock.NewResult(0, 0))

		logger := newRecordingLogger()

		must.Error(t, testGuard().Exec(t.Context(), beginOpWith(t, logger), db, query, nil, "id-1", "finish", "finishing thing"))

		lines := logger.recorded()
		must.SliceLen(t, 1, lines)
		test.EqOp(t, "thing left the active set before its outcome could be recorded", lines[0].message)
		test.EqOp(t, "id-1", lines[0].values["things.id"])
	})

	// The other half of that guard: a guard that is not keyed by an identifier
	// says nothing rather than logging an empty one, which would read as a row
	// whose ID is the empty string.
	T.Run("a guard with no ID key logs no identifier", func(t *testing.T) {
		t.Parallel()

		db, mock, dbErr := sqlmock.New()
		must.NoError(t, dbErr)

		t.Cleanup(func() { _ = db.Close() })

		mock.ExpectExec(query).WillReturnResult(sqlmock.NewResult(0, 0))

		g := testGuard()
		g.IDKey = ""

		logger := newRecordingLogger()

		must.Error(t, g.Exec(t.Context(), beginOpWith(t, logger), db, query, nil, "id-1", "finish", "finishing thing"))

		lines := logger.recorded()
		must.SliceLen(t, 1, lines)
		test.MapNotContainsKey(t, lines[0].values, "things.id")
	})

	// A caller that has no sentinel to offer still gets a legible error rather
	// than a nil one that reads as success.
	T.Run("a guard with no sentinel returns its reason alone", func(t *testing.T) {
		t.Parallel()

		db, mock, dbErr := sqlmock.New()
		must.NoError(t, dbErr)

		t.Cleanup(func() { _ = db.Close() })

		mock.ExpectExec(query).WillReturnResult(sqlmock.NewResult(0, 0))

		g := testGuard()
		g.NotFound = nil

		err := g.Exec(t.Context(), beginOp(t), db, query, nil, "id-1", "finish", "finishing thing")

		must.Error(t, err)
		test.StrContains(t, err.Error(), `thing "id-1" is no longer active`)
	})
}

func TestGuard_OpAttr(T *testing.T) {
	T.Parallel()

	// The attribute name is derived from the namespace rather than configured,
	// so two packages cannot spell the same fact differently and land in series
	// nothing groups together.
	T.Run("names the attribute after the namespace", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, (&Guard{Namespace: "things"}).OpAttr("finish"))
	})
}

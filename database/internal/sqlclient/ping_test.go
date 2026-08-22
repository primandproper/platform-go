package sqlclient

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/trace"
)

// valueLogger records every WithValue key and value written through it. Every
// derivation returns the same recorder, so values attached by a derived logger
// are recorded too.
type valueLogger struct {
	logging.Logger
	values []any
	mu     sync.Mutex
}

func newValueLogger() *valueLogger {
	return &valueLogger{Logger: loggingnoop.NewLogger()}
}

func (l *valueLogger) Clone() logging.Logger                      { return l }
func (l *valueLogger) WithName(string) logging.Logger             { return l }
func (l *valueLogger) WithRequest(*http.Request) logging.Logger   { return l }
func (l *valueLogger) WithResponse(*http.Response) logging.Logger { return l }
func (l *valueLogger) WithError(error) logging.Logger             { return l }
func (l *valueLogger) WithSpan(trace.Span) logging.Logger         { return l }

func (l *valueLogger) WithValue(key string, value any) logging.Logger {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.values = append(l.values, key, value)

	return l
}

func (l *valueLogger) WithValues(values map[string]any) logging.Logger {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, v := range values {
		l.values = append(l.values, k, v)
	}

	return l
}

func (l *valueLogger) snapshot() []any {
	l.mu.Lock()
	defer l.mu.Unlock()

	return slices.Clone(l.values)
}

// unpingableConnector never yields a connection, so PingContext always fails
// and WaitForPing exhausts its attempts.
type unpingableConnector struct{}

func (unpingableConnector) Connect(context.Context) (driver.Conn, error) {
	return nil, driver.ErrBadConn
}

func (unpingableConnector) Driver() driver.Driver { return nil }

// dsn is the shape of the value that must not reach a log line: a connection
// string carrying a password.
const dsn = "postgres://svc:hunter2@db.internal:5432/app?sslmode=require"

func TestWaitForPing(T *testing.T) {
	T.Parallel()

	T.Run("labels the connection by role, never by connection string", func(t *testing.T) {
		t.Parallel()

		logger := newValueLogger()
		o11y := observability.NewObserver("test", logger, nil)
		ctx, op := o11y.Begin(t.Context())
		defer op.End()

		db := sql.OpenDB(unpingableConnector{})
		t.Cleanup(func() { _ = db.Close() })

		test.False(t, WaitForPing(ctx, op, db, "read", 1, time.Millisecond))

		values := logger.snapshot()
		must.SliceContains(t, values, any("connection"))
		must.SliceContains(t, values, any("read"))

		// The regression this guards: a probe that fails repeatedly is exactly
		// the situation that would fill a log with its own credential.
		test.SliceNotContains(t, values, any(dsn))
	})

	T.Run("stops early when the context is done", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())
		o11y := observability.NewObserver("test", nil, nil)
		ctx, op := o11y.Begin(ctx)
		defer op.End()

		db := sql.OpenDB(unpingableConnector{})
		t.Cleanup(func() { _ = db.Close() })

		cancel()

		// A canceled context must not sleep through the remaining attempts.
		start := time.Now()
		test.False(t, WaitForPing(ctx, op, db, "write", 5, time.Hour))
		test.Less(t, time.Minute, time.Since(start))
	})

	T.Run("does not sleep after the final attempt", func(t *testing.T) {
		t.Parallel()

		o11y := observability.NewObserver("test", nil, nil)
		ctx, op := o11y.Begin(t.Context())
		defer op.End()

		db := sql.OpenDB(unpingableConnector{})
		t.Cleanup(func() { _ = db.Close() })

		start := time.Now()
		test.False(t, WaitForPing(ctx, op, db, "read", 1, time.Hour))
		test.Less(t, time.Minute, time.Since(start))
	})
}

func TestNow(T *testing.T) {
	T.Parallel()

	T.Run("falls back to the wall clock", func(t *testing.T) {
		t.Parallel()

		test.False(t, Now(nil).IsZero())
	})

	T.Run("reads an injected clock", func(t *testing.T) {
		t.Parallel()

		fixed := time.Date(2026, time.August, 10, 0, 0, 0, 0, time.UTC)

		test.EqOp(t, fixed, Now(func() time.Time { return fixed }))
	})
}

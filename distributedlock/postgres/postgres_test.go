package postgres

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v5/circuitbreaking"
	cbmock "github.com/primandproper/platform-go/v5/circuitbreaking/mock"
	cbnoop "github.com/primandproper/platform-go/v5/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v5/database"
	"github.com/primandproper/platform-go/v5/distributedlock"
	"github.com/primandproper/platform-go/v5/identifiers"
	"github.com/primandproper/platform-go/v5/observability"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	metricsnoop "github.com/primandproper/platform-go/v5/observability/metrics/noop"
	"github.com/primandproper/platform-go/v5/testutils/containers"

	"github.com/DATA-DOG/go-sqlmock"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"github.com/testcontainers/testcontainers-go"
	postgrescontainer "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.opentelemetry.io/otel/metric"
)

const postgresImage = "postgres:17-alpine"

// testDBClient is a minimal database.Client backed by a single *sql.DB. It exists
// only to avoid pulling in database/postgres for tests in this leaf package.
type testDBClient struct {
	db *sql.DB
}

func (c *testDBClient) WriteDB() *sql.DB { return c.db }
func (c *testDBClient) ReadDB() *sql.DB  { return c.db }
func (c *testDBClient) Close() error     { return c.db.Close() }
func (c *testDBClient) CurrentTime() time.Time {
	return time.Now()
}
func (c *testDBClient) RollbackTransaction(_ context.Context, tx database.SQLQueryExecutorAndTransactionManager) {
	_ = tx.Rollback()
}

func buildContainerBackedPostgres(t *testing.T) (client *testDBClient, shutdown func(context.Context) error) {
	t.Helper()

	ctx := t.Context()
	container, err := containers.StartWithRetry(ctx, func(ctx context.Context) (*postgrescontainer.PostgresContainer, error) {
		return postgrescontainer.Run(
			ctx,
			postgresImage,
			postgrescontainer.WithDatabase("locktest"),
			postgrescontainer.WithUsername("locktest"),
			postgrescontainer.WithPassword("locktest"),
			testcontainers.WithWaitStrategyAndDeadline(2*time.Minute, wait.ForLog("database system is ready to accept connections").WithOccurrence(2)),
		)
	})
	must.NoError(t, err)
	must.NotNil(t, container)

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	must.NoError(t, err)

	db, err := sql.Open("pgx", connStr)
	must.NoError(t, err)
	// Allow plenty of conns so the parallel subtests don't starve.
	db.SetMaxOpenConns(64)
	must.NoError(t, db.PingContext(ctx))

	return &testDBClient{db: db}, func(ctx context.Context) error {
		_ = db.Close()
		return container.Terminate(ctx)
	}
}

func newTestLocker(t *testing.T, client database.Client) distributedlock.Locker {
	t.Helper()
	l, err := NewPostgresLocker(&Config{}, client, nil, nil, nil, cbnoop.NewCircuitBreaker())
	must.NoError(t, err)
	must.NotNil(t, l)
	return l
}

// --------- unit tests (no container) ---------

// errorAtCallProvider wraps a noop metrics provider but injects errors at a
// specific Int64Counter call index or on the Float64Histogram call. It exists
// so the constructor's metric-creation error branches can be exercised.
type errorAtCallProvider struct {
	metrics.Provider
	errOnInt64Counter     int
	int64CallCount        int
	errOnFloat64Histogram bool
}

func newErrorAtCallProvider(int64FailIdx int, histFail bool) *errorAtCallProvider {
	return &errorAtCallProvider{
		Provider:              metricsnoop.NewMetricsProvider(),
		errOnInt64Counter:     int64FailIdx,
		errOnFloat64Histogram: histFail,
	}
}

func (p *errorAtCallProvider) NewInt64Counter(name string, options ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
	p.int64CallCount++
	if p.errOnInt64Counter == p.int64CallCount {
		return nil, errors.New("simulated counter error")
	}
	return p.Provider.NewInt64Counter(name, options...)
}

func (p *errorAtCallProvider) NewFloat64Histogram(name string, options ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
	if p.errOnFloat64Histogram {
		return nil, errors.New("simulated histogram error")
	}
	return p.Provider.NewFloat64Histogram(name, options...)
}

// buildSqlmockClient builds a testDBClient backed by go-sqlmock so unit tests
// can drive the locker without spinning up a real postgres.
func buildSqlmockClient(t *testing.T) (*testDBClient, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	must.NoError(t, err)
	return &testDBClient{db: db}, mock
}

func newTestLockerWithCB(t *testing.T, client database.Client, cb circuitbreaking.CircuitBreaker) distributedlock.Locker {
	t.Helper()
	l, err := NewPostgresLocker(&Config{}, client, nil, nil, nil, cb)
	must.NoError(t, err)
	must.NotNil(t, l)
	return l
}

// newRecordingLocker builds a locker with a RecordingObserver swapped in, so a
// test can both drive it and assert which operations it observed.
func newRecordingLocker(t *testing.T, client database.Client) (*locker, *observability.RecordingObserver) {
	t.Helper()
	l, err := NewPostgresLocker(&Config{}, client, nil, nil, nil, cbnoop.NewCircuitBreaker())
	must.NoError(t, err)
	must.NotNil(t, l)

	concrete, ok := l.(*locker)
	must.True(t, ok)

	obs := observability.NewRecordingObserver()
	concrete.o11y = obs

	return concrete, obs
}

func TestNewPostgresLocker(T *testing.T) {
	T.Parallel()

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()
		_, err := NewPostgresLocker(nil, &testDBClient{}, nil, nil, nil, cbnoop.NewCircuitBreaker())
		must.ErrorIs(t, err, distributedlock.ErrNilConfig)
	})

	T.Run("nil database", func(t *testing.T) {
		t.Parallel()
		_, err := NewPostgresLocker(&Config{}, nil, nil, nil, nil, cbnoop.NewCircuitBreaker())
		must.ErrorIs(t, err, distributedlock.ErrNilDatabaseClient)
	})

	T.Run("standard happy path", func(t *testing.T) {
		t.Parallel()
		client, _ := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		l, err := NewPostgresLocker(&Config{Namespace: 7}, client, nil, nil, nil, cbnoop.NewCircuitBreaker())
		must.NoError(t, err)
		must.NotNil(t, l)
	})

	// Each Int64Counter creation has its own error branch; exercise them all so
	// no error path is left untested.
	for idx := 1; idx <= 5; idx++ {
		T.Run("int64 counter creation failure", func(t *testing.T) {
			t.Parallel()
			client, _ := buildSqlmockClient(t)
			t.Cleanup(func() { _ = client.Close() })
			mp := newErrorAtCallProvider(idx, false)
			_, err := NewPostgresLocker(&Config{}, client, nil, nil, mp, cbnoop.NewCircuitBreaker())
			must.Error(t, err)
		})
	}

	T.Run("float64 histogram creation failure", func(t *testing.T) {
		t.Parallel()
		client, _ := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		mp := newErrorAtCallProvider(0, true)
		_, err := NewPostgresLocker(&Config{}, client, nil, nil, mp, cbnoop.NewCircuitBreaker())
		must.Error(t, err)
	})
}

func TestLocker_Acquire_Unit(T *testing.T) {
	T.Parallel()

	T.Run("happy path", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		l := newTestLocker(t, client)

		mock.ExpectQuery(`SELECT pg_try_advisory_lock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))

		got, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
		must.NotNil(t, got)
		test.EqOp(t, "k", got.Key())
		test.EqOp(t, time.Minute, got.TTL())
		must.NoError(t, mock.ExpectationsWereMet())
	})

	T.Run("rejects empty key", func(t *testing.T) {
		t.Parallel()
		client, _ := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		l := newTestLocker(t, client)
		_, err := l.Acquire(t.Context(), "", time.Minute)
		must.ErrorIs(t, err, distributedlock.ErrEmptyKey)
	})

	T.Run("rejects zero TTL", func(t *testing.T) {
		t.Parallel()
		client, _ := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		l := newTestLocker(t, client)
		_, err := l.Acquire(t.Context(), "k", 0)
		must.ErrorIs(t, err, distributedlock.ErrInvalidTTL)
	})

	T.Run("rejects negative TTL", func(t *testing.T) {
		t.Parallel()
		client, _ := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		l := newTestLocker(t, client)
		_, err := l.Acquire(t.Context(), "k", -time.Second)
		must.ErrorIs(t, err, distributedlock.ErrInvalidTTL)
	})

	T.Run("blocked by circuit breaker", func(t *testing.T) {
		t.Parallel()
		client, _ := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		cb := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return true },
		}
		l := newTestLockerWithCB(t, client, cb)
		_, err := l.Acquire(t.Context(), "k", time.Minute)
		must.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		must.SliceNotEmpty(t, cb.CannotProceedCalls())
	})

	T.Run("Conn reservation failure", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		mock.ExpectClose()
		l := newTestLocker(t, client)
		// Close the underlying DB so Conn() returns an error.
		must.NoError(t, client.Close())

		_, err := l.Acquire(t.Context(), "k", time.Minute)
		must.Error(t, err)
	})

	T.Run("pg_try_advisory_lock query failure", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		l := newTestLocker(t, client)

		mock.ExpectQuery(`SELECT pg_try_advisory_lock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnError(errors.New("query boom"))

		_, err := l.Acquire(t.Context(), "k", time.Minute)
		must.Error(t, err)
		must.NoError(t, mock.ExpectationsWereMet())
	})

	T.Run("contention returns ErrLockNotAcquired", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		l := newTestLocker(t, client)

		mock.ExpectQuery(`SELECT pg_try_advisory_lock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(false))

		_, err := l.Acquire(t.Context(), "k", time.Minute)
		must.ErrorIs(t, err, distributedlock.ErrLockNotAcquired)
		must.NoError(t, mock.ExpectationsWereMet())
	})

	T.Run("observes the operation on the happy path", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		l, obs := newRecordingLocker(t, client)

		mock.ExpectQuery(`SELECT pg_try_advisory_lock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))

		got, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
		must.NotNil(t, got)

		op := obs.ObservedOperationWithData(t, map[string]any{})
		must.SliceEmpty(t, op.Errors)
	})

	T.Run("records the error when conn reservation fails", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		mock.ExpectClose()
		l, obs := newRecordingLocker(t, client)
		// Close the underlying DB so Conn() returns an error.
		must.NoError(t, client.Close())

		_, err := l.Acquire(t.Context(), "k", time.Minute)
		must.Error(t, err)

		// Even though the acquire failed, the operation must have been observed,
		// and the failure itself recorded on it.
		op := obs.ObservedOperationWithData(t, map[string]any{})
		must.SliceLen(t, 1, op.Errors)
	})
}

func TestLocker_Release_Unit(T *testing.T) {
	T.Parallel()

	T.Run("happy path", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		l := newTestLocker(t, client)

		mock.ExpectQuery(`SELECT pg_try_advisory_lock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))
		mock.ExpectQuery(`SELECT pg_advisory_unlock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))

		h, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
		must.NoError(t, h.Release(t.Context()))
		must.NoError(t, mock.ExpectationsWereMet())
	})

	T.Run("unlock reporting false surfaces an error", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		l := newTestLocker(t, client)

		mock.ExpectQuery(`SELECT pg_try_advisory_lock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))
		// pg_advisory_unlock returns false: the session did not hold the lock, which
		// must not be reported as a clean release.
		mock.ExpectQuery(`SELECT pg_advisory_unlock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(false))

		h, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
		test.Error(t, h.Release(t.Context()))
	})

	T.Run("blocked by circuit breaker", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		var cannotProceedCalls int
		cb := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool {
				cannotProceedCalls++
				return cannotProceedCalls > 1 // first call (Acquire) proceeds, second (Release) is blocked
			},
			SucceededFunc: func() {},
		}
		l := newTestLockerWithCB(t, client, cb)

		mock.ExpectQuery(`SELECT pg_try_advisory_lock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))

		h, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
		must.ErrorIs(t, h.Release(t.Context()), circuitbreaking.ErrCircuitBroken)
		must.SliceLen(t, 2, cb.CannotProceedCalls())
		must.SliceLen(t, 1, cb.SucceededCalls())
	})

	T.Run("double release returns ErrLockNotHeld", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		l := newTestLocker(t, client)

		mock.ExpectQuery(`SELECT pg_try_advisory_lock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))
		mock.ExpectQuery(`SELECT pg_advisory_unlock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))

		h, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
		must.NoError(t, h.Release(t.Context()))
		must.ErrorIs(t, h.Release(t.Context()), distributedlock.ErrLockNotHeld)
	})

	T.Run("releaseLocked deferred conn close error tolerated", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		l := newTestLocker(t, client)

		mock.ExpectQuery(`SELECT pg_try_advisory_lock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))

		h, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)

		// Force the deferred conn.Close inside releaseLocked to fail by closing
		// the conn here first. The QueryRowContext on the already-closed conn
		// will also fail (covered by the SQL failure subtest below); the value
		// of this case is exercising the deferred Close error branch.
		inner := h.(*lock)
		must.NoError(t, inner.conn.Close())

		must.Error(t, h.Release(t.Context()))
	})

	T.Run("releaseLocked SQL failure trips breaker", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		cb := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
			FailedFunc:        func() {},
		}
		l := newTestLockerWithCB(t, client, cb)

		mock.ExpectQuery(`SELECT pg_try_advisory_lock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))
		mock.ExpectQuery(`SELECT pg_advisory_unlock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnError(errors.New("unlock boom"))

		h, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
		must.Error(t, h.Release(t.Context()))
		must.SliceLen(t, 1, cb.SucceededCalls())
		must.SliceLen(t, 1, cb.FailedCalls())
	})

	T.Run("release with canceled ctx fails and discards the conn", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		l := newTestLocker(t, client)

		mock.ExpectQuery(`SELECT pg_try_advisory_lock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))

		h, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)

		// An already-canceled ctx makes database/sql return before the unlock touches the
		// wire — the exact case (C-14) where the session could keep holding the advisory
		// lock. Release must surface the error; the conn is force-discarded so the lock
		// isn't leaked back into the pool.
		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		must.Error(t, h.Release(ctx))
	})
}

func TestLocker_Refresh_Unit(T *testing.T) {
	T.Parallel()

	T.Run("happy path updates TTL", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		l := newTestLocker(t, client)

		mock.ExpectQuery(`SELECT pg_try_advisory_lock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))
		mock.ExpectQuery(`SELECT 1`).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(1))

		h, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
		must.NoError(t, h.Refresh(t.Context(), 5*time.Minute))
		test.EqOp(t, 5*time.Minute, h.TTL())
	})

	T.Run("rejects zero TTL", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		l := newTestLocker(t, client)

		mock.ExpectQuery(`SELECT pg_try_advisory_lock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))

		h, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
		must.ErrorIs(t, h.Refresh(t.Context(), 0), distributedlock.ErrInvalidTTL)
		test.EqOp(t, time.Minute, h.TTL())
	})

	T.Run("blocked by circuit breaker", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		var cannotProceedCalls int
		cb := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool {
				cannotProceedCalls++
				return cannotProceedCalls > 1 // first call (Acquire) proceeds, second (Refresh) is blocked
			},
			SucceededFunc: func() {},
		}
		l := newTestLockerWithCB(t, client, cb)

		mock.ExpectQuery(`SELECT pg_try_advisory_lock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))

		h, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
		must.ErrorIs(t, h.Refresh(t.Context(), time.Minute), circuitbreaking.ErrCircuitBroken)
		must.SliceLen(t, 2, cb.CannotProceedCalls())
		must.SliceLen(t, 1, cb.SucceededCalls())
	})

	T.Run("refresh after release returns ErrLockNotHeld", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		l := newTestLocker(t, client)

		mock.ExpectQuery(`SELECT pg_try_advisory_lock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))
		mock.ExpectQuery(`SELECT pg_advisory_unlock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))

		h, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
		must.NoError(t, h.Release(t.Context()))
		must.ErrorIs(t, h.Refresh(t.Context(), time.Minute), distributedlock.ErrLockNotHeld)
	})

	T.Run("liveness check failure returns ErrLockNotHeld", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		l := newTestLocker(t, client)

		mock.ExpectQuery(`SELECT pg_try_advisory_lock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))
		mock.ExpectQuery(`SELECT 1`).
			WillReturnError(errors.New("conn dead"))

		h, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)
		must.ErrorIs(t, h.Refresh(t.Context(), 5*time.Minute), distributedlock.ErrLockNotHeld)
		// TTL must remain unchanged on failure.
		test.EqOp(t, time.Minute, h.TTL())
	})
}

func TestLocker_PingClose_Unit(T *testing.T) {
	T.Parallel()

	T.Run("ping success", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		l := newTestLocker(t, client)

		mock.ExpectPing()
		must.NoError(t, l.Ping(t.Context()))
		must.NoError(t, mock.ExpectationsWereMet())
	})

	T.Run("ping error", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		l := newTestLocker(t, client)

		mock.ExpectPing().WillReturnError(errors.New("ping boom"))
		must.Error(t, l.Ping(t.Context()))
	})

	T.Run("close with no outstanding locks", func(t *testing.T) {
		t.Parallel()
		client, _ := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		l := newTestLocker(t, client)
		must.NoError(t, l.Close())
	})

	T.Run("close releases all outstanding locks", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		l := newTestLocker(t, client)

		mock.ExpectQuery(`SELECT pg_try_advisory_lock`).
			WithArgs(hashLockID(0, "a")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))
		mock.ExpectQuery(`SELECT pg_advisory_unlock`).
			WithArgs(hashLockID(0, "a")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))

		_, err := l.Acquire(t.Context(), "a", time.Minute)
		must.NoError(t, err)
		must.NoError(t, l.Close())
		must.NoError(t, mock.ExpectationsWereMet())
	})

	T.Run("close surfaces release errors", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		l := newTestLocker(t, client)

		mock.ExpectQuery(`SELECT pg_try_advisory_lock`).
			WithArgs(hashLockID(0, "a")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))
		mock.ExpectQuery(`SELECT pg_advisory_unlock`).
			WithArgs(hashLockID(0, "a")).
			WillReturnError(errors.New("unlock boom"))

		_, err := l.Acquire(t.Context(), "a", time.Minute)
		must.NoError(t, err)
		must.Error(t, l.Close())
	})
}

func TestHashLockID(T *testing.T) {
	T.Parallel()

	T.Run("stable across calls", func(t *testing.T) {
		t.Parallel()
		test.EqOp(t, hashLockID(0, "k"), hashLockID(0, "k"))
	})

	T.Run("namespace changes the result", func(t *testing.T) {
		t.Parallel()
		test.NotEq(t, hashLockID(0, "k"), hashLockID(1, "k"))
	})

	T.Run("different keys produce different ids", func(t *testing.T) {
		t.Parallel()
		test.NotEq(t, hashLockID(0, "a"), hashLockID(0, "b"))
	})
}

func TestLocker_TTLExpiry_Unit(T *testing.T) {
	T.Parallel()

	T.Run("refresh after TTL expiry returns ErrLockNotHeld and frees the lock", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		l := newTestLocker(t, client)

		mock.ExpectQuery(`SELECT pg_try_advisory_lock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))
		// Expiry is detected before the SELECT 1 liveness probe, so the handle frees
		// the advisory lock instead of pinging.
		mock.ExpectQuery(`SELECT pg_advisory_unlock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))

		h, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)

		// Force the TTL to have elapsed.
		h.(*lock).expiresAt = time.Now().Add(-time.Second)

		must.ErrorIs(t, h.Refresh(t.Context(), 5*time.Minute), distributedlock.ErrLockNotHeld)
		must.NoError(t, mock.ExpectationsWereMet())
		// TTL bookkeeping is untouched on a failed refresh.
		test.EqOp(t, time.Minute, h.TTL())

		// The lock was dropped, so a later release just sees it gone.
		must.ErrorIs(t, h.Release(t.Context()), distributedlock.ErrLockNotHeld)
	})

	T.Run("release after TTL expiry returns ErrLockNotHeld but frees the conn", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		l := newTestLocker(t, client)

		mock.ExpectQuery(`SELECT pg_try_advisory_lock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))
		mock.ExpectQuery(`SELECT pg_advisory_unlock`).
			WithArgs(hashLockID(0, "k")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))

		h, err := l.Acquire(t.Context(), "k", time.Minute)
		must.NoError(t, err)

		h.(*lock).expiresAt = time.Now().Add(-time.Second)

		must.ErrorIs(t, h.Release(t.Context()), distributedlock.ErrLockNotHeld)
		must.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestLocker_Acquire_PoolSaturation_Unit(T *testing.T) {
	T.Parallel()

	T.Run("returns ErrLockNotAcquired when the write pool is saturated", func(t *testing.T) {
		t.Parallel()
		client, mock := buildSqlmockClient(t)
		t.Cleanup(func() { _ = client.Close() })
		// Only one write connection exists; the first held lock pins it for its
		// whole lifetime, leaving none for the second Acquire.
		client.WriteDB().SetMaxOpenConns(1)

		l, err := NewPostgresLocker(&Config{ConnWaitTimeout: 50 * time.Millisecond}, client, nil, nil, nil, cbnoop.NewCircuitBreaker())
		must.NoError(t, err)

		mock.ExpectQuery(`SELECT pg_try_advisory_lock`).
			WithArgs(hashLockID(0, "a")).
			WillReturnRows(sqlmock.NewRows([]string{"v"}).AddRow(true))

		first, err := l.Acquire(t.Context(), "a", time.Minute)
		must.NoError(t, err)
		// first pins the pool's single conn; leave it held for the duration.
		t.Cleanup(func() { _ = first.Release(context.Background()) })

		// The second acquire cannot reserve a conn. It must fail fast with
		// ErrLockNotAcquired rather than block indefinitely in Conn().
		start := time.Now()
		_, err = l.Acquire(t.Context(), "b", time.Minute)
		must.ErrorIs(t, err, distributedlock.ErrLockNotAcquired)
		elapsed := time.Since(start)
		test.True(t, elapsed < 2*time.Second, test.Sprintf("expected fast failure, took %s", elapsed))
	})
}

// --------- container-backed integration tests ---------

func TestPostgresLocker_Container(T *testing.T) {
	T.Parallel()

	containers.SkipIfNotRunning(T)

	client, shutdown := buildContainerBackedPostgres(T)
	T.Cleanup(func() { _ = shutdown(context.Background()) })

	T.Run("Acquire happy path", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		l := newTestLocker(t, client)
		key := "happy_" + identifiers.New()

		lock, err := l.Acquire(ctx, key, time.Minute)
		must.NoError(t, err)
		must.NotNil(t, lock)
		test.EqOp(t, key, lock.Key())
		test.EqOp(t, time.Minute, lock.TTL())
		must.NoError(t, lock.Release(ctx))
	})

	T.Run("Acquire contended on the same locker returns ErrLockNotAcquired", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		l := newTestLocker(t, client)
		key := "contend_same_" + identifiers.New()

		first, err := l.Acquire(ctx, key, time.Minute)
		must.NoError(t, err)
		t.Cleanup(func() { _ = first.Release(ctx) })

		_, err = l.Acquire(ctx, key, time.Minute)
		must.ErrorIs(t, err, distributedlock.ErrLockNotAcquired)
	})

	T.Run("Acquire contended across separate lockers returns ErrLockNotAcquired", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		l1 := newTestLocker(t, client)
		l2 := newTestLocker(t, client)
		key := "contend_cross_" + identifiers.New()

		first, err := l1.Acquire(ctx, key, time.Minute)
		must.NoError(t, err)
		t.Cleanup(func() { _ = first.Release(ctx) })

		_, err = l2.Acquire(ctx, key, time.Minute)
		must.ErrorIs(t, err, distributedlock.ErrLockNotAcquired)
	})

	T.Run("Acquire rejects empty key", func(t *testing.T) {
		t.Parallel()
		l := newTestLocker(t, client)
		_, err := l.Acquire(t.Context(), "", time.Minute)
		must.ErrorIs(t, err, distributedlock.ErrEmptyKey)
	})

	T.Run("Acquire rejects zero TTL", func(t *testing.T) {
		t.Parallel()
		l := newTestLocker(t, client)
		_, err := l.Acquire(t.Context(), "k", 0)
		must.ErrorIs(t, err, distributedlock.ErrInvalidTTL)
	})

	T.Run("Released lock can be reacquired", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		l := newTestLocker(t, client)
		key := "reacquire_" + identifiers.New()

		first, err := l.Acquire(ctx, key, time.Minute)
		must.NoError(t, err)
		must.NoError(t, first.Release(ctx))

		second, err := l.Acquire(ctx, key, time.Minute)
		must.NoError(t, err)
		must.NoError(t, second.Release(ctx))
	})

	T.Run("Double release returns ErrLockNotHeld on second call", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		l := newTestLocker(t, client)
		key := "double_" + identifiers.New()

		lock, err := l.Acquire(ctx, key, time.Minute)
		must.NoError(t, err)
		must.NoError(t, lock.Release(ctx))
		must.ErrorIs(t, lock.Release(ctx), distributedlock.ErrLockNotHeld)
	})

	T.Run("Refresh succeeds and updates local TTL", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		l := newTestLocker(t, client)
		key := "refresh_" + identifiers.New()

		lock, err := l.Acquire(ctx, key, time.Minute)
		must.NoError(t, err)
		t.Cleanup(func() { _ = lock.Release(ctx) })

		must.NoError(t, lock.Refresh(ctx, 5*time.Minute))
		test.EqOp(t, 5*time.Minute, lock.TTL())
	})

	T.Run("Refresh after release returns ErrLockNotHeld", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		l := newTestLocker(t, client)
		key := "refresh_after_release_" + identifiers.New()

		lock, err := l.Acquire(ctx, key, time.Minute)
		must.NoError(t, err)
		must.NoError(t, lock.Release(ctx))

		must.ErrorIs(t, lock.Refresh(ctx, time.Minute), distributedlock.ErrLockNotHeld)
	})

	T.Run("Close releases all outstanding locks", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		l := newTestLocker(t, client)
		keyA := "close_a_" + identifiers.New()
		keyB := "close_b_" + identifiers.New()

		_, err := l.Acquire(ctx, keyA, time.Minute)
		must.NoError(t, err)
		_, err = l.Acquire(ctx, keyB, time.Minute)
		must.NoError(t, err)
		must.NoError(t, l.Close())

		// Both keys are acquirable again from a fresh locker.
		l2 := newTestLocker(t, client)
		t.Cleanup(func() { _ = l2.Close() })

		_, err = l2.Acquire(ctx, keyA, time.Minute)
		must.NoError(t, err)
		_, err = l2.Acquire(ctx, keyB, time.Minute)
		must.NoError(t, err)
	})

	T.Run("Ping success", func(t *testing.T) {
		t.Parallel()
		l := newTestLocker(t, client)
		must.NoError(t, l.Ping(t.Context()))
	})
}

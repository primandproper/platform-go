package postgres

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/observability"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// testClientConfig is a test implementation of database.ClientConfig.
type testClientConfig struct {
	readConnectionString  string
	writeConnectionString string
	connectionString      string
	maxPingAttempts       uint64
	pingWaitPeriod        time.Duration
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string {
	if c.readConnectionString != "" {
		return c.readConnectionString
	}
	return c.connectionString
}

func (c *testClientConfig) GetWriteConnectionString() string {
	if c.writeConnectionString != "" {
		return c.writeConnectionString
	}
	return c.connectionString
}

func (c *testClientConfig) GetMaxPingAttempts() uint64 {
	return c.maxPingAttempts
}

func (c *testClientConfig) GetPingWaitPeriod() time.Duration {
	return c.pingWaitPeriod
}

func (c *testClientConfig) GetMaxIdleConns() int {
	return 5
}

func (c *testClientConfig) GetMaxOpenConns() int {
	return 7
}

func (c *testClientConfig) GetConnMaxLifetime() time.Duration {
	return 30 * time.Minute
}

// loggingClientConfig adds the optional GetLogQueries capability that
// NewDatabaseClient discovers by interface assertion.
type loggingClientConfig struct {
	testClientConfig
	logQueries bool
}

func (c *loggingClientConfig) GetLogQueries() bool {
	return c.logQueries
}

func buildTestClient(t *testing.T) (*Client, sqlmock.Sqlmock) {
	t.Helper()

	fakeDB, sqlMock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	must.NoError(t, err)

	c := &Client{
		readDB:  fakeDB,
		writeDB: fakeDB,
		config: &testClientConfig{
			maxPingAttempts: 1,
			pingWaitPeriod:  time.Second,
		},
		o11y:     observability.NewObserverForTest("test"),
		timeFunc: time.Now,
	}

	return c, sqlMock
}

// end helper funcs

func TestQuerier_IsReady(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c, db := buildTestClient(t)
		c.config = &testClientConfig{pingWaitPeriod: time.Second, maxPingAttempts: 1}

		// same DB for read/write, so only one ping
		db.ExpectPing().WillDelayFor(0)

		test.True(t, c.IsReady(ctx))
	})

	T.Run("with read DB ping error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c, db := buildTestClient(t)
		c.config = &testClientConfig{pingWaitPeriod: time.Millisecond, maxPingAttempts: 1}

		db.ExpectPing().WillReturnError(errors.New("blah"))

		test.False(t, c.IsReady(ctx))
	})

	T.Run("with write DB ping error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		readDB, readMock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
		must.NoError(t, err)

		writeDB, writeMock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
		must.NoError(t, err)

		c := &Client{
			readDB:  readDB,
			writeDB: writeDB,
			config:  &testClientConfig{pingWaitPeriod: time.Millisecond, maxPingAttempts: 1},
			o11y:    observability.NewObserverForTest("test"),
		}

		readMock.ExpectPing().WillDelayFor(0)
		writeMock.ExpectPing().WillReturnError(errors.New("blah"))

		test.False(t, c.IsReady(ctx))
	})

	T.Run("exhausting all available queries", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()

		c, db := buildTestClient(t)
		c.config = &testClientConfig{pingWaitPeriod: time.Millisecond, maxPingAttempts: 1}

		db.ExpectPing().WillReturnError(errors.New("blah"))

		test.False(t, c.IsReady(ctx))
	})

	T.Run("a canceled context ends the retry wait", func(t *testing.T) {
		t.Parallel()

		c, db := buildTestClient(t)
		// Several attempts an hour apart: without the cancellation check the
		// call would sit here long past the caller's deadline.
		c.config = &testClientConfig{pingWaitPeriod: time.Hour, maxPingAttempts: 3}

		db.ExpectPing().WillReturnError(errors.New("blah"))

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		test.False(t, c.IsReady(ctx))
	})

	T.Run("waits between attempts and retries", func(t *testing.T) {
		t.Parallel()

		c, db := buildTestClient(t)
		c.config = &testClientConfig{pingWaitPeriod: time.Millisecond, maxPingAttempts: 3}

		// The first ping fails, so the loop sleeps out its wait period before the
		// second one succeeds — the path a database taking a moment to come up takes.
		db.ExpectPing().WillReturnError(errors.New("not up yet"))
		db.ExpectPing().WillDelayFor(0)

		test.True(t, c.IsReady(t.Context()))
		test.NoError(t, db.ExpectationsWereMet())
	})
}

func TestClient_ReaderWriter(T *testing.T) {
	T.Parallel()

	T.Run("hand back the non-transactional executors", func(t *testing.T) {
		t.Parallel()

		c, _ := buildTestClient(t)

		test.Eq(t, database.SQLQueryExecutor(c.readDB), c.Reader())
		test.Eq(t, database.SQLQueryExecutor(c.writeDB), c.Writer())
	})
}

func TestNewDatabaseClient(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		exampleConfig := &testClientConfig{
			connectionString: "user=test password=test database=test host=localhost port=5432",
			maxPingAttempts:  1,
		}

		actual, err := NewDatabaseClient(ctx, exampleConfig)
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with no connection strings", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		exampleConfig := &testClientConfig{}

		actual, err := NewDatabaseClient(ctx, exampleConfig)
		test.Nil(t, actual)
		test.Error(t, err)
	})

	T.Run("with only read connection string", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		exampleConfig := &testClientConfig{
			readConnectionString: "user=test password=test database=test host=localhost port=5432",
			maxPingAttempts:      1,
		}

		actual, err := NewDatabaseClient(ctx, exampleConfig)
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with only write connection string", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		exampleConfig := &testClientConfig{
			writeConnectionString: "user=test password=test database=test host=localhost port=5432",
			maxPingAttempts:       1,
		}

		actual, err := NewDatabaseClient(ctx, exampleConfig)
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with metrics provider and distinct read and write connections", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		exampleConfig := &testClientConfig{
			readConnectionString:  "user=test password=test database=read host=localhost port=5432",
			writeConnectionString: "user=test password=test database=write host=localhost port=5432",
			maxPingAttempts:       1,
		}

		actual, err := NewDatabaseClient(ctx, exampleConfig,
			WithMetricsProvider(metricsnoop.NewMetricsProvider()))
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with metrics provider and single connection", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		// One connection string means readDB == writeDB, so stats are registered
		// once rather than twice.
		exampleConfig := &testClientConfig{
			readConnectionString: "user=test password=test database=test host=localhost port=5432",
			maxPingAttempts:      1,
		}

		actual, err := NewDatabaseClient(ctx, exampleConfig,
			WithMetricsProvider(metricsnoop.NewMetricsProvider()))
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with every observability option", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		exampleConfig := &testClientConfig{
			connectionString: "user=test password=test database=test host=localhost port=5432",
			maxPingAttempts:  1,
		}

		actual, err := NewDatabaseClient(ctx, exampleConfig,
			WithLogger(loggingnoop.NewLogger()),
			WithTracerProvider(tracingnoop.NewTracerProvider()),
			WithMetricsProvider(metricsnoop.NewMetricsProvider()))
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("a config that opts out of query logging suppresses db.statement", func(t *testing.T) {
		t.Parallel()

		// The flag is discovered by interface assertion, so the only way to
		// reach the suppression branch is a config that carries the method.
		exampleConfig := &loggingClientConfig{
			testClientConfig: testClientConfig{
				connectionString: "user=test password=test database=test host=localhost port=5432",
				maxPingAttempts:  1,
			},
		}

		actual, err := NewDatabaseClient(t.Context(), exampleConfig)
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("a config that opts into query logging keeps db.statement", func(t *testing.T) {
		t.Parallel()

		exampleConfig := &loggingClientConfig{
			testClientConfig: testClientConfig{
				connectionString: "user=test password=test database=test host=localhost port=5432",
				maxPingAttempts:  1,
			},
			logQueries: true,
		}

		actual, err := NewDatabaseClient(t.Context(), exampleConfig)
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("a failing write side does not leak the read side", func(t *testing.T) {
		t.Parallel()

		exampleConfig := &testClientConfig{
			readConnectionString:  "user=test password=test database=test host=localhost port=5432",
			writeConnectionString: "postgres://user:pass@localhost:not-a-port/test",
			maxPingAttempts:       1,
		}

		actual, err := NewDatabaseClient(t.Context(), exampleConfig)
		test.Nil(t, actual)
		must.Error(t, err)
		test.StrContains(t, err.Error(), "connecting to write postgres database")
	})

	T.Run("an unparseable read connection string is reported", func(t *testing.T) {
		t.Parallel()

		exampleConfig := &testClientConfig{
			readConnectionString: "postgres://user:pass@localhost:not-a-port/test",
			maxPingAttempts:      1,
		}

		actual, err := NewDatabaseClient(t.Context(), exampleConfig)
		test.Nil(t, actual)
		must.Error(t, err)
		test.StrContains(t, err.Error(), "connecting to read postgres database")
	})
}

func TestDefaultTimeFunc(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.False(t, (&Client{}).CurrentTime().IsZero())
	})
}

func TestQuerier_currentTime(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		c, _ := buildTestClient(t)

		test.False(t, c.CurrentTime().IsZero())
	})

	T.Run("handles nil", func(t *testing.T) {
		t.Parallel()

		var c *Client

		test.False(t, c.CurrentTime().IsZero())
	})
}

func TestQuerier_rollbackTransaction(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c, db := buildTestClient(t)

		obs := observability.NewRecordingObserver()
		c.o11y = obs

		db.ExpectBegin()
		db.ExpectRollback().WillReturnError(errors.New("blah"))

		tx, err := c.writeDB.BeginTx(ctx, nil)
		must.NoError(t, err)

		c.RollbackTransaction(ctx, tx)

		// The rollback failed, so the operation must have recorded the error.
		op := obs.ObservedOperationWithData(t, map[string]any{})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("with successful rollback", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c, db := buildTestClient(t)

		obs := observability.NewRecordingObserver()
		c.o11y = obs

		db.ExpectBegin()
		db.ExpectRollback()

		tx, err := c.writeDB.BeginTx(ctx, nil)
		must.NoError(t, err)

		c.RollbackTransaction(ctx, tx)

		op := obs.ObservedOperationWithData(t, map[string]any{})
		must.SliceLen(t, 0, op.Errors)
	})
}

func TestClient_WithTransaction(T *testing.T) {
	T.Parallel()

	T.Run("commits on nil return", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c, db := buildTestClient(t)

		db.ExpectBegin()
		db.ExpectExec("UPDATE things").WillReturnResult(sqlmock.NewResult(1, 1))
		db.ExpectCommit()

		err := c.WithTransaction(ctx, func(tx database.SQLQueryExecutor) error {
			_, execErr := tx.ExecContext(ctx, "UPDATE things SET x = 1")
			return execErr
		})

		test.NoError(t, err)
		must.NoError(t, db.ExpectationsWereMet())
	})

	T.Run("rolls back on error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c, db := buildTestClient(t)

		db.ExpectBegin()
		db.ExpectRollback()

		sentinel := errors.New("boom")
		err := c.WithTransaction(ctx, func(_ database.SQLQueryExecutor) error {
			return sentinel
		})

		test.ErrorIs(t, err, sentinel)
		must.NoError(t, db.ExpectationsWereMet())
	})
}

func TestClient_ReadDB(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		c, _ := buildTestClient(t)

		test.NotNil(t, c.ReadDB())
	})
}

func TestClient_WriteDB(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		c, _ := buildTestClient(t)

		test.NotNil(t, c.WriteDB())
	})
}

func TestClient_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		c, db := buildTestClient(t)

		db.ExpectClose()

		test.NoError(t, c.Close())
	})

	T.Run("with separate read and write DBs", func(t *testing.T) {
		t.Parallel()

		readDB, readMock, err := sqlmock.New()
		must.NoError(t, err)

		writeDB, writeMock, err := sqlmock.New()
		must.NoError(t, err)

		c := &Client{
			readDB:  readDB,
			writeDB: writeDB,
			o11y:    observability.NewObserverForTest("test"),
		}

		readMock.ExpectClose()
		writeMock.ExpectClose()

		test.NoError(t, c.Close())
	})

	T.Run("with read close error", func(t *testing.T) {
		t.Parallel()

		c, db := buildTestClient(t)

		db.ExpectClose().WillReturnError(errors.New("blah"))

		test.Error(t, c.Close())
	})

	T.Run("with write close error", func(t *testing.T) {
		t.Parallel()

		readDB, readMock, err := sqlmock.New()
		must.NoError(t, err)

		writeDB, writeMock, err := sqlmock.New()
		must.NoError(t, err)

		c := &Client{
			readDB:  readDB,
			writeDB: writeDB,
			o11y:    observability.NewObserverForTest("test"),
		}

		readMock.ExpectClose()
		writeMock.ExpectClose().WillReturnError(errors.New("blah"))

		test.Error(t, c.Close())
	})
}

func TestClient_PgxAccess(T *testing.T) {
	T.Parallel()

	T.Run("single connection shares one pool across both sides", func(t *testing.T) {
		t.Parallel()

		// Only the read side is configured; the write side falls back to it and
		// must share the same pool rather than opening a second one.
		exampleConfig := &testClientConfig{
			readConnectionString: "user=test password=test database=test host=localhost port=5432",
			maxPingAttempts:      1,
		}

		client, err := NewDatabaseClient(t.Context(), exampleConfig)
		must.NoError(t, err)
		t.Cleanup(func() {
			must.NoError(t, client.Close())
		})

		must.NotNil(t, client.ReadPool())
		test.EqOp(t, client.ReadPool(), client.WritePool())
	})

	T.Run("split connections get distinct pools", func(t *testing.T) {
		t.Parallel()

		exampleConfig := &testClientConfig{
			readConnectionString:  "user=test password=test database=test host=localhost port=5432",
			writeConnectionString: "user=test password=test database=other host=localhost port=5432",
			maxPingAttempts:       1,
		}

		client, err := NewDatabaseClient(t.Context(), exampleConfig)
		must.NoError(t, err)
		t.Cleanup(func() {
			must.NoError(t, client.Close())
		})

		must.NotNil(t, client.ReadPool())
		must.NotNil(t, client.WritePool())
		test.NotEqOp(t, client.ReadPool(), client.WritePool())
	})

	T.Run("pool config mirrors the client config", func(t *testing.T) {
		t.Parallel()

		exampleConfig := &testClientConfig{
			connectionString: "user=test password=test database=test host=localhost port=5432",
			maxPingAttempts:  1,
		}

		client, err := NewDatabaseClient(t.Context(), exampleConfig)
		must.NoError(t, err)
		t.Cleanup(func() {
			must.NoError(t, client.Close())
		})

		cfg := client.WritePool().Config()
		test.EqOp(t, int32(exampleConfig.GetMaxOpenConns()), cfg.MaxConns)
		test.EqOp(t, exampleConfig.GetConnMaxLifetime(), cfg.MaxConnLifetime)
	})

	T.Run("invalid connection string fails construction", func(t *testing.T) {
		t.Parallel()

		exampleConfig := &testClientConfig{
			connectionString: "host=localhost port=notanumber",
			maxPingAttempts:  1,
		}

		client, err := NewDatabaseClient(t.Context(), exampleConfig)
		test.Nil(t, client)
		test.Error(t, err)
	})
}

func TestClampToInt32(T *testing.T) {
	T.Parallel()

	T.Run("passes small values through and saturates large ones", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, int32(7), clampToInt32(7))
		test.EqOp(t, int32(math.MaxInt32), clampToInt32(math.MaxInt32))
		if math.MaxInt > math.MaxInt32 {
			test.EqOp(t, int32(math.MaxInt32), clampToInt32(math.MaxInt))
		}
	})
}

package mysql

import (
	"context"
	"errors"
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

// loggingClientConfig adds the optional GetLogQueries method NewDatabaseClient
// probes for. testClientConfig deliberately lacks it, so the two types together
// cover both sides of that type assertion.
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

		obs := observability.NewRecordingObserver()
		c.o11y = obs

		// same DB for read/write, so only one ping
		db.ExpectPing().WillDelayFor(0)

		test.True(t, c.IsReady(ctx))

		obs.ObservedOperationWithData(t, map[string]any{
			"db.ping.max_attempts": 1,
			"db.ping.wait_period":  time.Second,
		})
	})

	T.Run("with read DB ping error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		c, db := buildTestClient(t)
		c.config = &testClientConfig{pingWaitPeriod: time.Millisecond, maxPingAttempts: 1}

		obs := observability.NewRecordingObserver()
		c.o11y = obs

		db.ExpectPing().WillReturnError(errors.New("blah"))

		test.False(t, c.IsReady(ctx))

		obs.ObservedOperationWithData(t, map[string]any{
			"db.ping.max_attempts": 1,
			"db.ping.wait_period":  time.Millisecond,
		})
	})

	T.Run("with write DB ping error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		readDB, readMock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
		must.NoError(t, err)

		writeDB, writeMock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
		must.NoError(t, err)

		obs := observability.NewRecordingObserver()

		c := &Client{
			readDB:  readDB,
			writeDB: writeDB,
			config:  &testClientConfig{pingWaitPeriod: time.Millisecond, maxPingAttempts: 1},
			o11y:    obs,
		}

		readMock.ExpectPing().WillDelayFor(0)
		writeMock.ExpectPing().WillReturnError(errors.New("blah"))

		test.False(t, c.IsReady(ctx))

		obs.ObservedOperationWithData(t, map[string]any{
			"db.ping.max_attempts": 1,
			"db.ping.wait_period":  time.Millisecond,
		})
	})

	T.Run("exhausting all available queries", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()

		c, db := buildTestClient(t)
		c.config = &testClientConfig{pingWaitPeriod: time.Millisecond, maxPingAttempts: 1}

		obs := observability.NewRecordingObserver()
		c.o11y = obs

		db.ExpectPing().WillReturnError(errors.New("blah"))

		test.False(t, c.IsReady(ctx))

		obs.ObservedOperationWithData(t, map[string]any{
			"db.ping.max_attempts": 1,
			"db.ping.wait_period":  time.Millisecond,
		})
	})

	T.Run("waits between attempts and retries", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		c, db := buildTestClient(t)
		c.config = &testClientConfig{pingWaitPeriod: time.Millisecond, maxPingAttempts: 3}

		// The first ping fails, so the loop sleeps out its wait period before the
		// second one succeeds — the path a real database taking a moment to accept
		// connections takes.
		db.ExpectPing().WillReturnError(errors.New("not up yet"))
		db.ExpectPing().WillDelayFor(0)

		test.True(t, c.IsReady(ctx))
		test.NoError(t, db.ExpectationsWereMet())
	})

	T.Run("gives up promptly when the context is canceled mid-wait", func(t *testing.T) {
		t.Parallel()

		ctx, cancel := context.WithCancel(t.Context())

		c, db := buildTestClient(t)
		// A wait period far longer than the test's patience: if cancellation were
		// not honored, this would sleep through it rather than return.
		c.config = &testClientConfig{pingWaitPeriod: time.Hour, maxPingAttempts: 5}

		db.ExpectPing().WillReturnError(errors.New("blah")).WillDelayFor(0)

		cancel()

		test.False(t, c.IsReady(ctx))
	})
}

func TestNewDatabaseClient(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		exampleConfig := &testClientConfig{
			connectionString: "test:test@tcp(localhost:3306)/test",
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
			readConnectionString: "test:test@tcp(localhost:3306)/test",
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
			writeConnectionString: "test:test@tcp(localhost:3306)/test",
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
			readConnectionString:  "test:test@tcp(localhost:3306)/read",
			writeConnectionString: "test:test@tcp(localhost:3306)/write",
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
			readConnectionString: "test:test@tcp(localhost:3306)/test",
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
			connectionString: "test:test@tcp(localhost:3306)/test",
			maxPingAttempts:  1,
		}

		actual, err := NewDatabaseClient(ctx, exampleConfig,
			WithLogger(loggingnoop.NewLogger()),
			WithTracerProvider(tracingnoop.NewTracerProvider()),
			WithMetricsProvider(metricsnoop.NewMetricsProvider()))
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with a config that opts out of query logging", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		// A config exposing GetLogQueries() == false suppresses db.statement on
		// spans, so query text does not leak into traces.
		exampleConfig := &loggingClientConfig{
			testClientConfig: testClientConfig{
				connectionString: "test:test@tcp(localhost:3306)/test",
				maxPingAttempts:  1,
			},
		}

		actual, err := NewDatabaseClient(ctx, exampleConfig)
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with a config that opts in to query logging", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		exampleConfig := &loggingClientConfig{
			testClientConfig: testClientConfig{
				connectionString: "test:test@tcp(localhost:3306)/test",
				maxPingAttempts:  1,
			},
			logQueries: true,
		}

		actual, err := NewDatabaseClient(ctx, exampleConfig)
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with an unparseable read connection string", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		exampleConfig := &testClientConfig{
			readConnectionString: "not-a-mysql-dsn",
			maxPingAttempts:      1,
		}

		actual, err := NewDatabaseClient(ctx, exampleConfig)
		test.Nil(t, actual)
		test.Error(t, err)
	})

	T.Run("with an unparseable write connection string", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		// The read side is valid, so failure has to come from the write side.
		exampleConfig := &testClientConfig{
			readConnectionString:  "test:test@tcp(localhost:3306)/test",
			writeConnectionString: "not-a-mysql-dsn",
			maxPingAttempts:       1,
		}

		actual, err := NewDatabaseClient(ctx, exampleConfig)
		test.Nil(t, actual)
		test.Error(t, err)
	})
}

func TestClient_Reader(T *testing.T) {
	T.Parallel()

	T.Run("returns the read database", func(t *testing.T) {
		t.Parallel()

		c, _ := buildTestClient(t)

		reader := c.Reader()
		must.NotNil(t, reader)
		test.True(t, reader == database.SQLQueryExecutor(c.readDB))
	})
}

func TestClient_Writer(T *testing.T) {
	T.Parallel()

	T.Run("returns the write database", func(t *testing.T) {
		t.Parallel()

		c, _ := buildTestClient(t)

		writer := c.Writer()
		must.NotNil(t, writer)
		test.True(t, writer == database.SQLQueryExecutor(c.writeDB))
	})

	T.Run("is the write database when read and write differ", func(t *testing.T) {
		t.Parallel()

		readDB, _, err := sqlmock.New()
		must.NoError(t, err)

		writeDB, _, err := sqlmock.New()
		must.NoError(t, err)

		c := &Client{
			readDB:  readDB,
			writeDB: writeDB,
			o11y:    observability.NewObserverForTest("test"),
		}

		test.True(t, c.Writer() == database.SQLQueryExecutor(writeDB))
		test.True(t, c.Reader() == database.SQLQueryExecutor(readDB))
	})
}

func TestClient_WithTransaction(T *testing.T) {
	T.Parallel()

	T.Run("commits when fn returns nil", func(t *testing.T) {
		t.Parallel()

		c, db := buildTestClient(t)

		db.ExpectBegin()
		db.ExpectCommit()

		var ran bool
		err := c.WithTransaction(t.Context(), func(tx database.SQLQueryExecutor) error {
			ran = true
			test.NotNil(t, tx)

			return nil
		})

		test.NoError(t, err)
		test.True(t, ran)
		test.NoError(t, db.ExpectationsWereMet())
	})

	T.Run("rolls back when fn returns an error", func(t *testing.T) {
		t.Parallel()

		c, db := buildTestClient(t)

		db.ExpectBegin()
		db.ExpectRollback()

		expected := errors.New("nope")
		err := c.WithTransaction(t.Context(), func(database.SQLQueryExecutor) error {
			return expected
		})

		test.Error(t, err)
		test.NoError(t, db.ExpectationsWereMet())
	})

	T.Run("surfaces a begin failure", func(t *testing.T) {
		t.Parallel()

		c, db := buildTestClient(t)

		db.ExpectBegin().WillReturnError(errors.New("cannot begin"))

		var ran bool
		err := c.WithTransaction(t.Context(), func(database.SQLQueryExecutor) error {
			ran = true

			return nil
		})

		test.Error(t, err)
		test.False(t, ran)
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

		must.SliceLen(t, 1, obs.Operations)
		must.SliceLen(t, 1, obs.Operations[0].Errors)
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

		must.SliceLen(t, 1, obs.Operations)
		test.SliceEmpty(t, obs.Operations[0].Errors)
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

package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v3/database"
	"github.com/primandproper/platform-go/v3/observability"
	loggingnoop "github.com/primandproper/platform-go/v3/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v3/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v3/observability/tracing/noop"

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
		timeFunc: defaultTimeFunc,
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
}

func TestProvideDatabaseClient(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		exampleConfig := &testClientConfig{
			connectionString: "user=test password=test database=test host=localhost port=5432",
			maxPingAttempts:  1,
		}

		actual, err := ProvideDatabaseClient(ctx, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), exampleConfig, nil)
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with no connection strings", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		exampleConfig := &testClientConfig{}

		actual, err := ProvideDatabaseClient(ctx, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), exampleConfig, nil)
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

		actual, err := ProvideDatabaseClient(ctx, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), exampleConfig, nil)
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

		actual, err := ProvideDatabaseClient(ctx, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), exampleConfig, nil)
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with metrics provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		exampleConfig := &testClientConfig{
			connectionString: "user=test password=test database=test host=localhost port=5432",
			maxPingAttempts:  1,
		}

		actual, err := ProvideDatabaseClient(ctx, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), exampleConfig, metricsnoop.NewMetricsProvider())
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with metrics provider and single connection", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		exampleConfig := &testClientConfig{
			readConnectionString: "user=test password=test database=test host=localhost port=5432",
			maxPingAttempts:      1,
		}

		actual, err := ProvideDatabaseClient(ctx, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), exampleConfig, metricsnoop.NewMetricsProvider())
		test.NotNil(t, actual)
		test.NoError(t, err)
	})
}

func TestDefaultTimeFunc(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.False(t, defaultTimeFunc().IsZero())
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

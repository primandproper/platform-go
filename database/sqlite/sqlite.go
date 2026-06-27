package sqlite

import (
	"context"
	"database/sql"
	"time"

	"github.com/primandproper/platform-go/database"
	"github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/keys"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"

	"github.com/XSAM/otelsql"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	_ "modernc.org/sqlite"
)

const (
	tracingName = "db_client"
)

// Client is the primary database querying client.
type Client struct {
	o11y     observability.Observer
	timeFunc func() time.Time
	config   database.ClientConfig
	readDB   *sql.DB
	writeDB  *sql.DB
}

// ProvideDatabaseClient provides a new DataManager client.
// If metricsProvider is non-nil, the DB driver will use it so SQL latency and other
// db.sql.* metrics are emitted (e.g. db_sql_latency_milliseconds_bucket in Prometheus).
func ProvideDatabaseClient(ctx context.Context, logger logging.Logger, tracerProvider tracing.TracerProvider, cfg database.ClientConfig, metricsProvider metrics.Provider) (database.Client, error) {
	o11y := observability.NewObserver(tracingName, logger, tracerProvider)

	ctx, op := o11y.Begin(ctx)
	defer op.End()

	opts := []otelsql.Option{
		otelsql.WithAttributes(
			attribute.KeyValue{
				Key:   semconv.ServiceNameKey,
				Value: attribute.StringValue("database"),
			},
		),
	}
	if metricsProvider != nil {
		opts = append(opts, otelsql.WithMeterProvider(metricsProvider.MeterProvider()))
	}

	var readDB, writeDB *sql.DB
	var err error

	if readConnStr := cfg.GetReadConnectionString(); readConnStr != "" {
		readDB, err = connect(ctx, readConnStr, cfg, opts, false)
		if err != nil {
			return nil, errors.Wrap(err, "connecting to read sqlite database")
		}
	}

	if writeConnStr := cfg.GetWriteConnectionString(); writeConnStr != "" {
		writeDB, err = connect(ctx, writeConnStr, cfg, opts, true)
		if err != nil {
			return nil, errors.Wrap(err, "connecting to write sqlite database")
		}
	}

	// Fall back: if only one connection is configured, use it for both.
	if readDB == nil && writeDB == nil {
		return nil, errors.New("at least one of read or write connection string must be provided")
	}
	if readDB == nil {
		readDB = writeDB
	}
	if writeDB == nil {
		writeDB = readDB
	}

	if metricsProvider != nil {
		if _, err = otelsql.RegisterDBStatsMetrics(readDB, otelsql.WithAttributes(semconv.DBSystemSqlite)); err != nil {
			return nil, errors.Wrap(err, "registering readDB stats metrics")
		}

		if readDB != writeDB {
			if _, err = otelsql.RegisterDBStatsMetrics(writeDB, otelsql.WithAttributes(semconv.DBSystemSqlite)); err != nil {
				return nil, errors.Wrap(err, "registering writeDB stats metrics")
			}
		}
	}

	c := &Client{
		readDB:   readDB,
		writeDB:  writeDB,
		config:   cfg,
		o11y:     o11y,
		timeFunc: defaultTimeFunc,
	}

	return c, nil
}

func connect(ctx context.Context, connStr string, cfg database.ClientConfig, opts []otelsql.Option, isWriter bool) (*sql.DB, error) {
	db, err := otelsql.Open("sqlite", connStr, opts...)
	if err != nil {
		return nil, errors.Wrap(err, "connecting to sqlite database")
	}

	if _, err = db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		return nil, errors.Wrap(err, "enabling WAL mode")
	}

	if _, err = db.ExecContext(ctx, "PRAGMA foreign_keys=ON"); err != nil {
		return nil, errors.Wrap(err, "enabling foreign keys")
	}

	if isWriter {
		// SQLite allows only one writer at a time.
		db.SetMaxOpenConns(1)
	} else {
		db.SetMaxOpenConns(cfg.GetMaxOpenConns())
	}

	db.SetMaxIdleConns(cfg.GetMaxIdleConns())
	db.SetConnMaxLifetime(cfg.GetConnMaxLifetime())

	return db, nil
}

// ReadDB provides the database object.
func (q *Client) ReadDB() *sql.DB {
	return q.readDB
}

// WriteDB provides the database object.
func (q *Client) WriteDB() *sql.DB {
	return q.writeDB
}

// Close closes the database connection.
func (q *Client) Close() error {
	if err := q.readDB.Close(); err != nil {
		q.o11y.Logger().Error("closing read database connection", err)
		return err
	}

	if q.writeDB != q.readDB {
		if err := q.writeDB.Close(); err != nil {
			q.o11y.Logger().Error("closing write database connection", err)
			return err
		}
	}

	return nil
}

// IsReady returns whether the database is ready for the querier.
func (q *Client) IsReady(ctx context.Context) bool {
	ctx, op := q.o11y.Begin(ctx)
	defer op.End()

	maxAttempts := int(q.config.GetMaxPingAttempts())
	waitPeriod := q.config.GetPingWaitPeriod()

	readReady := q.waitForPing(ctx, q.readDB, q.config.GetReadConnectionString(), maxAttempts, waitPeriod)
	if !readReady {
		return false
	}

	if q.writeDB != q.readDB {
		return q.waitForPing(ctx, q.writeDB, q.config.GetWriteConnectionString(), maxAttempts, waitPeriod)
	}

	return true
}

func (q *Client) waitForPing(ctx context.Context, db *sql.DB, connectionURL string, maxAttempts int, waitPeriod time.Duration) bool {
	logger := q.o11y.Logger().WithValue(keys.ConnectionURLKey, connectionURL)

	attemptCount := 0
	for {
		if err := db.PingContext(ctx); err != nil {
			logger.WithValue("attempt_count", attemptCount).Info("ping failed, waiting for db")
			time.Sleep(waitPeriod)

			attemptCount++
			if attemptCount >= maxAttempts {
				return false
			}
		} else {
			return true
		}
	}
}

func defaultTimeFunc() time.Time {
	return time.Now()
}

func (q *Client) CurrentTime() time.Time {
	if q == nil || q.timeFunc == nil {
		return defaultTimeFunc()
	}

	return q.timeFunc()
}

func (q *Client) RollbackTransaction(ctx context.Context, tx database.SQLQueryExecutorAndTransactionManager) {
	_, op := q.o11y.Begin(ctx)
	defer op.End()

	op.Logger().Debug("rolling back transaction")

	if err := tx.Rollback(); err != nil {
		op.Acknowledge(err, "rolling back transaction")
	}

	op.Logger().Debug("transaction rolled back")
}

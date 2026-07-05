package mysql

import (
	"context"
	"database/sql"
	"time"

	"github.com/primandproper/platform-go/v4/database"
	"github.com/primandproper/platform-go/v4/errors"
	"github.com/primandproper/platform-go/v4/observability"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/metrics"
	"github.com/primandproper/platform-go/v4/observability/tracing"

	"github.com/XSAM/otelsql"
	_ "github.com/go-sql-driver/mysql"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
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

	_, op := o11y.Begin(ctx)
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

	// Gate raw SQL text on spans behind the config's LogQueries flag. When the
	// config opts out (the default), suppress db.statement so query text is not
	// leaked into traces.
	if lq, ok := cfg.(interface{ GetLogQueries() bool }); ok && !lq.GetLogQueries() {
		opts = append(opts, otelsql.WithSpanOptions(otelsql.SpanOptions{DisableQuery: true}))
	}

	var readDB, writeDB *sql.DB
	var err error

	readConnStr := cfg.GetReadConnectionString()
	writeConnStr := cfg.GetWriteConnectionString()

	op.Set("db.system", "mysql").
		Set("db.read_configured", readConnStr != "").
		Set("db.write_configured", writeConnStr != "")

	if readConnStr != "" {
		readDB, err = connect(readConnStr, cfg, opts)
		if err != nil {
			return nil, errors.Wrap(err, "connecting to read mysql database")
		}
	}

	if writeConnStr != "" {
		writeDB, err = connect(writeConnStr, cfg, opts)
		if err != nil {
			return nil, errors.Wrap(err, "connecting to write mysql database")
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
		if _, err = otelsql.RegisterDBStatsMetrics(readDB, otelsql.WithAttributes(semconv.DBSystemMySQL)); err != nil {
			return nil, errors.Wrap(err, "registering readDB stats metrics")
		}

		if readDB != writeDB {
			if _, err = otelsql.RegisterDBStatsMetrics(writeDB, otelsql.WithAttributes(semconv.DBSystemMySQL)); err != nil {
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

func connect(connStr string, cfg database.ClientConfig, opts []otelsql.Option) (*sql.DB, error) {
	db, err := otelsql.Open("mysql", connStr, opts...)
	if err != nil {
		return nil, errors.Wrap(err, "connecting to mysql database")
	}

	db.SetMaxIdleConns(cfg.GetMaxIdleConns())
	db.SetMaxOpenConns(cfg.GetMaxOpenConns())
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
	logger := q.o11y.Logger()

	var errs error

	if err := q.readDB.Close(); err != nil {
		logger.Error("closing read database connection", err)
		errs = errors.Join(errs, err)
	}

	// Always attempt to close the write pool even if the read pool failed to close,
	// so a read-close error can't leak the write connection.
	if q.writeDB != q.readDB {
		if err := q.writeDB.Close(); err != nil {
			logger.Error("closing write database connection", err)
			errs = errors.Join(errs, err)
		}
	}

	return errs
}

// IsReady returns whether the database is ready for the querier.
func (q *Client) IsReady(ctx context.Context) bool {
	ctx, op := q.o11y.Begin(ctx)
	defer op.End()

	maxAttempts := int(q.config.GetMaxPingAttempts())
	waitPeriod := q.config.GetPingWaitPeriod()

	op.Set("db.ping.max_attempts", maxAttempts).Set("db.ping.wait_period", waitPeriod)

	readReady := q.waitForPing(ctx, op, q.readDB, "read", maxAttempts, waitPeriod)
	if !readReady {
		return false
	}

	if q.writeDB != q.readDB {
		return q.waitForPing(ctx, op, q.writeDB, "write", maxAttempts, waitPeriod)
	}

	return true
}

func (q *Client) waitForPing(ctx context.Context, op observability.Operation, db *sql.DB, connectionName string, maxAttempts int, waitPeriod time.Duration) bool {
	logger := op.Logger().WithValue("connection", connectionName)

	for attemptCount := range maxAttempts {
		if err := db.PingContext(ctx); err == nil {
			return true
		}

		logger.WithValue("attempt_count", attemptCount).Info("ping failed, waiting for db")

		// Don't sleep after the final attempt, and abort promptly if the caller's
		// context is canceled rather than sleeping through it.
		if attemptCount == maxAttempts-1 {
			break
		}

		select {
		case <-ctx.Done():
			return false
		case <-time.After(waitPeriod):
		}
	}

	return false
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

package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v3/database"
	"github.com/primandproper/platform-go/v3/errors"
	"github.com/primandproper/platform-go/v3/observability"
	"github.com/primandproper/platform-go/v3/observability/keys"
	"github.com/primandproper/platform-go/v3/observability/logging"
	"github.com/primandproper/platform-go/v3/observability/metrics"
	"github.com/primandproper/platform-go/v3/observability/tracing"

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

	op.Set("db.system", "sqlite").
		Set("db.read_configured", readConnStr != "").
		Set("db.write_configured", writeConnStr != "")

	if readConnStr != "" {
		readDB, err = connect(ctx, readConnStr, cfg, opts, false)
		if err != nil {
			return nil, errors.Wrap(err, "connecting to read sqlite database")
		}
	}

	if writeConnStr != "" {
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
	// A private in-memory database is broken under this read/write pool
	// architecture: each connection modernc.org/sqlite opens gets its own separate
	// database, so writes vanish between statements. Reject it up front rather than
	// failing mysteriously at query time; a shared-cache DSN (cache=shared) is fine.
	if isUnsafeMemorySQLiteDSN(connStr) {
		return nil, errors.New("in-memory sqlite databases are not supported without cache=shared: each pooled connection would get its own private database; use a file path or a shared-cache DSN")
	}

	// foreign_keys is a per-connection setting: a one-off PRAGMA on the pool only reaches
	// the single connection that served it, leaving every other pooled/recycled conn with
	// enforcement off. Setting it in the DSN makes modernc.org/sqlite apply it on every
	// connection it opens.
	db, err := otelsql.Open("sqlite", withSQLitePragma(connStr, "foreign_keys(1)"), opts...)
	if err != nil {
		return nil, errors.Wrap(err, "connecting to sqlite database")
	}

	// journal_mode=WAL is persisted in the database file, so setting it once is sufficient.
	if _, err = db.ExecContext(ctx, "PRAGMA journal_mode=WAL"); err != nil {
		return nil, errors.Wrap(err, "enabling WAL mode")
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

// isUnsafeMemorySQLiteDSN reports whether a DSN designates an in-memory database
// (":memory:" or "mode=memory") without a shared cache. Such a database is
// per-connection, which the multi-connection read/write pools here cannot use.
func isUnsafeMemorySQLiteDSN(dsn string) bool {
	lower := strings.ToLower(dsn)
	if !strings.Contains(lower, ":memory:") && !strings.Contains(lower, "mode=memory") {
		return false
	}

	return !strings.Contains(lower, "cache=shared")
}

// withSQLitePragma appends a modernc.org/sqlite `_pragma=` query parameter to a DSN,
// which the driver applies to every connection it opens.
func withSQLitePragma(dsn, pragma string) string {
	sep := "?"
	if strings.Contains(dsn, "?") {
		sep = "&"
	}

	return dsn + sep + "_pragma=" + pragma
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
	var errs error

	if err := q.readDB.Close(); err != nil {
		q.o11y.Logger().Error("closing read database connection", err)
		errs = errors.Join(errs, err)
	}

	// Always attempt to close the write pool even if the read pool failed to close,
	// so a read-close error can't leak the write connection.
	if q.writeDB != q.readDB {
		if err := q.writeDB.Close(); err != nil {
			q.o11y.Logger().Error("closing write database connection", err)
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

	readReady := q.waitForPing(ctx, op, q.readDB, q.config.GetReadConnectionString(), maxAttempts, waitPeriod)
	if !readReady {
		return false
	}

	if q.writeDB != q.readDB {
		return q.waitForPing(ctx, op, q.writeDB, q.config.GetWriteConnectionString(), maxAttempts, waitPeriod)
	}

	return true
}

func (q *Client) waitForPing(ctx context.Context, op observability.Operation, db *sql.DB, connectionURL string, maxAttempts int, waitPeriod time.Duration) bool {
	logger := op.Logger().WithValue(keys.ConnectionURLKey, connectionURL)

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

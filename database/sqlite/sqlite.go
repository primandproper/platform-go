package sqlite

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/internal/sqlclient"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"

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

var (
	_ database.Client    = (*Client)(nil)
	_ database.RawAccess = (*Client)(nil)
)

// NewDatabaseClient provides a new DataManager client.
// If a metrics provider is supplied via WithMetricsProvider, the DB driver will
// use it so SQL latency and other db.sql.* metrics are emitted (e.g.
// db_sql_latency_milliseconds_bucket in Prometheus).
func NewDatabaseClient(ctx context.Context, cfg database.ClientConfig, opts ...Option) (*Client, error) {
	o := newOptions(opts)
	o11y := observability.NewObserver(tracingName, o.logger, o.tracerProvider)

	ctx, op := o11y.Begin(ctx)
	defer op.End()

	otelsqlOpts := []otelsql.Option{
		otelsql.WithAttributes(
			attribute.KeyValue{
				Key:   semconv.ServiceNameKey,
				Value: attribute.StringValue("database"),
			},
		),
	}
	if o.metricsProvider != nil {
		otelsqlOpts = append(otelsqlOpts, otelsql.WithMeterProvider(o.metricsProvider.MeterProvider()))
	}

	// Gate raw SQL text on spans behind the config's LogQueries flag. When the
	// config opts out (the default), suppress db.statement so query text is not
	// leaked into traces.
	if lq, ok := cfg.(interface{ GetLogQueries() bool }); ok && !lq.GetLogQueries() {
		otelsqlOpts = append(otelsqlOpts, otelsql.WithSpanOptions(otelsql.SpanOptions{DisableQuery: true}))
	}

	var readDB, writeDB *sql.DB
	var err error

	readConnStr := cfg.GetReadConnectionString()
	writeConnStr := cfg.GetWriteConnectionString()

	op.Set("db.system", "sqlite").
		Set("db.read_configured", readConnStr != "").
		Set("db.write_configured", writeConnStr != "")

	if readConnStr != "" {
		readDB, err = connect(ctx, readConnStr, cfg, otelsqlOpts, false)
		if err != nil {
			return nil, errors.Wrap(err, "connecting to read sqlite database")
		}
	}

	if writeConnStr != "" {
		writeDB, err = connect(ctx, writeConnStr, cfg, otelsqlOpts, true)
		if err != nil {
			// Don't leak the read side when the write side fails to construct —
			// the same fix postgres already carries.
			return nil, sqlclient.ClosePools(errors.Wrap(err, "connecting to write sqlite database"), readDB, nil)
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
		// The read handle is about to serve writes too, and it was opened without
		// the single-writer cap — SQLite admits one writer at a time, so a read
		// pool sized for concurrency hands out N connections that then contend and
		// fail with SQLITE_BUSY. Apply the cap the writer path would have.
		readDB.SetMaxOpenConns(1)
		writeDB = readDB
	}

	if o.metricsProvider != nil {
		// Both pools are open by this point, so every failure path below has to
		// close them; returning early here leaked a fully-connected pool pair.
		if _, err = otelsql.RegisterDBStatsMetrics(readDB, otelsql.WithAttributes(semconv.DBSystemSqlite)); err != nil {
			return nil, sqlclient.ClosePools(errors.Wrap(err, "registering readDB stats metrics"), readDB, writeDB)
		}

		if readDB != writeDB {
			if _, err = otelsql.RegisterDBStatsMetrics(writeDB, otelsql.WithAttributes(semconv.DBSystemSqlite)); err != nil {
				return nil, sqlclient.ClosePools(errors.Wrap(err, "registering writeDB stats metrics"), readDB, writeDB)
			}
		}
	}

	c := &Client{
		readDB:   readDB,
		writeDB:  writeDB,
		config:   cfg,
		o11y:     o11y,
		timeFunc: time.Now,
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

// WriteDB provides the database object. It satisfies database.RawAccess; prefer Writer
// and WithTransaction on the Client interface.
func (q *Client) WriteDB() *sql.DB {
	return q.writeDB
}

// Dialect reports the SQL dialect this client speaks, which is always
// dialect.SQLite.
func (*Client) Dialect() dialect.Dialect {
	return dialect.SQLite
}

// Reader returns a non-transactional executor for the read database.
func (q *Client) Reader() database.SQLQueryExecutor {
	return q.readDB
}

// Writer returns a non-transactional executor for the write database.
func (q *Client) Writer() database.SQLQueryExecutor {
	return q.writeDB
}

// WithTransaction runs fn inside a transaction on the write database, committing on a
// nil return and rolling back on error or panic. See database.RunInTransaction.
func (q *Client) WithTransaction(ctx context.Context, fn func(tx database.SQLQueryExecutor) error) error {
	return sqlclient.WithTransaction(ctx, q.o11y, q.writeDB, q.RollbackTransaction, fn)
}

// Close closes the database connection.
func (q *Client) Close() error {
	return sqlclient.Close(q.o11y, q.readDB, q.writeDB)
}

// IsReady returns whether the database is ready for the querier.
func (q *Client) IsReady(ctx context.Context) bool {
	ctx, op := q.o11y.Begin(ctx)
	defer op.End()

	return sqlclient.IsReady(ctx, op, q.config, q.readDB, q.writeDB)
}

// CurrentTime reads the clock this client was built with.
func (q *Client) CurrentTime() time.Time {
	if q == nil {
		return sqlclient.Now(nil)
	}

	return sqlclient.Now(q.timeFunc)
}

// RollbackTransaction rolls tx back, recording a failure on a span rather than
// returning it.
func (q *Client) RollbackTransaction(ctx context.Context, tx database.SQLQueryExecutorAndTransactionManager) {
	sqlclient.RollbackTransaction(ctx, q.o11y, tx)
}

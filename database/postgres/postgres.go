package postgres

import (
	"context"
	"database/sql"
	"math"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/internal/sqlclient"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/XSAM/otelsql"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"go.opentelemetry.io/otel/attribute"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

const (
	tracingName = "db_client"
)

// PgxAccess is an optional capability exposing the native pgx connection pools, for
// callers that need driver features the database/sql surface cannot express —
// CopyFrom bulk loads, pgx.Batch, native array binding, or LISTEN/NOTIFY.
//
// A caller holding this package's *Client — what NewDatabaseClient returns — needs
// nothing from this interface: the methods are right there. It is for a caller
// holding the portable database.Client, who must ask whether the implementation
// behind it happens to be this one:
//
//	native, ok := client.(postgres.PgxAccess)
//
// The returned pools are the very pools backing Reader, Writer, and RawAccess — the
// database/sql handles are derived from them via a pool connector — so
// MaxOpenConns caps the union of both surfaces, and a connection held idle by the
// database/sql layer is unavailable to native callers until it is released.
//
// Like RawAccess, this is a deliberate step outside the portable Client surface; it
// is also postgres-only, so callers asserting it accept a hard pgx dependency.
type PgxAccess interface {
	ReadPool() *pgxpool.Pool
	WritePool() *pgxpool.Pool
}

// Client is the primary database querying client.
type Client struct {
	o11y      observability.Observer
	timeFunc  func() time.Time
	config    database.ClientConfig
	readPool  *pgxpool.Pool
	writePool *pgxpool.Pool
	readDB    *sql.DB
	writeDB   *sql.DB
}

var (
	_ database.Client    = (*Client)(nil)
	_ database.RawAccess = (*Client)(nil)
	_ PgxAccess          = (*Client)(nil)
)

// NewDatabaseClient provides a new DataManager client.
//
// Construction is pgx-native-first: each side opens a *pgxpool.Pool (reachable via
// the PgxAccess capability) and derives its database/sql handle from that pool, so
// both surfaces share one set of connections. The database/sql layer keeps its
// otelsql instrumentation; if a metrics provider is supplied via
// WithMetricsProvider, the driver emits SQL latency and other db.sql.* metrics
// (e.g. db_sql_latency_milliseconds_bucket in Prometheus). Native pool usage is
// not yet traced — instrument at the call site, or thread a pgx tracer through
// here when a consumer needs it.
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

	var (
		readPool, writePool *pgxpool.Pool
		readDB, writeDB     *sql.DB
		err                 error
	)

	readConnStr := cfg.GetReadConnectionString()
	writeConnStr := cfg.GetWriteConnectionString()

	op.Set("db.system", "postgresql").
		Set("db.read_configured", readConnStr != "").
		Set("db.write_configured", writeConnStr != "")

	if readConnStr != "" {
		readPool, readDB, err = connect(ctx, readConnStr, cfg, otelsqlOpts)
		if err != nil {
			return nil, errors.Wrap(err, "connecting to read postgres database")
		}
	}

	if writeConnStr != "" {
		writePool, writeDB, err = connect(ctx, writeConnStr, cfg, otelsqlOpts)
		if err != nil {
			err = errors.Wrap(err, "connecting to write postgres database")

			// Don't leak the read side when the write side fails to construct.
			if readDB != nil {
				if closeErr := readDB.Close(); closeErr != nil {
					err = errors.Join(err, errors.Wrap(closeErr, "closing read database after write connect failure"))
				}
			}
			if readPool != nil {
				readPool.Close()
			}

			return nil, err
		}
	}

	// Fall back: if only one connection is configured, use it for both.
	if readDB == nil && writeDB == nil {
		return nil, errors.New("at least one of read or write connection string must be provided")
	}
	if readDB == nil {
		readPool, readDB = writePool, writeDB
	}
	if writeDB == nil {
		writePool, writeDB = readPool, readDB
	}

	if o.metricsProvider != nil {
		// Both pools are open by this point, so every failure path below has to
		// close them; returning early here leaked a fully-connected pool pair —
		// the connect paths above already guard against exactly this.
		if _, err = otelsql.RegisterDBStatsMetrics(readDB, otelsql.WithAttributes(semconv.DBSystemPostgreSQL)); err != nil {
			return nil, closePools(errors.Wrap(err, "registering readDB stats metrics"), readDB, writeDB, readPool, writePool)
		}

		if readDB != writeDB {
			if _, err = otelsql.RegisterDBStatsMetrics(writeDB, otelsql.WithAttributes(semconv.DBSystemPostgreSQL)); err != nil {
				return nil, closePools(errors.Wrap(err, "registering writeDB stats metrics"), readDB, writeDB, readPool, writePool)
			}
		}
	}

	c := &Client{
		readPool:  readPool,
		writePool: writePool,
		readDB:    readDB,
		writeDB:   writeDB,
		config:    cfg,
		o11y:      o11y,
		timeFunc:  time.Now,
	}

	return c, nil
}

// closePools releases whatever was opened, for the failure paths after a
// successful connect. It is the shared sqlclient.ClosePools plus the pgx pools
// the database/sql handles are derived from, closed in that order so the
// handles drain back before the pool waits on them.
func closePools(cause error, readDB, writeDB *sql.DB, readPool, writePool *pgxpool.Pool) error {
	cause = sqlclient.ClosePools(cause, readDB, writeDB)

	if readPool != nil {
		readPool.Close()
	}

	if writePool != nil && writePool != readPool {
		writePool.Close()
	}

	return cause
}

// connect opens the pgx pool for one side of the read/write split and derives its
// database/sql handle from it. The pool is the single authority on connection
// count and lifetime; the derived handle is capped to the same values so the two
// layers can never disagree about the real limit.
func connect(ctx context.Context, connStr string, cfg database.ClientConfig, opts []otelsql.Option) (*pgxpool.Pool, *sql.DB, error) {
	poolCfg, err := pgxpool.ParseConfig(connStr)
	if err != nil {
		return nil, nil, errors.Wrap(err, "parsing postgres connection string")
	}

	// Zero config values keep pgxpool's parsed defaults (max(4, NumCPU) conns,
	// 1h lifetime) rather than being forwarded, mirroring database/sql's
	// "zero means unlimited" without giving pgxpool a nonsensical bound.
	if n := cfg.GetMaxOpenConns(); n > 0 {
		poolCfg.MaxConns = clampToInt32(n)
	}
	if d := cfg.GetConnMaxLifetime(); d > 0 {
		poolCfg.MaxConnLifetime = d
	}

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, nil, errors.Wrap(err, "creating postgres connection pool")
	}

	db := otelsql.OpenDB(stdlib.GetPoolConnector(pool), opts...)

	// An idle connection at this layer is still checked out of the pgx pool, so
	// MaxIdleConns bounds how many pool connections the database/sql surface may
	// pin while unused.
	db.SetMaxIdleConns(cfg.GetMaxIdleConns())
	db.SetMaxOpenConns(cfg.GetMaxOpenConns())
	db.SetConnMaxLifetime(cfg.GetConnMaxLifetime())

	return pool, db, nil
}

// clampToInt32 converts a positive int to int32, saturating at MaxInt32.
func clampToInt32(n int) int32 {
	if n > math.MaxInt32 {
		return math.MaxInt32
	}

	return int32(n)
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

// ReadPool provides the native pgx pool behind the read database. It satisfies
// PgxAccess; see that interface's documentation for the sharing semantics.
func (q *Client) ReadPool() *pgxpool.Pool {
	return q.readPool
}

// WritePool provides the native pgx pool behind the write database. It satisfies
// PgxAccess; see that interface's documentation for the sharing semantics.
func (q *Client) WritePool() *pgxpool.Pool {
	return q.writePool
}

// Dialect reports the SQL dialect this client speaks, which is always
// dialect.Postgres.
func (*Client) Dialect() dialect.Dialect {
	return dialect.Postgres
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

// Close closes the database/sql layer first so its connections drain back to the
// pools, then closes the pools themselves. pgxpool's Close blocks until every
// connection is returned, so a connection leaked by a caller (an unclosed Rows,
// an unreleased native Acquire) will hang Close rather than be abandoned.
func (q *Client) Close() error {
	errs := sqlclient.Close(q.o11y, q.readDB, q.writeDB)

	// Pools are nil on clients constructed directly around a plain *sql.DB (tests);
	// the derived handles above are the only layer in that case.
	if q.readPool != nil {
		q.readPool.Close()
	}
	if q.writePool != nil && q.writePool != q.readPool {
		q.writePool.Close()
	}

	return errs
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

package database

import (
	"context"
	"database/sql"
	stderrors "errors"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/sessions"
	"github.com/primandproper/platform-go/v13/sessions/database/migrations"
)

// serviceName names the loggers and spans this backend emits. The counters that
// describe what an operation meant live on the Store; the sweeper's live here,
// because nothing above this layer knows a sweep happened.
const serviceName = "sessions_database"

// DefaultTablePrefix is the namespace the session table carries when none is
// configured, which is none — rendering plain "sessions".
//
// The sessions segment is the schema's, not the caller's: a table always says
// which package created it. Setting a namespace of "ddb" renders ddb_sessions,
// for a database shared between applications. A namespace must not end in '_';
// database/ddl supplies the separator.
const DefaultTablePrefix = ""

// Backend stores session records in a SQL table.
//
// It is a concrete type rather than an interface because it does one thing more
// than sessions.Backend describes: Sweep removes rows whose deadlines have
// passed. A cache expires its own entries and needs no equivalent, so the
// method does not belong on the interface — but a caller who chose this backend
// has to be able to reach it.
type Backend[T any] struct {
	db    database.Client
	codec encoding.Codec
	clock clock.Clock
	o11y  observability.Observer

	sweptCounter       metrics.Int64Counter
	sweepErrorsCounter metrics.Int64Counter

	table   string
	dialect dialect.Dialect
}

var _ sessions.Backend[struct{}] = (*Backend[struct{}])(nil)

// NewBackend builds a Backend over a database client.
//
// Reads go through the write pool, deliberately. A session is written and then
// read on the very next request, and replica lag turns that into a sign-in that
// did not take — the one failure a user retries by signing in again, producing
// another session that also appears not to exist. Session rows are small,
// single-key, and short-lived; they are not the reads worth scaling out.
func NewBackend[T any](cfg *Config, db database.Client, opts ...Option) (*Backend[T], error) {
	if cfg == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil session database config")
	}
	if db == nil {
		return nil, ErrNilClient
	}

	d := db.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "session store dialect %q", d)
	}

	if err := migrations.ValidatePrefix(cfg.TablePrefix); err != nil {
		return nil, err
	}

	o := newOptions(opts)

	b := &Backend[T]{
		db:      db,
		codec:   o.codec,
		clock:   o.clock,
		table:   tableName(cfg.TablePrefix),
		dialect: d,
		o11y:    observability.NewObserver(serviceName, o.logger, o.tracerProvider),
	}

	var err error
	if b.sweptCounter, b.sweepErrorsCounter, err = newSweepInstruments(o.metricsProvider); err != nil {
		return nil, err
	}

	if o.sweepCtx != nil {
		go b.sweepEvery(o.sweepCtx, o.sweepInterval)
	}

	return b, nil
}

// Load reads the record stored under id.
func (b *Backend[T]) Load(ctx context.Context, id string) (*sessions.Record[T], error) {
	ctx, op := b.o11y.Begin(ctx)
	defer op.End()

	query, args := buildSelect(b.dialect, b.table, id)

	var (
		data       []byte
		createdAt  time.Time
		lastSeenAt time.Time
		version    int
	)

	if err := b.db.Writer().QueryRowContext(ctx, query, args...).
		Scan(&data, &createdAt, &lastSeenAt, &version); err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			return nil, sessions.ErrNotFound
		}

		return nil, op.Error(err, "reading session row")
	}

	record := &sessions.Record[T]{
		// Read back as UTC unconditionally: Postgres hands back a time in the
		// session's zone, and every deadline is computed by comparing these
		// against a UTC now.
		CreatedAt:  createdAt.UTC(),
		LastSeenAt: lastSeenAt.UTC(),
		Version:    version,
	}

	// A NULL payload is a session that was established without one, and comes
	// back as the nil it went in as rather than as a zero T.
	if data != nil {
		var value T
		if err := b.codec.Unmarshal(ctx, data, &value); err != nil {
			// Undecodable is treated the same as absent. The alternative is to
			// fail every request carrying that identifier until it expires,
			// where discarding it costs one sign-in — and a payload this
			// binary cannot read is exactly what Record.Version exists to
			// catch, so arriving here means something more unusual than a
			// deploy.
			op.Acknowledge(err, "decoding session payload")

			return nil, sessions.ErrNotFound
		}

		record.Data = &value
	}

	return record, nil
}

// Create inserts a session row.
func (b *Backend[T]) Create(
	ctx context.Context,
	id string,
	record *sessions.Record[T],
	ttl time.Duration,
) error {
	ctx, op := b.o11y.Begin(ctx)
	defer op.End()

	r, err := b.row(ctx, id, record, ttl)
	if err != nil {
		return op.Error(err, "encoding new session row")
	}

	query, args := buildInsert(b.dialect, b.table, r)

	affected, err := b.exec(ctx, b.db.Writer(), query, args)
	if err != nil {
		return op.Error(err, "storing new session row")
	}

	if affected == 0 {
		return sessions.ErrIDConflict
	}

	return nil
}

// Update overwrites an existing session row.
//
// The WHERE clause is the whole guarantee: a row that has been deleted is not
// recreated, so a request that read a session immediately before it was signed
// out cannot write it back afterwards. That is a single statement here and only
// an approximation in the cache backend.
func (b *Backend[T]) Update(
	ctx context.Context,
	id string,
	record *sessions.Record[T],
	ttl time.Duration,
) error {
	ctx, op := b.o11y.Begin(ctx)
	defer op.End()

	r, err := b.row(ctx, id, record, ttl)
	if err != nil {
		return op.Error(err, "encoding session row")
	}

	query, args := buildUpdate(b.dialect, b.table, r)

	affected, err := b.exec(ctx, b.db.Writer(), query, args)
	if err != nil {
		return op.Error(err, "updating session row")
	}

	if affected > 0 {
		return nil
	}

	// Zero rows affected is ambiguous on MySQL, which reports it for an update
	// that matched a row and changed nothing. Only the row's continued
	// existence separates "already signed out" from "wrote the same bytes
	// twice", so it is asked rather than assumed.
	exists, err := b.exists(ctx, id)
	if err != nil {
		return op.Error(err, "checking session row after a no-op update")
	}

	if !exists {
		return sessions.ErrNotFound
	}

	return nil
}

// Rename moves a record from oldID to newID inside one transaction.
//
// This is the operation the database backend exists for. Either the old
// identifier stops resolving and the new one starts, or neither happens; there
// is no interval in which both work, which is the interval a fixation attack
// needs.
func (b *Backend[T]) Rename(
	ctx context.Context,
	oldID, newID string,
	record *sessions.Record[T],
	ttl time.Duration,
) error {
	ctx, op := b.o11y.Begin(ctx)
	defer op.End()

	r, err := b.row(ctx, newID, record, ttl)
	if err != nil {
		return op.Error(err, "encoding renewed session row")
	}

	if err = b.db.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		deleteQuery, deleteArgs := buildDelete(b.dialect, b.table, oldID)

		affected, execErr := b.exec(ctx, q, deleteQuery, deleteArgs)
		if execErr != nil {
			return platformerrors.Wrap(execErr, "removing renewed session's previous row")
		}
		if affected == 0 {
			return sessions.ErrNotFound
		}

		insertQuery, insertArgs := buildInsert(b.dialect, b.table, r)

		if affected, execErr = b.exec(ctx, q, insertQuery, insertArgs); execErr != nil {
			return platformerrors.Wrap(execErr, "storing renewed session row")
		}
		if affected == 0 {
			return sessions.ErrIDConflict
		}

		return nil
	}); err != nil {
		if stderrors.Is(err, sessions.ErrNotFound) || stderrors.Is(err, sessions.ErrIDConflict) {
			return err
		}

		return op.Error(err, "renewing session row")
	}

	return nil
}

// Delete removes the row stored under id. A row that was already gone is not an
// error.
func (b *Backend[T]) Delete(ctx context.Context, id string) error {
	ctx, op := b.o11y.Begin(ctx)
	defer op.End()

	query, args := buildDelete(b.dialect, b.table, id)

	if _, err := b.exec(ctx, b.db.Writer(), query, args); err != nil {
		return op.Error(err, "removing session row")
	}

	return nil
}

// Close releases the database client.
func (b *Backend[T]) Close() error {
	return b.db.Close()
}

// row encodes a record into bound parameters.
func (b *Backend[T]) row(
	ctx context.Context,
	id string,
	record *sessions.Record[T],
	ttl time.Duration,
) (*row, error) {
	r := &row{
		createdAt:  record.CreatedAt,
		lastSeenAt: record.LastSeenAt,
		// Stamped from this backend's clock rather than from the store's, since
		// the interface passes a duration. It feeds the sweeper and nothing
		// else — whether a session is live is decided from the two anchors
		// above — so the two clocks disagreeing costs a row swept early or
		// late, never a session that reads as live when it is not.
		expiresAt: b.clock.Now().UTC().Add(ttl),
		id:        id,
		version:   record.Version,
	}

	if record.Data == nil {
		return r, nil
	}

	data, err := b.codec.Marshal(ctx, record.Data)
	if err != nil {
		return nil, platformerrors.Wrap(err, "encoding session payload")
	}

	r.data = data

	return r, nil
}

// exists reports whether a row is stored under id.
func (b *Backend[T]) exists(ctx context.Context, id string) (bool, error) {
	query, args := buildExists(b.dialect, b.table, id)

	var found int
	if err := b.db.Writer().QueryRowContext(ctx, query, args...).Scan(&found); err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			return false, nil
		}

		return false, err
	}

	return true, nil
}

// exec runs a statement and reports how many rows it affected.
func (b *Backend[T]) exec(
	ctx context.Context,
	q database.SQLQueryExecutor,
	query string,
	args []any,
) (int64, error) {
	result, err := q.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return 0, platformerrors.Wrap(err, "counting affected session rows")
	}

	return affected, nil
}

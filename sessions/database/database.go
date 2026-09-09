package database

import (
	"context"
	"database/sql"
	stderrors "errors"
	"time"

	"github.com/primandproper/platform-go/v14/sessions"
	"github.com/primandproper/platform-go/v14/sessions/database/internal/sessionsdb"
	"github.com/primandproper/platform-go/v14/sessions/database/migrations"

	"github.com/primandproper/primitives-go/clock"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/ddl"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/encoding"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/metrics"
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
	q     sessionsdb.Querier
	codec encoding.Codec
	clock clock.Clock
	o11y  observability.Observer

	sweptCounter       metrics.Int64Counter
	sweepErrorsCounter metrics.Int64Counter
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
		db:    db,
		codec: o.codec,
		clock: o.clock,
		o11y:  observability.NewObserver(serviceName, o.logger, o.tracerProvider),
	}

	// The generated querier, instantiated once the prefix is settled and the
	// dialect is known — the only two things the generated statements do not
	// already carry. What executes is what sqlc analyzed, with one marker
	// substitution; see sessions/database/internal/sessionsdb.
	qd, err := sessionsdbDialect(d)
	if err != nil {
		return nil, err
	}

	if b.q, err = sessionsdb.New(qd, ddl.Qualify(cfg.TablePrefix)); err != nil {
		return nil, platformerrors.Wrap(err, "building the session querier")
	}

	if b.sweptCounter, b.sweepErrorsCounter, err = newSweepInstruments(o.metricsProvider); err != nil {
		return nil, err
	}

	if o.sweepCtx != nil {
		go b.sweepEvery(o.sweepCtx, o.sweepInterval)
	}

	return b, nil
}

// sessionsdbDialect maps this module's dialect names onto the generated
// package's. The set is closed on both sides — NewBackend has already rejected
// anything d.Valid() declines — so the default arm is reachable only when this
// module learns a dialect the generated package was not generated for. That is
// a construction failure like any other, and it names the dialect, rather than
// panicking or leaning on sessionsdb.New refusing the empty string.
func sessionsdbDialect(d dialect.Dialect) (sessionsdb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return sessionsdb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return sessionsdb.DialectMySQL, nil
	case dialect.SQLite:
		return sessionsdb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported, "no generated session queries for dialect %q", d)
	}
}

// Load reads the record stored under id.
func (b *Backend[T]) Load(ctx context.Context, id string) (*sessions.Record[T], error) {
	ctx, op := b.o11y.Begin(ctx)
	defer op.End()

	found, err := b.q.GetSession(ctx, b.db.Writer(), sessionsdb.GetSessionParams{ID: id})
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			return nil, sessions.ErrNotFound
		}

		return nil, op.Error(err, "reading session row")
	}

	record := &sessions.Record[T]{
		// Read back as UTC unconditionally: Postgres hands back a time in the
		// session's zone, and every deadline is computed by comparing these
		// against a UTC now.
		CreatedAt:  found.CreatedAt.UTC(),
		LastSeenAt: found.LastSeenAt.UTC(),
		Holder:     sessions.Holder{Scope: found.Scope, Principal: found.Principal},
		Metadata: sessions.Metadata{
			DeviceName:  found.DeviceName,
			IPAddress:   found.IPAddress,
			UserAgent:   found.UserAgent,
			LoginMethod: found.LoginMethod,
		},
		Version: int(found.Version),
	}

	// A NULL payload is a session that was established without one, and comes
	// back as the nil it went in as rather than as a zero T.
	if found.Data != nil {
		var value T
		if err = b.codec.Unmarshal(ctx, found.Data, &value); err != nil {
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

	affected, err := b.q.CreateSession(ctx, b.db.Writer(), r.create())
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

	affected, err := b.q.UpdateSession(ctx, b.db.Writer(), r.update())
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
	exists, err := b.q.SessionExists(ctx, b.db.Writer(), sessionsdb.SessionExistsParams{ID: id})
	if err != nil {
		return op.Error(err, "checking session row after a no-op update")
	}

	if !exists.Exists {
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

	if err = b.db.WithTransaction(ctx, func(q database.Tx) error {
		affected, execErr := b.q.DeleteSession(ctx, q, sessionsdb.DeleteSessionParams{ID: oldID})
		if execErr != nil {
			return platformerrors.Wrap(execErr, "removing renewed session's previous row")
		}
		if affected == 0 {
			return sessions.ErrNotFound
		}

		if affected, execErr = b.q.CreateSession(ctx, q, r.create()); execErr != nil {
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

	if _, err := b.q.DeleteSession(ctx, b.db.Writer(), sessionsdb.DeleteSessionParams{ID: id}); err != nil {
		return op.Error(err, "removing session row")
	}

	return nil
}

// Close releases the database client.
func (b *Backend[T]) Close() error {
	return b.db.Close()
}

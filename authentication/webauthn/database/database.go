package database

import (
	"context"
	"database/sql"
	stderrors "errors"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/webauthn"
	"github.com/primandproper/platform-go/v13/authentication/webauthn/database/migrations"
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"
)

// serviceName names the loggers and spans this store emits.
const serviceName = "webauthn_database"

// challengeKey is the span and log key the ceremony's challenge is recorded
// under, matching the key the relying party records it under, so that a Begin
// and its Finish join on one field.
const challengeKey = "webauthn.challenge"

// SessionStore keeps WebAuthn ceremony state in a SQL table.
//
// It is a concrete type rather than an interface because it does one thing more
// than webauthn.SessionStore describes: Sweep removes rows whose deadlines have
// passed. A cache expires its own entries and needs no equivalent, so the
// method does not belong on the interface — but a caller who chose this store
// has to be able to reach it.
type SessionStore struct {
	db    database.Client
	codec encoding.Codec
	clock clock.Clock
	o11y  observability.Observer

	sweptCounter       metrics.Int64Counter
	sweepErrorsCounter metrics.Int64Counter

	table   string
	dialect dialect.Dialect
}

var _ webauthn.SessionStore = (*SessionStore)(nil)

// NewSessionStore builds a SessionStore over a database client.
//
// Reads go through the write pool, deliberately. Ceremony state is written by
// the request that begins a ceremony and read by the very next request from the
// same user, and replica lag turns that into a passkey login that fails and
// then works when retried — the failure that gets reported as "sometimes it
// doesn't work". These rows are small, single-key, and live for a minute; they
// are not the reads worth scaling out.
//
// It does not create the table. Hand migrations.SQL to your own migration run.
func NewSessionStore(cfg *Config, db database.Client, opts ...Option) (*SessionStore, error) {
	if cfg == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil webauthn session database config")
	}

	if db == nil {
		return nil, ErrNilClient
	}

	d := db.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "webauthn session store dialect %q", d)
	}

	if err := migrations.ValidatePrefix(cfg.TablePrefix); err != nil {
		return nil, err
	}

	o := newOptions(opts)

	s := &SessionStore{
		db:      db,
		codec:   o.codec,
		clock:   o.clock,
		table:   tableName(cfg.TablePrefix),
		dialect: d,
		o11y:    observability.NewObserver(serviceName, o.logger, o.tracerProvider),
	}

	var err error
	if s.sweptCounter, s.sweepErrorsCounter, err = newSweepInstruments(o.metricsProvider); err != nil {
		return nil, err
	}

	if o.sweepCtx != nil {
		go s.sweepEvery(o.sweepCtx, o.sweepInterval)
	}

	return s, nil
}

// Save stores a ceremony's state under its own challenge for ttl.
func (s *SessionStore) Save(ctx context.Context, session *webauthn.SessionData, ttl time.Duration) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := webauthn.ValidateSession(session, ttl); err != nil {
		return err
	}

	op.Set(challengeKey, session.Challenge)

	data, err := s.codec.Marshal(ctx, session)
	if err != nil {
		return op.Error(err, "encoding webauthn ceremony session")
	}

	query, args := buildUpsert(s.dialect, s.table, session.Challenge, data, s.clock.Now().UTC().Add(ttl))

	if _, err = s.db.Writer().ExecContext(ctx, query, args...); err != nil {
		return op.Error(err, "storing webauthn ceremony session row")
	}

	return nil
}

// Consume returns the state stored under challenge and removes it.
//
// The read and the delete are one transaction, and it is the delete that
// decides the answer. Two requests answering the same challenge at the same
// instant both read the row; the second one's delete finds it already gone and
// reports no rows, so exactly one of them is handed the ceremony and the other
// is told there is none. A read that decided, with a delete afterwards, would
// hand it to both.
func (s *SessionStore) Consume(ctx context.Context, challenge string) (*webauthn.SessionData, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if challenge == "" {
		return nil, webauthn.ErrChallengeRequired
	}

	op.Set(challengeKey, challenge)

	var (
		data      []byte
		expiresAt time.Time
	)

	if err := s.db.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		return s.take(ctx, q, challenge, &data, &expiresAt)
	}); err != nil {
		if stderrors.Is(err, webauthn.ErrSessionNotFound) {
			return nil, err
		}

		return nil, op.Error(err, "consuming webauthn ceremony session row")
	}

	// The row has already been deleted by the time this runs, which is what an
	// expired ceremony deserves: it is unusable, and leaving it for the sweeper
	// would leave a challenge that is refused but present.
	if !s.clock.Now().UTC().Before(expiresAt.UTC()) {
		return nil, webauthn.ErrSessionExpired
	}

	var session webauthn.SessionData
	if err := s.codec.Unmarshal(ctx, data, &session); err != nil {
		// Undecodable is reported rather than swallowed. A row this binary
		// cannot read is a ceremony that cannot be completed either way, and
		// the caller's only recourse — start again — is the same one
		// ErrSessionNotFound would produce, so nothing is bought by hiding
		// which happened.
		return nil, op.Error(err, "decoding webauthn ceremony session")
	}

	return &session, nil
}

// take reads a ceremony's row and removes it, reporting webauthn.ErrSessionNotFound
// when the delete finds nothing to remove.
func (s *SessionStore) take(
	ctx context.Context,
	q database.SQLQueryExecutor,
	challenge string,
	data *[]byte,
	expiresAt *time.Time,
) error {
	selectQuery, selectArgs := buildSelect(s.dialect, s.table, challenge)

	if err := q.QueryRowContext(ctx, selectQuery, selectArgs...).Scan(data, expiresAt); err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			return webauthn.ErrSessionNotFound
		}

		return platformerrors.Wrap(err, "reading webauthn ceremony session row")
	}

	deleteQuery, deleteArgs := buildDelete(s.dialect, s.table, challenge)

	result, err := q.ExecContext(ctx, deleteQuery, deleteArgs...)
	if err != nil {
		return platformerrors.Wrap(err, "removing webauthn ceremony session row")
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return platformerrors.Wrap(err, "counting removed webauthn ceremony session rows")
	}

	if affected == 0 {
		return webauthn.ErrSessionNotFound
	}

	return nil
}

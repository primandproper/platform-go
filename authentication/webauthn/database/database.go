package database

import (
	"context"
	"database/sql"
	stderrors "errors"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/webauthn/database/internal/webauthndb"
	"github.com/primandproper/platform-go/v14/authentication/webauthn/database/migrations"

	"github.com/primandproper/primitives-go/authentication/webauthn"
	"github.com/primandproper/primitives-go/clock"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/ddl"
	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/encoding"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/metrics"
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
	q     webauthndb.Querier
	codec encoding.Codec
	clock clock.Clock
	o11y  observability.Observer

	sweptCounter       metrics.Int64Counter
	sweepErrorsCounter metrics.Int64Counter
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
		db:    db,
		codec: o.codec,
		clock: o.clock,
		o11y:  observability.NewObserver(serviceName, o.logger, o.tracerProvider),
	}

	// The generated querier, instantiated once the prefix is settled and the
	// dialect is known — the only two things the generated statements do not
	// already carry. What executes is what sqlc analyzed, with one marker
	// substitution; see internal/webauthndb.
	qd, err := webauthndbDialect(d)
	if err != nil {
		return nil, err
	}

	// The table's name lives nowhere else in this package: the canonical
	// spelling is internal/queries' and the separator is database/ddl's, so a
	// namespaced deployment cannot end up with two renderings of one name.
	if s.q, err = webauthndb.New(qd, ddl.Qualify(cfg.TablePrefix)); err != nil {
		return nil, platformerrors.Wrap(err, "building the webauthn session querier")
	}

	if s.sweptCounter, s.sweepErrorsCounter, err = newSweepInstruments(o.metricsProvider); err != nil {
		return nil, err
	}

	if o.sweepCtx != nil {
		go s.sweepEvery(o.sweepCtx, o.sweepInterval)
	}

	return s, nil
}

// webauthndbDialect maps this module's dialect names onto the generated
// package's. The set is closed on both sides — NewSessionStore has already
// rejected anything d.Valid() declines — so the default arm is reachable only
// when this module learns a dialect the generated package was not generated
// for. That is a construction failure like any other, and it names the dialect.
func webauthndbDialect(d dialect.Dialect) (webauthndb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return webauthndb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return webauthndb.DialectMySQL, nil
	case dialect.SQLite:
		return webauthndb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported, "no generated webauthn session queries for dialect %q", d)
	}
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

	if err = s.q.UpsertSession(ctx, s.db.Writer(), webauthndb.UpsertSessionParams{
		Challenge:   session.Challenge,
		SessionData: data,
		ExpiresAt:   s.clock.Now().UTC().Add(ttl),
	}); err != nil {
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

	var row webauthndb.GetSessionRow

	if err := s.db.WithTransaction(ctx, func(q database.Tx) error {
		var takeErr error
		row, takeErr = s.take(ctx, q, challenge)

		return takeErr
	}); err != nil {
		if stderrors.Is(err, webauthn.ErrSessionNotFound) {
			return nil, err
		}

		return nil, op.Error(err, "consuming webauthn ceremony session row")
	}

	// The row has already been deleted by the time this runs, which is what an
	// expired ceremony deserves: it is unusable, and leaving it for the sweeper
	// would leave a challenge that is refused but present.
	if !s.clock.Now().UTC().Before(row.ExpiresAt.UTC()) {
		return nil, webauthn.ErrSessionExpired
	}

	var session webauthn.SessionData
	if err := s.codec.Unmarshal(ctx, row.SessionData, &session); err != nil {
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
	q database.Tx,
	challenge string,
) (webauthndb.GetSessionRow, error) {
	row, err := s.q.GetSession(ctx, q, webauthndb.GetSessionParams{Challenge: challenge})
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			return row, webauthn.ErrSessionNotFound
		}

		return row, platformerrors.Wrap(err, "reading webauthn ceremony session row")
	}

	// The count is the answer, which is why the statement is annotated
	// :execrows. A driver that declines to report it reaches this as an error
	// rather than as an acknowledged unknown — the generated method has no seam
	// between running the statement and reading the count — and that is the
	// right reading here: a delete whose count is unreadable cannot say who
	// owns the ceremony, and reporting zero would say somebody else does.
	affected, err := s.q.DeleteSession(ctx, q, webauthndb.DeleteSessionParams{Challenge: challenge})
	if err != nil {
		return row, platformerrors.Wrap(err, "removing webauthn ceremony session row")
	}

	if affected == 0 {
		return row, webauthn.ErrSessionNotFound
	}

	return row, nil
}

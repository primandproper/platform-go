package database

import (
	"context"
	"database/sql"
	stderrors "errors"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	"github.com/primandproper/platform-go/v13/authentication/oauth2server/database/migrations"
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"
)

// serviceName names the loggers, spans, and instruments this store emits.
const serviceName = "oauth2server_database"

// DefaultTablePrefix is the namespace the tables carry when none is configured,
// which is none — rendering plain "oauth2_clients" and friends.
//
// The oauth2 segment is the schema's, not the caller's: a table always says
// which package created it. A namespace must not end in '_'; database/ddl
// supplies the separator.
const DefaultTablePrefix = ""

var _ oauth2server.Store = (*Store)(nil)

// Store keeps every authorization server record in SQL tables.
//
// This is the implementation the package exists for. The four maps behind an
// RWMutex that every consumer writes from the reference examples work perfectly
// until there are two replicas, at which point the authorization code is issued
// by one and redeemed at the other and the login fails — intermittently, in
// proportion to how well the load balancer spreads traffic, which is the
// hardest possible way to notice.
type Store struct {
	db    database.Client
	clock clock.Clock
	o11y  observability.Observer

	sweptCounter       metrics.Int64Counter
	sweepErrorsCounter metrics.Int64Counter

	clients string
	codes   string
	access  string
	refresh string

	dialect dialect.Dialect
}

// NewStore builds a Store over a database client.
//
// Reads go through the write pool, deliberately. An authorization code is
// written by /authorize and read by /token milliseconds later, and replica lag
// turns that into a login that silently did not take — which the user answers
// by logging in again, producing another code that also appears not to exist.
// These rows are small, single-key, and short-lived; they are not the reads
// worth scaling out.
func NewStore(cfg *Config, db database.Client, opts ...Option) (*Store, error) {
	if cfg == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil oauth2 database config")
	}

	if db == nil {
		return nil, ErrNilClient
	}

	d := db.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "oauth2 store dialect %q", d)
	}

	if err := migrations.ValidatePrefix(cfg.TablePrefix); err != nil {
		return nil, err
	}

	o := newOptions(opts)

	s := &Store{
		db:      db,
		clock:   o.clock,
		o11y:    observability.NewObserver(serviceName, o.logger, o.tracerProvider),
		clients: tableName(cfg.TablePrefix, tableClients),
		codes:   tableName(cfg.TablePrefix, tableCodes),
		access:  tableName(cfg.TablePrefix, tableAccess),
		refresh: tableName(cfg.TablePrefix, tableRefresh),
		dialect: d,
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

// CreateClient records a registration.
func (s *Store) CreateClient(ctx context.Context, client *oauth2server.Client) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if client == nil {
		return oauth2server.ErrNilRecord
	}
	if client.ID == "" {
		return oauth2server.ErrEmptyIdentifier
	}

	query, args := buildInsertClient(s.dialect, s.clients, client)

	affected, err := s.exec(ctx, s.db.Writer(), query, args)
	if err != nil {
		return op.Error(err, "storing client registration")
	}

	if affected == 0 {
		return oauth2server.ErrClientExists
	}

	return nil
}

// GetClient reads a registration.
func (s *Store) GetClient(ctx context.Context, clientID string) (*oauth2server.Client, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if clientID == "" {
		return nil, oauth2server.ErrEmptyIdentifier
	}

	query, args := buildSelectClient(s.dialect, s.clients, clientID)

	client, err := scanClient(s.db.Writer().QueryRowContext(ctx, query, args...))
	if err != nil {
		return nil, s.readError(op, err, "reading client registration")
	}

	// A NULL expires_at reads back as the zero time and means the registration
	// does not lapse, which is not the same as one that lapsed at the zero
	// time.
	if !client.ExpiresAt.IsZero() && !s.now().Before(client.ExpiresAt) {
		return nil, oauth2server.ErrExpired
	}

	return client, nil
}

// DeleteClient removes a registration. A registration that was already gone is
// not an error.
func (s *Store) DeleteClient(ctx context.Context, clientID string) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if clientID == "" {
		return oauth2server.ErrEmptyIdentifier
	}

	query, args := buildDeleteClient(s.dialect, s.clients, clientID)

	if _, err := s.exec(ctx, s.db.Writer(), query, args); err != nil {
		return op.Error(err, "removing client registration")
	}

	return nil
}

// CreateAuthorizationCode records an issued code.
func (s *Store) CreateAuthorizationCode(ctx context.Context, code *oauth2server.AuthorizationCode) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if code == nil {
		return oauth2server.ErrNilRecord
	}
	if code.Hash == "" {
		return oauth2server.ErrEmptyIdentifier
	}

	query, args := buildInsertCode(s.dialect, s.codes, code)

	affected, err := s.exec(ctx, s.db.Writer(), query, args)
	if err != nil {
		return op.Error(err, "storing authorization code")
	}

	if affected == 0 {
		return oauth2server.ErrRecordExists
	}

	return nil
}

// ConsumeAuthorizationCode marks a code redeemed and returns it.
//
// One guarded UPDATE decides it, and the SELECT that follows only explains what
// the UPDATE already settled. That ordering is the whole reason this store can
// be swapped for the map-backed one: the check and the mark are a single
// statement, so two requests carrying one code cannot both be told they won.
func (s *Store) ConsumeAuthorizationCode(ctx context.Context, hash string) (*oauth2server.AuthorizationCode, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if hash == "" {
		return nil, oauth2server.ErrEmptyIdentifier
	}

	var (
		code    *oauth2server.AuthorizationCode
		outcome error
	)

	if err := s.db.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		now := s.now()

		update, updateArgs := buildConsumeCode(s.dialect, s.codes, hash, now)

		affected, execErr := s.exec(ctx, q, update, updateArgs)
		if execErr != nil {
			return platformerrors.Wrap(execErr, "redeeming authorization code")
		}

		read, readArgs := buildSelectCode(s.dialect, s.codes, hash)

		record, scanErr := scanCode(q.QueryRowContext(ctx, read, readArgs...))
		if scanErr != nil {
			if stderrors.Is(scanErr, sql.ErrNoRows) {
				outcome = oauth2server.ErrNotFound

				return nil
			}

			return platformerrors.Wrap(scanErr, "reading authorization code")
		}

		if affected > 0 {
			// This request is the one that spent it. The row was re-read rather
			// than reconstructed so that what the caller acts on is what is
			// actually stored, redeemed_at included.
			code = record

			return nil
		}

		// Zero rows affected, and the row exists, so the predicate refused it.
		// Which half refused it is the difference between "somebody replayed
		// this" and "this timed out", and only the first one revokes anything.
		code, outcome = redemptionOutcome(record, record.RedeemedAt, now, record.ExpiresAt)

		return nil
	}); err != nil {
		return nil, op.Error(err, "redeeming authorization code")
	}

	return code, outcome
}

// CreateAccessToken records an issued access token.
func (s *Store) CreateAccessToken(ctx context.Context, token *oauth2server.AccessToken) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if token == nil {
		return oauth2server.ErrNilRecord
	}
	if token.Hash == "" {
		return oauth2server.ErrEmptyIdentifier
	}

	query, args := buildInsertAccess(s.dialect, s.access, token)

	affected, err := s.exec(ctx, s.db.Writer(), query, args)
	if err != nil {
		return op.Error(err, "storing access token")
	}

	if affected == 0 {
		return oauth2server.ErrRecordExists
	}

	return nil
}

// GetAccessToken reads an access token.
func (s *Store) GetAccessToken(ctx context.Context, hash string) (*oauth2server.AccessToken, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if hash == "" {
		return nil, oauth2server.ErrEmptyIdentifier
	}

	query, args := buildSelectAccess(s.dialect, s.access, hash)

	token, err := scanAccess(s.db.Writer().QueryRowContext(ctx, query, args...))
	if err != nil {
		return nil, s.readError(op, err, "reading access token")
	}

	if !token.Active(s.now()) {
		return nil, oauth2server.ErrExpired
	}

	return token, nil
}

// RevokeAccessToken marks one access token revoked.
func (s *Store) RevokeAccessToken(ctx context.Context, hash string) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if hash == "" {
		return oauth2server.ErrEmptyIdentifier
	}

	query, args := buildRevokeOne(s.dialect, s.access, hash, s.now())

	// Zero rows affected is not reported. RFC 7009 §2.2 requires the revocation
	// endpoint to answer 200 for a token it has never seen, so a store that
	// distinguished absent from revoked would be inviting that endpoint to leak
	// which tokens exist.
	if _, err := s.exec(ctx, s.db.Writer(), query, args); err != nil {
		return op.Error(err, "revoking access token")
	}

	return nil
}

// CreateRefreshToken records an issued refresh token.
func (s *Store) CreateRefreshToken(ctx context.Context, token *oauth2server.RefreshToken) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if token == nil {
		return oauth2server.ErrNilRecord
	}
	if token.Hash == "" {
		return oauth2server.ErrEmptyIdentifier
	}

	query, args := buildInsertRefresh(s.dialect, s.refresh, token)

	affected, err := s.exec(ctx, s.db.Writer(), query, args)
	if err != nil {
		return op.Error(err, "storing refresh token")
	}

	if affected == 0 {
		return oauth2server.ErrRecordExists
	}

	return nil
}

// ConsumeRefreshToken marks a refresh token redeemed and returns it. See
// ConsumeAuthorizationCode for why it is one guarded UPDATE.
func (s *Store) ConsumeRefreshToken(ctx context.Context, hash string) (*oauth2server.RefreshToken, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if hash == "" {
		return nil, oauth2server.ErrEmptyIdentifier
	}

	var (
		token   *oauth2server.RefreshToken
		outcome error
	)

	if err := s.db.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		now := s.now()

		update, updateArgs := buildConsumeRefresh(s.dialect, s.refresh, hash, now)

		affected, execErr := s.exec(ctx, q, update, updateArgs)
		if execErr != nil {
			return platformerrors.Wrap(execErr, "rotating refresh token")
		}

		read, readArgs := buildSelectRefresh(s.dialect, s.refresh, hash)

		record, scanErr := scanRefresh(q.QueryRowContext(ctx, read, readArgs...))
		if scanErr != nil {
			if stderrors.Is(scanErr, sql.ErrNoRows) {
				outcome = oauth2server.ErrNotFound

				return nil
			}

			return platformerrors.Wrap(scanErr, "reading refresh token")
		}

		if affected > 0 {
			token = record

			return nil
		}

		// A revoked token is reported as expired rather than as a replay: it
		// was never exchanged, so calling it reuse would revoke a family every
		// time somebody signs out and their client retries.
		if !record.RevokedAt.IsZero() {
			outcome = oauth2server.ErrExpired

			return nil
		}

		token, outcome = redemptionOutcome(record, record.RedeemedAt, now, record.ExpiresAt)

		return nil
	}); err != nil {
		return nil, op.Error(err, "rotating refresh token")
	}

	return token, outcome
}

// GetRefreshToken reads a refresh token without consuming it.
func (s *Store) GetRefreshToken(ctx context.Context, hash string) (*oauth2server.RefreshToken, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if hash == "" {
		return nil, oauth2server.ErrEmptyIdentifier
	}

	query, args := buildSelectRefresh(s.dialect, s.refresh, hash)

	token, err := scanRefresh(s.db.Writer().QueryRowContext(ctx, query, args...))
	if err != nil {
		return nil, s.readError(op, err, "reading refresh token")
	}

	// Redeemed is deliberately not a reason to withhold it — see the interface.
	if !token.RevokedAt.IsZero() || !s.now().Before(token.ExpiresAt) {
		return nil, oauth2server.ErrExpired
	}

	return token, nil
}

// RevokeRefreshToken marks one refresh token revoked.
func (s *Store) RevokeRefreshToken(ctx context.Context, hash string) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if hash == "" {
		return oauth2server.ErrEmptyIdentifier
	}

	query, args := buildRevokeOne(s.dialect, s.refresh, hash, s.now())

	if _, err := s.exec(ctx, s.db.Writer(), query, args); err != nil {
		return op.Error(err, "revoking refresh token")
	}

	return nil
}

// RevokeFamily revokes every access and refresh token in a family.
//
// Both statements run in one transaction. A partial revocation is the worst
// available outcome here: it is reached only by detecting a token reuse, and
// leaving half a family live means the response to a detected theft was to
// revoke some of what the thief holds.
func (s *Store) RevokeFamily(ctx context.Context, familyID string) (int64, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if familyID == "" {
		return 0, oauth2server.ErrEmptyIdentifier
	}

	var revoked int64

	if err := s.db.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		now := s.now()

		for _, table := range []string{s.access, s.refresh} {
			query, args := buildRevokeFamily(s.dialect, table, familyID, now)

			affected, execErr := s.exec(ctx, q, query, args)
			if execErr != nil {
				return platformerrors.Wrap(execErr, "revoking token family")
			}

			revoked += affected
		}

		return nil
	}); err != nil {
		return 0, op.Error(err, "revoking token family")
	}

	return revoked, nil
}

// Sweep removes every row past its deadline.
//
// One statement per table, in one transaction, no batching. The rows are small
// and each table's expires_at index makes the delete proportional to what is
// actually dead rather than to the table; a deployment that outgrows that wants
// a scheduled sweep with its own batching rather than a bigger one here.
func (s *Store) Sweep(ctx context.Context, now time.Time) (int64, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	var swept int64

	if err := s.db.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		for _, table := range []string{s.codes, s.access, s.refresh} {
			query, args := buildSweep(s.dialect, table, now)

			affected, execErr := s.exec(ctx, q, query, args)
			if execErr != nil {
				return platformerrors.Wrap(execErr, "sweeping expired oauth2 records")
			}

			swept += affected
		}

		// Clients are swept by their own statement, because a registration with
		// no expiry stores NULL and must not be reached by a predicate that
		// would read that as the beginning of time.
		query, args := buildSweepClients(s.dialect, s.clients, now)

		affected, execErr := s.exec(ctx, q, query, args)
		if execErr != nil {
			return platformerrors.Wrap(execErr, "sweeping lapsed client registrations")
		}

		swept += affected

		return nil
	}); err != nil {
		s.sweepErrorsCounter.Add(ctx, 1)

		return 0, op.Error(err, "sweeping expired oauth2 records")
	}

	s.sweptCounter.Add(ctx, swept)
	op.Set(sweptKey, swept)

	return swept, nil
}

// Close releases the database client.
func (s *Store) Close() error {
	return s.db.Close()
}

// now reads the clock at the resolution every stamped time uses.
func (s *Store) now() time.Time {
	return s.clock.Now().UTC().Truncate(time.Microsecond)
}

// exec runs a statement and reports how many rows it affected.
func (s *Store) exec(ctx context.Context, q database.SQLQueryExecutor, query string, args []any) (int64, error) {
	result, err := q.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, err
	}

	affected, err := result.RowsAffected()
	if err != nil {
		return 0, platformerrors.Wrap(err, "counting affected oauth2 rows")
	}

	return affected, nil
}

// readError maps a failed single-row read onto this package's sentinels.
//
// An absent row is an outcome rather than a fault: at the token endpoint most
// of what arrives is a credential somebody typed wrong or that timed out, and
// marking those spans errored would make the ordinary case indistinguishable
// from the database being down.
func (s *Store) readError(op observability.Operation, err error, description string) error {
	if stderrors.Is(err, sql.ErrNoRows) {
		return oauth2server.ErrNotFound
	}

	return op.Error(err, "%s", description)
}

// redemptionOutcome explains a consume that the guarded UPDATE refused.
//
// It is shared by the code and refresh paths because the rule is the same one
// and getting it different in two places is exactly how a store ends up
// reporting a replay for something that merely expired. A replayed credential
// comes back with its record — the caller needs it to revoke what the
// credential issued — and an expired one does not, because there is nothing to
// revoke and the record would only invite the caller to act on it.
func redemptionOutcome[T any](record *T, redeemedAt, now, expiresAt time.Time) (*T, error) {
	if !redeemedAt.IsZero() {
		return record, oauth2server.ErrAlreadyRedeemed
	}

	if !now.Before(expiresAt) {
		return nil, oauth2server.ErrExpired
	}

	// Neither redeemed nor expired, and yet the UPDATE matched nothing. The
	// only way to reach this is another transaction changing the row between
	// the two statements, which the transaction is supposed to prevent — so it
	// is reported as unusable rather than quietly retried.
	return nil, oauth2server.ErrExpired
}

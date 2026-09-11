package oauth2serverstore

import (
	"context"
	"database/sql"
	stderrors "errors"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/oauth2serverstore/internal/oauth2serverdb"
	"github.com/primandproper/platform-go/v14/authentication/oauth2serverstore/migrations"

	"github.com/primandproper/primitives-go/v2/authentication/oauth2server"
	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
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
	q     oauth2serverdb.Querier
	clock clock.Clock
	o11y  observability.Observer

	sweptCounter       metrics.Int64Counter
	sweepErrorsCounter metrics.Int64Counter
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
		db:    db,
		clock: o.clock,
		o11y:  observability.NewObserver(serviceName, o.logger, o.tracerProvider),
	}

	// The generated querier, instantiated once the prefix is settled and the
	// dialect is known — the only two things the generated statements do not
	// already carry. What executes is what sqlc analyzed, with one marker
	// substitution; see internal/oauth2serverdb.
	qd, err := oauth2serverdbDialect(d)
	if err != nil {
		return nil, err
	}

	// The four table names live nowhere else in this package: the canonical
	// spellings are internal/queries' and the separator is database/ddl's, so a
	// namespaced deployment cannot end up with two renderings of one name.
	if s.q, err = oauth2serverdb.New(qd, ddl.Qualify(cfg.TablePrefix)); err != nil {
		return nil, platformerrors.Wrap(err, "building the oauth2 querier")
	}

	if s.sweptCounter, s.sweepErrorsCounter, err = newSweepInstruments(o.metricsProvider); err != nil {
		return nil, err
	}

	if o.sweepCtx != nil {
		go s.sweepEvery(o.sweepCtx, o.sweepInterval)
	}

	return s, nil
}

// oauth2serverdbDialect maps this module's dialect names onto the generated
// package's. The set is closed on both sides — NewStore has already rejected
// anything d.Valid() declines — so the default arm is reachable only when this
// module learns a dialect the generated package was not generated for. That is
// a construction failure like any other, and it names the dialect.
func oauth2serverdbDialect(d dialect.Dialect) (oauth2serverdb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return oauth2serverdb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return oauth2serverdb.DialectMySQL, nil
	case dialect.SQLite:
		return oauth2serverdb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported, "no generated oauth2 queries for dialect %q", d)
	}
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

	affected, err := s.q.CreateClient(ctx, s.db.Writer(), createClientParams(client))
	if err != nil {
		return op.Error(err, "storing client registration")
	}

	// The insert skips a row already there rather than raising, so zero rows is
	// how a duplicate is reported without parsing a dialect's SQLSTATE.
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

	row, err := s.q.GetClient(ctx, s.db.Writer(), oauth2serverdb.GetClientParams{ID: clientID})
	if err != nil {
		return nil, s.readError(op, err, "reading client registration")
	}

	client, err := clientFromRow(&row)
	if err != nil {
		return nil, op.Error(err, "reading client registration")
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

	if _, err := s.q.DeleteClient(ctx, s.db.Writer(),
		oauth2serverdb.DeleteClientParams{ID: clientID}); err != nil {
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

	affected, err := s.q.CreateAuthorizationCode(ctx, s.db.Writer(), createAuthorizationCodeParams(code))
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

	if err := s.db.WithTransaction(ctx, func(q database.Tx) error {
		now := s.now()

		affected, execErr := s.q.ConsumeAuthorizationCode(ctx, q, oauth2serverdb.ConsumeAuthorizationCodeParams{
			RedeemedAt: stamp(now),
			Hash:       hash,
			Now:        now,
		})
		if execErr != nil {
			return platformerrors.Wrap(execErr, "redeeming authorization code")
		}

		row, readErr := s.q.GetAuthorizationCode(ctx, q, oauth2serverdb.GetAuthorizationCodeParams{Hash: hash})
		if readErr != nil {
			if stderrors.Is(readErr, sql.ErrNoRows) {
				outcome = oauth2server.ErrNotFound

				return nil
			}

			return platformerrors.Wrap(readErr, "reading authorization code")
		}

		record, convertErr := codeFromRow(&row)
		if convertErr != nil {
			return convertErr
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

	affected, err := s.q.CreateAccessToken(ctx, s.db.Writer(), createAccessTokenParams(token))
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

	row, err := s.q.GetAccessToken(ctx, s.db.Writer(), oauth2serverdb.GetAccessTokenParams{Hash: hash})
	if err != nil {
		return nil, s.readError(op, err, "reading access token")
	}

	token, err := accessTokenFromRow(&row)
	if err != nil {
		return nil, op.Error(err, "reading access token")
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

	// Zero rows affected is not reported. RFC 7009 §2.2 requires the revocation
	// endpoint to answer 200 for a token it has never seen, so a store that
	// distinguished absent from revoked would be inviting that endpoint to leak
	// which tokens exist.
	if _, err := s.q.RevokeAccessToken(ctx, s.db.Writer(), oauth2serverdb.RevokeAccessTokenParams{
		RevokedAt: stamp(s.now()),
		Hash:      hash,
	}); err != nil {
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

	affected, err := s.q.CreateRefreshToken(ctx, s.db.Writer(), createRefreshTokenParams(token))
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

	if err := s.db.WithTransaction(ctx, func(q database.Tx) error {
		now := s.now()

		affected, execErr := s.q.ConsumeRefreshToken(ctx, q, oauth2serverdb.ConsumeRefreshTokenParams{
			RedeemedAt: stamp(now),
			Hash:       hash,
			Now:        now,
		})
		if execErr != nil {
			return platformerrors.Wrap(execErr, "rotating refresh token")
		}

		row, readErr := s.q.GetRefreshToken(ctx, q, oauth2serverdb.GetRefreshTokenParams{Hash: hash})
		if readErr != nil {
			if stderrors.Is(readErr, sql.ErrNoRows) {
				outcome = oauth2server.ErrNotFound

				return nil
			}

			return platformerrors.Wrap(readErr, "reading refresh token")
		}

		record, convertErr := refreshTokenFromRow(&row)
		if convertErr != nil {
			return convertErr
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

	row, err := s.q.GetRefreshToken(ctx, s.db.Writer(), oauth2serverdb.GetRefreshTokenParams{Hash: hash})
	if err != nil {
		return nil, s.readError(op, err, "reading refresh token")
	}

	token, err := refreshTokenFromRow(&row)
	if err != nil {
		return nil, op.Error(err, "reading refresh token")
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

	if _, err := s.q.RevokeRefreshToken(ctx, s.db.Writer(), oauth2serverdb.RevokeRefreshTokenParams{
		RevokedAt: stamp(s.now()),
		Hash:      hash,
	}); err != nil {
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
//
// The two are separate named statements rather than one over a table name the
// store substitutes. They are the same shape and they are checked against two
// different tables, which is the whole reason a corpus is enumerated: a column
// that exists on one and not the other is a failed generate rather than a
// runtime error on whichever half ran second.
func (s *Store) RevokeFamily(ctx context.Context, familyID string) (int64, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if familyID == "" {
		return 0, oauth2server.ErrEmptyIdentifier
	}

	var revoked int64

	if err := s.db.WithTransaction(ctx, func(q database.Tx) error {
		now := stamp(s.now())

		access, execErr := s.q.RevokeAccessTokenFamily(ctx, q, oauth2serverdb.RevokeAccessTokenFamilyParams{
			RevokedAt: now,
			FamilyID:  familyID,
		})
		if execErr != nil {
			return platformerrors.Wrap(execErr, "revoking access token family")
		}

		refresh, execErr := s.q.RevokeRefreshTokenFamily(ctx, q, oauth2serverdb.RevokeRefreshTokenFamilyParams{
			RevokedAt: now,
			FamilyID:  familyID,
		})
		if execErr != nil {
			return platformerrors.Wrap(execErr, "revoking refresh token family")
		}

		revoked = access + refresh

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
//
// The horizon is this store's own clock rather than the database server's, and
// rather than an instant a caller passes. It is the clock every deadline in
// this schema was stamped from and the one every read above refuses against,
// so a sweep reclaims exactly the rows this store has already stopped
// answering with — which is the only decision a garbage collector is entitled
// to make. A caller-supplied horizon was a third opinion between the two: it
// could reap a row a request had just read, or spare one the next read would
// refuse, and nothing in the signature said which was meant. WithClock is the
// lever, for a test and for a deployment that points this store and its
// Server at one source. The four sweepers this module already had read their
// clocks the same way.
func (s *Store) Sweep(ctx context.Context) (int64, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	var swept int64

	if err := s.db.WithTransaction(ctx, func(q database.Tx) error {
		horizon := s.now()

		codes, execErr := s.q.SweepAuthorizationCodes(ctx, q,
			oauth2serverdb.SweepAuthorizationCodesParams{Now: horizon})
		if execErr != nil {
			return platformerrors.Wrap(execErr, "sweeping expired authorization codes")
		}

		access, execErr := s.q.SweepAccessTokens(ctx, q,
			oauth2serverdb.SweepAccessTokensParams{Now: horizon})
		if execErr != nil {
			return platformerrors.Wrap(execErr, "sweeping expired access tokens")
		}

		refresh, execErr := s.q.SweepRefreshTokens(ctx, q,
			oauth2serverdb.SweepRefreshTokensParams{Now: horizon})
		if execErr != nil {
			return platformerrors.Wrap(execErr, "sweeping expired refresh tokens")
		}

		// Clients are swept by their own statement, because a registration with
		// no expiry stores NULL and must not be reached by a predicate that
		// would read that as the beginning of time. Its horizon is a pointer
		// for the same reason: sqlc reads an argument's nullability off the
		// column it is compared against, and that column is the nullable one.
		clients, execErr := s.q.SweepClients(ctx, q,
			oauth2serverdb.SweepClientsParams{Now: &horizon})
		if execErr != nil {
			return platformerrors.Wrap(execErr, "sweeping lapsed client registrations")
		}

		swept = codes + access + refresh + clients

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

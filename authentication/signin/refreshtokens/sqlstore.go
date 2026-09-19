package refreshtokens

import (
	"context"
	"database/sql"
	stderrors "errors"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens/internal/signindb"
	"github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens/migrations"

	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/cryptography/hashing"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/random"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// serviceName names the loggers, spans, and instruments this store emits.
const serviceName = "signin_refresh_tokens"

// The keys this store records against a span and a log line.
//
// None of them is the secret, and none of them can be turned into it — the hash
// is not among them either, because a digest is still a name for a live
// credential and a trace is a place operators paste into tickets. What a trace
// shows is which login, whose sign-in, and which directory.
const (
	familyKey  = "signin.family_id"
	subjectKey = "signin.subject_id"
	scopeKey   = "identity.scope"

	// revokedKey is how many rows a revocation withdrew. On the span only: it is
	// a fact about one request rather than a metric, and a log line carrying it
	// beside a user id for every erasure would be a count nobody reads.
	revokedKey = "signin.revoked"
)

// DefaultTablePrefix is the namespace the refresh token table carries when none
// is configured, which is none — rendering plain "signin_refresh_tokens".
//
// The signin_refresh_tokens segment is the schema's, not the caller's: a table
// always says which package created it. Setting a namespace of "ddb" renders
// ddb_signin_refresh_tokens, for a database shared between applications. A
// namespace must not end in '_'; database/ddl supplies the separator.
const DefaultTablePrefix = ""

var _ signin.RefreshTokenStore = (*SQLStore)(nil)

// SQLStore keeps sign-in refresh tokens in a SQL table, against the schema
// refreshtokens/migrations renders.
//
// It is exported, and returned by NewSQLStore, so a caller who has chosen SQL
// storage can depend on that choice rather than on the signin.RefreshTokenStore
// seam. It does two things more than that interface describes — Sweep removes
// rows past their purge deadline, and Digest renders the column a stored token
// is found by — and neither belongs on it: a store backed by something that
// expires its own entries needs no sweep, and a store that is not a table has no
// column.
type SQLStore struct {
	// db is not what the four writes run on — those are handed the caller's
	// transaction. It is here for the two things a Client answers that a Tx
	// cannot: the dialect the generated statements are rendered for, read once at
	// construction, and the executor Sweep runs on, which belongs to nobody's
	// request and so can join nobody's transaction.
	db        database.Client
	q         signindb.Querier
	clock     clock.Clock
	generator random.Generator
	hasher    hashing.Hasher
	o11y      observability.Observer

	sweptCounter       metrics.Int64Counter
	sweepErrorsCounter metrics.Int64Counter

	retention time.Duration

	secretBytes int
}

// NewSQLStore builds a SQLStore over a database client.
//
// The client is not what the writes execute on. Issue, Redeem, RevokeFamily and
// RevokeForSubject take the caller's database.Tx; what this one supplies is the
// dialect the generated statements are rendered for and the executor Sweep runs
// on, which serves the store's own machinery rather than a request.
//
// There is no read here that a replica could serve. The one read this store
// makes is the exchange's read-back, on the transaction that just wrote — see
// Redeem — so nothing in this package ever touches Client.Reader(), and replica
// lag cannot turn a freshly minted token into one that is "not found" and then
// works when retried.
//
// It does not create the table. Hand migrations.SQL to your own migration run.
func NewSQLStore(cfg *Config, db database.Client, opts ...Option) (*SQLStore, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}

	if db == nil {
		return nil, ErrNilDatabaseClient
	}

	d := db.Dialect()
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "refresh token store dialect %q", d)
	}

	if err := migrations.ValidatePrefix(cfg.TablePrefix); err != nil {
		return nil, err
	}

	o := newOptions(opts)

	s := &SQLStore{
		db:          db,
		clock:       o.clock,
		generator:   o.generator,
		hasher:      o.hasher,
		retention:   o.retention,
		secretBytes: o.secretBytes,
		o11y:        observability.NewObserver(serviceName, o.logger, o.tracerProvider),
	}

	// The generated querier, instantiated once the prefix is settled and the
	// dialect is known — the only two things the generated statements do not
	// already carry. What executes is what sqlc analyzed, with one marker
	// substitution; see internal/signindb.
	qd, err := querierDialect(d)
	if err != nil {
		return nil, err
	}

	// The table's name lives nowhere else in this package: the canonical spelling
	// is internal/queries' and the separator is database/ddl's, so a namespaced
	// deployment cannot end up with two renderings of one name.
	if s.q, err = signindb.New(qd, ddl.Qualify(cfg.TablePrefix)); err != nil {
		return nil, platformerrors.Wrap(err, "building the refresh token querier")
	}

	if s.sweptCounter, s.sweepErrorsCounter, err = newSweepInstruments(o.metricsProvider); err != nil {
		return nil, err
	}

	if o.sweepCtx != nil {
		go s.sweepEvery(o.sweepCtx, o.sweepInterval)
	}

	return s, nil
}

// querierDialect maps this module's dialect names onto the generated package's.
// The set is closed on both sides — NewSQLStore has already rejected anything
// d.Valid() declines — so the default arm is reachable only when this module
// learns a dialect the generated package was not generated for. That is a
// construction failure like any other, and it names the dialect.
func querierDialect(d dialect.Dialect) (signindb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return signindb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return signindb.DialectMySQL, nil
	case dialect.SQLite:
		return signindb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported,
			"no generated refresh token queries for dialect %q", d)
	}
}

// Digest renders what the hash column holds for a secret.
//
// It is exported for the one caller the seam cannot serve: a deployment
// migrating off a hand-written table, which has to write the new column from
// tokens it is holding, and a test asserting that the raw value is nowhere in the
// row. It is not a verification — comparing its output to a column by hand is how
// the single-use guarantee gets reimplemented badly — and it is not reversible,
// so a caller holding one of these holds nothing.
func (s *SQLStore) Digest(secret string) string {
	return hashing.HexString(s.hasher, secret)
}

// Issue mints a refresh token, stores its digest, and returns the secret exactly
// once.
//
// The row lands with tx. Hand the secret to whoever signed in after the commit,
// not from inside the callback: a credential returned for a transaction that then
// rolled back is one nothing will ever accept.
func (s *SQLStore) Issue(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	request *signin.RefreshTokenRequest,
) (*signin.RefreshTokenIssuance, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := s.validateMint(scope, request); err != nil {
		return nil, err
	}

	op.SetValues(map[string]any{
		scopeKey:   scope.String(),
		familyKey:  request.FamilyID,
		subjectKey: request.SubjectID,
	})

	secret, err := s.generator.GenerateBase64EncodedString(ctx, s.secretBytes)
	if err != nil {
		return nil, op.Error(err, "generating refresh token")
	}

	now := s.clock.Now().UTC()

	token := &signin.RefreshToken{
		Scope:           scope,
		FamilyID:        request.FamilyID,
		SubjectID:       request.SubjectID,
		ActiveAccountID: request.ActiveAccountID,
		Administrative:  request.Administrative,
		IssuedAt:        now,
		ExpiresAt:       now.Add(request.TTL),
		PurgeAfter:      now.Add(request.TTL).Add(s.retention),
	}

	if err = s.q.InsertRefreshToken(ctx, tx, signindb.InsertRefreshTokenParams{
		Hash:            s.Digest(secret),
		Scope:           token.Scope,
		FamilyID:        token.FamilyID,
		SubjectID:       token.SubjectID,
		ActiveAccountID: token.ActiveAccountID,
		Administrative:  token.Administrative,
		IssuedAt:        token.IssuedAt,
		ExpiresAt:       token.ExpiresAt,
		PurgeAfter:      token.PurgeAfter,
	}); err != nil {
		return nil, op.Error(err, "storing refresh token row")
	}

	return &signin.RefreshTokenIssuance{Token: token, Secret: secret}, nil
}

// Redeem spends a secret and answers with the token it spent.
//
// This is the operation this store exists for, and it is two statements that
// have to run in one transaction to mean anything.
//
// The first is a guarded UPDATE, and its predicate repeats every row-state test
// the answer depends on: not yet redeemed, not revoked, not past its deadline.
// Two requests presenting one token at the same instant both reach it; the first
// one's update matches, the second one's finds redeemed_at already set and
// reports no rows. The count is the answer, which is why a read cannot be.
//
// The second is a read of the row on the same transaction, and it is what turns
// an ambiguous zero into a specific refusal. Zero rows means expired, revoked,
// already redeemed, or no such row — four different facts behind one number —
// and only one of them is a detected theft. MySQL has no RETURNING, so the read
// is a second statement in every case rather than only in this one, and the
// successful path reads back through it too, for the family and the account the
// successor inherits.
//
// A row that exists with redeemed_at set is the reuse case: the whole family is
// revoked here, in tx, and ErrRefreshTokenReused is returned. Reporting the
// reuse without the revocation would be a theft detected and then allowed to
// continue, so the two are one act rather than something a caller is trusted to
// follow up.
//
// Everything else collapses to signin.ErrInvalidCredentials, for the reason the
// password door collapses its four: told apart, an unknown token, a revoked one
// and an expired one are an oracle for whoever is presenting guesses.
//
// # One case this cannot see, and what it costs
//
// Reuse is detected by the read-back finding the winner's stamp, and on MySQL a
// transaction's consistent read is the snapshot it opened with. A replay
// presented in a *later* transaction than the one that spent the token sees the
// stamp and is detected — which is every real reuse, since a stolen token is
// presented minutes or days later. What the snapshot can hide is the narrow case
// of two exchanges of one token racing inside overlapping transactions, where
// the loser may read redeemed_at still NULL and be refused as invalid rather
// than reported as a reuse. That is the retry-after-a-dropped-response case far
// more often than it is a theft, and refusing it is the conservative answer:
// nothing is minted either way, and a family is not ended on a race the client
// did not cause.
func (s *SQLStore) Redeem(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	secret string,
) (*signin.RefreshToken, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := s.validateLookup(scope, secret); err != nil {
		return nil, err
	}

	op.Set(scopeKey, scope.String())

	hash := s.Digest(secret)
	now := s.clock.Now().UTC()

	// The count is the answer, which is why the statement is annotated
	// :execrows. A driver that declines to report it reaches this as an error
	// rather than as an acknowledged unknown, and that is the right reading: a
	// redemption whose count is unreadable cannot say who spent the token, and
	// reporting zero would say somebody else did.
	affected, err := s.q.RedeemRefreshToken(ctx, tx, signindb.RedeemRefreshTokenParams{
		RedeemedAt: &now,
		Hash:       hash,
		Scope:      scope,
		Now:        now,
	})
	if err != nil {
		return nil, op.Error(err, "redeeming refresh token row")
	}

	row, err := s.q.GetRefreshToken(ctx, tx, signindb.GetRefreshTokenParams{Hash: hash, Scope: scope})
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			// No row at all, whichever branch we are in. A winning UPDATE cannot
			// reach this, so it is an unknown token or one from another
			// directory — which is the same thing from here.
			return nil, signin.ErrInvalidCredentials
		}

		return nil, op.Error(err, "reading refresh token row")
	}

	token := tokenFromRow(&row)

	op.SetValues(map[string]any{familyKey: token.FamilyID, subjectKey: token.SubjectID})

	if affected == 0 {
		return nil, s.refuse(ctx, tx, op, token)
	}

	// The stamp this call wrote, rather than the one the read-back happened to
	// see. The two are the same value on every engine that shows this
	// transaction its own writes, and this is the one that is this call's answer
	// on every engine.
	token.RedeemedAt = &now

	return token, nil
}

// refuse decides what a zero-row redemption of an existing row means, and acts
// on it.
//
// A stamp on redeemed_at is the only one of the three that is a replay, and it is
// the only one that ends a family. A revoked token was never spent, so reporting
// reuse for it would revoke a family every time somebody signs out and their
// client retries; an expired one was never spent either.
func (s *SQLStore) refuse(
	ctx context.Context,
	tx database.Tx,
	op observability.Operation,
	token *signin.RefreshToken,
) error {
	if token.RedeemedAt == nil {
		return signin.ErrInvalidCredentials
	}

	if _, err := s.RevokeFamily(ctx, tx, token.Scope, token.FamilyID); err != nil {
		// The revocation is the half that matters, so a failure to run it is
		// reported as itself rather than collapsed into the refusal. Joining the
		// two would let a caller match ErrRefreshTokenReused and conclude the
		// family had been ended.
		return op.Error(err, "revoking a reused refresh token's family")
	}

	return signin.ErrRefreshTokenReused
}

// RevokeFamily ends one login and reports how many tokens it withdrew.
//
// It runs in tx, so the reuse that triggered it and the revocation are one fact
// rather than two — which is what makes "detected and ended" a single outcome
// rather than a sequence something could interrupt halfway.
func (s *SQLStore) RevokeFamily(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	familyID string,
) (int64, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := scope.Validate(); err != nil {
		return 0, err
	}

	if familyID == "" {
		return 0, ErrEmptyFamilyID
	}

	op.SetValues(map[string]any{scopeKey: scope.String(), familyKey: familyID})

	at := s.clock.Now().UTC()

	revoked, err := s.q.RevokeRefreshTokenFamily(ctx, tx, signindb.RevokeRefreshTokenFamilyParams{
		RevokedAt: &at,
		Scope:     scope,
		FamilyID:  familyID,
	})
	if err != nil {
		return 0, op.Error(err, "revoking a refresh token family's rows")
	}

	op.SpanOnly(revokedKey, revoked)

	return revoked, nil
}

// RevokeForSubject ends every login one person holds and reports how many tokens
// it withdrew.
//
// It runs in tx, so "disable this account" and the sign-out that goes with it
// are one fact — and so that an erasure's other writes and this one land
// together or not at all.
func (s *SQLStore) RevokeForSubject(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	subjectID string,
) (int64, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := scope.Validate(); err != nil {
		return 0, err
	}

	if subjectID == "" {
		return 0, ErrEmptySubjectID
	}

	op.SetValues(map[string]any{scopeKey: scope.String(), subjectKey: subjectID})

	at := s.clock.Now().UTC()

	revoked, err := s.q.RevokeRefreshTokensForSubject(ctx, tx, signindb.RevokeRefreshTokensForSubjectParams{
		RevokedAt: &at,
		Scope:     scope,
		SubjectID: subjectID,
	})
	if err != nil {
		return 0, op.Error(err, "revoking a subject's refresh token rows")
	}

	op.SpanOnly(revokedKey, revoked)

	return revoked, nil
}

// tokenFromRow renders one stored row as a signin.RefreshToken.
//
// Every instant is converted to UTC here rather than left as the driver chose. A
// location is not the instant, so nothing this package compares would change —
// but a RefreshToken is handed to a caller who prints it, serializes it and shows
// it to somebody, and a timestamp that reads differently on Postgres than on
// SQLite is a difference in this package's output rather than in a driver's.
func tokenFromRow(row *signindb.GetRefreshTokenRow) *signin.RefreshToken {
	token := &signin.RefreshToken{
		Scope:           row.Scope,
		FamilyID:        row.FamilyID,
		SubjectID:       row.SubjectID,
		ActiveAccountID: row.ActiveAccountID,
		Administrative:  row.Administrative,
		IssuedAt:        row.IssuedAt.UTC(),
		ExpiresAt:       row.ExpiresAt.UTC(),
		PurgeAfter:      row.PurgeAfter.UTC(),
	}

	if row.RedeemedAt != nil {
		utc := row.RedeemedAt.UTC()
		token.RedeemedAt = &utc
	}

	if row.RevokedAt != nil {
		utc := row.RevokedAt.UTC()
		token.RevokedAt = &utc
	}

	return token
}

// validateMint rejects a mint that named no scope, no login, nobody, or no
// lifetime before it reaches the database.
func (s *SQLStore) validateMint(scope tenancy.Scope, request *signin.RefreshTokenRequest) error {
	if err := scope.Validate(); err != nil {
		return err
	}

	if request == nil {
		return ErrNilRequest
	}

	if request.FamilyID == "" {
		return ErrEmptyFamilyID
	}

	if request.SubjectID == "" {
		return ErrEmptySubjectID
	}

	if request.TTL <= 0 {
		return ErrNonPositiveLifetime
	}

	return nil
}

// validateLookup rejects a redemption that named no scope or presented nothing
// before it reaches the database.
func (s *SQLStore) validateLookup(scope tenancy.Scope, secret string) error {
	if err := scope.Validate(); err != nil {
		return err
	}

	if secret == "" {
		return ErrEmptySecret
	}

	return nil
}

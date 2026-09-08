package passwordreset

import (
	"context"
	"database/sql"
	stderrors "errors"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset/internal/passwordresetdb"
	"github.com/primandproper/platform-go/v14/authentication/passwordreset/migrations"

	"github.com/primandproper/primitives-go/clock"
	"github.com/primandproper/primitives-go/cryptography/hashing"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/database/ddl"
	"github.com/primandproper/primitives-go/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/identifiers"
	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/observability/metrics"
	"github.com/primandproper/primitives-go/random"
	"github.com/primandproper/primitives-go/tenancy"
)

// serviceName names the loggers, spans, and instruments this store emits.
const serviceName = "password_reset"

// The keys this store records against a span and a log line.
//
// None of them is the secret, and none of them can be turned into it. What a
// trace shows is which row, whose reset, and which scope — enough to follow one
// password reset through a system, and not enough to complete somebody else's.
const (
	tokenKey = "password_reset.token_id"
	userKey  = "password_reset.user_id"
	scopeKey = "password_reset.scope"
)

var _ Store = (*SQLStore)(nil)

// SQLStore keeps password reset tokens in a SQL table, against the schema
// passwordreset/migrations renders.
//
// It is exported, and returned by NewSQLStore, so a caller who has chosen SQL
// storage can depend on that choice rather than on the Store seam. It does two
// things more than Store describes — Sweep removes rows past their deadline, and
// Digest renders the column a stored token is found by — and neither belongs on
// the interface: a store backed by something that expires its own entries needs
// no sweep, and a store that is not a table has no column.
type SQLStore struct {
	// db is not what the writes run on — those are handed the caller's
	// transaction. It is here for the two things a Client answers that a Tx
	// cannot: the dialect the generated statements are rendered for, read once
	// at construction, and the executor Sweep runs on, which belongs to nobody's
	// request and so can join nobody's transaction.
	db        database.Client
	q         passwordresetdb.Querier
	clock     clock.Clock
	generator random.Generator
	hasher    hashing.Hasher
	o11y      observability.Observer

	sweptCounter       metrics.Int64Counter
	sweepErrorsCounter metrics.Int64Counter

	secretBytes int
}

// NewSQLStore builds a SQLStore over a database client.
//
// The client is not what the writes execute on. Issue, Consume and
// RevokeForUser take the caller's database.Tx; what this one supplies is the
// dialect the generated statements are rendered for and the executor Sweep runs
// on, which serves the store's own machinery rather than a request.
//
// Verify takes its executor too, and which one is the caller's choice with one
// recommendation: not Client.Reader(). A reset token is written by the request
// that asks for one and read by the very next request the user makes — the one
// they made by following a link that arrived seconds later — and replica lag
// turns that into a reset link that is "not found" and then works when
// reloaded. Pass Client.Writer(), or the Tx the redemption is about to run in.
// These rows are small, single-key, and live for an hour; they are not the
// reads worth scaling out.
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
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "password reset store dialect %q", d)
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
		secretBytes: o.secretBytes,
		o11y:        observability.NewObserver(serviceName, o.logger, o.tracerProvider),
	}

	// The generated querier, instantiated once the prefix is settled and the
	// dialect is known — the only two things the generated statements do not
	// already carry. What executes is what sqlc analyzed, with one marker
	// substitution; see internal/passwordresetdb.
	qd, err := querierDialect(d)
	if err != nil {
		return nil, err
	}

	// The table's name lives nowhere else in this package: the canonical
	// spelling is internal/queries' and the separator is database/ddl's, so a
	// namespaced deployment cannot end up with two renderings of one name.
	if s.q, err = passwordresetdb.New(qd, ddl.Qualify(cfg.TablePrefix)); err != nil {
		return nil, platformerrors.Wrap(err, "building the password reset token querier")
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
func querierDialect(d dialect.Dialect) (passwordresetdb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return passwordresetdb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return passwordresetdb.DialectMySQL, nil
	case dialect.SQLite:
		return passwordresetdb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported,
			"no generated password reset token queries for dialect %q", d)
	}
}

// Digest renders what the token_digest column holds for a secret.
//
// It is exported for the one caller the Store seam cannot serve: a deployment
// migrating off a hand-written table, which has to write the new column from
// tokens it is holding, and a test asserting that the raw value is nowhere in
// the row. It is not a verification — comparing its output to a column by hand
// is how the single-use guarantee gets reimplemented badly — and it is not
// reversible, so a caller holding one of these holds nothing.
func (s *SQLStore) Digest(secret string) string {
	return hashing.HexString(s.hasher, secret)
}

// Issue mints a token for a principal, stores its digest, and returns the secret
// exactly once.
//
// The row lands with tx. Mail the secret after the commit, not before it: a
// link sent for a transaction that then rolled back is a reset nobody can
// complete.
func (s *SQLStore) Issue(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID string,
	ttl time.Duration,
) (*Issuance, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := scope.Validate(); err != nil {
		return nil, err
	}

	if userID == "" {
		return nil, ErrEmptyUserID
	}

	if ttl <= 0 {
		return nil, ErrNonPositiveLifetime
	}

	op.SetValues(map[string]any{userKey: userID, scopeKey: scope.String()})

	secret, err := s.generator.GenerateBase64EncodedString(ctx, s.secretBytes)
	if err != nil {
		return nil, op.Error(err, "generating password reset token")
	}

	now := s.clock.Now().UTC()

	token := &Token{
		ID:        identifiers.New(),
		Scope:     scope,
		UserID:    userID,
		CreatedAt: now,
		ExpiresAt: now.Add(ttl),
	}

	if err = s.q.InsertToken(ctx, tx, passwordresetdb.InsertTokenParams{
		ID:            token.ID,
		Scope:         token.Scope,
		BelongsToUser: token.UserID,
		TokenDigest:   s.Digest(secret),
		ExpiresAt:     token.ExpiresAt,
		CreatedAt:     token.CreatedAt,
	}); err != nil {
		return nil, op.Error(err, "storing password reset token row")
	}

	op.Set(tokenKey, token.ID)

	return &Issuance{Token: token, Secret: secret}, nil
}

// Verify resolves a secret to its token without spending it.
//
// Called with the Tx a redemption is about to run in, it reads that
// transaction's own writes; called with Client.Writer(), it is the page load
// that precedes the form.
func (s *SQLStore) Verify(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	secret string,
) (*Token, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := s.validateLookup(scope, secret); err != nil {
		return nil, err
	}

	op.Set(scopeKey, scope.String())

	token, err := s.read(ctx, q, scope, secret)
	if err != nil {
		if isTokenError(err) {
			return nil, err
		}

		return nil, op.Error(err, "reading password reset token row")
	}

	observe(op, token)

	if err = s.liveness(token); err != nil {
		return nil, err
	}

	return token, nil
}

// Consume spends a secret and returns the token it spent.
//
// The read and the redemption both run in tx, and it is the redemption that
// decides the answer. Two requests answering the same link at the same instant,
// in two transactions, both read the row live; the second one's update finds
// redeemed_at already set and reports no rows, so exactly one of them is handed
// the token and the other is told it has been spent. A read that decided, with
// an update afterwards, would hand it to both.
//
// What closes the window entirely is the password write joining that same
// transaction, which is why the Tx is an argument rather than something this
// method opens for itself. A store that opened its own would leave the caller's
// password write outside it, and the gap between the two commits is the one this
// package exists to remove.
func (s *SQLStore) Consume(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	secret string,
) (*Token, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := s.validateLookup(scope, secret); err != nil {
		return nil, err
	}

	op.Set(scopeKey, scope.String())

	token, err := s.redeem(ctx, tx, scope, secret)
	if err != nil {
		if isTokenError(err) {
			return nil, err
		}

		return nil, op.Error(err, "consuming password reset token")
	}

	observe(op, token)

	return token, nil
}

// RevokeForUser destroys every unredeemed token a principal holds.
//
// It runs in tx, so the reset that supersedes those links and the removal of
// them are one fact rather than two.
func (s *SQLStore) RevokeForUser(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID string,
) (int64, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := scope.Validate(); err != nil {
		return 0, err
	}

	if userID == "" {
		return 0, ErrEmptyUserID
	}

	op.SetValues(map[string]any{userKey: userID, scopeKey: scope.String()})

	revoked, err := s.q.RevokeTokensForUser(ctx, tx, passwordresetdb.RevokeTokensForUserParams{
		Scope:         scope,
		BelongsToUser: userID,
	})
	if err != nil {
		return 0, op.Error(err, "revoking password reset token rows")
	}

	return revoked, nil
}

// redeem reads a token and stamps its redemption, reporting ErrTokenRedeemed
// when the stamp finds nothing to write.
//
// It takes the narrower type where read takes the wider one, and has only ever
// had one caller. Two statements decide one thing here, and the second's
// affected-row count is only an answer if the first ran against the same
// transaction — so this is the helper that is not correct in either context.
func (s *SQLStore) redeem(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	secret string,
) (*Token, error) {
	token, err := s.read(ctx, tx, scope, secret)
	if err != nil {
		return nil, err
	}

	// Checked before the write rather than folded into its predicate. The
	// alternative is a guard comparing expires_at against the server's clock,
	// which would make the boundary a different comparison on each of the three
	// engines and would collapse "expired" and "already redeemed" into one
	// affected-row count of zero.
	if err = s.liveness(token); err != nil {
		return nil, err
	}

	at := s.clock.Now().UTC()

	// The count is the answer, which is why the statement is annotated
	// :execrows. A driver that declines to report it reaches this as an error
	// rather than as an acknowledged unknown — the generated method has no seam
	// between running the statement and reading the count — and that is the
	// right reading here: a redemption whose count is unreadable cannot say who
	// spent the token, and reporting zero would say somebody else did.
	affected, err := s.q.RedeemToken(ctx, tx, passwordresetdb.RedeemTokenParams{
		RedeemedAt: &at,
		ID:         token.ID,
	})
	if err != nil {
		return nil, platformerrors.Wrap(err, "redeeming password reset token row")
	}

	if affected == 0 {
		return nil, ErrTokenRedeemed
	}

	token.RedeemedAt = &at

	return token, nil
}

// read resolves a secret to its row, reporting ErrTokenNotFound when the digest
// matches nothing in scope.
func (s *SQLStore) read(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	secret string,
) (*Token, error) {
	row, err := s.q.GetTokenByDigest(ctx, q, passwordresetdb.GetTokenByDigestParams{
		TokenDigest: s.Digest(secret),
		Scope:       scope,
	})
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			return nil, ErrTokenNotFound
		}

		return nil, platformerrors.Wrap(err, "reading password reset token row")
	}

	token := &Token{
		ID:        row.ID,
		Scope:     row.Scope,
		UserID:    row.BelongsToUser,
		ExpiresAt: row.ExpiresAt.UTC(),
		CreatedAt: row.CreatedAt.UTC(),
	}

	// Converted here rather than left as the driver chose. A location is not
	// the instant, so nothing this package compares would change — but a Token
	// is handed to a caller who prints it, serializes it, and shows it to
	// somebody, and a timestamp that reads differently on Postgres than on
	// SQLite is a difference in this package's output rather than in a driver's.
	if row.RedeemedAt != nil {
		utc := row.RedeemedAt.UTC()
		token.RedeemedAt = &utc
	}

	return token, nil
}

// liveness reports why a token cannot be spent, or nil when it can.
//
// The boundary itself is Token.Live's, deliberately, and this only names which
// half of it failed: a store comparing the deadline here as well would be a
// second copy of the comparison an administrative view renders from, free to
// disagree with it about the last second of a link's life.
//
// Redemption is reported before expiry, so a link somebody used yesterday says
// so rather than reporting the deadline it has since passed — which is the more
// useful of the two answers and the one a support conversation turns on.
func (s *SQLStore) liveness(token *Token) error {
	if token.Live(s.clock.Now()) {
		return nil
	}

	if token.RedeemedAt != nil {
		return ErrTokenRedeemed
	}

	return ErrTokenExpired
}

// validateLookup rejects a call that named no scope or no token before it
// reaches the database.
func (s *SQLStore) validateLookup(scope tenancy.Scope, secret string) error {
	if err := scope.Validate(); err != nil {
		return err
	}

	if secret == "" {
		return ErrEmptySecret
	}

	return nil
}

// observe records which row and whose reset an operation touched. The secret is
// not among them, and neither is its digest.
func observe(op observability.Operation, token *Token) {
	op.SetValues(map[string]any{tokenKey: token.ID, userKey: token.UserID})
}

// isTokenError reports whether err is one of the three outcomes a caller reads
// rather than an outcome an operator does.
//
// They are returned bare rather than through op.Error because none of them is a
// failure: a user following an expired link is the flow working. Marking those
// spans as errors would put the most routine event in this package at the top of
// an error dashboard, and would bury the driver failures that belong there.
func isTokenError(err error) bool {
	return stderrors.Is(err, ErrTokenNotFound) ||
		stderrors.Is(err, ErrTokenExpired) ||
		stderrors.Is(err, ErrTokenRedeemed)
}

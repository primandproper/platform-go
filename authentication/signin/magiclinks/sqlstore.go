package magiclinks

import (
	"context"
	"database/sql"
	stderrors "errors"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/magiclinks/internal/magiclinkdb"
	"github.com/primandproper/platform-go/v14/authentication/signin/magiclinks/migrations"

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
const serviceName = "signin_magic_links"

// The keys this store records against a span and a log line.
//
// None of them is the secret, and none of them can be turned into it — the hash
// is not among them either, because a digest is still a name for a live
// credential and a trace is a place operators paste into tickets. What a trace
// shows is whose directory, which person, and why a refusal was a refusal.
const (
	subjectKey = "signin.subject_id"
	scopeKey   = "identity.scope"

	// reasonKey records which guard refused a redemption. It is on the span only,
	// and it is the whole reason this store reads the row back after a write that
	// matched nothing: the caller is told one sentinel for four refusals, and
	// this is where the difference survives. It is the same key signin's own
	// doors record a refusal under.
	reasonKey = "signin.reason"

	// revokedKey is how many rows a revocation withdrew. On the span only: it is
	// a fact about one request rather than a metric, and a log line carrying it
	// beside a user id for every erasure would be a count nobody reads.
	revokedKey = "signin.revoked"
)

// The reasons a redemption can be refused, as a span records them.
//
// They are spelled here rather than at the comparison because the set is what a
// dashboard groups by, and a fifth spelling invented at a call site would be a
// series nobody is looking at.
const (
	reasonRedeemed = "already redeemed"
	reasonRevoked  = "revoked"
	reasonExpired  = "expired"
	reasonUnknown  = "no such link"
)

// DefaultTablePrefix is the namespace the sign-in link table carries when none
// is configured, which is none — rendering plain "signin_magic_links".
//
// The signin_magic_links segment is the schema's, not the caller's: a table
// always says which package created it. Setting a namespace of "ddb" renders
// ddb_signin_magic_links, for a database shared between applications. A
// namespace must not end in '_'; database/ddl supplies the separator.
const DefaultTablePrefix = ""

var _ signin.MagicLinkStore = (*SQLStore)(nil)

// SQLStore keeps sign-in links in a SQL table, against the schema
// magiclinks/migrations renders.
//
// It is exported, and returned by NewSQLStore, so a caller who has chosen SQL
// storage can depend on that choice rather than on the signin.MagicLinkStore
// seam. It does two things more than that interface describes — Sweep removes
// rows past their purge deadline, and Digest renders the column a stored link is
// found by — and neither belongs on it: a store backed by something that expires
// its own entries needs no sweep, and a store that is not a table has no column.
type SQLStore struct {
	// db is not what the three writes run on — those are handed the caller's
	// transaction. It is here for the two things a Client answers that a Tx
	// cannot: the dialect the generated statements are rendered for, read once at
	// construction, and the executor Sweep runs on, which belongs to nobody's
	// request and so can join nobody's transaction.
	db        database.Client
	q         magiclinkdb.Querier
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
// The client is not what the writes execute on. Issue, Redeem and
// RevokeForSubject take the caller's database.Tx; what this one supplies is the
// dialect the generated statements are rendered for and the executor Sweep runs
// on, which serves the store's own machinery rather than a request.
//
// There is no read here that a replica could serve. The one read this store
// makes is the redemption's read-back, on the transaction that just wrote — see
// Redeem — so nothing in this package ever touches Client.Reader(), and replica
// lag cannot turn a freshly minted link into one that is "not found" and then
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
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "sign-in link store dialect %q", d)
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
	// substitution; see internal/magiclinkdb.
	qd, err := querierDialect(d)
	if err != nil {
		return nil, err
	}

	// The table's name lives nowhere else in this package: the canonical spelling
	// is internal/queries' and the separator is database/ddl's, so a namespaced
	// deployment cannot end up with two renderings of one name.
	if s.q, err = magiclinkdb.New(qd, ddl.Qualify(cfg.TablePrefix)); err != nil {
		return nil, platformerrors.Wrap(err, "building the sign-in link querier")
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
func querierDialect(d dialect.Dialect) (magiclinkdb.Dialect, error) {
	switch d {
	case dialect.Postgres:
		return magiclinkdb.DialectPostgreSQL, nil
	case dialect.MySQL:
		return magiclinkdb.DialectMySQL, nil
	case dialect.SQLite:
		return magiclinkdb.DialectSQLite, nil
	default:
		return "", platformerrors.Wrapf(dialect.ErrUnsupported,
			"no generated sign-in link queries for dialect %q", d)
	}
}

// Digest renders what the hash column holds for a secret.
//
// It is exported for the one caller the seam cannot serve: a test asserting that
// the raw value is nowhere in the row. It is not a verification — comparing its
// output to a column by hand is how the single-use guarantee gets reimplemented
// badly — and it is not reversible, so a caller holding one of these holds
// nothing.
func (s *SQLStore) Digest(secret string) string {
	return hashing.HexString(s.hasher, secret)
}

// Issue mints a sign-in link, stores its digest, and returns the secret exactly
// once.
//
// The row lands with tx. Hand the secret to a mailer after the commit, not from
// inside the callback: a link mailed for a transaction that then rolled back is
// a URL in somebody's inbox that will never redeem, and this store has no way to
// take it back.
//
// The address on the request is written down as it is given, and this package
// neither folds it nor reads anything from it — it is the caller's record of
// where the mail went, kept so that a redemption can be refused when the subject
// no longer holds it. Give it the same form the comparison at redemption will
// use, which for signin means the directory's folded handle.
func (s *SQLStore) Issue(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	request *signin.MagicLinkRequest,
) (*signin.MagicLinkIssuance, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := s.validateMint(scope, request); err != nil {
		return nil, err
	}

	op.SetValues(map[string]any{
		scopeKey:   scope.String(),
		subjectKey: request.SubjectID,
	})

	secret, err := s.generator.GenerateBase64EncodedString(ctx, s.secretBytes)
	if err != nil {
		return nil, op.Error(err, "generating sign-in link token")
	}

	now := s.clock.Now().UTC()

	link := &signin.MagicLink{
		Scope:        scope,
		SubjectID:    request.SubjectID,
		EmailAddress: request.EmailAddress,
		IssuedAt:     now,
		ExpiresAt:    now.Add(request.TTL),
		PurgeAfter:   now.Add(request.TTL).Add(s.retention),
	}

	if err = s.q.InsertMagicLink(ctx, tx, magiclinkdb.InsertMagicLinkParams{
		Hash:         s.Digest(secret),
		Scope:        link.Scope,
		SubjectID:    link.SubjectID,
		EmailAddress: link.EmailAddress,
		IssuedAt:     link.IssuedAt,
		ExpiresAt:    link.ExpiresAt,
		PurgeAfter:   link.PurgeAfter,
	}); err != nil {
		return nil, op.Error(err, "storing sign-in link row")
	}

	return &signin.MagicLinkIssuance{Link: link, Secret: secret}, nil
}

// Redeem spends a secret and answers with the link it spent.
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
// The second is a read of the row on the same transaction, and it runs on both
// paths for two different reasons. On the winning path it is the only way the
// subject and the address are learned at all — the row is what says who is
// signing in and where the mail went, and MySQL has no RETURNING to hand either
// back from the write. On the losing path it is what
// turns an ambiguous zero into something an operator can read: expired, already
// followed, withdrawn, or no such row, four facts behind one number.
//
// None of those four reaches the caller. Every refusal is
// signin.ErrInvalidMagicLink, for the reason the password door collapses its
// four — told apart, they are an oracle for whoever is presenting guesses, and
// the remedy is the same in every case.
//
// # What it deliberately does not do
//
// It does not withdraw the subject's other outstanding links. A link that is
// still in an inbox is exposed exactly as much after this succeeds as it was
// before, and burning it would cost somebody the second mail they asked for
// while closing nothing. RevokeForSubject is the caller for the cases where
// something did change.
//
// It reports no reuse. A spent sign-in link presented twice is somebody's
// browser retrying, a mail client prefetching a URL, or a link forwarded to a
// colleague — all of which the refresh token store beside this one would treat
// as a detected theft, because there the second presentation implies a stolen
// credential in active use. Here there is no family to end and no successor to
// have been stolen: the link is spent, the sign-in it produced is governed by
// the refresh token that sign-in minted, and that store does the reuse
// detection.
func (s *SQLStore) Redeem(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	secret string,
) (*signin.MagicLink, error) {
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
	// redemption whose count is unreadable cannot say who spent the link, and
	// reporting zero would say somebody else did.
	affected, err := s.q.RedeemMagicLink(ctx, tx, magiclinkdb.RedeemMagicLinkParams{
		RedeemedAt: &now,
		Hash:       hash,
		Scope:      scope,
		Now:        now,
	})
	if err != nil {
		return nil, op.Error(err, "redeeming sign-in link row")
	}

	row, err := s.q.GetMagicLink(ctx, tx, magiclinkdb.GetMagicLinkParams{Hash: hash, Scope: scope})
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			// No row at all, whichever branch we are in. A winning UPDATE cannot
			// reach this, so it is an unknown token or one from another
			// directory — which is the same thing from here.
			op.SpanOnly(reasonKey, reasonUnknown)

			return nil, signin.ErrInvalidMagicLink
		}

		return nil, op.Error(err, "reading sign-in link row")
	}

	link := linkFromRow(&row)

	op.Set(subjectKey, link.SubjectID)

	if affected == 0 {
		op.SpanOnly(reasonKey, refusalReason(link, now))

		return nil, signin.ErrInvalidMagicLink
	}

	// The stamp this call wrote, rather than the one the read-back happened to
	// see. The two are the same value on every engine that shows this
	// transaction its own writes, and this is the one that is this call's answer
	// on every engine.
	link.RedeemedAt = &now

	return link, nil
}

// refusalReason names which guard refused a redemption, for the span.
//
// It is read off the row the guarded write did not match, so it is the row's own
// account of itself rather than a second evaluation of the predicate: the three
// arms are the three guards, in the order the statement lists them. Nothing here
// is told to the caller.
//
// The default arm is reachable only where a row exists, matched none of the
// three, and was still not updated — which the statement's own predicate says is
// impossible. It is named rather than left off so that a fourth guard added to
// the corpus without a reading here is a span that says so.
func refusalReason(link *signin.MagicLink, now time.Time) string {
	switch {
	case link.RedeemedAt != nil:
		return reasonRedeemed
	case link.RevokedAt != nil:
		return reasonRevoked
	case !now.Before(link.ExpiresAt):
		return reasonExpired
	default:
		return reasonUnknown
	}
}

// RevokeForSubject withdraws every outstanding link one person holds and reports
// how many it withdrew.
//
// It runs in tx, so whatever the caller is doing at the same moment — disabling
// an account, erasing a subject — and the withdrawal are one fact rather than
// two that could half happen.
//
// Already-withdrawn rows are left where they are: the statement guards on
// revoked_at being absent, so a second revocation reports zero rather than
// moving the timestamp, and the record still says when the links actually
// stopped working. Rows that lapsed on their own are moved, because this corpus
// carries no liveness predicate outside the redemption — and after an account is
// disabled, "somebody withdrew this" is the more useful of the two true
// sentences.
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

	revoked, err := s.q.RevokeMagicLinksForSubject(ctx, tx, magiclinkdb.RevokeMagicLinksForSubjectParams{
		RevokedAt: &at,
		Scope:     scope,
		SubjectID: subjectID,
	})
	if err != nil {
		return 0, op.Error(err, "withdrawing a subject's sign-in link rows")
	}

	op.SpanOnly(revokedKey, revoked)

	return revoked, nil
}

// validateMint is the argument check both halves of a mint share.
func (s *SQLStore) validateMint(scope tenancy.Scope, request *signin.MagicLinkRequest) error {
	if err := scope.Validate(); err != nil {
		return err
	}

	if request == nil {
		return ErrNilRequest
	}

	if request.SubjectID == "" {
		return ErrEmptySubjectID
	}

	if request.EmailAddress == "" {
		return ErrEmptyEmailAddress
	}

	if request.TTL <= 0 {
		return ErrNonPositiveLifetime
	}

	return nil
}

// validateLookup is the argument check a redemption makes before it hashes.
//
// The secret is checked for emptiness rather than hashed and looked up, because
// the digest of the empty string is a perfectly good digest and would cost a
// round trip to find nothing.
func (s *SQLStore) validateLookup(scope tenancy.Scope, secret string) error {
	if err := scope.Validate(); err != nil {
		return err
	}

	if secret == "" {
		return ErrEmptySecret
	}

	return nil
}

// linkFromRow renders a read row as the value the seam carries.
//
// The hash is not among the columns the read projects — see internal/queries —
// so there is nothing here to drop, and no path by which a stored credential's
// digest leaves this package.
func linkFromRow(row *magiclinkdb.GetMagicLinkRow) *signin.MagicLink {
	return &signin.MagicLink{
		Scope:        row.Scope,
		SubjectID:    row.SubjectID,
		EmailAddress: row.EmailAddress,
		IssuedAt:     row.IssuedAt.UTC(),
		ExpiresAt:    row.ExpiresAt.UTC(),
		PurgeAfter:   row.PurgeAfter.UTC(),
		RedeemedAt:   utcPtr(row.RedeemedAt),
		RevokedAt:    utcPtr(row.RevokedAt),
	}
}

// utcPtr normalises an optional stamp to UTC, leaving an absent one absent.
//
// The two nullable columns come back in whatever zone the driver decided, and a
// consumer comparing one against a UTC clock — which is the only clock this
// module reads — would otherwise be comparing two zones. The three non-null
// stamps are normalised at their assignment above; this is the arm that has to
// survive a nil.
func utcPtr(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	utc := t.UTC()

	return &utc
}

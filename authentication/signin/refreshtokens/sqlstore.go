package refreshtokens

import (
	"context"
	"database/sql"
	stderrors "errors"
	"time"

	"github.com/primandproper/platform-go/v15/authentication/signin"
	"github.com/primandproper/platform-go/v15/authentication/signin/refreshtokens/internal/signindb"
	"github.com/primandproper/platform-go/v15/authentication/signin/refreshtokens/migrations"

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
// always says which package created it. Setting a namespace of "app" renders
// app_signin_refresh_tokens, for a database shared between applications. A
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
	// db is not what the writes run on — those are handed the caller's
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
// EndSignIns take the caller's database.Tx; what this one supplies is the
// dialect the generated statements are rendered for and the executor Sweep runs
// on, which serves the store's own machinery rather than a request.
//
// The exchange's read-back runs on the transaction that just wrote — see Redeem
// — so replica lag cannot turn a freshly minted token into one that is "not
// found" and then works when retried. The reads a replica may serve are
// ListActiveSignIns and LiveToken, which take whatever executor their caller
// hands them, and nothing in this package reaches for Client.Reader() on its
// own.
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

	// A mint that begins a login has no earlier instant to inherit, so the
	// login began now; a successor carries its family's forward.
	signedInAt := request.SignedInAt.UTC()
	if request.SignedInAt.IsZero() {
		signedInAt = now
	}

	token := &signin.RefreshToken{
		Scope:           scope,
		FamilyID:        request.FamilyID,
		SubjectID:       request.SubjectID,
		ActiveAccountID: request.ActiveAccountID,
		Administrative:  request.Administrative,
		IssuedAt:        now,
		SignedInAt:      signedInAt,
		ExpiresAt:       now.Add(request.TTL),
		PurgeAfter:      now.Add(request.TTL).Add(s.retention),
		AccessTokenID:   request.AccessTokenID,
		ActorID:         request.ActorID,
		CredentialKind:  request.CredentialKind,
	}

	if err = s.q.InsertRefreshToken(ctx, tx, signindb.InsertRefreshTokenParams{
		Hash:            s.Digest(secret),
		Scope:           token.Scope,
		FamilyID:        token.FamilyID,
		SubjectID:       token.SubjectID,
		ActiveAccountID: token.ActiveAccountID,
		Administrative:  token.Administrative,
		IssuedAt:        token.IssuedAt,
		SignedInAt:      token.SignedInAt,
		ExpiresAt:       token.ExpiresAt,
		PurgeAfter:      token.PurgeAfter,
		AccessTokenID:   optionalString(token.AccessTokenID),
		ActorID:         optionalString(token.ActorID),
		CredentialKind:  optionalString(string(token.CredentialKind)),
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

	// What the refusal reports as ended is what was live when the revocation
	// ran, read off the rows it locked, and not a successor an exchange racing
	// this one minted after a read.
	ended, err := s.endFamily(ctx, tx, token.Scope, token.FamilyID, false)
	if err != nil {
		// The revocation is the half that matters, so a failure to run it is
		// reported as itself rather than collapsed into the refusal. Joining the
		// two would let a caller match ErrRefreshTokenReused and conclude the
		// family had been ended.
		return op.Error(err, "revoking a reused refresh token's family")
	}

	// The whole family is revoked whether or not it was live — a lapsed
	// family's spent rows are stamped as withdrawn, which after a detected
	// reuse is the more useful of the two true sentences — but it is reported
	// as ended only if it was.
	return &signin.RefreshTokenReusedError{
		FamilyID:  token.FamilyID,
		SubjectID: token.SubjectID,
		Ended:     ended.live,
	}
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

	ended, err := s.endFamily(ctx, tx, scope, familyID, false)
	if err != nil {
		return 0, op.Error(err, "revoking a refresh token family's rows")
	}

	op.SpanOnly(revokedKey, ended.revoked)

	return ended.revoked, nil
}

// maxFamilyRows is the most unrevoked rows endFamily will lock by key in one
// family. A family holds one row per exchange its login has made since its
// oldest row was swept, so this is far past any login a person keeps; one
// longer than it takes the fallback rather than an IN list of that length.
const maxFamilyRows = int64(1000)

// endedFamily is what endFamily did to one family: how many rows it withdrew,
// and whether the family was live when it did.
type endedFamily struct {
	revoked int64
	live    bool
}

// endFamily withdraws every unrevoked row of one family, locking them by
// primary key rather than by the family's range, and says whether the family
// was live when it did.
//
// It reads the family's rows without a lock, locks those rows by key, and
// compares: rows as they were mean the read saw the whole family — nothing can
// join a family without writing a row already in it — and they are revoked by
// key. Rows that moved mean an exchange or a retry's re-mint wrote the family
// after the read, possibly adding a successor the read could not see, and the
// family is read again by range and revoked by its id, which is what this store
// did for every revocation before. See internal/queries' readFamilyRows for
// why the range is the fallback rather than the rule: on MySQL it locks the gap
// a new login's row is inserted into, and a revocation holding that gap while
// it waits on an audit chain's head deadlocks with a sign-in that took the head
// first.
//
// liveOnly is EndSignIns' reading: a family that is no longer live when this
// runs was ended by somebody else and is left alone. RevokeFamily's reading is
// the other: the whole family is withdrawn either way.
func (s *SQLStore) endFamily(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	familyID string,
	liveOnly bool,
) (*endedFamily, error) {
	now := s.clock.Now().UTC()

	read, err := s.q.ReadRefreshTokenFamilyRows(ctx, tx, signindb.ReadRefreshTokenFamilyRowsParams{
		Scope:       scope,
		FamilyID:    familyID,
		ResultLimit: maxFamilyRows,
	})
	if err != nil {
		return nil, platformerrors.Wrap(err, "reading a refresh token family's rows")
	}

	if len(read) == 0 {
		return &endedFamily{}, nil
	}

	if int64(len(read)) == maxFamilyRows {
		return s.endFamilyByRange(ctx, tx, scope, familyID, now, liveOnly)
	}

	hashes := make([]string, 0, len(read))
	for i := range read {
		hashes = append(hashes, read[i].Hash)
	}

	locked, err := s.q.LockRefreshTokens(ctx, tx, signindb.LockRefreshTokensParams{Scope: scope, Hashes: hashes})
	if err != nil {
		return nil, platformerrors.Wrap(err, "locking a refresh token family's rows")
	}

	if moved(read, locked) {
		return s.endFamilyByRange(ctx, tx, scope, familyID, now, liveOnly)
	}

	ended := &endedFamily{}

	for i := range locked {
		if locked[i].RedeemedAt == nil && locked[i].RevokedAt == nil && locked[i].ExpiresAt.After(now) {
			ended.live = true
		}
	}

	if liveOnly && !ended.live {
		return ended, nil
	}

	if ended.revoked, err = s.q.RevokeRefreshTokens(ctx, tx, signindb.RevokeRefreshTokensParams{
		RevokedAt: &now,
		Scope:     scope,
		Hashes:    hashes,
	}); err != nil {
		return nil, platformerrors.Wrap(err, "revoking a refresh token family's rows by key")
	}

	return ended, nil
}

// endFamilyByRange is endFamily's fallback, and this store's revocation as it
// was before it locked by key: the family's live row locked by the family's
// range, and the family revoked by its id. It is reached only by a revocation
// that raced a write to the same family, or one whose family is too long to
// name by key.
func (s *SQLStore) endFamilyByRange(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	familyID string,
	now time.Time,
	liveOnly bool,
) (*endedFamily, error) {
	live, err := s.q.LockLiveRefreshTokenFamily(ctx, tx, signindb.LockLiveRefreshTokenFamilyParams{
		Scope:       scope,
		FamilyID:    familyID,
		Now:         now,
		ResultLimit: endBatch,
	})
	if err != nil {
		return nil, platformerrors.Wrap(err, "locking a refresh token family's live row")
	}

	ended := &endedFamily{live: len(live) > 0}

	if liveOnly && !ended.live {
		return ended, nil
	}

	if ended.revoked, err = s.q.RevokeRefreshTokenFamily(ctx, tx, signindb.RevokeRefreshTokenFamilyParams{
		RevokedAt: &now,
		Scope:     scope,
		FamilyID:  familyID,
	}); err != nil {
		return nil, platformerrors.Wrap(err, "revoking a refresh token family by its id")
	}

	return ended, nil
}

// moved reports whether any row a revocation read without a lock is not, now
// that it is locked, what the read said it was.
func moved(read []signindb.ReadRefreshTokenFamilyRowsRow, locked []signindb.LockRefreshTokensRow) bool {
	if len(read) != len(locked) {
		return true
	}

	now := make(map[string]signindb.LockRefreshTokensRow, len(locked))
	for i := range locked {
		now[locked[i].Hash] = locked[i]
	}

	for i := range read {
		row, ok := now[read[i].Hash]
		if !ok ||
			!sameInstant(read[i].RedeemedAt, row.RedeemedAt) ||
			!sameInstant(read[i].RevokedAt, row.RevokedAt) ||
			!sameString(read[i].RedeemedWithKey, row.RedeemedWithKey) ||
			!sameString(read[i].SuccessorHash, row.SuccessorHash) {
			return true
		}
	}

	return false
}

func sameInstant(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}

	return a.Equal(*b)
}

func sameString(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}

	return *a == *b
}

// endBatch is how many families one pass of EndSignIns reads and revokes.
//
// It bounds the rows one read returns and one round trip carries, not
// what a call ends: EndSignIns runs passes until one comes back short. It is
// signin.MaxSignInListLimit, the most logins a person is ever shown, so a
// person who could see every login they hold on one screen is signed out of
// all of them in one pass.
const endBatch = int64(signin.MaxSignInListLimit)

// EndSignIns ends the live logins selector names, and answers with the ones it
// ended.
//
// Each pass reads up to endBatch of the selected families' live rows without a
// lock, and ends each family by key with endFamily, all on tx; a pass that
// comes back full is followed by another, which finds the families the last one
// did not reach because a revoked family is no longer live. See internal/queries'
// selectFamiliesForSubject for why the read comes first, and readFamilyRows for
// why nothing here locks by range.
//
// A login already over — spent, revoked or lapsed — is not selected and not
// reported. A lapsed family's rows are left unstamped, where the subject-wide
// revocation this replaced stamped them: they can no longer be exchanged, and a
// later replay of one of its spent tokens stamps the family then.
func (s *SQLStore) EndSignIns(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	selector signin.SignInSelector,
) ([]*signin.EndedSignIn, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := scope.Validate(); err != nil {
		return nil, err
	}

	if selector.SubjectID == "" && selector.FamilyID == "" {
		return nil, ErrEmptySelector
	}

	// A login spared from a selection of one login, or from a selection of
	// nobody's, is two instructions that disagree; neither reading is safe to
	// guess.
	if selector.ExceptFamilyID != "" && (selector.SubjectID == "" || selector.FamilyID != "") {
		return nil, ErrContradictorySelector
	}

	op.SetValues(map[string]any{scopeKey: scope.String(), subjectKey: selector.SubjectID, familyKey: selector.FamilyID})

	var (
		ended   []*signin.EndedSignIn
		revoked int64
	)

	// The offset counts the candidates found already over: a family this pass
	// ends is no longer live and leaves the next page, but one somebody else
	// ended after this transaction's snapshot is still in it, and still
	// ordered ahead of every family no pass has reached yet.
	for offset := int64(0); ; {
		candidates, err := s.selectLive(ctx, tx, scope, selector, offset)
		if err != nil {
			return nil, op.Error(err, "selecting the live refresh token families a revocation ends")
		}

		for _, candidate := range candidates {
			family, endErr := s.endFamily(ctx, tx, scope, candidate.FamilyID, true)
			if endErr != nil {
				return nil, op.Error(endErr, "revoking a live refresh token family")
			}

			if !family.live {
				offset++

				continue
			}

			candidate.Revoked = family.revoked
			revoked += family.revoked
			ended = append(ended, candidate)
		}

		if int64(len(candidates)) < endBatch {
			break
		}
	}

	op.SpanOnly(revokedKey, revoked)

	return ended, nil
}

// selectLive runs whichever of the candidate reads selector names, from
// offset, and answers with one EndedSignIn per live family it found, its count
// still to come. Nothing it reads is locked; endFamily locks each family by key.
func (s *SQLStore) selectLive(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	selector signin.SignInSelector,
	offset int64,
) ([]*signin.EndedSignIn, error) {
	now := s.clock.Now().UTC()

	var live []*signin.EndedSignIn

	switch {
	case selector.ExceptFamilyID != "":
		rows, err := s.q.SelectOtherLiveRefreshTokenFamiliesForSubject(ctx, tx, signindb.SelectOtherLiveRefreshTokenFamiliesForSubjectParams{
			Scope:        scope,
			SubjectID:    selector.SubjectID,
			KeepFamilyID: selector.ExceptFamilyID,
			Now:          now,
			ResultLimit:  endBatch,
			ResultOffset: offset,
		})
		if err != nil {
			return nil, err
		}

		for i := range rows {
			live = append(live, &signin.EndedSignIn{FamilyID: rows[i].FamilyID, SubjectID: rows[i].SubjectID})
		}
	case selector.SubjectID != "" && selector.FamilyID != "":
		rows, err := s.q.SelectLiveRefreshTokenFamilyForSubject(ctx, tx, signindb.SelectLiveRefreshTokenFamilyForSubjectParams{
			Scope:        scope,
			SubjectID:    selector.SubjectID,
			FamilyID:     selector.FamilyID,
			Now:          now,
			ResultLimit:  endBatch,
			ResultOffset: offset,
		})
		if err != nil {
			return nil, err
		}

		for i := range rows {
			live = append(live, &signin.EndedSignIn{FamilyID: rows[i].FamilyID, SubjectID: rows[i].SubjectID})
		}
	case selector.FamilyID != "":
		rows, err := s.q.SelectLiveRefreshTokenFamily(ctx, tx, signindb.SelectLiveRefreshTokenFamilyParams{
			Scope:        scope,
			FamilyID:     selector.FamilyID,
			Now:          now,
			ResultLimit:  endBatch,
			ResultOffset: offset,
		})
		if err != nil {
			return nil, err
		}

		for i := range rows {
			live = append(live, &signin.EndedSignIn{FamilyID: rows[i].FamilyID, SubjectID: rows[i].SubjectID})
		}
	default:
		rows, err := s.q.SelectLiveRefreshTokenFamiliesForSubject(ctx, tx, signindb.SelectLiveRefreshTokenFamiliesForSubjectParams{
			Scope:        scope,
			SubjectID:    selector.SubjectID,
			Now:          now,
			ResultLimit:  endBatch,
			ResultOffset: offset,
		})
		if err != nil {
			return nil, err
		}

		for i := range rows {
			live = append(live, &signin.EndedSignIn{FamilyID: rows[i].FamilyID, SubjectID: rows[i].SubjectID})
		}
	}

	return live, nil
}

// ListActiveSignIns answers one entry per live login a subject holds, most
// recently refreshed first, and no more than limit of them.
//
// Each login is its family's one live row — unspent, unrevoked, unexpired —
// under the same three guards the exchange carries, compared against this
// store's own clock for the reason the exchange's deadline is. A zero limit is
// refused rather than read as "none" or as "all": the service resolves its
// default before it calls, and a store that invented one would be a second
// place that policy lived.
func (s *SQLStore) ListActiveSignIns(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	subjectID string,
	limit uint16,
) ([]*signin.ActiveSignIn, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := scope.Validate(); err != nil {
		return nil, err
	}

	if q == nil {
		return nil, ErrNilExecutor
	}

	if subjectID == "" {
		return nil, ErrEmptySubjectID
	}

	if limit == 0 {
		return nil, ErrZeroLimit
	}

	op.SetValues(map[string]any{scopeKey: scope.String(), subjectKey: subjectID})

	rows, err := s.q.ListLiveRefreshTokenFamilies(ctx, q, signindb.ListLiveRefreshTokenFamiliesParams{
		Scope:       scope,
		SubjectID:   subjectID,
		Now:         s.clock.Now().UTC(),
		ResultLimit: int64(limit),
	})
	if err != nil {
		return nil, op.Error(err, "listing a subject's live refresh token families")
	}

	signIns := make([]*signin.ActiveSignIn, 0, len(rows))
	for i := range rows {
		signIns = append(signIns, &signin.ActiveSignIn{
			FamilyID:        rows[i].FamilyID,
			ActiveAccountID: rows[i].ActiveAccountID,
			Administrative:  rows[i].Administrative,
			SignedInAt:      rows[i].SignedInAt.UTC(),
			LastRefreshedAt: rows[i].IssuedAt.UTC(),
			ExpiresAt:       rows[i].ExpiresAt.UTC(),
		})

		if rows[i].ActorID != nil {
			signIns[len(signIns)-1].ActorID = *rows[i].ActorID
		}

		if rows[i].CredentialKind != nil {
			signIns[len(signIns)-1].CredentialKind = signin.CredentialKind(*rows[i].CredentialKind)
		}
	}

	return signIns, nil
}

// LiveToken answers one login's current refresh token, named by its family, or
// signin.ErrSignInEnded where it has none.
//
// It is the listing's three guards read by family rather than by subject, and
// against this store's own clock for the reason the exchange's deadline is, so
// the row it answers is the row an exchange would still accept. The family index
// serves it, which matters because a consumer may make this
// read on every request.
//
// A family unknown in scope reads exactly as one that ended, because from here
// it is one: nothing it could present would be accepted.
func (s *SQLStore) LiveToken(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	familyID string,
) (*signin.RefreshToken, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := scope.Validate(); err != nil {
		return nil, err
	}

	if q == nil {
		return nil, ErrNilExecutor
	}

	if familyID == "" {
		return nil, ErrEmptyFamilyID
	}

	op.SetValues(map[string]any{scopeKey: scope.String(), familyKey: familyID})

	row, err := s.q.GetLiveRefreshTokenForFamily(ctx, q, signindb.GetLiveRefreshTokenForFamilyParams{
		Scope:    scope,
		FamilyID: familyID,
		Now:      s.clock.Now().UTC(),
	})
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			return nil, signin.ErrSignInEnded
		}

		return nil, op.Error(err, "reading a refresh token family's live row")
	}

	live := signindb.GetRefreshTokenRow(row)

	return tokenFromRow(&live), nil
}

// optionalString is the bound value of a nullable text column: NULL for the
// empty string, which is how a caller with nothing to record says so.
func optionalString(value string) *string {
	if value == "" {
		return nil
	}

	return &value
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
		SignedInAt:      row.SignedInAt.UTC(),
		ExpiresAt:       row.ExpiresAt.UTC(),
		PurgeAfter:      row.PurgeAfter.UTC(),
	}

	if row.AccessTokenID != nil {
		token.AccessTokenID = *row.AccessTokenID
	}

	if row.ActorID != nil {
		token.ActorID = *row.ActorID
	}

	if row.CredentialKind != nil {
		token.CredentialKind = signin.CredentialKind(*row.CredentialKind)
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

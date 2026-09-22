package refreshtokens

import (
	"context"
	"database/sql"
	stderrors "errors"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens/internal/signindb"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/idempotency"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

var _ signin.IdempotentRefreshTokenStore = (*SQLStore)(nil)

// RemintGrace is how long after an exchange this store will honor a retry of
// it presented with the key that spent the token.
//
// Ten minutes, and it is the weaker of the two bounds rather than the main one.
// The condition doing the real work is that the successor is still unspent: a
// client that used its new token has demonstrably received it, and the window
// closes at that moment rather than on a clock. This is the backstop for the
// case that never makes progress, and it is sized for an app that was
// backgrounded and reopened rather than for a dropped packet — a minute would
// cover the network and refuse the phone.
//
// It is not configurable, and that is a decision rather than an omission. What a
// consumer would be choosing is how long a captured exchange request stays worth
// something, and the answer that makes the choice safe to get wrong is the one
// beside it: a key buys exactly one re-mint, so lengthening this window does not
// multiply what an attacker can take out of a replay. Nothing is left for a
// deployment to tune, so there is nothing for it to tune badly.
const RemintGrace = 10 * time.Minute

// MaximumIdempotencyKeyLength is the longest key this store will accept.
//
// It is the width of the column on MySQL, where it is load-bearing rather than
// generous: an over-long value there is truncated on write and reported as a
// success, so two keys agreeing in their first 255 bytes would become one stored
// key and a retry's evidence would match a request that never sent it. Refusing
// in Go is what keeps the column's width from being a silent equality rule.
const MaximumIdempotencyKeyLength = 255

// remintKey records on the span that a retry was honored — the spend that did
// not happen, and the successor that was replaced.
//
// On the span only, and never as a metric. It is a fact about one request, and a
// counter of it would be a rate of dropped responses reported per process rather
// than per client, which is not the question anybody has when they look.
const remintKey = "signin.refresh_reminted"

// ErrSuccessorNotRecorded indicates RecordSuccessor named a predecessor this
// store holds no row for.
//
// It is unreachable through Service.ExchangeRefreshToken, which records against
// the row the same transaction has just spent, and it is a refusal rather than a
// silence anyway: a successor nothing recorded is a spent row that cannot name
// what it minted, which turns that client's next retry into a revoked family.
var ErrSuccessorNotRecorded = platformerrors.Wrap(platformerrors.ErrUnrecognizedInputValue,
	"no refresh token row to record a successor against")

// RedeemIdempotently spends a secret the way Redeem does, and additionally
// records the key that spent it — so that a retry of the same exchange can be
// told from a replay of it.
//
// The happy path is Redeem exactly: one guarded UPDATE whose count is the
// answer, one read of the row on the same transaction, and the token that was
// spent. The key rides along in the same assignment rather than in a second
// write, because a row stamped spent by a statement that had not yet recorded
// which key spent it is a row whose own client's retry reads as theft — the
// failure this method exists to remove, reintroduced in a narrower window.
//
// What it adds is the branch taken when that UPDATE matches nothing and the row
// says it was already spent. Redeem has one answer there and it is the right one
// for every reuse: revoke the family, report ErrRefreshTokenReused. This method
// asks one question first — was this the same exchange, retried — and honors it
// only on all of:
//
//   - the row was spent with this key, and the key has not been spent since;
//   - the row knows what it minted, and that successor is neither spent nor
//     revoked;
//   - no more than [RemintGrace] has passed since the row was spent.
//
// A presentation meeting all of them gets the spent token back with a nil error,
// exactly as a first exchange would, and the caller mints a *fresh* successor
// while the one the first attempt minted is revoked. It is deliberately not a
// replay of a recorded response: this store keeps no secret at rest to replay,
// and a replay would hand an attacker who captured the request the very token
// the legitimate client is holding — two parties sharing one credential, which
// is what reuse detection exists to prevent. Re-minting makes the same capture
// loud instead, because the client's next call fails and it signs in again.
//
// Anything else is the reuse branch unchanged, family revocation included. That
// ordering is not an implementation detail: Redeem writes the revocation into tx
// before returning, so a grace check made after it would have already ended the
// login it was about to forgive.
//
// # One retry, not a window of them
//
// The key buys exactly one re-mint. Honoring it clears the column, so a second
// presentation of the same evidence falls through to reuse and ends the family.
// A client retrying one logical exchange more than once is already outside the
// contract — the first re-mint handed it a working token — and the bound is what
// keeps this from granting capability that does not exist today: without it, one
// captured request mints a live token for ten minutes, where a spent token is
// currently worth nothing to anybody.
//
// # What the clock costs on SQLite
//
// The grace comparison is made in Go against the stamp the row carries, and that
// stamp is stored to whole seconds on SQLite. A retry there gets up to a second
// less than [RemintGrace], never more, which is the direction that fails closed.
func (s *SQLStore) RedeemIdempotently(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	secret string,
	key string,
) (*signin.RefreshToken, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := s.validateLookup(scope, secret); err != nil {
		return nil, err
	}

	// The key's own shape is idempotency's to judge, and its sentinels are
	// mapped by both transports already — so a malformed key is a 400 here
	// rather than a refusal this package had to invent words for.
	if err := idempotency.ValidateKey(idempotency.Key(key), MaximumIdempotencyKeyLength); err != nil {
		return nil, err
	}

	op.Set(scopeKey, scope.String())

	hash := s.Digest(secret)
	now := s.clock.Now().UTC()

	affected, err := s.q.RedeemRefreshTokenWithKey(ctx, tx, signindb.RedeemRefreshTokenWithKeyParams{
		RedeemedAt:      &now,
		RedeemedWithKey: &key,
		Hash:            hash,
		Scope:           scope,
		Now:             now,
	})
	if err != nil {
		return nil, op.Error(err, "redeeming refresh token row")
	}

	row, err := s.q.GetRefreshToken(ctx, tx, signindb.GetRefreshTokenParams{Hash: hash, Scope: scope})
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			return nil, signin.ErrInvalidCredentials
		}

		return nil, op.Error(err, "reading refresh token row")
	}

	token := tokenFromRow(&row)

	op.SetValues(map[string]any{familyKey: token.FamilyID, subjectKey: token.SubjectID})

	if affected != 0 {
		// The stamp this call wrote, for the reason Redeem gives.
		token.RedeemedAt = &now

		return token, nil
	}

	return s.remint(ctx, tx, op, token, hash, key, now)
}

// remint decides whether a zero-row idempotent redemption of an existing row is
// a retry, and acts on it.
//
// Its shape is refuse's with one branch in front, and it falls back to refuse
// for everything it declines — so the reuse answer is the same code rather than
// a second rendering of it that could come to disagree about what a detected
// theft does.
func (s *SQLStore) remint(
	ctx context.Context,
	tx database.Tx,
	op observability.Operation,
	token *signin.RefreshToken,
	hash, key string,
	now time.Time,
) (*signin.RefreshToken, error) {
	// Revoked, expired, or lost a race, which is refuse's ErrInvalidCredentials
	// and never a family revocation. A token that was never spent cannot have
	// been spent by this key.
	if token.RedeemedAt == nil {
		return nil, signin.ErrInvalidCredentials
	}

	successor, eligible, err := s.retryOf(ctx, tx, op, token, hash, key, now)
	if err != nil {
		return nil, err
	}

	if !eligible {
		return nil, s.refuse(ctx, tx, op, token)
	}

	// The claim, and the one statement that makes this a single grant rather
	// than a renewable one. Its count is the answer for the same reason the
	// exchange's is: two presentations of one captured request both reach it,
	// and the loser finds the column already cleared.
	claimed, err := s.q.ClaimRefreshTokenRemint(ctx, tx, signindb.ClaimRefreshTokenRemintParams{
		RedeemedWithKey: nil,
		Hash:            hash,
		Scope:           token.Scope,
		ExpectedKey:     &key,
	})
	if err != nil {
		return nil, op.Error(err, "claiming a refresh token re-mint")
	}

	if claimed == 0 {
		return nil, s.refuse(ctx, tx, op, token)
	}

	// The successor alone, never the family. Revoking the family would be the
	// outcome the retry exists to avoid; revoking nothing would leave this login
	// holding two live refresh tokens once the caller mints the replacement.
	if _, err = s.q.RevokeRefreshToken(ctx, tx, signindb.RevokeRefreshTokenParams{
		RevokedAt: &now,
		Hash:      successor,
		Scope:     token.Scope,
	}); err != nil {
		return nil, op.Error(err, "revoking the successor a re-minted exchange replaces")
	}

	op.SpanOnly(remintKey, true)

	return token, nil
}

// retryOf reports whether a spent row's redemption was this key's, and names the
// successor that redemption minted.
//
// Every condition it checks is a reason to refuse, and refusing means the reuse
// branch — so the caller gets one bool rather than a reason, deliberately. There
// is no answer here a client is told apart from any other: a retry outside its
// window and a thief's replay are the same request from this side, and telling
// them apart on the wire would be an oracle for which of two tokens somebody is
// holding.
func (s *SQLStore) retryOf(
	ctx context.Context,
	tx database.Tx,
	op observability.Operation,
	token *signin.RefreshToken,
	hash, key string,
	now time.Time,
) (successorHash string, retry bool, err error) {
	redemption, err := s.q.GetRefreshTokenRedemption(ctx, tx, signindb.GetRefreshTokenRedemptionParams{
		Hash:  hash,
		Scope: token.Scope,
	})
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}

		return "", false, op.Error(err, "reading a refresh token's redemption")
	}

	// Spent without a key, spent with somebody else's, or spent with this one
	// and already re-minted once — the column is cleared by the re-mint that
	// honored it, so the third case arrives here as the first.
	if redemption.RedeemedWithKey == nil || *redemption.RedeemedWithKey != key {
		return "", false, nil
	}

	// A spent row that cannot name what it minted. Unreachable through a
	// transaction that spends and records together, and there is nothing safe to
	// do with it here: re-minting would leave the earlier successor live.
	if redemption.SuccessorHash == nil {
		return "", false, nil
	}

	if !now.Before(token.RedeemedAt.Add(RemintGrace)) {
		return "", false, nil
	}

	// The condition that does the real work. A client that spent its successor
	// received it, so the exchange it is retrying was not the one that failed.
	successor, err := s.q.GetRefreshToken(ctx, tx, signindb.GetRefreshTokenParams{
		Hash:  *redemption.SuccessorHash,
		Scope: token.Scope,
	})
	if err != nil {
		if stderrors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}

		return "", false, op.Error(err, "reading the successor a refresh token minted")
	}

	if successor.RedeemedAt != nil || successor.RevokedAt != nil {
		return "", false, nil
	}

	return *redemption.SuccessorHash, true, nil
}

// RecordSuccessor writes onto a spent token the digest of the token its exchange
// minted.
//
// It takes both secrets rather than either digest, because the digest is this
// store's own rendering and handing one out — even of a token that is already
// spent — would put a column's value in a caller's hands for no gain. Both
// secrets are already in that caller's possession at the one moment this is
// called: it presented the first and has just been handed the second.
//
// It is the second half of an idempotent exchange and belongs in the same
// transaction as the spend and the mint. Without it the row is spent and cannot
// say what it minted, so the client's own retry has nothing to revoke and is
// answered as a reuse — the whole mechanism silently off for that login.
//
// A store built on this one may call it after a plain Redeem too, and nothing
// breaks: the successor is recorded, no key is, and the retry branch is never
// reachable because it requires both.
func (s *SQLStore) RecordSuccessor(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	predecessor string,
	successor string,
) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if err := scope.Validate(); err != nil {
		return err
	}

	if predecessor == "" || successor == "" {
		return ErrEmptySecret
	}

	op.Set(scopeKey, scope.String())

	// The count is read rather than discarded. Zero means there is no row to
	// record against, which is not a state this module's exchange can reach and
	// is not one to pass over in silence either — the cost of the write that
	// quietly did nothing is paid by a client whose retry ends its login.
	// The assignment is a *string because the column is nullable and sqlc types
	// the argument from the column. This one is never nil — a successor the
	// statement could not name is a statement with no reason to run — so the
	// address is taken of a value rather than of anything that could be absent.
	successorHash := s.Digest(successor)

	recorded, err := s.q.RecordRefreshTokenSuccessor(ctx, tx, signindb.RecordRefreshTokenSuccessorParams{
		SuccessorHash: &successorHash,
		Hash:          s.Digest(predecessor),
		Scope:         scope,
	})
	if err != nil {
		return op.Error(err, "recording a refresh token's successor")
	}

	if recorded == 0 {
		return ErrSuccessorNotRecorded
	}

	return nil
}

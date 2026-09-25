package signin

import (
	"context"

	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// RecoveryCodeStore is where a user's recovery codes live, and it is optional: a
// service built without [WithRecoveryCodeStore] accepts no recovery code, and its
// two recovery code doors refuse with [ErrRecoveryCodesNotConfigured].
//
// This module ships a SQL implementation,
// [github.com/primandproper/platform-go/v14/authentication/signin/recoverycodes],
// together with the DDL it needs.
//
// What an implementation owes its callers is not "these four methods". It is the
// properties they exist to hold, none of which the signatures can state:
//
// The codes are never stored. Replace mints them, returns them once, and persists
// something a reader cannot reverse into them.
//
// A code is spendable exactly once, and the store decides which caller spends it.
// Two concurrent Consume calls for one code must produce one success and one
// refusal, with no cooperation from the caller. That single guarantee is the
// whole of single use, and it is why Consume exists rather than the service
// reading a row and writing it back.
//
// Verify decides nothing. It is the read a door makes before its transaction
// opens, and a nil answer from it is an observation that can be stale by the
// time Consume runs — which is why every door that verifies a code consumes it
// again inside its transaction, and refuses the sign-in when the second answer
// disagrees with the first.
//
// A code is compared as a person types it. Consume and Verify accept the code
// with or without the separators Replace printed it with, and in either case,
// because a person copying a code off paper will type it both ways.
//
// Every method takes a tenancy.Scope and a user, and none of them offers a
// variant without either: an implementation filters on both rather than
// treating them as hints. A code presented for somebody else matches nothing,
// which is what it is from there.
//
// # Why this is not the TOTP secret's column
//
// A recovery code is a second factor a person keeps on paper, so it could have
// been a column on identity's user row beside the TOTP secret. It is a table of
// its own for the reason the refresh token and the sign-in link are: a set of
// single-use secrets is rows, each spent on its own, and the directory is an
// interface a consumer may satisfy with a schema that is not identity's. What it
// shares with the TOTP secret is the door that checks it — see
// [Service.LoginForToken], where a code is tried as a TOTP code first and as a
// recovery code second.
type RecoveryCodeStore interface {
	// Replace mints a fresh set of count codes for a user, deletes whatever set
	// they held before, and returns the new codes exactly once.
	//
	// Deleting and minting are one write in tx, so a replacement that rolls back
	// leaves the old set working, and one that commits leaves nothing of it —
	// spent or unspent — for anybody to present.
	//
	// The codes are returned before tx commits. Show them to the person after
	// the commit, not from inside the callback: a code shown for a transaction
	// that then rolled back is a code that will never work.
	Replace(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID string,
		count int,
	) ([]string, error)

	// Verify reports whether a user holds a code, still unspent, and changes
	// nothing.
	//
	// A nil error means they did at the moment of the read. Anything that is not
	// a code they hold unspent is [ErrInvalidCredentials] — an unknown code, a
	// spent one and one belonging to somebody else are one answer, for the
	// reason the password door collapses its four.
	Verify(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		userID, code string,
	) error

	// Consume spends a code, atomically.
	//
	// A nil error is a decision rather than an observation: it means this
	// caller, and no other, spent that code. Every refusal is
	// [ErrInvalidCredentials], and the refusal a caller who verified the code a
	// moment earlier sees is the one that says somebody else got there first.
	Consume(
		ctx context.Context,
		tx database.Tx,
		scope tenancy.Scope,
		userID, code string,
	) error

	// Remaining reports how many unspent codes a user holds. Zero is not an
	// error: somebody who never asked for a set holds none, and somebody who
	// spent their last one holds none too.
	Remaining(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		userID string,
	) (int, error)
}

// RecoveryCodeReplacement is a request for a fresh set of recovery codes, by the
// person who will hold them.
type RecoveryCodeReplacement struct {
	_ struct{} `json:"-"`

	// CurrentPassword is required. A set of codes that each stand in for the
	// second factor is not something a stolen session gets to mint.
	CurrentPassword string `json:"-"`

	// TOTPCode is the second-factor code — from the authenticator, or one of the
	// codes being replaced. It is required: a user who holds no proven second
	// factor has nothing a recovery code would recover, and is refused with
	// [ErrSecondFactorNotEnrolled].
	TOTPCode string `json:"-"`
}

// errRecoveryCodeSpent is why a door is refused when the recovery code it
// verified was spent by somebody else before its own transaction could.
//
// It is unexported and wraps [ErrInvalidCredentials] because it is one more way
// for a second-factor code not to verify, and the caller is told what a wrong
// code is told: a refusal spelled apart here would say, to whoever is presenting
// guesses, that the code had been real a moment ago. What tells it apart is the
// span, which is where every other collapsed refusal is told apart too.
var errRecoveryCodeSpent = platformerrors.Wrap(ErrInvalidCredentials,
	"recovery code was spent by a concurrent request")

// ReplaceRecoveryCodes issues the calling user a fresh set of recovery codes,
// withdraws the set they held, and returns the new codes to them, once.
//
// The current password is required, and so is a second factor: a code from the
// authenticator, or one of the recovery codes being replaced. A user who holds
// no proven second factor is refused with [ErrSecondFactorNotEnrolled], because
// a recovery code recovers a second factor and they have none to recover; a user
// who holds no password is refused with [ErrNoPasswordCredential], as
// [Service.RefreshTOTPSecret] refuses them.
//
// Spending a recovery code never mints a new set, and nothing here does it on a
// schedule. What to do when somebody reaches zero is the consumer's —
// [Hooks.AfterRecoveryCodeUsed] tells them how many are left each time one is
// spent — and replacing a set is the person's own action, behind this door.
//
// The returned codes are the one moment they exist in plain text. They go to
// exactly one person, they are not given to a hook, and they are not logged or
// traced. Do not put them anywhere they will be read twice.
//
// It requires [WithRecoveryCodeStore], and refuses with
// [ErrRecoveryCodesNotConfigured] until it has one. How many codes a set holds
// is [WithRecoveryCodeCount].
func (s *Service) ReplaceRecoveryCodes(
	ctx context.Context,
	scope tenancy.Scope,
	userID string,
	replacement *RecoveryCodeReplacement,
) (codes []string, err error) {
	ctx, op, done := s.begin(ctx, opReplaceRecoveryCodes,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer func() { done(err) }()

	if userID == "" {
		return nil, op.Error(ErrEmptyUserID, "replacing recovery codes")
	}

	if replacement == nil {
		return nil, op.Error(ErrNilRecoveryCodeReplacement, "replacing recovery codes")
	}

	if s.recoveryCodes == nil {
		return nil, op.Error(ErrRecoveryCodesNotConfigured, "replacing recovery codes")
	}

	user, err := s.directory.GetUser(ctx, s.client.Reader(), scope, userID)
	if err != nil {
		return nil, op.Error(err, "reading the user replacing recovery codes")
	}

	if !user.HasPassword() {
		return nil, op.Error(ErrNoPasswordCredential, "replacing recovery codes")
	}

	if !user.TwoFactorEnabled() {
		return nil, op.Error(ErrSecondFactorNotEnrolled, "replacing recovery codes")
	}

	usedRecoveryCode, err := s.reauthenticate(ctx, scope, user, replacement.CurrentPassword, replacement.TOTPCode, true)
	if err != nil {
		return nil, op.Error(err, "replacing recovery codes")
	}

	redacted := user.Redacted()

	// The spend before the replacement, though the replacement deletes the code
	// either way. It is the spend's guarded write that decides which of two
	// requests presenting one code goes through; a replacement run first would
	// delete the row the loser's spend is looking for, and both would succeed.
	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		if usedRecoveryCode {
			if txErr := s.spendRecoveryCode(ctx, tx, scope, redacted, replacement.TOTPCode); txErr != nil {
				return txErr
			}
		}

		var txErr error
		if codes, txErr = s.recoveryCodes.Replace(ctx, tx, scope, userID, s.recoveryCodeCount); txErr != nil {
			return txErr
		}

		return s.hooks.AfterReplaceRecoveryCodes(ctx, tx, scope, redacted)
	}); err != nil {
		return nil, op.Error(err, "storing recovery codes")
	}

	return codes, nil
}

// RecoveryCodesRemaining reports how many unspent recovery codes a user holds,
// which is what a settings page shows beside a "generate new codes" button.
//
// It reads on the reader, and asks for no credential: it is a count, and the
// person asking is the one it is about.
//
// It requires [WithRecoveryCodeStore], and refuses with
// [ErrRecoveryCodesNotConfigured] until it has one.
func (s *Service) RecoveryCodesRemaining(
	ctx context.Context,
	scope tenancy.Scope,
	userID string,
) (remaining int, err error) {
	ctx, op, done := s.begin(ctx, opRecoveryCodesRemaining,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer func() { done(err) }()

	if userID == "" {
		return 0, op.Error(ErrEmptyUserID, "counting recovery codes")
	}

	if s.recoveryCodes == nil {
		return 0, op.Error(ErrRecoveryCodesNotConfigured, "counting recovery codes")
	}

	if remaining, err = s.recoveryCodes.Remaining(ctx, s.client.Reader(), scope, userID); err != nil {
		return 0, op.Error(err, "counting recovery codes")
	}

	return remaining, nil
}

// checkSecondFactorCode proves a second-factor code against a user who holds a
// proven secret, and reports whether it was a recovery code that proved it.
//
// The TOTP secret is tried first and the recovery codes second, and only where
// a store is configured and the door accepts one. Both refusals are one refusal:
// a code that is neither is [ErrInvalidCredentials], whichever of the two it was
// meant to be, so there is no answer here that says "that was a recovery code".
//
// A recovery code that verifies is not spent here. For the password doors this
// runs before the transaction opens — outside it on purpose, so that a refusal
// and the lockout bookkeeping behind it never wait on a write — and the spend
// belongs to the transaction that acts on the proof. See
// [Service.spendRecoveryCode].
//
// q is what the recovery code check reads on: the reader for a door that has
// not opened its transaction yet, and the transaction itself for one that has.
// A door already holding a transaction that read somewhere else would be asking
// the pool for a second connection while it holds the first, which is a
// deadlock on a pool of one and a stall on a busy pool of any size.
func (s *Service) checkSecondFactorCode(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	user *identity.User,
	code string,
	acceptRecoveryCode bool,
) (bool, error) {
	if err := s.verifier.Verify(ctx, user.TwoFactorSecret, code); err == nil {
		return false, nil
	}

	if !acceptRecoveryCode || s.recoveryCodes == nil {
		return false, ErrInvalidCredentials
	}

	if err := s.recoveryCodes.Verify(ctx, q, scope, user.ID, code); err != nil {
		if platformerrors.Is(err, ErrInvalidCredentials) {
			return false, ErrInvalidCredentials
		}

		// A store that could not answer is not a wrong guess, and collapsing it
		// into one would tell somebody to check their code while the database is
		// down — readByHandle passes the directory's failures through for the
		// same reason.
		return false, err
	}

	return true, nil
}

// spendRecoveryCode spends a recovery code a door has already verified, on that
// door's transaction, and runs [Hooks.AfterRecoveryCodeUsed] beside it.
//
// It is the first write in every transaction it is part of, so nothing is
// minted and nothing is written for a sign-in whose code somebody else spent
// first: the spend's refusal is [errRecoveryCodeSpent], the transaction rolls
// back, and the door refuses the attempt as it refuses a wrong code.
//
// The count the hook is handed is read on the same transaction, after the
// spend, so it is the number the person has left once this commits.
func (s *Service) spendRecoveryCode(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	user *identity.User,
	code string,
) error {
	if err := s.recoveryCodes.Consume(ctx, tx, scope, user.ID, code); err != nil {
		if platformerrors.Is(err, ErrInvalidCredentials) {
			return errRecoveryCodeSpent
		}

		return err
	}

	remaining, err := s.recoveryCodes.Remaining(ctx, tx, scope, user.ID)
	if err != nil {
		return err
	}

	return s.hooks.AfterRecoveryCodeUsed(ctx, tx, scope, user, remaining)
}

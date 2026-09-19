package identity

import (
	"context"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The Service's credential operations: one per write in CredentialStore, each a
// transaction with a hook in it.
//
// They are the thinnest operations this layer has — a write, a read back, a
// hook — and they are here for the reason Invite is rather than the reason
// Register is. The write is one statement and needs no orchestrating; what needs
// the transaction is the companion. A completed password reset is an audit entry
// and a hash. A verified address is an audit entry, a stamp, and the mail that
// welcomes somebody in. Every application that adopted the store wrote this
// block per credential write, and the one that wrote it without a transaction
// has resets nobody can account for.
//
// # Why they are not in authentication/signin
//
// That package has three credential operations of its own, and each re-checks
// the current password before it writes. That is the right rule for a signed-in
// person changing their own credential and the wrong one for every flow that has
// no current password to ask for: a password reset answers a mailed link, an
// operator forcing a change is not the subject, and a verification link is
// itself the proof. Those flows cannot go through signin, so they were reaching
// the store directly — which is exactly where the hook was missing, and why the
// seam belongs on this Service, beside the seventeen operations a consumer's
// audit layer already hangs off.
//
// The three that overlap are not a second implementation of signin's. signin
// asks for the password, hashes, verifies a code, and then writes; the write and
// the hook are the tail of a longer operation whose front half is authentication
// this package does not do. What is shared is the store write, which is where
// sharing it belongs.
//
// # What they still do not hold
//
// No policy, as everywhere else here. Whether the reset token was valid, how
// long a verification link lives, whether this operator may force a change,
// which mail goes out — all decided before the call. The hash and the secret are
// produced by the engines in primitives-go and handed in: this package still
// never hashes, never compares, and never generates.
//
// Every one of them answers with the user as the write left them, redacted, and
// hands that same value to the hook. Five read the row back to produce it and
// two take it from a write that answers with one; three of the five read the
// pre-state as well, because the write is about to clear a column a hook would
// have wanted and Hooks says so on each of the three methods that gets one.

// UpdateUserPassword writes a new password hash, in one transaction with
// whatever Hooks.AfterUpdateUserPassword writes beside it.
//
// This is the reset path. There is no current password here and no check for
// one: the caller has already established that whoever is asking may — a link
// they mailed, an operator's decision, a recovery code — and a check this layer
// could make would be one the flow with no old password to offer cannot pass.
// A signed-in person changing a password they still know goes through
// authentication/signin, which asks for it.
//
// The hash is the caller's, from the argon2 engine, and an empty one is refused
// by the store rather than written. The store releases any forced password
// change in the same statement, which is what makes a forced change terminate,
// and the flag as it stood reaches the hook because the write is what cleared
// it.
//
// The user handed back and passed to the hook is read after the write, on the
// transaction that made it, and is redacted — so it carries the new
// PasswordLastChangedAt and not the hash that moved.
func (s *Service) UpdateUserPassword(
	ctx context.Context,
	scope tenancy.Scope,
	userID, hashedPassword string,
) (*User, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer op.End()

	var updated *User

	err := s.run(ctx, op, opUpdateUserPassword, func(tx database.Tx) error {
		before, err := s.store.GetUser(ctx, tx, scope, userID)
		if err != nil {
			return err
		}

		previouslyRequiredChange := before.RequiresPasswordChange

		if err = s.store.UpdateUserPassword(ctx, tx, scope, userID, hashedPassword); err != nil {
			return err
		}

		after, err := s.store.GetUser(ctx, tx, scope, userID)
		if err != nil {
			return err
		}

		updated = after.Redacted()

		return s.hooks.AfterUpdateUserPassword(ctx, tx, scope, updated, previouslyRequiredChange)
	})
	if err != nil {
		return nil, op.Error(err, "updating password of identity user %q", userID)
	}

	return updated, nil
}

// SetUserRequiresPasswordChange forces or releases a password change at the
// user's next sign-in.
//
// It is the operator's write — a credential believed compromised, a shared
// password on a handover — and the one credential operation whose subject is not
// the person calling. That is most of why it wants a hook: an obligation imposed
// on somebody's account by somebody else is the entry an investigation opens
// with.
//
// Releasing is the same call with false, and is not the same thing as the
// release UpdateUserPassword performs: this one withdraws the requirement
// without a new password behind it.
//
// The user handed back and passed to the hook is read after the write, on the
// transaction that made it, and is redacted.
func (s *Service) SetUserRequiresPasswordChange(
	ctx context.Context,
	scope tenancy.Scope,
	userID string,
	requires bool,
) (*User, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer op.End()

	var updated *User

	err := s.run(ctx, op, opSetUserRequiresPasswordChange, func(tx database.Tx) error {
		if err := s.store.SetUserRequiresPasswordChange(ctx, tx, scope, userID, requires); err != nil {
			return err
		}

		after, err := s.store.GetUser(ctx, tx, scope, userID)
		if err != nil {
			return err
		}

		updated = after.Redacted()

		return s.hooks.AfterSetUserRequiresPasswordChange(ctx, tx, scope, updated)
	})
	if err != nil {
		return nil, op.Error(err, "setting password change requirement for identity user %q", userID)
	}

	return updated, nil
}

// UpdateUserTwoFactorSecret stores a newly issued TOTP secret, unverified, in
// one transaction with whatever Hooks.AfterUpdateUserTwoFactorSecret writes
// beside it.
//
// The secret is the caller's, from the totp engine, and the store marks it
// unverified and will not be told otherwise — so until
// Service.MarkUserTwoFactorSecretVerified the user holds no second factor at
// all. The moment the secret this one replaced had been proven reaches the hook,
// because that window is the fact worth alerting on and the write is what closed
// the answer.
//
// Like UpdateUserPassword this is the path with nothing to re-authenticate
// against: an operator re-enrolling somebody who lost a phone, a recovery flow
// that proved possession some other way. A signed-in person rotating their own
// secret goes through authentication/signin, which asks for the password and a
// code from the secret being replaced.
//
// The secret does not reach the hook and is not returned: it is in flight to
// exactly one person, and the caller already holds it. The user handed back and
// passed to the hook is read after the write, on the transaction that made it,
// and is redacted.
func (s *Service) UpdateUserTwoFactorSecret(
	ctx context.Context,
	scope tenancy.Scope,
	userID, secret string,
) (*User, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer op.End()

	var updated *User

	err := s.run(ctx, op, opUpdateUserTwoFactorSecret, func(tx database.Tx) error {
		before, err := s.store.GetUser(ctx, tx, scope, userID)
		if err != nil {
			return err
		}

		previousSecretVerifiedAt := before.TwoFactorSecretVerifiedAt

		if err = s.store.UpdateUserTwoFactorSecret(ctx, tx, scope, userID, secret); err != nil {
			return err
		}

		after, err := s.store.GetUser(ctx, tx, scope, userID)
		if err != nil {
			return err
		}

		updated = after.Redacted()

		return s.hooks.AfterUpdateUserTwoFactorSecret(ctx, tx, scope, updated, previousSecretVerifiedAt)
	})
	if err != nil {
		return nil, op.Error(err, "updating second-factor secret of identity user %q", userID)
	}

	return updated, nil
}

// MarkUserTwoFactorSecretVerified records that the user proved possession of the
// secret they hold, which is what turns it into a second factor.
//
// Whether the code validated is the caller's answer, from the totp engine,
// before the call. What this owns is that the stamp and the consumer's record of
// it are one commit.
//
// A user who has already verified matches nothing and gets ErrUserNotFound from
// the store, so a replay reaches no hook. The user handed back and passed to the
// hook is the row the write answered with — carrying the stamp this statement
// made rather than whatever the column held before — redacted.
func (s *Service) MarkUserTwoFactorSecretVerified(
	ctx context.Context,
	scope tenancy.Scope,
	userID string,
) (*User, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer op.End()

	var verified *User

	err := s.run(ctx, op, opMarkUserTwoFactorSecretVerified, func(tx database.Tx) error {
		user, err := s.store.MarkUserTwoFactorSecretVerified(ctx, tx, scope, userID)
		if err != nil {
			return err
		}

		verified = user.Redacted()

		return s.hooks.AfterMarkUserTwoFactorSecretVerified(ctx, tx, scope, verified)
	})
	if err != nil {
		return nil, op.Error(err, "marking second-factor secret of identity user %q verified", userID)
	}

	return verified, nil
}

// SetUserEmailAddressVerificationToken stores the token a verification link will
// carry, in one transaction with whatever
// Hooks.AfterSetUserEmailAddressVerificationToken writes beside it.
//
// The hook is the whole reason this is an operation: the mail carrying the link
// must not be sent for a token that did not commit, and it must not be sent from
// inside the transaction either. The hook writes the outbox row and the mail
// leaves after the commit, which is the same bargain Invite makes with the
// invitation it issues.
//
// The token is the caller's — its length, its alphabet, its expiry policy — and
// the store refuses an empty one. Any outstanding token is replaced, so
// re-sending invalidates the previous link, and any proof the address already had
// comes off in the same statement; what that proof was reaches the hook, because
// nothing can read it afterwards.
//
// The token does not reach the hook and is not returned: the caller minted it and
// is the one who needs it. The user handed back and passed to the hook is read
// after the write, on the transaction that made it, and is redacted.
func (s *Service) SetUserEmailAddressVerificationToken(
	ctx context.Context,
	scope tenancy.Scope,
	userID, token string,
) (*User, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer op.End()

	var updated *User

	err := s.run(ctx, op, opSetUserEmailAddressVerificationToken, func(tx database.Tx) error {
		before, err := s.store.GetUser(ctx, tx, scope, userID)
		if err != nil {
			return err
		}

		previousAddressVerifiedAt := before.EmailAddressVerifiedAt

		if err = s.store.SetUserEmailAddressVerificationToken(ctx, tx, scope, userID, token); err != nil {
			return err
		}

		after, err := s.store.GetUser(ctx, tx, scope, userID)
		if err != nil {
			return err
		}

		updated = after.Redacted()

		return s.hooks.AfterSetUserEmailAddressVerificationToken(ctx, tx, scope, updated, previousAddressVerifiedAt)
	})
	if err != nil {
		return nil, op.Error(err, "setting email verification token for identity user %q", userID)
	}

	return updated, nil
}

// MarkUserEmailAddressVerified stamps the address as proven and burns the token
// the link carried.
//
// The token is compared in the store's own predicate rather than trusted from the
// read that resolved it, which is what makes two clicks on one link write once —
// so the second click is ErrUserNotFound and reaches no hook. The caller resolves
// the token to a user through CredentialStore.GetUserByEmailVerificationToken,
// which is a read and needs no transaction of its own.
//
// The user handed back and passed to the hook is read after the write, on the
// transaction that made it, and is redacted — so it carries both the stamp and
// the address that was proven, which is the pair a record of a verification is
// written from.
func (s *Service) MarkUserEmailAddressVerified(
	ctx context.Context,
	scope tenancy.Scope,
	userID, token string,
) (*User, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer op.End()

	var verified *User

	err := s.run(ctx, op, opMarkUserEmailAddressVerified, func(tx database.Tx) error {
		if err := s.store.MarkUserEmailAddressVerified(ctx, tx, scope, userID, token); err != nil {
			return err
		}

		after, err := s.store.GetUser(ctx, tx, scope, userID)
		if err != nil {
			return err
		}

		verified = after.Redacted()

		return s.hooks.AfterMarkUserEmailAddressVerified(ctx, tx, scope, verified)
	})
	if err != nil {
		return nil, op.Error(err, "marking email address of identity user %q verified", userID)
	}

	return verified, nil
}

// MarkUserEmailAddressUnverified withdraws the proof from an address the user
// keeps.
//
// It is the administrative direction — a bounce, a support decision, a
// deliverability sweep — and takes no token, because the caller is not answering
// anything. Whatever link is outstanding survives: it was minted for this
// address and the address has not moved.
//
// The user handed back and passed to the hook is the row the write answered with,
// redacted. The address on it is the fact a record of this is written from, since
// the column the write cleared says only that something was proven and never
// which address the proof was for.
func (s *Service) MarkUserEmailAddressUnverified(
	ctx context.Context,
	scope tenancy.Scope,
	userID string,
) (*User, error) {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer op.End()

	var unverified *User

	err := s.run(ctx, op, opMarkUserEmailAddressUnverified, func(tx database.Tx) error {
		user, err := s.store.MarkUserEmailAddressUnverified(ctx, tx, scope, userID)
		if err != nil {
			return err
		}

		unverified = user.Redacted()

		return s.hooks.AfterMarkUserEmailAddressUnverified(ctx, tx, scope, unverified)
	})
	if err != nil {
		return nil, op.Error(err, "marking email address of identity user %q unverified", userID)
	}

	return unverified, nil
}

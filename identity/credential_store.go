package identity

import (
	"context"

	"github.com/primandproper/platform-go/v14/identity/internal/identitydb"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/pointer"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The SQLStore's CredentialStore: where the authentication engines' output
// lands. Every method here writes exactly one credential fact, so that none of
// them can be reached by a read-modify-write over a whole User.
var _ CredentialStore = (*SQLStore)(nil)

// GetUserByEmailVerificationToken reads the live user a verification link names.
func (s *SQLStore) GetUserByEmailVerificationToken(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	token string,
) (*User, error) {
	if token == "" {
		// An empty token is what the column holds for every user with no
		// outstanding link, so the query would match an arbitrary one of them.
		// Refusing here rather than running it is the difference between a
		// rejected verification and a verified stranger.
		return nil, platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty email verification token")
	}

	return s.liveUser(ctx, q, scope, "reading identity user by email verification token",
		func(ctx context.Context) (*User, error) {
			row, err := s.q.GetUserByEmailVerificationToken(ctx, q,
				identitydb.GetUserByEmailVerificationTokenParams{
					EmailAddressVerificationToken: token,
					Scope:                         scope,
				})
			if err != nil {
				return nil, err
			}

			return userFromEmailVerificationTokenRow(&row), nil
		})
}

// UpdateUserPassword replaces the hash, stamps the change, and releases any
// forced password change.
func (s *SQLStore) UpdateUserPassword(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID, hashedPassword string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return op.Error(err, "updating identity user password")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "updating identity user password")
	}

	if hashedPassword == "" {
		// An empty hash would be written and then compared against on the next
		// sign-in, by an engine with no way to know it was never set.
		return op.Error(
			platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty password hash"),
			"updating identity user password",
		)
	}

	// The forced-change flag is released in the same statement rather than left
	// to the caller — see Store.UpdateUserPassword for why that is what makes a
	// forced password change terminate.
	count, err := s.q.UpdateUserPassword(ctx, tx, identitydb.UpdateUserPasswordParams{
		ID:                     userID,
		Scope:                  scope,
		HashedPassword:         hashedPassword,
		RequiresPasswordChange: false,
		PasswordLastChangedAt:  pointer.To(s.now()),
	})
	if err = s.guardCount(ctx, count, err, ErrUserNotFound, "updating identity user password"); err != nil {
		return op.Error(err, "updating identity user password")
	}

	return nil
}

// SetUserRequiresPasswordChange forces or releases a password change at next
// sign-in.
func (s *SQLStore) SetUserRequiresPasswordChange(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID string,
	requires bool,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return op.Error(err, "setting identity password change requirement")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "setting identity password change requirement")
	}

	count, err := s.q.SetUserRequiresPasswordChange(ctx, tx, identitydb.SetUserRequiresPasswordChangeParams{
		ID:                     userID,
		Scope:                  scope,
		RequiresPasswordChange: requires,
	})
	if err = s.guardCount(ctx, count, err, ErrUserNotFound, "setting identity password change requirement"); err != nil {
		return op.Error(err, "setting identity password change requirement")
	}

	return nil
}

// UpdateUserTwoFactorSecret stores a new TOTP secret, unverified.
func (s *SQLStore) UpdateUserTwoFactorSecret(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID, secret string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return op.Error(err, "updating identity two factor secret")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "updating identity two factor secret")
	}

	if secret == "" {
		return op.Error(
			platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty two factor secret"),
			"updating identity two factor secret",
		)
	}

	// The new secret and its cleared verification are one statement. Two would
	// leave a window in which a freshly issued secret reads as already proven,
	// which is a window in which a second factor is bypassed by re-enrolling.
	count, err := s.q.UpdateUserTwoFactorSecret(ctx, tx, identitydb.UpdateUserTwoFactorSecretParams{
		ID:                        userID,
		Scope:                     scope,
		TwoFactorSecret:           secret,
		TwoFactorSecretVerifiedAt: nil,
	})
	if err = s.guardCount(ctx, count, err, ErrUserNotFound, "updating identity two factor secret"); err != nil {
		return op.Error(err, "updating identity two factor secret")
	}

	return nil
}

// MarkUserTwoFactorSecretVerified records that the user proved possession of
// their secret.
//
// A user who has already verified matches nothing, and that reports
// ErrUserNotFound rather than succeeding silently — a second verification is
// either a replayed request or a flow that lost track of its own state, and
// both are worth surfacing.
func (s *SQLStore) MarkUserTwoFactorSecretVerified(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return op.Error(err, "marking identity two factor secret verified")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "marking identity two factor secret verified")
	}

	// The guards are the statement's, not this method's: a secret that exists
	// and has not been proven. Neither is an equality against a value held
	// here, so neither is expressible as an argument — see querygen's
	// Comparand — and that is what keeps a replayed verification from moving
	// the timestamp forward.
	count, err := s.q.MarkUserTwoFactorSecretVerified(ctx, tx,
		identitydb.MarkUserTwoFactorSecretVerifiedParams{
			ID:                        userID,
			Scope:                     scope,
			TwoFactorSecretVerifiedAt: pointer.To(s.now()),
		})
	if err = s.guardCount(ctx, count, err, ErrUserNotFound, "marking identity two factor secret verified"); err != nil {
		return op.Error(err, "marking identity two factor secret verified")
	}

	return nil
}

// SetUserEmailAddressVerificationToken stores the token a verification link will
// carry, replacing any outstanding one and dropping any proof the address
// already had.
func (s *SQLStore) SetUserEmailAddressVerificationToken(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID, token string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return op.Error(err, "setting identity email verification token")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "setting identity email verification token")
	}

	if token == "" {
		// The empty string is how "no outstanding link" is stored, so writing it
		// here would be a clear dressed as an issue.
		return op.Error(
			platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty email verification token"),
			"setting identity email verification token",
		)
	}

	// Any outstanding token is replaced, so re-sending a verification email
	// invalidates the previous link rather than leaving two live.
	//
	// The stamp comes off with it, in the same statement and for the reason
	// UpdateUserTwoFactorSecret enrolls a secret unverified: an outstanding link
	// and a recorded proof are two answers to one question, and a row holding
	// both leaves which one is true up to whichever column a reader consulted.
	// Issuing a link is a statement that the address wants proving, so it is the
	// column that says otherwise which has to go.
	count, err := s.q.SetUserEmailAddressVerificationToken(ctx, tx,
		identitydb.SetUserEmailAddressVerificationTokenParams{
			ID:                            userID,
			Scope:                         scope,
			EmailAddressVerificationToken: token,
			EmailAddressVerifiedAt:        nil,
		})
	if err = s.guardCount(ctx, count, err, ErrUserNotFound, "setting identity email verification token"); err != nil {
		return op.Error(err, "setting identity email verification token")
	}

	return nil
}

// MarkUserEmailAddressVerified stamps the address as proven and burns the token.
func (s *SQLStore) MarkUserEmailAddressVerified(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID, token string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return op.Error(err, "marking identity email address verified")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "marking identity email address verified")
	}

	if token == "" {
		return op.Error(
			platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "empty email verification token"),
			"marking identity email address verified",
		)
	}

	// The token is in the predicate as well as being cleared by the write, which
	// is what makes two concurrent clicks on the same link write once: the
	// second finds it already cleared and matches nothing. Comparing it here
	// rather than trusting an earlier read is the whole of that guarantee.
	count, err := s.q.MarkUserEmailAddressVerified(ctx, tx,
		identitydb.MarkUserEmailAddressVerifiedParams{
			ID:                                   userID,
			Scope:                                scope,
			EmailAddressVerifiedAt:               pointer.To(s.now()),
			EmailAddressVerificationToken:        "",
			CurrentEmailAddressVerificationToken: token,
		})
	if err = s.guardCount(ctx, count, err, ErrUserNotFound, "marking identity email address verified"); err != nil {
		return op.Error(err, "marking identity email address verified")
	}

	return nil
}

// MarkUserEmailAddressUnverified withdraws the proof without touching the
// address it was given for.
func (s *SQLStore) MarkUserEmailAddressUnverified(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID string,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return op.Error(err, "marking identity email address unverified")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "marking identity email address unverified")
	}

	// No token and no guard. There is nothing to compare against — the caller is
	// not answering a link, it is deciding that the address needs proving again
	// — and a guard here would only be able to fail an unverify that raced
	// another unverify, which is a race whose two outcomes are the same row.
	//
	// nil is written rather than left out because the statement assigns the
	// column: this package's writes name what they set, so "unverified" is a
	// value bound here rather than a NULL literal in the SQL.
	count, err := s.q.MarkUserEmailAddressUnverified(ctx, tx,
		identitydb.MarkUserEmailAddressUnverifiedParams{
			ID:                     userID,
			Scope:                  scope,
			EmailAddressVerifiedAt: nil,
		})
	if err = s.guardCount(ctx, count, err, ErrUserNotFound, "marking identity email address unverified"); err != nil {
		return op.Error(err, "marking identity email address unverified")
	}

	return nil
}

package signin

import (
	"context"

	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/authentication/totp"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// UpdatePassword replaces the calling user's password with one they chose.
//
// The current password is required and checked, and a second-factor code is
// required from a user who holds a proven second factor. Neither is redundant
// with being signed in: a token proves somebody had the password once, and the
// whole point of asking again is the laptop that was left unlocked in between.
//
// Whether the new password is acceptable is the consumer's rule, applied before
// this call. This package holds no password policy — see [PasswordUpdate] — and
// the one rule it does apply is that the new password is not empty, which is
// not a policy but a write that would lock the user out.
//
// Hashing happens outside the transaction; the transaction holds the write and
// [Hooks.AfterUpdatePassword] and nothing else. The store clears
// RequiresPasswordChange as part of the write, so a forced change terminates.
//
// A user who holds no password gets [ErrNoPasswordCredential] rather than the
// sign-in path's collapsed refusal: the caller is the subject and is already
// signed in, so the specific answer tells them nothing they did not know, and
// it is the only answer that sends them anywhere useful.
func (s *Service) UpdatePassword(
	ctx context.Context,
	scope tenancy.Scope,
	userID string,
	update *PasswordUpdate,
) (err error) {
	ctx, op, done := s.begin(ctx, opUpdatePassword,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer func() { done(err) }()

	if userID == "" {
		return op.Error(ErrEmptyUserID, "updating a password")
	}

	if update == nil {
		return op.Error(ErrNilPasswordUpdate, "updating a password")
	}

	if update.NewPassword == "" {
		return op.Error(ErrEmptyPassword, "updating a password")
	}

	user, err := s.directory.GetUser(ctx, s.client.Reader(), scope, userID)
	if err != nil {
		return op.Error(err, "reading the user whose password is changing")
	}

	if !user.HasPassword() {
		return op.Error(ErrNoPasswordCredential, "updating a password")
	}

	if err = s.reauthenticate(ctx, user, update.CurrentPassword, update.TOTPCode); err != nil {
		return op.Error(err, "updating a password")
	}

	hashed, err := s.authenticator.HashPassword(ctx, update.NewPassword)
	if err != nil {
		return op.Error(err, "hashing a new password")
	}

	redacted := user.Redacted()

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		if txErr := s.directory.UpdateUserPassword(ctx, tx, scope, userID, hashed); txErr != nil {
			return txErr
		}

		return s.hooks.AfterUpdatePassword(ctx, tx, scope, redacted)
	}); err != nil {
		return op.Error(err, "writing a new password")
	}

	return nil
}

// RefreshTOTPSecret issues the calling user a new second-factor secret and
// returns it to them, once.
//
// The secret it returns is unproven, and until [Service.VerifyTOTPSecret] is
// called with a code from it the user holds no second factor at all — the store
// marks a new secret unverified and will not be told otherwise. That window is
// deliberate: a secret that counted before anybody demonstrated possession of it
// is a second factor somebody may have failed to scan.
//
// It replaces whatever secret the user had, which is what makes it the
// re-enrollment path as well as the enrollment one. A user who holds a proven
// secret must send a code from it, so losing a phone is not a way to replace
// the factor that phone held; that recovery is the consumer's, through an
// operator or a recovery code, and neither is here.
//
// The returned [totp.Enrollment] is the one moment a live second-factor secret
// is legitimately in flight. It goes to exactly one person, it is not given to a
// hook, and it is not logged or traced. Do not put it anywhere it will be read
// twice.
//
// It requires [WithTOTPIssuer], and refuses with [ErrTOTPIssuerNotConfigured]
// until it has one: the issuer is the label an authenticator app shows, and this
// package will not invent a name that appears on somebody's phone.
func (s *Service) RefreshTOTPSecret(
	ctx context.Context,
	scope tenancy.Scope,
	userID string,
	refresh *SecretRefresh,
) (enrollment *totp.Enrollment, err error) {
	ctx, op, done := s.begin(ctx, opRefreshTOTPSecret,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer func() { done(err) }()

	if userID == "" {
		return nil, op.Error(ErrEmptyUserID, "refreshing a second-factor secret")
	}

	if refresh == nil {
		return nil, op.Error(ErrNilSecretRefresh, "refreshing a second-factor secret")
	}

	if s.totpIssuer == "" {
		return nil, op.Error(ErrTOTPIssuerNotConfigured, "refreshing a second-factor secret")
	}

	user, err := s.directory.GetUser(ctx, s.client.Reader(), scope, userID)
	if err != nil {
		return nil, op.Error(err, "reading the user enrolling a second factor")
	}

	if !user.HasPassword() {
		return nil, op.Error(ErrNoPasswordCredential, "refreshing a second-factor secret")
	}

	if err = s.reauthenticate(ctx, user, refresh.CurrentPassword, refresh.TOTPCode); err != nil {
		return nil, op.Error(err, "refreshing a second-factor secret")
	}

	// The account name is the username rather than the email address: it is what
	// identifies the person within this application, which is what the label in
	// an authenticator app is for, and it does not move when they change where
	// their mail goes.
	if enrollment, err = s.generator.Generate(ctx, s.totpIssuer, user.Username); err != nil {
		return nil, op.Error(err, "generating a second-factor secret")
	}

	redacted := user.Redacted()

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		if txErr := s.directory.UpdateUserTwoFactorSecret(ctx, tx, scope, userID, enrollment.Secret); txErr != nil {
			return txErr
		}

		return s.hooks.AfterRefreshTOTPSecret(ctx, tx, scope, redacted)
	}); err != nil {
		return nil, op.Error(err, "storing a second-factor secret")
	}

	return enrollment, nil
}

// VerifyTOTPSecret records that the calling user proved possession of the secret
// they were issued, which is what turns it into a second factor.
//
// It takes a code and nothing else. The password is not asked for again because
// this is the second half of an operation that already asked — the enrollment —
// and asking twice buys nothing against an attacker who would need the secret
// this step proves possession of.
//
// A code that does not validate is [ErrInvalidCredentials], the same answer a
// sign-in gives, and running it against a user who has already verified their
// secret is not an error: the write records a fact that is already recorded.
func (s *Service) VerifyTOTPSecret(
	ctx context.Context,
	scope tenancy.Scope,
	userID, code string,
) (err error) {
	ctx, op, done := s.begin(ctx, opVerifyTOTPSecret,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer func() { done(err) }()

	if userID == "" {
		return op.Error(ErrEmptyUserID, "verifying a second-factor secret")
	}

	user, err := s.directory.GetUser(ctx, s.client.Reader(), scope, userID)
	if err != nil {
		return op.Error(err, "reading the user proving a second factor")
	}

	if user.TwoFactorSecret == "" {
		return op.Error(ErrSecondFactorNotEnrolled, "verifying a second-factor secret")
	}

	if code == "" {
		return op.Error(ErrSecondFactorRequired, "verifying a second-factor secret")
	}

	if verifyErr := s.verifier.Verify(ctx, user.TwoFactorSecret, code); verifyErr != nil {
		op.SpanOnly(reasonKey, verifyErr.Error())

		return op.Error(ErrInvalidCredentials, "verifying a second-factor secret")
	}

	redacted := user.Redacted()

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		if txErr := s.directory.MarkUserTwoFactorSecretVerified(ctx, tx, scope, userID); txErr != nil {
			return txErr
		}

		return s.hooks.AfterVerifyTOTPSecret(ctx, tx, scope, redacted)
	}); err != nil {
		return op.Error(err, "marking a second-factor secret verified")
	}

	return nil
}

// reauthenticate is the check the two credential writes share: the password
// again, and a code from a proven second factor.
//
// It is one function rather than two copies because the pair is exactly the kind
// of thing that can be got wrong twice — the copy that forgets the second factor
// is the one that lets a stolen session replace the second factor.
//
// The refusals are the sign-in path's, unchanged. A caller who is signed in and
// gets the current password wrong is told the same thing an anonymous one is,
// which is one less answer to keep consistent.
func (s *Service) reauthenticate(ctx context.Context, user *identity.User, password, code string) error {
	if password == "" {
		return ErrEmptyPassword
	}

	matches, err := s.authenticator.PasswordMatches(ctx, user.HashedPassword, password)
	if err != nil {
		return platformerrors.Join(ErrInvalidCredentials, err)
	}

	if !matches {
		return ErrInvalidCredentials
	}

	if !user.TwoFactorEnabled() {
		return nil
	}

	if code == "" {
		return ErrSecondFactorRequired
	}

	if err = s.verifier.Verify(ctx, user.TwoFactorSecret, code); err != nil {
		return ErrInvalidCredentials
	}

	return nil
}

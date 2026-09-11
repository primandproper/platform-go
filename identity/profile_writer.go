package identity

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v14/identity/internal/identitydb"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/pointer"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// The SQLStore's ProfileWriter: what a user or an account may change about
// itself. The columns it deliberately does not write — credentials, billing
// state, ownership, status — are listed on the interface.
var _ ProfileWriter = (*SQLStore)(nil)

// UpdateUser writes the user's profile and nothing else, plus the two
// verification columns it has to decide rather than accept — see
// profileUpdateParams.
//
// It validates the profile rather than the whole user, because it writes the
// profile rather than the whole user. The value a consumer has to hand is
// almost always a redacted one — every bulk read and Principal.User returns
// users with their credentials cleared — and a write that demanded a password
// hash back taught the caller to carry one, which is the habit this package
// exists to make unnecessary. See User.validateProfile.
func (s *SQLStore) UpdateUser(ctx context.Context, tx database.Tx, scope tenancy.Scope, user *User) error {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return op.Error(err, "updating identity user")
	}

	if user == nil {
		return op.Error(ErrNilUser, "updating identity user")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "updating identity user")
	}

	if err := adoptScope(scope, &user.Scope, "user"); err != nil {
		return op.Error(err, "updating identity user")
	}

	if err := user.validateProfile(ctx); err != nil {
		return op.Error(err, "updating identity user")
	}

	op.Set(userIDKey, user.ID)

	// The uniqueness checks, the read and the write are the caller's one
	// transaction rather than a second one opened here: checking outside a
	// transaction leaves a window in which the handle is free at the check and
	// taken at the write, which surfaces as the driver's constraint violation
	// rather than as ErrUsernameTaken.
	if err := s.ensureUsernameFree(ctx, tx, scope, user.Username, user.ID); err != nil {
		return op.Error(err, "updating identity user")
	}

	if err := s.ensureEmailAddressFree(ctx, tx, scope, user.EmailAddress, user.ID); err != nil {
		return op.Error(err, "updating identity user")
	}

	params, err := s.profileUpdateParams(ctx, tx, user)
	if err != nil {
		return op.Error(err, "updating identity user")
	}

	count, err := s.q.UpdateUser(ctx, tx, params)
	if err = s.guardCount(ctx, count, err, ErrUserNotFound, "updating identity user"); err != nil {
		return op.Error(err, "updating identity user")
	}

	return nil
}

// profileUpdateParams assembles what the profile update binds, deciding from the
// row as it stands what the two verification columns should hold afterwards:
// what they hold now if the address is unchanged, and nothing if it is not.
//
// Changing an address has to clear the proof that went with it, or a user could
// move to an address they have never proven and stay verified. That used to be
// a CASE inside the UPDATE, comparing the stored address against the new one in
// the same statement, and the statement querygen renders assigns columns rather
// than computing them — so the comparison moves here, into the caller's
// transaction, which is the same one the update runs in.
//
// The outstanding token clears with the stamp, in the same statement, because a
// token is proof of nothing on its own: the column records that a link was
// mailed, not which address it was mailed to. Leaving it live across an address
// change is the front door onto the hole the stamp's clearing closes — the link
// sent to the address being left behind comes back and verifies the address
// being moved to, which nobody ever proved. A flow that means to verify the new
// address mints a new link after the write, not before.
//
// What that costs is precise and worth stating: the CASE read the stored
// address as part of the write, where this reads it a moment before. Another
// transaction proving the address in that gap would have its stamp overwritten
// by the value read here. The only writer of that column is MarkEmailVerified,
// which requires a token this update has no reason to be racing, and the read
// runs on the caller's executor and so inside the same transaction as the
// write — so the window is narrow and the loss is a verification the user can
// repeat, rather than an unproven address reading as verified, which is the
// direction that mattered.
func (s *SQLStore) profileUpdateParams(
	ctx context.Context,
	q database.SQLQueryExecutor,
	user *User,
) (identitydb.UpdateUserParams, error) {
	stored, err := s.readUser(ctx, q, user.Scope, user.ID)
	if err != nil {
		return identitydb.UpdateUserParams{}, err
	}

	verifiedAt, token := stored.EmailAddressVerifiedAt, stored.EmailAddressVerificationToken
	if stored.EmailAddress != user.EmailAddress {
		verifiedAt, token = nil, ""
	}

	return updateUserParams(user, verifiedAt, token), nil
}

// UpdateAccount writes the account's name and billing address.
func (s *SQLStore) UpdateAccount(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	account *Account,
) error {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(scopeKey, scope.String()))
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return op.Error(err, "updating identity account")
	}

	if account == nil {
		return op.Error(ErrNilAccount, "updating identity account")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "updating identity account")
	}

	if err := adoptScope(scope, &account.Scope, "account"); err != nil {
		return op.Error(err, "updating identity account")
	}

	if err := account.ValidateWithContext(ctx); err != nil {
		return op.Error(err, "updating identity account")
	}

	op.Set(accountIDKey, account.ID)

	count, err := s.q.UpdateAccount(ctx, tx, updateAccountParams(account))
	if err = s.guardCount(ctx, count, err, ErrAccountNotFound, "updating identity account"); err != nil {
		return op.Error(err, "updating identity account")
	}

	return nil
}

// RecordAgreement stamps the user's acceptance of one or more documents.
func (s *SQLStore) RecordAgreement(
	ctx context.Context,
	tx database.Tx,
	scope tenancy.Scope,
	userID string,
	agreements ...Agreement,
) error {
	ctx, op := s.o11y.Begin(ctx,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer op.End()

	if err := requireExecutor(tx); err != nil {
		return op.Error(err, "recording identity agreement")
	}

	if err := scope.Validate(); err != nil {
		return op.Error(err, "recording identity agreement")
	}

	if len(agreements) == 0 {
		return op.Error(
			platformerrors.Wrap(platformerrors.ErrEmptyInputParameter, "no agreements named"),
			"recording identity agreement",
		)
	}

	for _, agreement := range agreements {
		if !agreement.Valid() {
			return op.Error(
				platformerrors.Wrapf(platformerrors.ErrUnrecognizedInputValue, "agreement %q", agreement),
				"recording identity agreement",
			)
		}
	}

	// One statement per document, all of them in the caller's transaction. The
	// SET list was assembled from the documents a caller named until the corpus
	// grew a statement per column — accepting the terms rendered one string and
	// accepting both rendered another, which is dynamic SQL by construction and
	// nothing sqlc could check. Two round trips at a cardinality of two, inside a
	// transaction, buy a statement that is text.
	//
	// One stamp for all of them, read before the first write: accepting two
	// documents in one call is one act, and two clock reads would record it as
	// two moments a later comparison could order.
	stamp := pointer.To(s.now())

	for _, agreement := range agreements {
		if err := s.recordAgreement(ctx, tx, scope, userID, agreement, stamp); err != nil {
			return op.Error(err, "recording identity agreement")
		}
	}

	return nil
}

// recordAgreement stamps one document's acceptance.
//
// The agreement chooses the statement rather than a column, because a query
// name is a Go method name: what was one write over a map from Agreement to
// column is two writes and a switch that the compiler checks is exhaustive of
// the ones this package defines. An unrecognized agreement cannot reach here —
// RecordAgreement validates every one of them first — and the default arm says
// so rather than writing nothing and reporting success.
func (s *SQLStore) recordAgreement(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	userID string,
	agreement Agreement,
	at *time.Time,
) error {
	var (
		count int64
		err   error
	)

	switch agreement {
	case TermsOfService:
		count, err = s.q.RecordUserTermsOfServiceAgreement(ctx, q, identitydb.RecordUserTermsOfServiceAgreementParams{
			ID:                         userID,
			Scope:                      scope,
			LastAcceptedTermsOfService: at,
		})
	case PrivacyPolicy:
		count, err = s.q.RecordUserPrivacyPolicyAgreement(ctx, q, identitydb.RecordUserPrivacyPolicyAgreementParams{
			ID:                        userID,
			Scope:                     scope,
			LastAcceptedPrivacyPolicy: at,
		})
	default:
		return platformerrors.Wrapf(platformerrors.ErrUnrecognizedInputValue, "agreement %q", agreement)
	}

	return s.guardCount(ctx, count, err, ErrUserNotFound, "recording identity agreement")
}

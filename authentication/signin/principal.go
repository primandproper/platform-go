package signin

import (
	"context"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// IssueForPrincipal mints a sign-in for a subject another credential has
// already proven — a passkey assertion, a device grant — and proves nothing
// itself.
//
// It is [Service.LoginForToken] with the proof taken out and nothing else: the
// same standing check, the same principal read, the same claims, lifetimes and
// family, and the same transaction holding the refresh token,
// [Hooks.AfterAuthenticate] and [Hooks.AfterIssueToken] in that order. A
// consumer whose people sign in with a passkey gets the token a password
// sign-in gets rather than re-deriving one, and a copy of the mint that could
// drift from this one on the lifetimes or the claims is the thing this door
// exists to make unnecessary.
//
// # What the caller is vouching for
//
// Everything a credential would have proven. This method takes a user ID, not
// a credential, so whoever calls it has decided who is signing in: it belongs
// behind the consumer's own verification of the credential they accept, and
// never behind a transport a client can reach with an identifier of its
// choosing. That is why no transport in this module exposes it.
//
// # What it still decides
//
// Whether the subject may sign in at all. The user is read on the reader and a
// status that admits no sign-in is refused with the status sentinel
// [Service.LoginForToken] gives, recorded through [Hooks.AfterFailedSignIn]
// with the user's ID and no handle — a proven credential still does not sign
// in a suspended user, and a credential that proved somebody who may not sign
// in is the event that hook exists for. A user the directory does not hold is
// the directory's answer, passed through: it is a caller that proved somebody
// who does not exist, not an attempt at an account.
//
// The account the token is for is activeAccountID, resolved by the directory
// exactly as [Credentials.ActiveAccountID] is.
//
// The second-factor rule is not applied. What counts as proof is the
// credential's, and a passkey asserted with user verification is two factors
// already; asking it for a TOTP code as well would be asking the consumer's
// strongest credential to be the weakest one's companion.
func (s *Service) IssueForPrincipal(
	ctx context.Context,
	scope tenancy.Scope,
	userID, activeAccountID string,
) (*SignIn, error) {
	return s.issueForPrincipal(ctx, scope, userID, activeAccountID, false)
}

// AdminIssueForPrincipal is IssueForPrincipal through the administrative door,
// and stands to it as [Service.AdminLoginForToken] stands to
// [Service.LoginForToken]: the subject must hold one of the service roles
// [WithAdminServiceRoles] named, and the token and any refresh token carry the
// administrative lifetimes and [ClaimAdministrative].
//
// A service that named no administrative roles has no administrative door here
// either, and every call is [ErrAdminLoginDisabled]. Both refusals are recorded
// through [Hooks.AfterFailedSignIn] with Administrative set.
//
// The second factor [Service.AdminLoginForToken] insists on is not insisted on
// here, for the reason IssueForPrincipal gives: this door proves nothing, so
// whether the credential in front of it was strong enough for an operator is
// the consumer's to have decided before calling it. A consumer admitting
// operators through a single-factor credential has made that choice at their
// own door, and this one cannot see it to refuse it.
func (s *Service) AdminIssueForPrincipal(
	ctx context.Context,
	scope tenancy.Scope,
	userID, activeAccountID string,
) (*SignIn, error) {
	return s.issueForPrincipal(ctx, scope, userID, activeAccountID, true)
}

// issueForPrincipal is both doors that mint for a principal somebody else
// proved.
func (s *Service) issueForPrincipal(
	ctx context.Context,
	scope tenancy.Scope,
	userID, activeAccountID string,
	administrative bool,
) (signIn *SignIn, err error) {
	name := opIssueForPrincipal
	if administrative {
		name = opAdminIssueForPrincipal
	}

	ctx, op, done := s.begin(ctx, name,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(adminKey, administrative),
	)
	defer func() { done(err) }()

	if err = scope.Validate(); err != nil {
		return nil, op.Error(err, "checking the scope a sign-in was issued in")
	}

	if userID == "" {
		return nil, op.Error(ErrEmptyUserID, "issuing a sign-in for a proven principal")
	}

	op.Set(userIDKey, userID)

	// The user row rather than the principal first, for the reason Service.prove
	// reads it first: the status refusals are told apart here, where the row and
	// its explanation are, and identity's principal read would collapse all
	// three into one.
	user, err := s.directory.GetUser(ctx, s.client.Reader(), scope, userID)
	if err != nil {
		return nil, op.Error(err, "reading the user a sign-in was issued for")
	}

	attempt := &FailedSignIn{UserID: user.ID, Administrative: administrative}

	if !user.AccountStatus.AdmitsSignIn() {
		return nil, s.refuse(ctx, op, scope, attempt, statusRefusal(user), "admitting a sign-in")
	}

	if administrative {
		if err = s.verifyAdministrator(user); err != nil {
			return nil, s.refuse(ctx, op, scope, attempt, err, "admitting an administrative sign-in")
		}
	}

	principal, err := s.directory.GetPrincipal(ctx, s.client.Reader(), scope, user.ID, activeAccountID)
	if err != nil {
		return nil, op.Error(err, "resolving the principal for a sign-in")
	}

	op.Set(accountIDKey, principal.ActiveAccountID)

	// The login this sign-in begins, minted here for Service.login's reason.
	familyID := identifiers.New()
	op.Set(familyKey, familyID)

	if signIn, err = s.mintToken(ctx, principal, familyID, administrative); err != nil {
		return nil, op.Error(err, "issuing a token")
	}

	auth := &Authentication{Principal: principal, Administrative: administrative}

	// Service.login's transaction, minus the recovery code: nothing was proven
	// here, so there is nothing of the proof's to spend.
	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		if txErr := s.mintRefreshToken(ctx, tx, scope, signIn, familyID, time.Time{}); txErr != nil {
			return txErr
		}

		if hookErr := s.hooks.AfterAuthenticate(ctx, tx, scope, auth); hookErr != nil {
			return hookErr
		}

		return s.hooks.AfterIssueToken(ctx, tx, scope, signIn)
	}); err != nil {
		return nil, op.Error(err, "recording a sign-in")
	}

	return signIn, nil
}

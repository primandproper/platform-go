package signin

import (
	"context"

	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/observability"
	"github.com/primandproper/primitives-go/tenancy"
)

// GetAuthStatus reports where a signed-in caller stands: who they are, which
// account they are in, and what a client has to make them do before anything
// else.
//
// It is the call a client makes on load, and it is two reads rather than one.
// The principal resolves the active account and the memberships, and returns
// the user redacted; the three booleans an application acts on — whether they
// hold a password, whether their second factor is proven, whether their address
// is verified — are computed from columns a redacted user does not carry. So
// the user is read a second time, unredacted, and only the answers cross back.
// That is the price of a redaction that is not optional, and it is the right
// price: the alternative is a principal read that returns a password hash to
// everything that calls it.
//
// It reads on the client's reader, outside any transaction, so what it reports
// is what committed rather than a snapshot.
//
// This service answers it for a caller who is signed in. Whether a caller *is*
// signed in is the transport's question, and signin/grpc answers it there —
// which is why there is no "authenticated" field here to be false.
func (s *Service) GetAuthStatus(
	ctx context.Context,
	scope tenancy.Scope,
	userID, activeAccountID string,
) (status *AuthStatus, err error) {
	ctx, op, done := s.begin(ctx, opGetAuthStatus,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer func() { done(err) }()

	if userID == "" {
		return nil, op.Error(ErrEmptyUserID, "reading an authentication status")
	}

	principal, err := s.directory.GetPrincipal(ctx, s.client.Reader(), scope, userID, activeAccountID)
	if err != nil {
		return nil, op.Error(err, "resolving the principal for an authentication status")
	}

	op.Set(accountIDKey, principal.ActiveAccountID)

	user, err := s.directory.GetUser(ctx, s.client.Reader(), scope, userID)
	if err != nil {
		return nil, op.Error(err, "reading the user behind an authentication status")
	}

	return &AuthStatus{
		User:                   principal.User,
		ActiveAccountID:        principal.ActiveAccountID,
		AccountIDs:             principal.AccountIDs(),
		HasPassword:            user.HasPassword(),
		TwoFactorEnrolled:      user.TwoFactorEnabled(),
		RequiresPasswordChange: user.RequiresPasswordChange,
		EmailAddressVerified:   user.EmailAddressVerified(),
	}, nil
}

// GetSelf reads the calling user, redacted.
//
// It is the profile read a client makes for the person using it, and it is here
// rather than only on the directory because a sign-in service that cannot
// answer "who am I" makes every consumer wire a second client to find out. It
// is the same row identity's own GetUser returns and the same redaction.
//
// The subject is the userID argument and there is no way to name somebody else:
// a transport passes the caller's own ID off the principal it resolved. Reading
// another user is the directory's [identity.DirectoryReader.GetUser], behind the
// permission that guards it.
func (s *Service) GetSelf(
	ctx context.Context,
	scope tenancy.Scope,
	userID string,
) (user *identity.User, err error) {
	ctx, op, done := s.begin(ctx, opGetSelf,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer func() { done(err) }()

	if userID == "" {
		return nil, op.Error(ErrEmptyUserID, "reading the calling user")
	}

	if user, err = s.directory.GetUser(ctx, s.client.Reader(), scope, userID); err != nil {
		return nil, op.Error(err, "reading the calling user")
	}

	return user.Redacted(), nil
}

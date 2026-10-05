package signin

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v15/identity"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// DefaultRecentSignInWindow is how long after a login began it still counts as
// re-authentication for a handle change made through it.
//
// Ten minutes is long enough for somebody who signed in to change their
// address to reach the form, and short enough that a session left open on a
// shared machine has stopped counting before anybody else sits down at it. It
// is a default rather than a policy, and [WithRecentSignInWindow] replaces it.
const DefaultRecentSignInWindow = 10 * time.Minute

// ProfileUpdater is the profile write a handle change makes, and it is
// optional: a service built without [WithProfileUpdater] refuses
// [Service.UpdateEmailAddress] and [Service.UpdateUsername] with
// [ErrProfileUpdaterNotConfigured].
//
// It is identity's Service rather than its Store, for [Registrar]'s reason. The
// write is identity's, and so is the hook a consumer records it with:
// identity.Hooks.AfterUpdateProfile fires whichever door made the change, so an
// audit trail of address changes is one hook rather than one per door. This
// package adds no hook of its own for the same reason — a second hook on the
// same write would be a second place for a consumer to have forgotten one.
//
// [github.com/primandproper/platform-go/v15/identity.Service] satisfies it.
type ProfileUpdater interface {
	// UpdateProfile saves the fields a user may change about themselves and
	// answers with the user as they stand after it, redacted.
	UpdateProfile(
		ctx context.Context,
		scope tenancy.Scope,
		userID string,
		update *identity.ProfileUpdate,
	) (*identity.User, error)
}

// HandleReauthentication is what a handle change offers as proof that the
// person making it is the person the account belongs to, beyond being signed
// in.
//
// It is one of two things. CurrentPassword, with TOTPCode from a user who holds
// a proven second factor — exactly what [Service.UpdatePassword] asks for. Or a
// login recent enough to stand in for one: FamilyID names the login the request
// came through, and a login that began inside [WithRecentSignInWindow] proves
// what typing the password again would have. A password holder may offer
// either; a user who holds no password has only the second.
//
// The second is what makes a passwordless user able to change their address at
// all, and it is the same proof a password re-entry is, made a few minutes
// earlier: the sign-in that began the login was a password, a sign-in link or a
// passkey, with the second factor the service required of it. A refresh does
// not move it, so a login kept alive for a month is a month old.
type HandleReauthentication struct {
	_ struct{} `json:"-"`

	// CurrentPassword is the caller's password. Empty means the caller is
	// offering a recent sign-in instead.
	CurrentPassword string `json:"-"`

	// TOTPCode is the second-factor code, required alongside CurrentPassword
	// from a user who holds a proven second factor. It is not read when no
	// password is sent: the recent sign-in already asked for it.
	TOTPCode string `json:"-"`

	// FamilyID is the login the request came through — the access token's
	// [ClaimFamilyID], taken off the caller by a transport and never off a
	// request body, since a client naming any login it liked could name the
	// freshest one somebody else holds. It is read only when no password is
	// sent.
	FamilyID string `json:"-"`
}

// EmailAddressUpdate is an address change by the person whose address it is.
type EmailAddressUpdate struct {
	_ struct{} `json:"-"`

	HandleReauthentication

	// NewEmailAddress is the address that replaces the current one.
	NewEmailAddress string `json:"newEmailAddress"`
}

// UsernameUpdate is a username change by the person whose username it is.
type UsernameUpdate struct {
	_ struct{} `json:"-"`

	HandleReauthentication

	// NewUsername is the handle that replaces the current one, as the person
	// spelled it. What is stored is identity's fold of it.
	NewUsername string `json:"newUsername"`
}

// UpdateEmailAddress moves the calling user's address, once they have proven
// again that they are who the account belongs to.
//
// An email address is the recovery path for the password, which makes it a
// credential in all but name: a stolen session that could move it would follow
// up with a password reset and own the account. So it asks what
// [Service.UpdatePassword] asks — see [HandleReauthentication] for the two
// proofs it takes — and refuses as that does: a wrong password or code is
// [ErrInvalidCredentials], a missing code [ErrSecondFactorRequired], and a
// password sent by somebody who holds none [ErrNoPasswordCredential]. Neither
// proof offered, or a login too old to stand in for one, is
// [ErrReauthenticationRequired].
//
// The write is identity's — see [ProfileUpdater] — and it clears the old
// address's proof in the store's own statement, since the column records that a
// link was mailed rather than where to. A service built with
// [WithVerifications] and [WithVerificationMailer] then mails the new address a
// link, as [Service.RequestVerificationEmail] would, after the change has
// committed; one built without them leaves the address unproven and the client
// to ask for a link.
//
// An error from the mailer is returned, and by then the change has landed. That
// is the honest reading of the order: the address moved and nobody has a link
// for it yet. Sending the same request again is the remedy — the change is
// already made, so it writes nothing, finds the address unproven and mails
// another.
func (s *Service) UpdateEmailAddress(
	ctx context.Context,
	scope tenancy.Scope,
	userID string,
	update *EmailAddressUpdate,
) (user *identity.User, err error) {
	ctx, op, done := s.begin(ctx, opUpdateEmailAddress,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer func() { done(err) }()

	if update == nil {
		return nil, op.Error(ErrNilEmailAddressUpdate, "updating an email address")
	}

	if update.NewEmailAddress == "" {
		return nil, op.Error(ErrEmptyNewEmailAddress, "updating an email address")
	}

	user, err = s.updateHandle(ctx, scope, userID, &update.HandleReauthentication,
		&identity.ProfileUpdate{EmailAddress: &update.NewEmailAddress})
	if err != nil {
		return nil, op.Error(err, "updating an email address")
	}

	// Read off the write's own answer rather than off whether this request
	// moved anything, so a retry after a failed send mails again.
	if user.EmailAddressVerified() || s.verifications == nil || s.verificationMailer == nil {
		return user, nil
	}

	if err = s.resendVerification(ctx, scope, userID); err != nil {
		return nil, op.Error(err, "mailing a moved address its verification link")
	}

	return user, nil
}

// UpdateUsername renames the calling user, once they have proven again that
// they are who the account belongs to.
//
// A username is what somebody signs in with, so it is guarded as the address
// is, with the same two proofs and the same refusals — see
// [Service.UpdateEmailAddress]. It mails nothing: a username proves nothing
// about reachability, so there is nothing to verify.
func (s *Service) UpdateUsername(
	ctx context.Context,
	scope tenancy.Scope,
	userID string,
	update *UsernameUpdate,
) (user *identity.User, err error) {
	ctx, op, done := s.begin(ctx, opUpdateUsername,
		observability.WithValue(scopeKey, scope.String()),
		observability.WithValue(userIDKey, userID),
	)
	defer func() { done(err) }()

	if update == nil {
		return nil, op.Error(ErrNilUsernameUpdate, "updating a username")
	}

	if update.NewUsername == "" {
		return nil, op.Error(ErrEmptyNewUsername, "updating a username")
	}

	user, err = s.updateHandle(ctx, scope, userID, &update.HandleReauthentication,
		&identity.ProfileUpdate{Username: &update.NewUsername})
	if err != nil {
		return nil, op.Error(err, "updating a username")
	}

	return user, nil
}

// updateHandle is what both handle doors share: the user read, the
// re-authentication, and the write.
//
// It is one function rather than two copies for reauthenticate's reason — the
// copy that forgets a proof is the door a stolen session walks through.
func (s *Service) updateHandle(
	ctx context.Context,
	scope tenancy.Scope,
	userID string,
	proof *HandleReauthentication,
	update *identity.ProfileUpdate,
) (*identity.User, error) {
	if s.profiles == nil {
		return nil, ErrProfileUpdaterNotConfigured
	}

	if userID == "" {
		return nil, ErrEmptyUserID
	}

	user, err := s.directory.GetUser(ctx, s.client.Reader(), scope, userID)
	if err != nil {
		return nil, platformerrors.Wrap(err, "reading the user whose handle is changing")
	}

	if err = s.reauthenticateHandleChange(ctx, scope, user, proof); err != nil {
		return nil, err
	}

	updated, err := s.profiles.UpdateProfile(ctx, scope, userID, update)
	if err != nil {
		return nil, platformerrors.Wrap(err, "writing a new handle")
	}

	return updated, nil
}

// reauthenticateHandleChange decides whether proof is enough to change one of
// user's handles: the password and second factor again, or a login recent
// enough to stand in for them. See HandleReauthentication.
func (s *Service) reauthenticateHandleChange(
	ctx context.Context,
	scope tenancy.Scope,
	user *identity.User,
	proof *HandleReauthentication,
) error {
	// A password sent is a password checked, whatever else the request
	// carries: a wrong one is refused as a wrong one, rather than passed over
	// for a recent sign-in that would make guessing it free.
	if proof.CurrentPassword != "" {
		if !user.HasPassword() {
			return ErrNoPasswordCredential
		}

		// No recovery code, for UpdatePassword's reason.
		_, err := s.reauthenticate(ctx, scope, user, proof.CurrentPassword, proof.TOTPCode, false)

		return err
	}

	return s.recentSignIn(ctx, scope, user.ID, proof.FamilyID)
}

// recentSignIn answers whether familyID is a login of userID's own that began
// inside the recent sign-in window.
//
// Every way of failing is ErrReauthenticationRequired, because the remedy is
// one: sign in again, or send the password. What failed is wrapped around it,
// which reaches the log and the span and not the client: the wire carries the
// sentinel's own words.
func (s *Service) recentSignIn(ctx context.Context, scope tenancy.Scope, userID, familyID string) error {
	refuse := func(reason string) error {
		return platformerrors.Wrap(ErrReauthenticationRequired, reason)
	}

	if s.refreshTokens == nil || s.recentSignInWindow <= 0 {
		return refuse("no recent sign-in is accepted on this service")
	}

	if familyID == "" {
		return refuse("the request names no sign-in")
	}

	live, err := s.refreshTokens.LiveToken(ctx, s.client.Reader(), scope, familyID)
	if err != nil {
		if platformerrors.Is(err, ErrSignInEnded) {
			return refuse("the sign-in has ended")
		}

		return platformerrors.Wrap(err, "reading the sign-in a handle change came through")
	}

	// A login somebody else holds is not proof about this person, and an
	// impersonation proves nothing about anybody but the operator — who is
	// exactly who a handle change made through one must not be from.
	switch {
	case live.SubjectID != userID:
		return refuse("the sign-in is somebody else's")
	case live.ActorID != "" || live.CredentialKind == CredentialKindImpersonation:
		return refuse("the sign-in is an impersonation")
	case s.clk.Now().Sub(live.SignedInAt) > s.recentSignInWindow:
		return refuse("the sign-in is not recent")
	}

	return nil
}

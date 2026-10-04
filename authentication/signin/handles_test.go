package signin_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// profileHooks records identity's AfterUpdateProfile, which is the one hook a
// handle change fires.
type profileHooks struct {
	identity.NoopHooks

	changed [][]string
	mu      sync.Mutex
}

func (h *profileHooks) AfterUpdateProfile(
	_ context.Context,
	_ database.Tx,
	_ tenancy.Scope,
	_ *identity.User,
	changed []string,
) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.changed = append(h.changed, changed)

	return nil
}

func (h *profileHooks) runs() [][]string {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.changed
}

// lateProfiles is a ProfileUpdater whose target is wired after the service is
// built, because the env builds the identity store the target needs and the
// service in one call.
type lateProfiles struct {
	target signin.ProfileUpdater
}

func (p *lateProfiles) UpdateProfile(
	ctx context.Context,
	scope tenancy.Scope,
	userID string,
	update *identity.ProfileUpdate,
) (*identity.User, error) {
	return p.target.UpdateProfile(ctx, scope, userID, update)
}

// handleEnv is a refresh env whose handle doors write through an identity
// Service carrying profileHooks, and whose verification links reach a mailbox.
func handleEnv(t *testing.T, opts ...signin.ServiceOption) (*env, *profileHooks, *verificationMailbox) {
	t.Helper()

	profiles := &lateProfiles{}
	mailbox := &verificationMailbox{}

	e := newRefreshEnv(t, append([]signin.ServiceOption{
		signin.WithProfileUpdater(profiles),
		signin.WithVerificationMailer(mailbox),
	}, opts...)...)

	hooks := &profileHooks{}

	directory, err := identity.NewService(e.client, e.store, hooks)
	must.NoError(t, err)

	profiles.target = directory

	return e, hooks, mailbox
}

func TestService_UpdateEmailAddress(T *testing.T) {
	T.Parallel()

	T.Run("the current password moves the address, clears its proof and mails the new one a link", func(t *testing.T) {
		t.Parallel()

		e, hooks, mailbox := handleEnv(t)
		e.verifyAddress(t)

		updated, err := e.svc.UpdateEmailAddress(t.Context(), testScope, e.user.ID, &signin.EmailAddressUpdate{
			CurrentPassword: e.password,
			NewEmailAddress: "jane.new@example.com",
		})
		must.NoError(t, err)

		test.EqOp(t, "jane.new@example.com", updated.EmailAddress)
		test.False(t, updated.EmailAddressVerified(), test.Sprint("a moved address kept the old one's proof"))
		test.EqOp(t, "", updated.HashedPassword, test.Sprint("the answer was not redacted"))

		must.SliceLen(t, 1, hooks.runs())
		test.Eq(t, []string{"emailAddress"}, hooks.runs()[0])

		must.EqOp(t, 1, mailbox.count())
		test.EqOp(t, "jane.new@example.com", mailbox.last(t).User.EmailAddress)

		// The link it mailed is the one that proves the new address.
		must.NoError(t, e.svc.VerifyEmailAddress(t.Context(), testScope, mailbox.last(t).Token))

		stored, err := e.store.GetUser(t.Context(), e.client.Reader(), testScope, e.user.ID)
		must.NoError(t, err)
		test.True(t, stored.EmailAddressVerified())
	})

	T.Run("a wrong password is refused and changes nothing", func(t *testing.T) {
		t.Parallel()

		e, hooks, mailbox := handleEnv(t)

		_, err := e.svc.UpdateEmailAddress(t.Context(), testScope, e.user.ID, &signin.EmailAddressUpdate{
			CurrentPassword: "not it",
			NewEmailAddress: "thief@example.com",
		})
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)

		stored, err := e.store.GetUser(t.Context(), e.client.Reader(), testScope, e.user.ID)
		must.NoError(t, err)
		test.EqOp(t, "jane@example.com", stored.EmailAddress)
		test.SliceEmpty(t, hooks.runs())
		test.EqOp(t, 0, mailbox.count())
	})

	T.Run("a wrong password is refused even on a recent sign-in", func(t *testing.T) {
		t.Parallel()

		e, _, _ := handleEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		// A password sent is a password checked: passing a wrong one over for
		// the sign-in beside it would make guessing it free.
		_, err = e.svc.UpdateEmailAddress(t.Context(), testScope, e.user.ID, &signin.EmailAddressUpdate{
			CurrentPassword: "not it", FamilyID: signedIn.FamilyID,
			NewEmailAddress: "thief@example.com",
		})
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
	})

	T.Run("a proven second factor is required alongside the password", func(t *testing.T) {
		t.Parallel()

		e, _, _ := handleEnv(t)
		secret := e.enrollTOTP(t)

		_, err := e.svc.UpdateEmailAddress(t.Context(), testScope, e.user.ID, &signin.EmailAddressUpdate{
			CurrentPassword: e.password,
			NewEmailAddress: "jane.new@example.com",
		})
		test.ErrorIs(t, err, signin.ErrSecondFactorRequired)

		_, err = e.svc.UpdateEmailAddress(t.Context(), testScope, e.user.ID, &signin.EmailAddressUpdate{
			CurrentPassword: e.password, TOTPCode: code(t, secret),
			NewEmailAddress: "jane.new@example.com",
		})
		must.NoError(t, err)
	})

	T.Run("a recent sign-in stands in for the password", func(t *testing.T) {
		t.Parallel()

		e, hooks, _ := handleEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		updated, err := e.svc.UpdateEmailAddress(t.Context(), testScope, e.user.ID, &signin.EmailAddressUpdate{
			FamilyID:        signedIn.FamilyID,
			NewEmailAddress: "jane.new@example.com",
		})
		must.NoError(t, err)
		test.EqOp(t, "jane.new@example.com", updated.EmailAddress)
		test.SliceLen(t, 1, hooks.runs())
	})

	T.Run("a passwordless user changes their address by a recent sign-in", func(t *testing.T) {
		t.Parallel()

		e, _, mailbox := handleEnv(t)
		passwordless := e.registerPasswordless(t, "passkeyonly")

		signedIn, err := e.svc.IssueForPrincipal(t.Context(), testScope, passwordless.ID, "")
		must.NoError(t, err)

		updated, err := e.svc.UpdateEmailAddress(t.Context(), testScope, passwordless.ID, &signin.EmailAddressUpdate{
			FamilyID:        signedIn.FamilyID,
			NewEmailAddress: "passkeyonly.new@example.com",
		})
		must.NoError(t, err)
		test.EqOp(t, "passkeyonly.new@example.com", updated.EmailAddress)
		test.EqOp(t, 1, mailbox.count())

		// A password from somebody who holds none is told so, as UpdatePassword
		// tells them.
		_, err = e.svc.UpdateEmailAddress(t.Context(), testScope, passwordless.ID, &signin.EmailAddressUpdate{
			CurrentPassword: "anything",
			NewEmailAddress: "passkeyonly.again@example.com",
		})
		test.ErrorIs(t, err, signin.ErrNoPasswordCredential)
	})

	T.Run("neither proof is refused", func(t *testing.T) {
		t.Parallel()

		e, hooks, _ := handleEnv(t)

		_, err := e.svc.UpdateEmailAddress(t.Context(), testScope, e.user.ID, &signin.EmailAddressUpdate{
			NewEmailAddress: "thief@example.com",
		})
		test.ErrorIs(t, err, signin.ErrReauthenticationRequired)
		test.SliceEmpty(t, hooks.runs())
	})

	T.Run("a login older than the window is refused", func(t *testing.T) {
		t.Parallel()

		e, _, _ := handleEnv(t, signin.WithRecentSignInWindow(time.Nanosecond))

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		time.Sleep(time.Millisecond)

		_, err = e.svc.UpdateEmailAddress(t.Context(), testScope, e.user.ID, &signin.EmailAddressUpdate{
			FamilyID:        signedIn.FamilyID,
			NewEmailAddress: "thief@example.com",
		})
		test.ErrorIs(t, err, signin.ErrReauthenticationRequired)
	})

	T.Run("a service built without recent sign-ins asks for the password", func(t *testing.T) {
		t.Parallel()

		e, _, _ := handleEnv(t, signin.WithoutRecentSignIn())

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		_, err = e.svc.UpdateEmailAddress(t.Context(), testScope, e.user.ID, &signin.EmailAddressUpdate{
			FamilyID:        signedIn.FamilyID,
			NewEmailAddress: "jane.new@example.com",
		})
		test.ErrorIs(t, err, signin.ErrReauthenticationRequired)
	})

	T.Run("somebody else's login is not proof", func(t *testing.T) {
		t.Parallel()

		e, _, _ := handleEnv(t)
		other := e.registerPasswordless(t, "mallory")

		theirs, err := e.svc.IssueForPrincipal(t.Context(), testScope, other.ID, "")
		must.NoError(t, err)

		_, err = e.svc.UpdateEmailAddress(t.Context(), testScope, e.user.ID, &signin.EmailAddressUpdate{
			FamilyID:        theirs.FamilyID,
			NewEmailAddress: "mallory@example.net",
		})
		test.ErrorIs(t, err, signin.ErrReauthenticationRequired)
	})

	T.Run("an impersonation is not proof", func(t *testing.T) {
		t.Parallel()

		e, _, _ := handleEnv(t, signin.WithImpersonationPolicy(admitAll))
		operator := e.newOperator(t)

		// The login is the subject's and it began a moment ago, and it proves
		// nothing about them: the operator is exactly who a handle change made
		// through it must not be from.
		impersonation, err := e.svc.IssueImpersonationToken(t.Context(), testScope, operator.ID, testScope, e.user.ID, "")
		must.NoError(t, err)

		_, err = e.svc.UpdateEmailAddress(t.Context(), testScope, e.user.ID, &signin.EmailAddressUpdate{
			FamilyID:        impersonation.FamilyID,
			NewEmailAddress: "operator@example.net",
		})
		test.ErrorIs(t, err, signin.ErrReauthenticationRequired)
	})

	T.Run("an ended login is not proof", func(t *testing.T) {
		t.Parallel()

		e, _, _ := handleEnv(t)

		signedIn, err := e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err)

		must.NoError(t, e.svc.SignOut(t.Context(), testScope, signedIn.RefreshToken))

		_, err = e.svc.UpdateEmailAddress(t.Context(), testScope, e.user.ID, &signin.EmailAddressUpdate{
			FamilyID:        signedIn.FamilyID,
			NewEmailAddress: "jane.new@example.com",
		})
		test.ErrorIs(t, err, signin.ErrReauthenticationRequired)
	})

	T.Run("a failed send is returned, and asking again mails another", func(t *testing.T) {
		t.Parallel()

		e, _, mailbox := handleEnv(t)
		mailbox.err = platformerrors.New("mail server is down")

		update := &signin.EmailAddressUpdate{
			CurrentPassword: e.password,
			NewEmailAddress: "jane.new@example.com",
		}

		_, err := e.svc.UpdateEmailAddress(t.Context(), testScope, e.user.ID, update)
		test.ErrorIs(t, err, mailbox.err)

		mailbox.mu.Lock()
		mailbox.err = nil
		mailbox.mu.Unlock()

		updated, err := e.svc.UpdateEmailAddress(t.Context(), testScope, e.user.ID, update)
		must.NoError(t, err)
		test.EqOp(t, "jane.new@example.com", updated.EmailAddress)
		test.EqOp(t, 1, mailbox.count())
	})

	T.Run("a service with no mailer moves the address and mails nothing", func(t *testing.T) {
		t.Parallel()

		profiles := &lateProfiles{}
		e := newEnv(t, signin.WithProfileUpdater(profiles))
		profiles.target = e.directory

		updated, err := e.svc.UpdateEmailAddress(t.Context(), testScope, e.user.ID, &signin.EmailAddressUpdate{
			CurrentPassword: e.password,
			NewEmailAddress: "jane.new@example.com",
		})
		must.NoError(t, err)
		test.EqOp(t, "jane.new@example.com", updated.EmailAddress)
	})

	T.Run("a service with no profile updater refuses", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		_, err := e.svc.UpdateEmailAddress(t.Context(), testScope, e.user.ID, &signin.EmailAddressUpdate{
			CurrentPassword: e.password,
			NewEmailAddress: "jane.new@example.com",
		})
		test.ErrorIs(t, err, signin.ErrProfileUpdaterNotConfigured)
	})

	T.Run("the empty inputs", func(t *testing.T) {
		t.Parallel()

		e, _, _ := handleEnv(t)

		_, err := e.svc.UpdateEmailAddress(t.Context(), testScope, e.user.ID, nil)
		test.ErrorIs(t, err, signin.ErrNilEmailAddressUpdate)

		_, err = e.svc.UpdateEmailAddress(t.Context(), testScope, e.user.ID, &signin.EmailAddressUpdate{})
		test.ErrorIs(t, err, signin.ErrEmptyNewEmailAddress)

		_, err = e.svc.UpdateEmailAddress(t.Context(), testScope, "", &signin.EmailAddressUpdate{NewEmailAddress: "a@example.com"})
		test.ErrorIs(t, err, signin.ErrEmptyUserID)
	})
}

func TestService_UpdateUsername(T *testing.T) {
	T.Parallel()

	T.Run("the current password renames, and mails nothing", func(t *testing.T) {
		t.Parallel()

		e, hooks, mailbox := handleEnv(t)

		updated, err := e.svc.UpdateUsername(t.Context(), testScope, e.user.ID, &signin.UsernameUpdate{
			CurrentPassword: e.password,
			NewUsername:     "jane2",
		})
		must.NoError(t, err)
		test.EqOp(t, "jane2", updated.Username)

		must.SliceLen(t, 1, hooks.runs())
		test.Eq(t, []string{"username"}, hooks.runs()[0])
		test.EqOp(t, 0, mailbox.count())

		_, err = e.svc.LoginForToken(t.Context(), testScope, &signin.Credentials{Username: "jane2", Password: e.password})
		must.NoError(t, err, must.Sprint("the new username does not sign in"))
	})

	T.Run("a wrong password is refused and changes nothing", func(t *testing.T) {
		t.Parallel()

		e, hooks, _ := handleEnv(t)

		_, err := e.svc.UpdateUsername(t.Context(), testScope, e.user.ID, &signin.UsernameUpdate{
			CurrentPassword: "not it",
			NewUsername:     "thief",
		})
		test.ErrorIs(t, err, signin.ErrInvalidCredentials)
		test.SliceEmpty(t, hooks.runs())

		_, err = e.svc.LoginForToken(t.Context(), testScope, e.credentials())
		must.NoError(t, err, must.Sprint("a refused rename moved the username anyway"))
	})

	T.Run("neither proof is refused", func(t *testing.T) {
		t.Parallel()

		e, _, _ := handleEnv(t)

		_, err := e.svc.UpdateUsername(t.Context(), testScope, e.user.ID, &signin.UsernameUpdate{NewUsername: "thief"})
		test.ErrorIs(t, err, signin.ErrReauthenticationRequired)
	})

	T.Run("the empty inputs", func(t *testing.T) {
		t.Parallel()

		e, _, _ := handleEnv(t)

		_, err := e.svc.UpdateUsername(t.Context(), testScope, e.user.ID, nil)
		test.ErrorIs(t, err, signin.ErrNilUsernameUpdate)

		_, err = e.svc.UpdateUsername(t.Context(), testScope, e.user.ID, &signin.UsernameUpdate{})
		test.ErrorIs(t, err, signin.ErrEmptyNewUsername)
	})
}

// verifyAddress proves the env user's address, so a test can see a change
// withdraw the proof.
func (e *env) verifyAddress(t *testing.T) {
	t.Helper()

	must.NoError(t, e.client.WithTransaction(t.Context(), func(tx database.Tx) error {
		return e.store.MarkUserEmailAddressProven(t.Context(), tx, testScope, e.user.ID)
	}))
}

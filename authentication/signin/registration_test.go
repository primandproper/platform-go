package signin_test

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The registration suite. It runs against the same live SQLite database and
// real identity store the rest of the package's tests do, for the same reason:
// what these operations are about is which rows are written, in what order, and
// what a subsequent sign-in then does with them — and a store of stubs would
// assert that this package calls the methods it calls.
//
// The property nearly every case here is really about is the one the whole
// change exists for: somebody registered over this package can sign in
// afterwards. So most of them end by actually signing in.

// newRegistration is the happy-path registration each test then breaks in one
// specific way. It names a username of its own, because the suite's env already
// holds "jane".
func newRegistration(username string, credential signin.Credential) *signin.Registration {
	return &signin.Registration{
		User: &identity.User{
			Username:     username,
			EmailAddress: username + "@example.com",
			FirstName:    "New",
			Scope:        testScope,
		},
		Account:    &identity.Account{Name: username + "'s", Scope: testScope},
		Credential: credential,
		OwnerRoles: []string{"owner"},
	}
}

func TestService_Register_password(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	registered, err := e.svc.Register(T.Context(), testScope, newRegistration("ada", signin.Password("hunter2 hunter2")))
	must.NoError(T, err)

	must.NotNil(T, registered.User)
	test.EqOp(T, "ada", registered.User.Username)
	must.NotNil(T, registered.Account)
	must.NotNil(T, registered.Membership)
	test.Nil(T, registered.Invitation)

	// Redacted, unlike identity's own registration results: this is the one
	// operation here that assembles a credential, so it is the one whose result
	// would otherwise carry a hash out of the package.
	test.EqOp(T, "", registered.User.HashedPassword)

	// The row holds the hash the service produced, which is the fact that makes
	// the sign-in below possible. Read through the store rather than asserted
	// from the argument.
	stored, err := e.store.GetUser(T.Context(), e.client.Reader(), testScope, registered.User.ID)
	must.NoError(T, err)
	test.True(T, stored.HasPassword())
	test.NotEqOp(T, "hunter2 hunter2", stored.HashedPassword)

	matches, err := argon2.NewArgon2Authenticator().PasswordMatches(T.Context(), stored.HashedPassword, "hunter2 hunter2")
	must.NoError(T, err)
	test.True(T, matches)

	// And the standing they land in is the one that admits no sign-in, which is
	// what the verification door exists to move.
	test.EqOp(T, identity.StatusUnverified, stored.AccountStatus)
}

func TestService_Register_thenVerifyThenSignIn(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	registered, err := e.svc.Register(T.Context(), testScope, newRegistration("ada", signin.Password("hunter2 hunter2")))
	must.NoError(T, err)
	must.NotEq(T, "", registered.EmailAddressVerificationToken)

	credentials := &signin.Credentials{Username: "ada", Password: "hunter2 hunter2"}

	// Before the link is answered: the password is right and the standing is
	// not, which is the dead end this whole change is about.
	_, err = e.svc.LoginForToken(T.Context(), testScope, credentials)
	test.ErrorIs(T, err, signin.ErrUserUnverified)

	must.NoError(T, e.svc.VerifyEmailAddress(T.Context(), testScope, registered.EmailAddressVerificationToken))

	signedIn, err := e.svc.LoginForToken(T.Context(), testScope, credentials)
	must.NoError(T, err)
	must.NotNil(T, signedIn.Principal)
	test.EqOp(T, registered.User.ID, signedIn.Principal.User.ID)
}

func TestService_Register_noPassword(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	registered, err := e.svc.Register(T.Context(), testScope, newRegistration("ada", signin.NoPassword()))
	must.NoError(T, err)

	stored, err := e.store.GetUser(T.Context(), e.client.Reader(), testScope, registered.User.ID)
	must.NoError(T, err)
	test.False(T, stored.HasPassword())

	must.NoError(T, e.svc.VerifyEmailAddress(T.Context(), testScope, registered.EmailAddressVerificationToken))

	// Verified, in good standing, and still with no way through this package's
	// doors — which is the cost NoPassword's documentation states rather than
	// leaves to be discovered. The refusal is the collapsed one: at the door,
	// "this account holds no password" names an account to whoever guessed a
	// handle.
	_, err = e.svc.LoginForToken(T.Context(), testScope,
		&signin.Credentials{Username: "ada", Password: "anything at all"})
	test.ErrorIs(T, err, signin.ErrInvalidCredentials)
}

func TestService_Register_namingNoCredential(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	// The case the Credential type exists for: a caller who left the field
	// alone, which is indistinguishable from a form that submitted an empty
	// password.
	_, err := e.svc.Register(T.Context(), testScope, newRegistration("ada", nil))
	test.ErrorIs(T, err, signin.ErrNoCredentialNamed)

	// And nothing was written. A registration refused for naming no credential
	// must not leave the user behind, since the refusal is about how they would
	// have signed in.
	_, err = e.store.GetUserByUsername(T.Context(), e.client.Reader(), testScope, "ada")
	test.ErrorIs(T, err, identity.ErrUserNotFound)
}

func TestService_Register_emptyPassword(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	// Password("") is a caller who meant to say something and said nothing,
	// which is not the same as NoPassword and is not treated as it.
	_, err := e.svc.Register(T.Context(), testScope, newRegistration("ada", signin.Password("")))
	test.ErrorIs(T, err, signin.ErrEmptyPassword)

	_, err = e.store.GetUserByUsername(T.Context(), e.client.Reader(), testScope, "ada")
	test.ErrorIs(T, err, identity.ErrUserNotFound)
}

func TestService_Register_refusals(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	_, err := e.svc.Register(T.Context(), testScope, nil)
	test.ErrorIs(T, err, signin.ErrNilRegistration)

	_, err = e.svc.Register(T.Context(), testScope, &signin.Registration{Credential: signin.NoPassword()})
	test.ErrorIs(T, err, identity.ErrNilUser)

	registration := newRegistration("ada", signin.NoPassword())
	registration.Account = nil

	_, err = e.svc.Register(T.Context(), testScope, registration)
	test.ErrorIs(T, err, identity.ErrNilAccount)
}

func TestService_Register_leavesTheCallersValueAlone(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	registration := newRegistration("ada", signin.Password("hunter2 hunter2"))

	_, err := e.svc.Register(T.Context(), testScope, registration)
	must.NoError(T, err)

	// Neither the hash nor the live verification token is written back onto what
	// the caller handed in, which is the reading identity's registrar already
	// takes of its own argument: a caller who goes on to use this value would
	// otherwise be holding both secrets.
	test.EqOp(T, "", registration.User.HashedPassword)
	test.EqOp(T, "", registration.User.EmailAddressVerificationToken)
	test.EqOp(T, "", registration.User.ID)
}

func TestService_Register_withoutARegistrar(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	// A service built as one was before registration existed: a credential check
	// over a directory something else fills.
	svc, err := signin.NewService(e.client, e.store, argon2.NewArgon2Authenticator(), e.issuer)
	must.NoError(T, err)

	_, err = svc.Register(T.Context(), testScope, newRegistration("ada", signin.NoPassword()))
	test.ErrorIs(T, err, signin.ErrRegistrationNotConfigured)
}

func TestService_Register_withInvitation(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	invitation, err := e.directory.Invite(T.Context(), testScope, &identity.Invitation{
		Scope:            testScope,
		FromUser:         e.user.ID,
		BelongsToAccount: e.accountID,
		ToEmail:          "ada@example.com",
		Token:            "invitation-token",
		Roles:            []string{"member"},
		ExpiresAt:        time.Now().UTC().Add(time.Hour),
	})
	must.NoError(T, err)

	registration := newRegistration("ada", signin.Password("hunter2 hunter2"))
	registration.InvitationID = invitation.ID
	registration.InvitationToken = "invitation-token"

	registered, err := e.svc.Register(T.Context(), testScope, registration)
	must.NoError(T, err)

	// No account: somebody arriving on an invitation joins one that already
	// exists, and the membership the answer filed says which.
	test.Nil(T, registered.Account)
	must.NotNil(T, registered.Membership)
	test.EqOp(T, e.accountID, registered.Membership.BelongsToAccount)
	must.NotNil(T, registered.Invitation)

	// The credential still landed, which is the whole point of registering
	// through this package rather than identity's own invited registration.
	stored, err := e.store.GetUser(T.Context(), e.client.Reader(), testScope, registered.User.ID)
	must.NoError(T, err)
	test.True(T, stored.HasPassword())
}

func TestService_Register_aDeadInvitationTakesTheUserWithIt(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	registration := newRegistration("ada", signin.Password("hunter2 hunter2"))
	registration.InvitationID = "no-such-invitation"
	registration.InvitationToken = "invitation-token"

	_, err := e.svc.Register(T.Context(), testScope, registration)
	test.Error(T, err)

	_, err = e.store.GetUserByUsername(T.Context(), e.client.Reader(), testScope, "ada")
	test.ErrorIs(T, err, identity.ErrUserNotFound)
}

func TestService_VerifyEmailAddress(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	registered, err := e.svc.Register(T.Context(), testScope, newRegistration("ada", signin.Password("hunter2 hunter2")))
	must.NoError(T, err)

	must.NoError(T, e.svc.VerifyEmailAddress(T.Context(), testScope, registered.EmailAddressVerificationToken))

	stored, err := e.store.GetUser(T.Context(), e.client.Reader(), testScope, registered.User.ID)
	must.NoError(T, err)
	test.EqOp(T, identity.StatusGood, stored.AccountStatus)
	test.True(T, stored.EmailAddressVerified())

	// One hook, carrying both facts: the address was proven, and the standing
	// moved.
	must.SliceLen(T, 1, e.hooks.verifieds)
	test.True(T, e.hooks.verifieds[0].EmailAddressProven)
	test.True(T, e.hooks.verifieds[0].Promoted)
	must.NotNil(T, e.hooks.verifieds[0].User)
	test.EqOp(T, identity.StatusGood, e.hooks.verifieds[0].User.AccountStatus)
}

func TestService_VerifyEmailAddress_twice(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	registered, err := e.svc.Register(T.Context(), testScope, newRegistration("ada", signin.Password("hunter2 hunter2")))
	must.NoError(T, err)

	must.NoError(T, e.svc.VerifyEmailAddress(T.Context(), testScope, registered.EmailAddressVerificationToken))

	// The second click matches nobody, because answering the link cleared the
	// column it matched — and it reads exactly as a wrong password does.
	err = e.svc.VerifyEmailAddress(T.Context(), testScope, registered.EmailAddressVerificationToken)
	test.ErrorIs(T, err, signin.ErrInvalidVerificationToken)
	test.ErrorIs(T, err, signin.ErrInvalidCredentials)

	test.SliceLen(T, 1, e.hooks.verifieds)
}

func TestService_VerifyEmailAddress_refusals(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	test.ErrorIs(T, e.svc.VerifyEmailAddress(T.Context(), testScope, ""), signin.ErrEmptyVerificationToken)
	test.ErrorIs(T, e.svc.VerifyEmailAddress(T.Context(), testScope, "never-issued"), signin.ErrInvalidVerificationToken)

	// An empty request is a client that did not submit rather than a guess that
	// missed, so it is not the refusal and costs no database round trip.
	test.SliceEmpty(T, e.hooks.verifieds)
}

func TestService_VerifyEmailAddress_doesNotReinstate(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	registered, err := e.svc.Register(T.Context(), testScope, newRegistration("ada", signin.Password("hunter2 hunter2")))
	must.NoError(T, err)

	must.NoError(T, e.client.WithTransaction(T.Context(), func(tx database.Tx) error {
		return e.store.UpdateUserAccountStatus(T.Context(), tx, testScope, registered.User.ID,
			identity.StatusBanned, "for cause")
	}))

	// The link still proves the address. What it does not do is overturn an
	// operator's decision, which is the one promotion rule these two doors
	// share.
	must.NoError(T, e.svc.VerifyEmailAddress(T.Context(), testScope, registered.EmailAddressVerificationToken))

	stored, err := e.store.GetUser(T.Context(), e.client.Reader(), testScope, registered.User.ID)
	must.NoError(T, err)
	test.EqOp(T, identity.StatusBanned, stored.AccountStatus)
	test.True(T, stored.EmailAddressVerified())

	must.SliceLen(T, 1, e.hooks.verifieds)
	test.True(T, e.hooks.verifieds[0].EmailAddressProven)
	test.False(T, e.hooks.verifieds[0].Promoted)
}

func TestService_CompleteVerification(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	registered, err := e.svc.Register(T.Context(), testScope, newRegistration("ada", signin.Password("hunter2 hunter2")))
	must.NoError(T, err)

	// The consumer proved whatever their own registration asked. No token, and
	// no claim about the address.
	must.NoError(T, e.svc.CompleteVerification(T.Context(), testScope, registered.User.ID))

	stored, err := e.store.GetUser(T.Context(), e.client.Reader(), testScope, registered.User.ID)
	must.NoError(T, err)
	test.EqOp(T, identity.StatusGood, stored.AccountStatus)
	test.False(T, stored.EmailAddressVerified())

	// The link is untouched and still answerable, which is what makes these two
	// doors independent rather than alternatives.
	must.NoError(T, e.svc.VerifyEmailAddress(T.Context(), testScope, registered.EmailAddressVerificationToken))

	must.SliceLen(T, 2, e.hooks.verifieds)
	test.False(T, e.hooks.verifieds[0].EmailAddressProven)
	test.True(T, e.hooks.verifieds[0].Promoted)

	// The second promoted nobody, because the first already had.
	test.True(T, e.hooks.verifieds[1].EmailAddressProven)
	test.False(T, e.hooks.verifieds[1].Promoted)

	_, err = e.svc.LoginForToken(T.Context(), testScope,
		&signin.Credentials{Username: "ada", Password: "hunter2 hunter2"})
	test.NoError(T, err)
}

func TestService_CompleteVerification_refusals(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	test.ErrorIs(T, e.svc.CompleteVerification(T.Context(), testScope, ""), signin.ErrEmptyUserID)
	test.ErrorIs(T, e.svc.CompleteVerification(T.Context(), testScope, "nobody"), identity.ErrUserNotFound)
}

func TestService_AttachPassword(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	registered, err := e.svc.Register(T.Context(), testScope, newRegistration("ada", signin.NoPassword()))
	must.NoError(T, err)

	must.NoError(T, e.svc.AttachPassword(T.Context(), testScope, &signin.PasswordAttachment{
		Token:       registered.EmailAddressVerificationToken,
		NewPassword: "hunter2 hunter2",
	}))

	stored, err := e.store.GetUser(T.Context(), e.client.Reader(), testScope, registered.User.ID)
	must.NoError(T, err)
	test.True(T, stored.HasPassword())

	// Attaching spends nothing: the same link goes on to verify, which is the
	// order a consumer doing both from one click needs.
	test.EqOp(T, identity.StatusUnverified, stored.AccountStatus)
	must.NoError(T, e.svc.VerifyEmailAddress(T.Context(), testScope, registered.EmailAddressVerificationToken))

	signedIn, err := e.svc.LoginForToken(T.Context(), testScope,
		&signin.Credentials{Username: "ada", Password: "hunter2 hunter2"})
	must.NoError(T, err)
	test.EqOp(T, registered.User.ID, signedIn.Principal.User.ID)

	must.SliceLen(T, 1, e.hooks.attached)
	must.NotNil(T, e.hooks.attached[0])
	test.EqOp(T, registered.User.ID, e.hooks.attached[0].ID)

	// The user the hook saw is redacted, and is the row as it stood before the
	// write — the only column that moved is one a redacted user does not carry.
	test.EqOp(T, "", e.hooks.attached[0].HashedPassword)
}

func TestService_AttachPassword_whenOneIsAlreadySet(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	registered, err := e.svc.Register(T.Context(), testScope, newRegistration("ada", signin.Password("hunter2 hunter2")))
	must.NoError(T, err)

	// The refusal that keeps a verification link from being a password reset.
	// The caller here holds a live token for this very account and still cannot
	// replace the credential on it.
	err = e.svc.AttachPassword(T.Context(), testScope, &signin.PasswordAttachment{
		Token:       registered.EmailAddressVerificationToken,
		NewPassword: "a password they chose",
	})
	test.ErrorIs(T, err, signin.ErrPasswordAlreadySet)

	// And the credential did not move.
	_, err = e.svc.LoginForToken(T.Context(), testScope,
		&signin.Credentials{Username: "ada", Password: "a password they chose"})
	test.ErrorIs(T, err, signin.ErrInvalidCredentials)

	test.SliceEmpty(T, e.hooks.attached)
}

func TestService_AttachPassword_refusals(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	err := e.svc.AttachPassword(T.Context(), testScope, nil)
	test.ErrorIs(T, err, signin.ErrNilPasswordAttachment)

	err = e.svc.AttachPassword(T.Context(), testScope, &signin.PasswordAttachment{NewPassword: "x"})
	test.ErrorIs(T, err, signin.ErrEmptyVerificationToken)

	err = e.svc.AttachPassword(T.Context(), testScope, &signin.PasswordAttachment{Token: "t"})
	test.ErrorIs(T, err, signin.ErrEmptyPassword)

	err = e.svc.AttachPassword(T.Context(), testScope, &signin.PasswordAttachment{
		Token:       "never-issued",
		NewPassword: "hunter2 hunter2",
	})
	test.ErrorIs(T, err, signin.ErrInvalidVerificationToken)
}

func TestService_verificationDoorsWithoutAVerificationsDirectory(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	svc, err := signin.NewService(e.client, e.store, argon2.NewArgon2Authenticator(), e.issuer)
	must.NoError(T, err)

	test.ErrorIs(T, svc.VerifyEmailAddress(T.Context(), testScope, "a-token"), signin.ErrVerificationsNotConfigured)
	test.ErrorIs(T, svc.CompleteVerification(T.Context(), testScope, e.user.ID), signin.ErrVerificationsNotConfigured)
	test.ErrorIs(T, svc.AttachPassword(T.Context(), testScope, &signin.PasswordAttachment{
		Token:       "a-token",
		NewPassword: "hunter2 hunter2",
	}), signin.ErrVerificationsNotConfigured)
}

func TestService_verificationHooksRunInTheTransaction(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	registered, err := e.svc.Register(T.Context(), testScope, newRegistration("ada", signin.NoPassword()))
	must.NoError(T, err)

	sentinel := platformerrors.New("the consumer could not record it")

	e.hooks.verifyErr = sentinel

	err = e.svc.VerifyEmailAddress(T.Context(), testScope, registered.EmailAddressVerificationToken)
	test.ErrorIs(T, err, sentinel)

	// The hook refused, so nothing it ran beside committed: the address is
	// unproven, the standing has not moved, and the link is still live.
	stored, err := e.store.GetUser(T.Context(), e.client.Reader(), testScope, registered.User.ID)
	must.NoError(T, err)
	test.EqOp(T, identity.StatusUnverified, stored.AccountStatus)
	test.False(T, stored.EmailAddressVerified())

	e.hooks.verifyErr = nil
	must.NoError(T, e.svc.VerifyEmailAddress(T.Context(), testScope, registered.EmailAddressVerificationToken))
}

func TestService_AttachPassword_hookRollsTheWriteBack(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	registered, err := e.svc.Register(T.Context(), testScope, newRegistration("ada", signin.NoPassword()))
	must.NoError(T, err)

	e.hooks.attachErr = platformerrors.New("the consumer could not record it")

	err = e.svc.AttachPassword(T.Context(), testScope, &signin.PasswordAttachment{
		Token:       registered.EmailAddressVerificationToken,
		NewPassword: "hunter2 hunter2",
	})
	test.ErrorIs(T, err, e.hooks.attachErr)

	stored, err := e.store.GetUser(T.Context(), e.client.Reader(), testScope, registered.User.ID)
	must.NoError(T, err)
	test.False(T, stored.HasPassword())
}

// TestService_Register_mintsItsOwnToken pins the one property that makes the
// mailed link an authority at all: the secret is this service's, not the
// caller's, so a client cannot choose the token that will prove somebody's
// address or claim their account.
func TestService_Register_mintsItsOwnToken(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	registration := newRegistration("ada", signin.NoPassword())
	registration.User.EmailAddressVerificationToken = "chosen-by-the-caller"

	registered, err := e.svc.Register(T.Context(), testScope, registration)
	must.NoError(T, err)

	test.NotEqOp(T, "chosen-by-the-caller", registered.EmailAddressVerificationToken)

	// And the one the caller tried to choose proves nothing.
	test.ErrorIs(T,
		e.svc.VerifyEmailAddress(T.Context(), testScope, "chosen-by-the-caller"),
		signin.ErrInvalidVerificationToken)

	must.NoError(T, e.svc.VerifyEmailAddress(T.Context(), testScope, registered.EmailAddressVerificationToken))
}

// TestService_Register_scopeIsTheArgument pins that the directory a registration
// lands in is the one named by the argument rather than the one on the value the
// caller assembled, which is the reading the module takes everywhere a write
// carries an entity with a scope on it.
func TestService_Register_scopeIsTheArgument(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	registration := newRegistration("ada", signin.NoPassword())
	registration.User.Scope = tenancy.Of("somebody-elses-directory")
	registration.Account.Scope = tenancy.Of("somebody-elses-directory")

	_, err := e.svc.Register(T.Context(), testScope, registration)
	test.Error(T, err)
}

// failingRegistrar is a Registrar that refuses, for the one case the live store
// cannot produce: a directory that answers an otherwise valid registration with
// an error of its own.
type failingRegistrar struct {
	err error
}

var _ signin.Registrar = (*failingRegistrar)(nil)

func (f *failingRegistrar) Register(
	context.Context, tenancy.Scope, *identity.User, *identity.Account, []string,
) (*identity.Registration, error) {
	return nil, f.err
}

func (f *failingRegistrar) RegisterWithInvitation(
	context.Context, tenancy.Scope, *identity.User, string, string, string,
) (*identity.InvitedRegistration, error) {
	return nil, f.err
}

func TestService_Register_passesTheDirectorysErrorBack(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	sentinel := platformerrors.New("the directory would not answer")

	svc, err := signin.NewService(e.client, e.store, argon2.NewArgon2Authenticator(), e.issuer,
		signin.WithRegistrar(&failingRegistrar{err: sentinel}),
	)
	must.NoError(T, err)

	_, err = svc.Register(T.Context(), testScope, newRegistration("ada", signin.Password("hunter2 hunter2")))
	test.True(T, errors.Is(err, sentinel))
}

// TestRegistrar_shape holds Registrar's documentation to the interface it
// documents, the way TestDirectory_MethodCount holds Directory's.
//
// The sentence a consumer reads says they are implementing two methods, and a
// third added here without revisiting it is a consumer told the wrong number.
// The satisfaction below is the other half: this seam is identity's Service
// rather than its Store, which is the one place in this package where the layer
// above the rows is the thing depended on, and a change that broke it would
// otherwise surface as a consumer's wiring failure rather than as a test.
func TestRegistrar_shape(T *testing.T) {
	T.Parallel()

	test.EqOp(T, 2, reflect.TypeFor[signin.Registrar]().NumMethod())

	var _ signin.Registrar = (*identity.Service)(nil)
}

// TestVerifications_shape is the same for the other new seam, whose sentence
// says three — the three doors that finish a registration — while the interface
// carries four methods.
//
// The numbers differ because the fourth, MarkUserEmailAddressProven, is not a
// door: it is the write Service.RedeemMagicLink makes on the way through, for a
// caller that proved the address without holding the link that was mailed for
// it. The sentence counts operations a consumer calls; this counts methods a
// consumer implements.
//
// It is satisfied by the Store rather than the Service, and deliberately: the
// two writes a verification makes have to land on one transaction, and every
// method on identity's Service opens one of its own.
func TestVerifications_shape(T *testing.T) {
	T.Parallel()

	test.EqOp(T, 4, reflect.TypeFor[signin.Verifications]().NumMethod())

	var _ signin.Verifications = (*identity.SQLStore)(nil)
}

// TestRegistrationOptions_nilIsIgnored pins the convention every option in this
// package follows: a nil is ignored rather than installed, so a caller who
// resolved one conditionally does not end up with a service that panics on the
// path they thought they had disabled.
func TestRegistrationOptions_nilIsIgnored(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	svc, err := signin.NewService(e.client, e.store, argon2.NewArgon2Authenticator(), e.issuer,
		signin.WithRegistrar(nil),
		signin.WithVerifications(nil),
	)
	must.NoError(T, err)

	// Ignored, so the service is the one that registers nobody rather than one
	// holding a nil seam.
	_, err = svc.Register(T.Context(), testScope, newRegistration("ada", signin.NoPassword()))
	test.ErrorIs(T, err, signin.ErrRegistrationNotConfigured)

	test.ErrorIs(T, svc.VerifyEmailAddress(T.Context(), testScope, "a-token"), signin.ErrVerificationsNotConfigured)
}

// TestWithSecretGenerator_nilLeavesTheRealOne is the same property for the seam
// where getting it wrong would be worst: a nil generator that replaced the real
// one would mint the empty string as every registrant's verification token, and
// every link would then prove every account.
func TestWithSecretGenerator_nilLeavesTheRealOne(T *testing.T) {
	T.Parallel()

	e := newEnv(T, signin.WithSecretGenerator(nil))

	first, err := e.svc.Register(T.Context(), testScope, newRegistration("ada", signin.NoPassword()))
	must.NoError(T, err)

	second, err := e.svc.Register(T.Context(), testScope, newRegistration("grace", signin.NoPassword()))
	must.NoError(T, err)

	test.NotEq(T, "", first.EmailAddressVerificationToken)
	test.NotEqOp(T, first.EmailAddressVerificationToken, second.EmailAddressVerificationToken)

	// And each proves its own account and not the other's.
	test.ErrorIs(T,
		e.svc.AttachPassword(T.Context(), testScope, &signin.PasswordAttachment{
			Token:       first.EmailAddressVerificationToken + "x",
			NewPassword: "hunter2 hunter2",
		}),
		signin.ErrInvalidVerificationToken)
}

// TestNoopHooks_finishesARegistration pins that the two hooks this change added
// are on NoopHooks, which is what makes them additive: a consumer who embedded
// it gains a no-op rather than a compile failure, and one who implements Hooks
// outright is told by their compiler rather than at runtime.
func TestNoopHooks_finishesARegistration(T *testing.T) {
	T.Parallel()

	var hooks signin.Hooks = signin.NoopHooks{}

	test.NoError(T, hooks.AfterAttachPassword(T.Context(), nil, testScope, nil))
	test.NoError(T, hooks.AfterVerify(T.Context(), nil, testScope, nil))
}

// staleStandingVerifications is identity's store with one lie in it: the read
// that resolves a verification link reports the standing the row had a moment
// ago rather than the standing it has now.
//
// It stands in for the gap the live store cannot be made to open — an operator
// suspending somebody between the read that resolves their link and the
// transaction that acts on it — and every other method is the real one, so the
// write the promotion would make is the write it really makes.
type staleStandingVerifications struct {
	*identity.SQLStore

	stale identity.AccountStatus
}

var _ signin.Verifications = (*staleStandingVerifications)(nil)

func (v *staleStandingVerifications) GetUserByEmailVerificationToken(
	ctx context.Context,
	q database.SQLQueryExecutor,
	scope tenancy.Scope,
	token string,
) (*identity.User, error) {
	user, err := v.SQLStore.GetUserByEmailVerificationToken(ctx, q, scope, token)
	if err != nil {
		return nil, err
	}

	stale := *user
	stale.AccountStatus = v.stale

	return &stale, nil
}

// TestService_VerifyEmailAddress_readsTheStandingInTheTransaction pins that the
// promotion turns on the row as the writing transaction finds it, not on the
// copy the caller resolved the link with.
//
// The two reads are a gap, and what fits in it is an operator's decision: a
// suspension landing there would be overturned by a promotion that trusted the
// earlier answer, which is precisely the thing VerifyEmailAddress documents it
// will not do. TestService_VerifyEmailAddress_doesNotReinstate asserts the rule;
// this one asserts that the rule is tested against the row rather than against
// something read before the transaction existed.
func TestService_VerifyEmailAddress_readsTheStandingInTheTransaction(T *testing.T) {
	T.Parallel()

	e := newEnv(T)

	registered, err := e.svc.Register(T.Context(), testScope, newRegistration("ada", signin.Password("hunter2 hunter2")))
	must.NoError(T, err)

	must.NoError(T, e.client.WithTransaction(T.Context(), func(tx database.Tx) error {
		return e.store.UpdateUserAccountStatus(T.Context(), tx, testScope, registered.User.ID,
			identity.StatusBanned, "for cause")
	}))

	// The resolving read answers with the standing from before the suspension,
	// which is what a read that ran before it would have seen.
	svc, err := signin.NewService(e.client, e.store, argon2.NewArgon2Authenticator(), e.issuer,
		signin.WithHooks(e.hooks),
		signin.WithVerifications(&staleStandingVerifications{
			SQLStore: e.store,
			stale:    identity.StatusUnverified,
		}),
	)
	must.NoError(T, err)

	must.NoError(T, svc.VerifyEmailAddress(T.Context(), testScope, registered.EmailAddressVerificationToken))

	stored, err := e.store.GetUser(T.Context(), e.client.Reader(), testScope, registered.User.ID)
	must.NoError(T, err)
	test.EqOp(T, identity.StatusBanned, stored.AccountStatus)

	// The address is still proven — the link does that much whatever the
	// standing — and the hook says nobody was promoted.
	test.True(T, stored.EmailAddressVerified())
	must.SliceLen(T, 1, e.hooks.verifieds)
	test.False(T, e.hooks.verifieds[0].Promoted)
}

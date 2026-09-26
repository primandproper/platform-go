package signin_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/pointer"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The registration policy seam. What each case pins is that a consumer whose
// own registration does more than the request says can say so here, and that
// what it says is what gets written — the standing, the roles, the account's
// name, the agreements and a second factor — with a refusal costing nothing.

// errTermsNotAccepted is the consumer's own refusal.
var errTermsNotAccepted = platformerrors.New("accept the terms of service to register")

// productRegistration is a consumer's registration policy, shaped like the one
// that motivated the seam: the terms are required, the registrant starts in good
// standing with a service role, the account is named when the registrant did
// not name it, and a second factor is minted with them.
type productRegistration struct {
	seen  *signin.Registration
	calls int
}

func (p *productRegistration) policy(_ context.Context, registration *signin.Registration) error {
	p.calls++
	p.seen = registration

	if !slices.Contains(registration.Agreements, identity.TermsOfService) ||
		!slices.Contains(registration.Agreements, identity.PrivacyPolicy) {
		return errTermsNotAccepted
	}

	registration.User.AccountStatus = identity.StatusGood
	registration.User.ServiceRoles = append(registration.User.ServiceRoles, "service_user")
	registration.OwnerRoles = append(registration.OwnerRoles, "account_admin")
	registration.EnrollTOTP = true

	if registration.Account == nil {
		registration.Account = &identity.Account{}
	}

	if registration.Account.Name == "" {
		registration.Account.Name = registration.User.Username + "'s household"
	}

	return nil
}

// agreed is newRegistration with both documents accepted.
func agreed(username string, credential signin.Credential) *signin.Registration {
	registration := newRegistration(username, credential)
	registration.Agreements = []identity.Agreement{identity.TermsOfService, identity.PrivacyPolicy}

	return registration
}

func TestRegistrationPolicy_shapesWhatIsWritten(T *testing.T) {
	T.Parallel()

	p := &productRegistration{}
	e := newEnv(T, signin.WithRegistrationPolicy(p.policy))

	registration := agreed("ada", signin.Password("hunter2 hunter2"))
	registration.Account.Name = ""

	registered, err := e.svc.Register(T.Context(), testScope, registration)
	must.NoError(T, err)
	test.EqOp(T, 1, p.calls)

	stored, err := e.store.GetUser(T.Context(), e.client.Reader(), testScope, registered.User.ID)
	must.NoError(T, err)

	test.EqOp(T, identity.StatusGood, stored.AccountStatus)
	test.Eq(T, []string{"service_user"}, stored.ServiceRoles)
	must.NotNil(T, stored.LastAcceptedTermsOfService)
	must.NotNil(T, stored.LastAcceptedPrivacyPolicy)
	test.EqOp(T, *stored.LastAcceptedTermsOfService, *stored.LastAcceptedPrivacyPolicy)

	must.NotNil(T, registered.Account)
	test.EqOp(T, "ada's household", registered.Account.Name)
	must.NotNil(T, registered.Membership)
	test.SliceContainsAll(T, []string{"owner", "account_admin"}, registered.Membership.Roles)

	// The minted second factor is the one the row holds, and it is unproven.
	must.NotNil(T, registered.TOTPEnrollment)
	test.NotEqOp(T, "", registered.TOTPEnrollment.URI)
	test.EqOp(T, registered.TOTPEnrollment.Secret, stored.TwoFactorSecret)
	test.Nil(T, stored.TwoFactorSecretVerifiedAt)

	// Good standing is the policy's decision and it holds: they sign in without
	// answering the verification link, and with a password alone, because an
	// unproven secret is not a second factor.
	credentials := &signin.Credentials{Username: "ada", Password: "hunter2 hunter2"}

	_, err = e.svc.LoginForToken(T.Context(), testScope, credentials)
	must.NoError(T, err)

	// Proving it is the signed-in door it always was, after which the code is
	// asked for.
	must.NoError(T, e.svc.VerifyTOTPSecret(T.Context(), testScope, registered.User.ID,
		code(T, registered.TOTPEnrollment.Secret)))

	_, err = e.svc.LoginForToken(T.Context(), testScope, credentials)
	test.ErrorIs(T, err, signin.ErrSecondFactorRequired)

	credentials.TOTPCode = code(T, registered.TOTPEnrollment.Secret)

	_, err = e.svc.LoginForToken(T.Context(), testScope, credentials)
	must.NoError(T, err)
}

func TestRegistrationPolicy_leavesTheCallersValueAlone(T *testing.T) {
	T.Parallel()

	p := &productRegistration{}
	e := newEnv(T, signin.WithRegistrationPolicy(p.policy))

	registration := agreed("ada", signin.Password("hunter2 hunter2"))
	registration.Account.Name = ""
	registration.OwnerRoles = make([]string, 1, 4)
	registration.OwnerRoles[0] = "owner"
	registration.User.ServiceRoles = make([]string, 0, 4)

	_, err := e.svc.Register(T.Context(), testScope, registration)
	must.NoError(T, err)

	// The policy was handed a copy, down to the slices it appended to: spare
	// capacity on the caller's slice is where an append through a shallow copy
	// would have landed.
	must.NotNil(T, p.seen)
	test.NotEqOp(T, registration, p.seen)
	test.EqOp(T, identity.AccountStatus(""), registration.User.AccountStatus)
	test.EqOp(T, "", registration.Account.Name)
	test.False(T, registration.EnrollTOTP)
	test.Eq(T, []string{"owner"}, registration.OwnerRoles)
	test.Eq(T, []string{"owner", "", "", ""}, registration.OwnerRoles[:cap(registration.OwnerRoles)])
	test.SliceEmpty(T, registration.User.ServiceRoles)
	test.Eq(T, []string{"", "", "", ""}, registration.User.ServiceRoles[:cap(registration.User.ServiceRoles)])
}

func TestRegistrationPolicy_refusal(T *testing.T) {
	T.Parallel()

	T.Run("a refused registration writes nobody", func(t *testing.T) {
		t.Parallel()

		p := &productRegistration{}
		e := newEnv(t, signin.WithRegistrationPolicy(p.policy))

		_, err := e.svc.Register(t.Context(), testScope, newRegistration("ada", signin.Password("hunter2 hunter2")))
		test.ErrorIs(t, err, signin.ErrRegistrationRefused)
		test.ErrorIs(t, err, errTermsNotAccepted)

		// The username is still free, so the same registration with the terms
		// accepted goes through.
		registered, err := e.svc.Register(t.Context(), testScope, agreed("ada", signin.Password("hunter2 hunter2")))
		must.NoError(t, err)
		test.EqOp(t, "ada", registered.User.Username)
		test.EqOp(t, 2, p.calls)
	})

	T.Run("before anything is hashed", func(t *testing.T) {
		t.Parallel()

		p := &productRegistration{}
		e := newEnv(t)
		authenticator := &stubAuthenticator{}

		svc, err := signin.NewService(e.client, e.store, authenticator, e.issuer,
			signin.WithRegistrar(e.directory),
			signin.WithTOTPIssuer("Example"),
			signin.WithRegistrationPolicy(p.policy),
		)
		must.NoError(t, err)

		_, err = svc.Register(t.Context(), testScope, newRegistration("ada", signin.Password("hunter2 hunter2")))
		test.ErrorIs(t, err, signin.ErrRegistrationRefused)
		test.EqOp(t, 0, authenticator.hashes)
	})

	T.Run("ahead of the password policy", func(t *testing.T) {
		t.Parallel()

		p := &productRegistration{}
		passwords := &minimumLength{}
		e := newEnv(t,
			signin.WithRegistrationPolicy(p.policy),
			signin.WithPasswordPolicy(passwords.policy),
		)

		_, err := e.svc.Register(t.Context(), testScope, newRegistration("ada", signin.Password("short")))
		test.ErrorIs(t, err, signin.ErrRegistrationRefused)
		test.False(t, errors.Is(err, signin.ErrPasswordRefused))
		test.EqOp(t, 0, passwords.calls)

		// And the password policy still has its say on what the registration
		// policy admitted.
		_, err = e.svc.Register(t.Context(), testScope, agreed("ada", signin.Password("short")))
		test.ErrorIs(t, err, signin.ErrPasswordRefused)
		test.False(t, errors.Is(err, signin.ErrRegistrationRefused))
	})

	T.Run("it is asked about an invited registration too", func(t *testing.T) {
		t.Parallel()

		p := &productRegistration{}
		e := newEnv(t, signin.WithRegistrationPolicy(p.policy))

		registration := newRegistration("ada", signin.Password("hunter2 hunter2"))
		registration.InvitationID = "invitation_1"
		registration.InvitationToken = "token"

		_, err := e.svc.Register(t.Context(), testScope, registration)
		test.ErrorIs(t, err, signin.ErrRegistrationRefused)
		must.NotNil(t, p.seen)
		test.EqOp(t, "invitation_1", p.seen.InvitationID)
	})
}

// TestRegistrationPolicy_onTheWire pins the order the refusal is joined in, for
// the reason TestPasswordPolicy_onTheWire does.
func TestRegistrationPolicy_onTheWire(T *testing.T) {
	T.Parallel()

	grpcerrors.RegisterClientSafeSentinels(signin.ClientSafeSentinels...)
	grpcerrors.RegisterClientSafeReasons(signin.ClientSafeReasons...)

	refuse := func(t *testing.T, policyErr error) error {
		t.Helper()

		e := newEnv(t, signin.WithRegistrationPolicy(func(context.Context, *signin.Registration) error {
			return policyErr
		}))

		_, err := e.svc.Register(t.Context(), testScope, newRegistration("ada", signin.NoPassword()))
		must.ErrorIs(t, err, signin.ErrRegistrationRefused)

		return err
	}

	T.Run("a policy's own error is passed over unless the consumer registered it", func(t *testing.T) {
		t.Parallel()

		err := refuse(t, platformerrors.New("the consumer's internal registration detail"))

		msg, ok := grpcerrors.ClientSafeMessage(err)
		must.True(t, ok)
		test.EqOp(t, signin.ErrRegistrationRefused.Error(), msg)

		reason, ok := grpcerrors.ClientSafeReason(err)
		must.True(t, ok)
		test.EqOp(t, "REGISTRATION_REFUSED", reason.Reason)
	})

	T.Run("a registered one is quoted, and the identifier stays", func(t *testing.T) {
		t.Parallel()

		consumers := platformerrors.New("accept the privacy policy to register")
		grpcerrors.RegisterClientSafeSentinels(consumers)

		err := refuse(t, consumers)

		msg, ok := grpcerrors.ClientSafeMessage(err)
		must.True(t, ok)
		test.EqOp(t, consumers.Error(), msg)

		reason, ok := grpcerrors.ClientSafeReason(err)
		must.True(t, ok)
		test.EqOp(t, "REGISTRATION_REFUSED", reason.Reason)
	})
}

func TestService_Register_agreements(T *testing.T) {
	T.Parallel()

	T.Run("are stamped on the row, with one clock read", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		registered, err := e.svc.Register(t.Context(), testScope, agreed("ada", signin.NoPassword()))
		must.NoError(t, err)

		stored, err := e.store.GetUser(t.Context(), e.client.Reader(), testScope, registered.User.ID)
		must.NoError(t, err)
		must.NotNil(t, stored.LastAcceptedTermsOfService)
		must.NotNil(t, stored.LastAcceptedPrivacyPolicy)
		test.EqOp(t, *stored.LastAcceptedTermsOfService, *stored.LastAcceptedPrivacyPolicy)
	})

	T.Run("naming one stamps one", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		registration := newRegistration("ada", signin.NoPassword())
		registration.Agreements = []identity.Agreement{identity.TermsOfService}

		registered, err := e.svc.Register(t.Context(), testScope, registration)
		must.NoError(t, err)

		stored, err := e.store.GetUser(t.Context(), e.client.Reader(), testScope, registered.User.ID)
		must.NoError(t, err)
		test.NotNil(t, stored.LastAcceptedTermsOfService)
		test.Nil(t, stored.LastAcceptedPrivacyPolicy)
	})

	T.Run("naming none stamps none", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		registered, err := e.svc.Register(t.Context(), testScope, newRegistration("ada", signin.NoPassword()))
		must.NoError(t, err)

		stored, err := e.store.GetUser(t.Context(), e.client.Reader(), testScope, registered.User.ID)
		must.NoError(t, err)
		test.Nil(t, stored.LastAcceptedTermsOfService)
		test.Nil(t, stored.LastAcceptedPrivacyPolicy)
	})

	T.Run("an unknown one refuses the registration", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		registration := newRegistration("ada", signin.NoPassword())
		registration.Agreements = []identity.Agreement{identity.TermsOfService, "cookie_policy"}

		_, err := e.svc.Register(t.Context(), testScope, registration)
		test.ErrorIs(t, err, platformerrors.ErrUnrecognizedInputValue)

		_, err = e.svc.Register(t.Context(), testScope, agreed("ada", signin.NoPassword()))
		must.NoError(t, err)
	})

	// The in-transaction moment a consumer records the agreements' provenance at
	// is identity's own hook, and what it is handed already carries them.
	T.Run("reach identity's hook on the registering transaction", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)
		hooks := &agreementHooks{}

		directory, err := identity.NewService(e.client, e.store, identity.WithHooks(hooks))
		must.NoError(t, err)

		svc, err := signin.NewService(e.client, e.store, argon2.NewArgon2Authenticator(), e.issuer,
			signin.WithRegistrar(directory),
		)
		must.NoError(t, err)

		_, err = svc.Register(t.Context(), testScope, agreed("ada", signin.NoPassword()))
		must.NoError(t, err)
		must.NotNil(t, hooks.terms)
		must.NotNil(t, hooks.privacy)
	})
}

// agreementHooks records what identity's registration hook saw of the
// agreements.
type agreementHooks struct {
	identity.NoopHooks

	terms, privacy *time.Time
}

func (h *agreementHooks) AfterRegister(
	_ context.Context,
	_ database.Tx,
	_ tenancy.Scope,
	registration *identity.Registration,
) error {
	h.terms = registration.User.LastAcceptedTermsOfService
	h.privacy = registration.User.LastAcceptedPrivacyPolicy

	return nil
}

func TestService_Register_enrollTOTP(T *testing.T) {
	T.Parallel()

	T.Run("in process, without a policy", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		registration := newRegistration("ada", signin.Password("hunter2 hunter2"))
		registration.EnrollTOTP = true

		registered, err := e.svc.Register(t.Context(), testScope, registration)
		must.NoError(t, err)
		must.NotNil(t, registered.TOTPEnrollment)

		stored, err := e.store.GetUser(t.Context(), e.client.Reader(), testScope, registered.User.ID)
		must.NoError(t, err)
		test.EqOp(t, registered.TOTPEnrollment.Secret, stored.TwoFactorSecret)
	})

	T.Run("not asked for, none is minted", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		registered, err := e.svc.Register(t.Context(), testScope, newRegistration("ada", signin.NoPassword()))
		must.NoError(t, err)
		test.Nil(t, registered.TOTPEnrollment)

		stored, err := e.store.GetUser(t.Context(), e.client.Reader(), testScope, registered.User.ID)
		must.NoError(t, err)
		test.EqOp(t, "", stored.TwoFactorSecret)
	})

	T.Run("a caller's proof of a secret does not survive the mint", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)

		registration := newRegistration("ada", signin.NoPassword())
		registration.EnrollTOTP = true
		registration.User.TwoFactorSecret = "CALLERCHOSE"
		registration.User.TwoFactorSecretVerifiedAt = pointer.To(time.Now())

		registered, err := e.svc.Register(t.Context(), testScope, registration)
		must.NoError(t, err)

		stored, err := e.store.GetUser(t.Context(), e.client.Reader(), testScope, registered.User.ID)
		must.NoError(t, err)
		test.NotEqOp(t, "CALLERCHOSE", stored.TwoFactorSecret)
		test.Nil(t, stored.TwoFactorSecretVerifiedAt)
	})

	T.Run("without an issuer, before anything is hashed or written", func(t *testing.T) {
		t.Parallel()

		e := newEnv(t)
		authenticator := &stubAuthenticator{}

		svc, err := signin.NewService(e.client, e.store, authenticator, e.issuer,
			signin.WithRegistrar(e.directory),
		)
		must.NoError(t, err)

		registration := newRegistration("ada", signin.Password("hunter2 hunter2"))
		registration.EnrollTOTP = true

		_, err = svc.Register(t.Context(), testScope, registration)
		test.ErrorIs(t, err, signin.ErrTOTPIssuerNotConfigured)
		test.EqOp(t, 0, authenticator.hashes)

		// Nobody was written, so the same username is still free.
		_, err = e.svc.Register(t.Context(), testScope, newRegistration("ada", signin.NoPassword()))
		must.NoError(t, err)
	})
}

func TestWithRegistrationPolicy_nilIsIgnored(T *testing.T) {
	T.Parallel()

	e := newEnv(T, signin.WithRegistrationPolicy(nil))

	registered, err := e.svc.Register(T.Context(), testScope, newRegistration("ada", signin.NoPassword()))
	must.NoError(T, err)
	test.EqOp(T, "ada", registered.User.Username)
}

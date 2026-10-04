package signincfg

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/identity"
	identitymock "github.com/primandproper/platform-go/v14/identity/mock"

	"github.com/primandproper/primitives-go/v2/authentication"
	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/authentication/tokens"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/pointer"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// stubTokenIssuer is the whole tokens.Issuer, which is the key the Tokens block
// registers under. IssueToken is stubIssuer's; the rest of the embedded
// interface is nil and never reached.
type stubTokenIssuer struct {
	tokens.Issuer
}

func (stubTokenIssuer) IssueToken(ctx context.Context, subject string, expiry time.Duration, claims map[string]any) (tokenStr, jti string, err error) {
	return stubIssuer{}.IssueToken(ctx, subject, expiry, claims)
}

// base registers what the service needs from the rest of the composition root
// and none of what the application supplies, so each case says only what it
// is about.
func base(t *testing.T, cfg *Config) do.Injector {
	t.Helper()

	i := do.New()
	do.ProvideValue[context.Context](i, t.Context())
	do.ProvideValue[database.Client](i, testDBClient(t))
	do.ProvideValue[identity.Store](i, &identitymock.StoreMock{})
	do.ProvideValue[tokens.Issuer](i, stubTokenIssuer{})
	do.ProvideValue(i, cfg)

	return i
}

// withAuthenticator registers the two things the application always supplies:
// the authenticator, and the hooks (NoopHooks, by name).
func withAuthenticator(i do.Injector) do.Injector {
	do.ProvideValue[authentication.Authenticator](i, argon2.NewArgon2Authenticator())
	do.ProvideValue[signin.Hooks](i, signin.NoopHooks{})

	return i
}

// withRegistrar registers what open registration needs, which is every case's
// but the ones about registration.
func withRegistrar(i do.Injector) do.Injector {
	do.ProvideValue(i, &identity.Service{})

	return i
}

func TestRegisterService(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := withRegistrar(withAuthenticator(base(t, &Config{DefaultOwnerRoles: ownerRoles})))
		RegisterService(i)

		svc, err := do.Invoke[*signin.Service](i)
		must.NoError(t, err)
		test.NotNil(t, svc)
	})

	T.Run("a missing authenticator fails naming it rather than defaulting", func(t *testing.T) {
		t.Parallel()

		i := base(t, &Config{DefaultOwnerRoles: ownerRoles})
		RegisterService(i)

		_, err := do.Invoke[*signin.Service](i)
		must.Error(t, err)
		test.StrContains(t, err.Error(), do.NameOf[authentication.Authenticator]())
	})

	T.Run("missing hooks fail naming them rather than defaulting", func(t *testing.T) {
		t.Parallel()

		i := withRegistrar(base(t, &Config{DefaultOwnerRoles: ownerRoles}))
		do.ProvideValue[authentication.Authenticator](i, argon2.NewArgon2Authenticator())
		RegisterService(i)

		_, err := do.Invoke[*signin.Service](i)
		test.ErrorIs(t, err, do.ErrServiceNotFound)
		test.StrContains(t, err.Error(), do.NameOf[signin.Hooks]())
	})

	T.Run("a missing token issuer fails naming it", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue[identity.Store](i, &identitymock.StoreMock{})
		do.ProvideValue(i, &Config{DefaultOwnerRoles: ownerRoles})
		withAuthenticator(i)
		RegisterService(i)

		_, err := do.Invoke[*signin.Service](i)
		must.Error(t, err)
		test.StrContains(t, err.Error(), do.NameOf[tokens.Issuer]())
	})

	T.Run("needs the identity store as its directory", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue[tokens.Issuer](i, stubTokenIssuer{})
		do.ProvideValue(i, &Config{DefaultOwnerRoles: ownerRoles})
		withAuthenticator(i)
		RegisterService(i)

		_, err := do.Invoke[*signin.Service](i)
		must.Error(t, err)
		test.StrContains(t, err.Error(), do.NameOf[identity.Store]())
	})

	T.Run("open registration needs identity's service", func(t *testing.T) {
		t.Parallel()

		i := withAuthenticator(base(t, &Config{DefaultOwnerRoles: ownerRoles}))
		RegisterService(i)

		_, err := do.Invoke[*signin.Service](i)
		must.Error(t, err)
		test.StrContains(t, err.Error(), do.NameOf[*identity.Service]())
	})

	T.Run("disabled registration needs no identity service", func(t *testing.T) {
		t.Parallel()

		i := withAuthenticator(base(t, &Config{DefaultOwnerRoles: ownerRoles, Registration: RegistrationConfig{Disabled: true}}))
		RegisterService(i)

		svc, err := do.Invoke[*signin.Service](i)
		must.NoError(t, err)

		_, err = svc.Register(t.Context(), tenancy.Of("tenant"), &signin.Registration{User: &identity.User{}})
		test.ErrorIs(t, err, signin.ErrRegistrationNotConfigured)
	})

	T.Run("the handle doors write through identity's service", func(t *testing.T) {
		t.Parallel()

		update := &signin.UsernameUpdate{NewUsername: "renamed"}

		// No identity service registered, and registration closed: the doors
		// refuse as the wiring failure that is.
		i := withAuthenticator(base(t, &Config{DefaultOwnerRoles: ownerRoles, Registration: RegistrationConfig{Disabled: true}}))
		RegisterService(i)

		off, err := do.Invoke[*signin.Service](i)
		must.NoError(t, err)

		_, err = off.UpdateUsername(t.Context(), tenancy.Of("tenant"), "", update)
		test.ErrorIs(t, err, signin.ErrProfileUpdaterNotConfigured)

		// Registered, it is attached whether or not registration is open: the
		// refusal is now the empty subject, which is past the wiring check.
		for _, disabled := range []bool{false, true} {
			i = withRegistrar(withAuthenticator(base(t, &Config{DefaultOwnerRoles: ownerRoles, Registration: RegistrationConfig{Disabled: disabled}})))
			RegisterService(i)

			on, invokeErr := do.Invoke[*signin.Service](i)
			must.NoError(t, invokeErr)

			_, err = on.UpdateUsername(t.Context(), tenancy.Of("tenant"), "", update)
			test.ErrorIs(t, err, signin.ErrEmptyUserID, test.Sprintf("registration disabled: %t", disabled))
		}
	})

	T.Run("a magic links block needs a mailer", func(t *testing.T) {
		t.Parallel()

		i := withRegistrar(withAuthenticator(base(t, &Config{DefaultOwnerRoles: ownerRoles, MagicLinks: &MagicLinksConfig{TablePrefix: "app"}})))
		RegisterService(i)

		_, err := do.Invoke[*signin.Service](i)
		must.Error(t, err)
		test.StrContains(t, err.Error(), do.NameOf[signin.MagicLinkMailer]())
	})

	T.Run("a magic links block uses the registered mailer", func(t *testing.T) {
		t.Parallel()

		i := withRegistrar(withAuthenticator(base(t, &Config{DefaultOwnerRoles: ownerRoles, MagicLinks: &MagicLinksConfig{
			TablePrefix:   "app",
			SweepInterval: pointer.To(time.Duration(0)),
			RequestFloor:  time.Millisecond,
		}})))
		do.ProvideValue[signin.MagicLinkMailer](i, discardingMailer{})
		RegisterService(i)

		svc, err := do.Invoke[*signin.Service](i)
		must.NoError(t, err)
		test.ErrorIs(t, svc.RequestMagicLink(t.Context(), tenancy.Of("tenant"), ""), signin.ErrEmptyHandle)
	})

	T.Run("a registered handle reminder mailer switches its door on", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			DefaultOwnerRoles:   ownerRoles,
			RefreshTokens:       RefreshTokensConfig{SweepInterval: pointer.To(time.Duration(0))},
			HandleReminderFloor: time.Millisecond,
		}

		i := withRegistrar(withAuthenticator(base(t, cfg)))
		RegisterService(i)

		off, err := do.Invoke[*signin.Service](i)
		must.NoError(t, err)
		test.ErrorIs(t, off.RequestHandleReminder(t.Context(), tenancy.Of("tenant"), ""),
			signin.ErrHandleRemindersNotConfigured)

		i = withRegistrar(withAuthenticator(base(t, cfg)))
		do.ProvideValue[signin.HandleReminderMailer](i, discardingMailer{})
		RegisterService(i)

		on, err := do.Invoke[*signin.Service](i)
		must.NoError(t, err)
		test.ErrorIs(t, on.RequestHandleReminder(t.Context(), tenancy.Of("tenant"), ""), signin.ErrEmptyHandle)
	})

	T.Run("no verification mailer leaves the resend door refusing", func(t *testing.T) {
		t.Parallel()

		i := withRegistrar(withAuthenticator(base(t, &Config{DefaultOwnerRoles: ownerRoles})))
		RegisterService(i)

		svc, err := do.Invoke[*signin.Service](i)
		must.NoError(t, err)

		err = svc.RequestVerificationEmail(t.Context(), tenancy.Of("tenant"), "someone")
		test.ErrorIs(t, err, signin.ErrVerificationMailerNotConfigured)
	})

	T.Run("a registered verification mailer is attached", func(t *testing.T) {
		t.Parallel()

		// Built by hand rather than from base, because the directory has to
		// answer: a proven address is the one refusal reachable without a
		// transaction's worth of rows, and it is past the mailer check.
		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue[identity.Store](i, &identitymock.StoreMock{
			GetUserFunc: func(context.Context, database.SQLQueryExecutor, tenancy.Scope, string) (*identity.User, error) {
				return &identity.User{ID: "someone", EmailAddressVerifiedAt: pointer.To(time.Now())}, nil
			},
		})
		do.ProvideValue[tokens.Issuer](i, stubTokenIssuer{})
		do.ProvideValue(i, &Config{DefaultOwnerRoles: ownerRoles})
		withRegistrar(withAuthenticator(i))
		do.ProvideValue[signin.VerificationMailer](i,
			signin.VerificationMailerFunc(func(context.Context, *signin.VerificationMail) error { return nil }))
		RegisterService(i)

		svc, err := do.Invoke[*signin.Service](i)
		must.NoError(t, err)

		err = svc.RequestVerificationEmail(t.Context(), tenancy.Of("tenant"), "someone")
		test.ErrorIs(t, err, signin.ErrEmailAddressAlreadyVerified)
	})

	T.Run("a registered password policy is attached", func(t *testing.T) {
		t.Parallel()

		refused := errors.New("too short")

		i := withRegistrar(withAuthenticator(base(t, &Config{DefaultOwnerRoles: ownerRoles, Registration: RegistrationConfig{VerificationLinkTTL: time.Hour}})))
		do.ProvideValue(i, signin.PasswordPolicy(func(context.Context, string) error { return refused }))
		RegisterService(i)

		svc, err := do.Invoke[*signin.Service](i)
		must.NoError(t, err)

		_, err = svc.Register(t.Context(), tenancy.Of("tenant"), &signin.Registration{
			User:       &identity.User{},
			Credential: signin.Password("hunter2"),
		})
		test.ErrorIs(t, err, signin.ErrPasswordRefused)
		test.ErrorIs(t, err, refused)
	})

	T.Run("a registered account password policy is attached", func(t *testing.T) {
		t.Parallel()

		refused := errors.New("not your username")

		hashed, err := argon2.NewArgon2Authenticator().HashPassword(t.Context(), "hunter2")
		must.NoError(t, err)

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue[identity.Store](i, &identitymock.StoreMock{
			GetUserFunc: func(context.Context, database.SQLQueryExecutor, tenancy.Scope, string) (*identity.User, error) {
				return &identity.User{ID: "u", Username: "ada", HashedPassword: hashed}, nil
			},
		})
		do.ProvideValue[tokens.Issuer](i, stubTokenIssuer{})
		do.ProvideValue(i, &Config{DefaultOwnerRoles: ownerRoles})
		withRegistrar(withAuthenticator(i))
		do.ProvideValue(i, signin.AccountPasswordPolicy(func(context.Context, *signin.PasswordChange) error { return refused }))
		RegisterService(i)

		svc, err := do.Invoke[*signin.Service](i)
		must.NoError(t, err)

		err = svc.UpdatePassword(t.Context(), tenancy.Of("tenant"), "u", &signin.PasswordUpdate{
			CurrentPassword: "hunter2",
			NewPassword:     "ada",
		})
		test.ErrorIs(t, err, signin.ErrPasswordRefused)
		test.ErrorIs(t, err, refused)
	})

	T.Run("a registered registration policy is attached", func(t *testing.T) {
		t.Parallel()

		refused := errors.New("accept the terms")

		i := withRegistrar(withAuthenticator(base(t, &Config{DefaultOwnerRoles: ownerRoles, Registration: RegistrationConfig{VerificationLinkTTL: time.Hour}})))
		do.ProvideValue(i, signin.RegistrationPolicy(func(context.Context, *signin.Registration) error { return refused }))
		RegisterService(i)

		svc, err := do.Invoke[*signin.Service](i)
		must.NoError(t, err)

		_, err = svc.Register(t.Context(), tenancy.Of("tenant"), &signin.Registration{
			User:       &identity.User{},
			Credential: signin.Password("hunter2"),
		})
		test.ErrorIs(t, err, signin.ErrRegistrationRefused)
		test.ErrorIs(t, err, refused)
	})

	T.Run("no impersonation policy leaves the door shut", func(t *testing.T) {
		t.Parallel()

		i := withRegistrar(withAuthenticator(base(t, &Config{DefaultOwnerRoles: ownerRoles})))
		RegisterService(i)

		svc, err := do.Invoke[*signin.Service](i)
		must.NoError(t, err)

		_, err = svc.IssueImpersonationToken(t.Context(), tenancy.Global(), "operator", tenancy.Global(), "subject", "")
		test.ErrorIs(t, err, signin.ErrImpersonationDisabled)
	})

	T.Run("a registered impersonation policy is attached", func(t *testing.T) {
		t.Parallel()

		// The policy is consulted once both people are read, so a refusal
		// carrying its own error is the proof it was the one registered.
		refused := errors.New("operators may not act as this customer")

		i := withRegistrar(withAuthenticator(base(t, &Config{DefaultOwnerRoles: ownerRoles})))
		do.OverrideValue[identity.Store](i, &identitymock.StoreMock{
			GetUserFunc: func(_ context.Context, _ database.SQLQueryExecutor, _ tenancy.Scope, id string) (*identity.User, error) {
				return &identity.User{ID: id, AccountStatus: identity.StatusGood}, nil
			},
		})
		do.ProvideValue(i, signin.ImpersonationPolicy(func(context.Context, *identity.User, *identity.User) error { return refused }))
		RegisterService(i)

		svc, err := do.Invoke[*signin.Service](i)
		must.NoError(t, err)

		_, err = svc.IssueImpersonationToken(t.Context(), tenancy.Global(), "operator", tenancy.Global(), "subject", "")
		test.ErrorIs(t, err, refused)
		test.False(t, errors.Is(err, signin.ErrImpersonationDisabled))
	})

	T.Run("a registered hook that fails to build is returned", func(t *testing.T) {
		t.Parallel()

		broken := errors.New("hooks could not be built")

		i := withRegistrar(base(t, &Config{DefaultOwnerRoles: ownerRoles}))
		do.ProvideValue[authentication.Authenticator](i, argon2.NewArgon2Authenticator())
		do.Provide(i, func(do.Injector) (signin.Hooks, error) { return nil, broken })
		RegisterService(i)

		_, err := do.Invoke[*signin.Service](i)
		test.ErrorIs(t, err, broken)
	})

	T.Run("surfaces a bad config", func(t *testing.T) {
		t.Parallel()

		i := withAuthenticator(base(t, &Config{DefaultOwnerRoles: ownerRoles, SecondFactor: "sometimes"}))
		RegisterService(i)

		_, err := do.Invoke[*signin.Service](i)
		test.Error(t, err)
	})
}

func TestAPillarsProviderThatFailsToBuildFailsTheRegistration(T *testing.T) {
	T.Parallel()

	broken := errors.New("exporter unreachable")

	i := withAuthenticator(base(T, &Config{DefaultOwnerRoles: ownerRoles}))
	do.Provide(i, func(do.Injector) (*observability.Pillars, error) { return nil, broken })
	RegisterService(i)

	_, err := do.Invoke[*signin.Service](i)
	test.ErrorIs(T, err, broken)
}

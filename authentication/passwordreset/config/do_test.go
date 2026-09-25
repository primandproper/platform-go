package passwordresetcfg

import (
	"context"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset"
	"github.com/primandproper/platform-go/v14/identity"
	identitymock "github.com/primandproper/platform-go/v14/identity/mock"

	"github.com/primandproper/primitives-go/v2/authentication"
	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// base registers what both registrations need from the rest of the
// composition root, and none of what the application supplies, so each case
// says only what it is about.
func base(t *testing.T, cfg *Config) do.Injector {
	t.Helper()

	i := do.New()
	do.ProvideValue[context.Context](i, t.Context())
	do.ProvideValue[database.Client](i, testDBClient(t))
	do.ProvideValue[identity.Store](i, &identitymock.StoreMock{})
	do.ProvideValue(i, cfg)

	return i
}

// withApplication registers the two things only the application can supply.
func withApplication(i do.Injector) do.Injector {
	do.ProvideValue[passwordreset.Mailer](i, &recordingMailer{})
	do.ProvideValue[authentication.Authenticator](i, argon2.NewArgon2Authenticator())

	return i
}

func TestRegisterStore(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := base(t, &Config{TablePrefix: "ddb"})
		RegisterStore(i)

		store, err := do.Invoke[passwordreset.Store](i)
		must.NoError(t, err)
		test.NotNil(t, store)
	})

	T.Run("surfaces a bad config", func(t *testing.T) {
		t.Parallel()

		i := base(t, &Config{TablePrefix: "has space"})
		RegisterStore(i)

		store, err := do.Invoke[passwordreset.Store](i)
		must.Error(t, err)
		test.Nil(t, store)
	})
}

func TestRegisterService(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := withApplication(base(t, &Config{}))
		RegisterStore(i)
		RegisterService(i)

		svc, err := do.Invoke[*passwordreset.Service](i)
		must.NoError(t, err)
		test.NotNil(t, svc)
	})

	T.Run("a missing mailer fails naming it", func(t *testing.T) {
		t.Parallel()

		// The mailer is the application's by definition — the library does not
		// know the URL a link points at — so a container without one is a boot
		// failure that says which registration is missing.
		i := base(t, &Config{})
		do.ProvideValue[authentication.Authenticator](i, argon2.NewArgon2Authenticator())
		RegisterStore(i)
		RegisterService(i)

		svc, err := do.Invoke[*passwordreset.Service](i)
		test.Nil(t, svc)
		test.ErrorIs(t, err, do.ErrServiceNotFound)
		test.StrContains(t, err.Error(), do.NameOf[passwordreset.Mailer]())
	})

	T.Run("a missing authenticator fails naming it rather than defaulting", func(t *testing.T) {
		t.Parallel()

		// The ruling this package's documentation records: a reset that hashes
		// with a different engine from sign-in is a login that fails after a
		// successful reset, so there is no argon2 default to fall back on.
		i := base(t, &Config{})
		do.ProvideValue[passwordreset.Mailer](i, &recordingMailer{})
		RegisterStore(i)
		RegisterService(i)

		svc, err := do.Invoke[*passwordreset.Service](i)
		test.Nil(t, svc)
		test.ErrorIs(t, err, do.ErrServiceNotFound)
		test.StrContains(t, err.Error(), do.NameOf[authentication.Authenticator]())
	})

	T.Run("needs the identity store as its directory", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})
		withApplication(i)
		RegisterStore(i)
		RegisterService(i)

		svc, err := do.Invoke[*passwordreset.Service](i)
		test.Nil(t, svc)
		test.ErrorIs(t, err, do.ErrServiceNotFound)
		test.StrContains(t, err.Error(), do.NameOf[identity.Store]())
	})

	T.Run("needs a store", func(t *testing.T) {
		t.Parallel()

		i := withApplication(base(t, &Config{}))
		RegisterService(i)

		svc, err := do.Invoke[*passwordreset.Service](i)
		test.Nil(t, svc)
		test.ErrorIs(t, err, do.ErrServiceNotFound)
	})

	T.Run("surfaces a bad config", func(t *testing.T) {
		t.Parallel()

		// The store is registered by hand so the refusal is the service's own
		// validation rather than the store's.
		i := withApplication(base(t, &Config{TokenLifetime: -1}))
		do.ProvideValue[passwordreset.Store](i, nil)
		RegisterService(i)

		svc, err := do.Invoke[*passwordreset.Service](i)
		test.Nil(t, svc)
		test.Error(t, err)
	})
}

// TestAPillarsProviderThatFailsToBuildFailsEveryRegistration is the line
// observability.InvokePillars draws: absent observability wires up silently,
// and a registered provider that cannot be built is a failure rather than a
// component that looks configured and exports nothing.
func TestAPillarsProviderThatFailsToBuildFailsEveryRegistration(T *testing.T) {
	T.Parallel()

	boom := errors.New("the collector is unreachable")

	build := func(t *testing.T) do.Injector {
		t.Helper()

		i := withApplication(base(t, &Config{}))
		do.Provide(i, func(do.Injector) (*observability.Pillars, error) { return nil, boom })
		RegisterStore(i)
		RegisterService(i)

		return i
	}

	T.Run("store", func(t *testing.T) {
		t.Parallel()

		store, err := do.Invoke[passwordreset.Store](build(t))
		test.Nil(t, store)
		test.ErrorIs(t, err, boom)
	})

	T.Run("service", func(t *testing.T) {
		t.Parallel()

		svc, err := do.Invoke[*passwordreset.Service](build(t))
		test.Nil(t, svc)
		test.ErrorIs(t, err, boom)
	})
}

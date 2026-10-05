package passkeyscfg

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v15/authentication/passkeys"

	"github.com/primandproper/primitives-go/v2/authentication/webauthn"
	"github.com/primandproper/primitives-go/v2/database"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// base registers what both registrations need from the rest of the
// composition root, and none of what the application supplies.
func base(t *testing.T, cfg *Config) do.Injector {
	t.Helper()

	i := do.New()
	do.ProvideValue[context.Context](i, t.Context())
	do.ProvideValue[database.Client](i, testDBClient(t))
	do.ProvideValue(i, testRelyingParty(t))
	do.ProvideValue(i, cfg)

	return i
}

func TestRegisterStore(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := base(t, &Config{TablePrefix: "app"})
		RegisterStore(i)

		store, err := do.Invoke[passkeys.Store](i)
		must.NoError(t, err)
		test.NotNil(t, store)
	})

	T.Run("surfaces a bad config", func(t *testing.T) {
		t.Parallel()

		i := base(t, &Config{TablePrefix: "has space"})
		RegisterStore(i)

		_, err := do.Invoke[passkeys.Store](i)
		test.Error(t, err)
	})
}

func TestRegisterService(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := base(t, &Config{})
		do.ProvideValue[passkeys.UserResolver](i, resolveNobody)
		do.ProvideValue[passkeys.EnrollmentGate](i, passkeys.AdmitEveryEnrollment)
		do.ProvideValue[passkeys.Hooks](i, passkeys.NoopHooks{})
		RegisterStore(i)
		RegisterService(i)

		svc, err := do.Invoke[*passkeys.Service](i)
		must.NoError(t, err)
		test.NotNil(t, svc)
	})

	T.Run("the application's user resolver is required", func(t *testing.T) {
		t.Parallel()

		i := base(t, &Config{})
		do.ProvideValue[passkeys.EnrollmentGate](i, passkeys.AdmitEveryEnrollment)
		RegisterStore(i)
		RegisterService(i)

		_, err := do.Invoke[*passkeys.Service](i)
		must.Error(t, err)
		test.StrContains(t, err.Error(), do.NameOf[passkeys.UserResolver]())
	})

	T.Run("the application's enrollment gate is required", func(t *testing.T) {
		t.Parallel()

		i := base(t, &Config{})
		do.ProvideValue[passkeys.UserResolver](i, resolveNobody)
		RegisterStore(i)
		RegisterService(i)

		_, err := do.Invoke[*passkeys.Service](i)
		must.Error(t, err)
		test.StrContains(t, err.Error(), do.NameOf[passkeys.EnrollmentGate]())
	})

	T.Run("the relying party is required", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue[database.Client](i, testDBClient(t))
		do.ProvideValue(i, &Config{})
		do.ProvideValue[passkeys.UserResolver](i, resolveNobody)
		do.ProvideValue[passkeys.EnrollmentGate](i, passkeys.AdmitEveryEnrollment)
		RegisterStore(i)
		RegisterService(i)

		_, err := do.Invoke[*passkeys.Service](i)
		must.Error(t, err)
		test.StrContains(t, err.Error(), do.NameOf[*webauthn.RelyingParty]())
	})
}

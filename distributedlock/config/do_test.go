package distributedlockcfg

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v13/distributedlock"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterLocker(T *testing.T) {
	T.Parallel()

	T.Run("builds without a registered database.Client when the provider is not postgres", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{Provider: MemoryProvider})

		RegisterLocker(i)

		locker, err := do.Invoke[distributedlock.Locker](i)
		must.NoError(t, err)
		test.NotNil(t, locker)
	})

	T.Run("errors when the provider is postgres but no database.Client is registered", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{Provider: PostgresProvider})

		RegisterLocker(i)

		locker, err := do.Invoke[distributedlock.Locker](i)
		must.Error(t, err)
		test.Nil(t, locker)
	})
}

func TestRegisterScopedLocker(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{Provider: MemoryProvider})

		RegisterScopedLocker(i)

		locker, err := do.Invoke[distributedlock.ScopedLocker](i)
		must.NoError(t, err)
		test.NotNil(t, locker)
	})
}

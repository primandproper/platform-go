package retrycfg

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/retry"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterPolicy(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{
			MaxAttempts:  2,
			InitialDelay: time.Millisecond,
			MaxDelay:     time.Millisecond,
			Multiplier:   2,
		})

		RegisterPolicy(i)

		policy, err := do.Invoke[retry.Policy](i)
		must.NoError(t, err)
		test.NotNil(t, policy)
	})

	T.Run("an invalid config surfaces through the injector", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{Provider: "nonsense"})

		RegisterPolicy(i)

		policy, err := do.Invoke[retry.Policy](i)
		test.Error(t, err)
		test.Nil(t, policy)
	})
}

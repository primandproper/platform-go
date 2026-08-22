package retrycfg

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func validConfig() *Config {
	return &Config{
		MaxAttempts:  3,
		InitialDelay: time.Millisecond,
		MaxDelay:     10 * time.Millisecond,
		Multiplier:   2,
	}
}

func TestNewPolicy(T *testing.T) {
	T.Parallel()

	T.Run("empty provider selects exponential backoff", func(t *testing.T) {
		t.Parallel()

		policy, err := NewPolicy(t.Context(), validConfig())
		must.NoError(t, err)
		must.NotNil(t, policy)

		attempts := 0
		test.Error(t, policy.Execute(t.Context(), func(context.Context) error {
			attempts++

			return errors.New("always fails")
		}))
		// Retries rather than running once: the unset provider must not be a noop.
		test.EqOp(t, 3, attempts)
	})

	T.Run("exponential named explicitly", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.Provider = ProviderExponential

		policy, err := NewPolicy(t.Context(), cfg)
		must.NoError(t, err)
		must.NotNil(t, policy)
	})

	T.Run("noop runs the operation exactly once", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.Provider = ProviderNoop

		policy, err := NewPolicy(t.Context(), cfg)
		must.NoError(t, err)

		attempts := 0
		test.Error(t, policy.Execute(t.Context(), func(context.Context) error {
			attempts++

			return errors.New("always fails")
		}))
		test.EqOp(t, 1, attempts)
	})

	T.Run("unknown provider is an error, not a policy that never retries", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.Provider = "exponentail"

		// Validation and dispatch read the same providers list, so a misspelling
		// is refused before dispatch is reached rather than at its default arm.
		policy, err := NewPolicy(t.Context(), cfg)
		test.Error(t, err)
		test.Nil(t, policy)
	})

	T.Run("nil config is an error", func(t *testing.T) {
		t.Parallel()

		policy, err := NewPolicy(t.Context(), nil)
		test.ErrorIs(t, err, errors.ErrNilInputParameter)
		test.Nil(t, policy)
	})

	T.Run("an invalid config is rejected", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.Multiplier = 0.5

		policy, err := NewPolicy(t.Context(), cfg)
		test.Error(t, err)
		test.Nil(t, policy)
	})
}

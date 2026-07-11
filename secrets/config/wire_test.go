package secretscfg

import (
	"context"
	"os"
	"testing"

	"github.com/shoenig/test/must"
)

func TestNewSecretSourceFromConfig(T *testing.T) {
	T.Parallel()

	T.Run("nil config returns env source", func(t *testing.T) {
		t.Parallel()

		var cfg *Config
		source, err := NewSecretSource(context.Background(), cfg, nil, nil, nil)
		must.NoError(t, err)
		must.NotNil(t, source)

		key := "TEST_WIRE_NIL_" + t.Name()
		value := "from-env"
		must.NoError(t, os.Setenv(key, value))
		t.Cleanup(func() { _ = os.Unsetenv(key) })

		got, err := source.GetSecret(context.Background(), key)
		must.NoError(t, err)
		must.EqOp(t, value, got)
	})

	T.Run("empty provider returns env source", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ""}
		source, err := NewSecretSource(context.Background(), cfg, nil, nil, nil)
		must.NoError(t, err)
		must.NotNil(t, source)

		key := "TEST_WIRE_EMPTY_" + t.Name()
		value := "from-env"
		must.NoError(t, os.Setenv(key, value))
		t.Cleanup(func() { _ = os.Unsetenv(key) })

		got, err := source.GetSecret(context.Background(), key)
		must.NoError(t, err)
		must.EqOp(t, value, got)
	})

	T.Run("noop provider returns noop source", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderNoop}
		source, err := NewSecretSource(context.Background(), cfg, nil, nil, nil)
		must.NoError(t, err)
		must.NotNil(t, source)

		got, err := source.GetSecret(context.Background(), "any")
		must.NoError(t, err)
		must.EqOp(t, "", got)
	})

	T.Run("env provider returns env source", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderEnv}
		source, err := NewSecretSource(context.Background(), cfg, nil, nil, nil)
		must.NoError(t, err)
		must.NotNil(t, source)

		key := "TEST_WIRE_ENV_" + t.Name()
		value := "from-env"
		must.NoError(t, os.Setenv(key, value))
		t.Cleanup(func() { _ = os.Unsetenv(key) })

		got, err := source.GetSecret(context.Background(), key)
		must.NoError(t, err)
		must.EqOp(t, value, got)
	})

	T.Run("provider error is wrapped", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: "vault"}
		source, err := NewSecretSource(context.Background(), cfg, nil, nil, nil)
		must.Error(t, err)
		must.Nil(t, source)
		must.StrContains(t, err.Error(), "provide secret source")
	})
}

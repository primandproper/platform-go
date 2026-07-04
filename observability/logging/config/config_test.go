package loggingcfg

import (
	"encoding/json"
	"testing"

	"github.com/primandproper/platform-go/v3/observability/logging"
	"github.com/primandproper/platform-go/v3/observability/logging/otelgrpc"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			ServiceName: t.Name(),
			Level:       logging.InfoLevel,
			Provider:    ProviderZerolog,
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("rejects missing service name", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Level:    logging.InfoLevel,
			Provider: ProviderZerolog,
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("accepts a level decoded from JSON", func(t *testing.T) {
		t.Parallel()

		// A decoded Level is a fresh pointer, not one of the package singletons; the
		// old validation.In (reflect.DeepEqual on a pointer type) rejected it.
		ctx := t.Context()
		var lvl logging.Level
		must.NoError(t, json.Unmarshal([]byte(`"debug"`), &lvl))

		cfg := &Config{
			ServiceName: t.Name(),
			Level:       lvl,
			Provider:    ProviderZerolog,
		}

		test.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("rejects an unknown level", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		var lvl logging.Level
		must.NoError(t, json.Unmarshal([]byte(`"bogus"`), &lvl))

		cfg := &Config{
			ServiceName: t.Name(),
			Level:       lvl,
			Provider:    ProviderZerolog,
		}

		test.Error(t, cfg.ValidateWithContext(ctx))
	})
}

func TestConfig_ProvideLogger(T *testing.T) {
	T.Parallel()

	T.Run("zerolog provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ProviderZerolog,
		}

		l, err := cfg.ProvideLogger(ctx)
		test.NoError(t, err)
		test.NotNil(t, l)
	})

	T.Run("zap provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ProviderZap,
		}

		l, err := cfg.ProvideLogger(ctx)
		test.NoError(t, err)
		test.NotNil(t, l)
	})

	T.Run("slog provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ProviderSlog,
		}

		l, err := cfg.ProvideLogger(ctx)
		test.NoError(t, err)
		test.NotNil(t, l)
	})

	T.Run("otelslog provider", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider:    ProviderOtelSlog,
			ServiceName: t.Name(),
			OtelSlog:    &otelgrpc.Config{},
		}

		l, err := cfg.ProvideLogger(ctx)
		test.NoError(t, err)
		test.NotNil(t, l)
	})

	T.Run("otelslog provider with nil otelslog config returns error", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider:    ProviderOtelSlog,
			ServiceName: t.Name(),
		}

		l, err := cfg.ProvideLogger(ctx)
		test.Error(t, err)
		test.Nil(t, l)
	})

	T.Run("no provider falls back to noop", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{}

		l, err := cfg.ProvideLogger(ctx)
		test.NoError(t, err)
		test.NotNil(t, l)
	})
}

func TestProvideLogger(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ProviderZerolog,
		}

		l, err := ProvideLogger(ctx, cfg)
		must.NoError(t, err)
		test.NotNil(t, l)
	})
}

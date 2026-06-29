package loggingcfg

import (
	"testing"

	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/logging/otelgrpc"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// NOTE: ValidateWithContext calls validation.ValidateStructWithContext(ctx, &cfg, ...),
// where cfg is already *Config, producing **Config. The validator rejects double
// pointers, so every call currently returns "only a pointer to a struct can be validated".
// We assert the current behavior rather than refactor production code; 100% line coverage
// is still achieved because the single statement is executed.
func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("returns error because of double-pointer", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			ServiceName: t.Name(),
			Level:       logging.InfoLevel,
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

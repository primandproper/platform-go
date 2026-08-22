package pyroscope

import (
	"testing"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/profiling"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewProfilingProvider(T *testing.T) {
	T.Parallel()

	T.Run("with nil config", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()

		p, err := NewProfilingProvider(ctx, logger, "test-service", nil)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
		test.Nil(t, p)
	})

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := &Config{
			ServerAddress: "http://localhost:99999",
			UploadRate:    15 * time.Second,
		}

		p, err := NewProfilingProvider(ctx, logger, "test-service", cfg)
		must.NoError(t, err)
		must.NotNil(t, p)

		must.NoError(t, p.Shutdown(ctx))
	})

	T.Run("with mutex and block profiles", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := &Config{
			ServerAddress:      "http://localhost:99999",
			UploadRate:         15 * time.Second,
			EnableMutexProfile: true,
			EnableBlockProfile: true,
		}

		p, err := NewProfilingProvider(ctx, logger, "test-service", cfg)
		must.NoError(t, err)
		must.NotNil(t, p)

		must.NoError(t, p.Shutdown(ctx))
	})

	T.Run("with tags", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := &Config{
			ServerAddress: "http://localhost:99999",
			UploadRate:    15 * time.Second,
			Tags:          map[string]string{"env": "test", "region": "us-east-1"},
		}

		p, err := NewProfilingProvider(ctx, logger, "test-service", cfg)
		must.NoError(t, err)
		must.NotNil(t, p)

		must.NoError(t, p.Shutdown(ctx))
	})
}

func TestProvider_Start(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := &Config{
			ServerAddress: "http://localhost:99999",
			UploadRate:    15 * time.Second,
		}

		p, err := NewProfilingProvider(ctx, logger, "test-service", cfg)
		must.NoError(t, err)

		test.NoError(t, p.Start(ctx))
		must.NoError(t, p.Shutdown(ctx))
	})
}

func TestProvider_Shutdown(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		cfg := &Config{
			ServerAddress: "http://localhost:99999",
			UploadRate:    15 * time.Second,
		}

		p, err := NewProfilingProvider(ctx, logger, "test-service", cfg)
		must.NoError(t, err)

		test.NoError(t, p.Shutdown(ctx))
	})
}

func TestProvider_InterfaceCompliance(T *testing.T) {
	T.Parallel()

	T.Run("implements profiling.Provider", func(t *testing.T) {
		t.Parallel()

		var _ profiling.Provider = (*Provider)(nil)
	})
}

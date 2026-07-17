package profilingcfg

import (
	"testing"
	"time"

	loggingnoop "github.com/primandproper/platform-go/v5/observability/logging/noop"
	"github.com/primandproper/platform-go/v5/observability/profiling/pprof"
	"github.com/primandproper/platform-go/v5/observability/profiling/pyroscope"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("valid empty provider", func(t *testing.T) {
		t.Parallel()
		c := &Config{Provider: ""}
		test.NoError(t, c.ValidateWithContext(t.Context()))
	})

	T.Run("valid pprof provider", func(t *testing.T) {
		t.Parallel()
		c := &Config{
			Provider: ProviderPprof,
			Pprof:    &pprof.Config{Port: 6060},
		}
		test.NoError(t, c.ValidateWithContext(t.Context()))
	})

	T.Run("valid pyroscope provider", func(t *testing.T) {
		t.Parallel()
		c := &Config{
			Provider: ProviderPyroscope,
			Pyroscope: &pyroscope.Config{
				ServerAddress: "http://localhost:4040",
				UploadRate:    1,
			},
		}
		test.NoError(t, c.ValidateWithContext(t.Context()))
	})

	T.Run("invalid provider string", func(t *testing.T) {
		t.Parallel()
		c := &Config{Provider: "invalid"}
		test.Error(t, c.ValidateWithContext(t.Context()))
	})

	T.Run("pyroscope provider without config", func(t *testing.T) {
		t.Parallel()
		c := &Config{Provider: ProviderPyroscope}
		test.Error(t, c.ValidateWithContext(t.Context()))
	})

	T.Run("pprof config present with empty provider is invalid", func(t *testing.T) {
		t.Parallel()
		c := &Config{
			Provider: "",
			Pprof:    &pprof.Config{Port: 6060},
		}
		test.Error(t, c.ValidateWithContext(t.Context()))
	})

	T.Run("pyroscope config present with pprof provider is invalid", func(t *testing.T) {
		t.Parallel()
		c := &Config{
			Provider: ProviderPprof,
			Pyroscope: &pyroscope.Config{
				ServerAddress: "http://localhost:4040",
				UploadRate:    1,
			},
		}
		test.Error(t, c.ValidateWithContext(t.Context()))
	})
}

func TestConfig_NewProfilingProvider(T *testing.T) {
	T.Parallel()

	logger := loggingnoop.NewLogger()

	T.Run("default provider returns noop", func(t *testing.T) {
		t.Parallel()
		c := &Config{Provider: ""}
		p, err := c.NewProfilingProvider(t.Context(), logger)
		must.NoError(t, err)
		test.NotNil(t, p)
	})

	T.Run("unknown provider returns noop", func(t *testing.T) {
		t.Parallel()
		c := &Config{Provider: "unknown"}
		p, err := c.NewProfilingProvider(t.Context(), logger)
		must.NoError(t, err)
		test.NotNil(t, p)
	})

	T.Run("pprof with nil config uses defaults", func(t *testing.T) {
		t.Parallel()
		c := &Config{Provider: ProviderPprof}
		p, err := c.NewProfilingProvider(t.Context(), logger)
		must.NoError(t, err)
		test.NotNil(t, p)
		must.NoError(t, p.Shutdown(t.Context()))
	})

	T.Run("pprof with config", func(t *testing.T) {
		t.Parallel()
		c := &Config{
			Provider: ProviderPprof,
			Pprof:    &pprof.Config{Port: 16060},
		}
		p, err := c.NewProfilingProvider(t.Context(), logger)
		must.NoError(t, err)
		test.NotNil(t, p)
		must.NoError(t, p.Shutdown(t.Context()))
	})

	T.Run("pyroscope with nil config returns noop", func(t *testing.T) {
		t.Parallel()
		c := &Config{Provider: ProviderPyroscope}
		p, err := c.NewProfilingProvider(t.Context(), logger)
		must.NoError(t, err)
		test.NotNil(t, p)
	})

	T.Run("pyroscope with config sets default upload rate", func(t *testing.T) {
		t.Parallel()
		c := &Config{
			Provider:    ProviderPyroscope,
			ServiceName: "test-service",
			Pyroscope: &pyroscope.Config{
				ServerAddress: "http://localhost:4040",
			},
		}
		p, err := c.NewProfilingProvider(t.Context(), logger)
		must.NoError(t, err)
		test.NotNil(t, p)
		test.EqOp(t, 15*time.Second, c.Pyroscope.UploadRate)
		must.NoError(t, p.Shutdown(t.Context()))
	})

	T.Run("pyroscope with non-default upload rate", func(t *testing.T) {
		t.Parallel()
		c := &Config{
			Provider:    ProviderPyroscope,
			ServiceName: "test-service",
			Pyroscope: &pyroscope.Config{
				ServerAddress: "http://localhost:4040",
				UploadRate:    5 * time.Second,
			},
		}
		p, err := c.NewProfilingProvider(t.Context(), logger)
		must.NoError(t, err)
		test.NotNil(t, p)
		test.EqOp(t, 5*time.Second, c.Pyroscope.UploadRate)
		must.NoError(t, p.Shutdown(t.Context()))
	})
}

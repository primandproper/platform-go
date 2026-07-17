package pprof

import (
	"context"
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v5/observability/logging/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewProfilingProvider(T *testing.T) {
	T.Parallel()

	T.Run("nil config returns noop", func(t *testing.T) {
		t.Parallel()
		p, err := NewProfilingProvider(context.Background(), loggingnoop.NewLogger(), nil)
		must.NoError(t, err)
		test.NotNil(t, p)
	})

	T.Run("zero port uses default", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Port: 0}
		p, err := NewProfilingProvider(context.Background(), loggingnoop.NewLogger(), cfg)
		must.NoError(t, err)
		test.NotNil(t, p)
		must.NoError(t, p.Shutdown(context.Background()))
	})

	T.Run("with mutex and block profiling", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Port:               16061,
			EnableMutexProfile: true,
			EnableBlockProfile: true,
		}
		p, err := NewProfilingProvider(context.Background(), loggingnoop.NewLogger(), cfg)
		must.NoError(t, err)
		test.NotNil(t, p)
		must.NoError(t, p.Shutdown(context.Background()))
	})

	T.Run("start and shutdown", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Port: 16062}
		p, err := NewProfilingProvider(context.Background(), loggingnoop.NewLogger(), cfg)
		must.NoError(t, err)
		must.NoError(t, p.Start(context.Background()))
		must.NoError(t, p.Shutdown(context.Background()))
	})
}

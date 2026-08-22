package redistest

import (
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v13/testutils/containers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewOptions(T *testing.T) {
	T.Parallel()

	T.Run("defaults", func(t *testing.T) {
		t.Parallel()

		cfg := newOptions(nil)
		test.EqOp(t, DefaultImage, cfg.image)
		test.False(t, cfg.clusterEnabled)
	})

	T.Run("options override defaults", func(t *testing.T) {
		t.Parallel()

		cfg := newOptions([]Option{WithImage("docker.io/redis:8"), WithClusterEnabled()})
		test.EqOp(t, "docker.io/redis:8", cfg.image)
		test.True(t, cfg.clusterEnabled)
	})
}

// TestStart_Gate pins the guarantee Start inherits from containers.Run: callers
// no longer have to remember containers.SkipIfNotRunning themselves.
//
//nolint:paralleltest // mutates the package-level RunningTests gate; must run serially
func TestStart_Gate(T *testing.T) {
	T.Run("skips instead of demanding a Docker daemon", func(t *testing.T) { //nolint:paralleltest // mutates the package-level RunningTests gate; must run serially
		original := containers.RunningTests
		t.Cleanup(func() { containers.RunningTests = original })
		containers.RunningTests = false

		t.Cleanup(func() { test.True(t, t.Skipped()) })

		Start(t)

		t.Error("Start returned instead of skipping the test")
	})
}

func TestStart_Container(T *testing.T) {
	T.Parallel()

	T.Run("returns a reachable container", func(t *testing.T) {
		t.Parallel()

		container := Start(t)
		must.NotNil(t, container)

		addr := Address(t, container)
		test.False(t, strings.HasPrefix(addr, "redis://"))
		test.StrContains(t, addr, ":")
	})
}

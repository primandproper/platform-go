package objectstorage

import (
	"testing"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// A registered observability provider that fails to build has to reach the
// caller. Treating it as absent would hand the component a noop and leave a
// misconfigured exporter looking configured — see observability.InvokePillars.
func TestRegister_failingObservabilityIsAnError(T *testing.T) {
	T.Parallel()

	// Asserted by identity, not merely that some error came back: a missing
	// config would also fail, and would not exercise this branch.
	errBuild := errors.New("building the metrics provider")

	T.Run("RegisterUploadManager", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.Provide(i, func(do.Injector) (metrics.Provider, error) {
			return nil, errBuild
		})

		RegisterUploadManager(i)

		_, err := do.Invoke[*Uploader](i)
		must.Error(t, err)
		test.ErrorIs(t, err, errBuild)
	})
}

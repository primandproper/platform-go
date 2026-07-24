package profilingcfg

import (
	"context"
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v6/observability/logging/noop"
	"github.com/primandproper/platform-go/v6/observability/profiling"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterProfilingProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, &Config{})

		RegisterProfilingProvider(i)

		p, err := do.Invoke[profiling.Provider](i)
		must.NoError(t, err)
		test.NotNil(t, p)
	})
}

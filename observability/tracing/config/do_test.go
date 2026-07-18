package tracingcfg

import (
	"context"
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v5/observability/logging/noop"
	"github.com/primandproper/platform-go/v5/observability/tracing"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterTracerProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, &Config{})

		RegisterTracerProvider(i)

		tp, err := do.Invoke[tracing.TracerProvider](i)
		must.NoError(t, err)
		test.NotNil(t, tp)
	})
}

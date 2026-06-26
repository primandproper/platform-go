package asynccfg

import (
	"testing"

	"github.com/primandproper/platform-go/notifications/async"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	"github.com/primandproper/platform-go/observability/metrics"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterAsyncNotifier(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue[metrics.Provider](i, nil)
		do.ProvideValue(i, &Config{})

		RegisterAsyncNotifier(i)

		notifier, err := do.Invoke[async.AsyncNotifier](i)
		must.NoError(t, err)
		test.NotNil(t, notifier)
	})
}

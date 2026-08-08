package asynccfg

import (
	"testing"

	"github.com/primandproper/platform-go/v10/notifications/async"
	"github.com/primandproper/platform-go/v10/notifications/async/fanout"
	loggingnoop "github.com/primandproper/platform-go/v10/observability/logging/noop"
	"github.com/primandproper/platform-go/v10/observability/metrics"
	tracingnoop "github.com/primandproper/platform-go/v10/observability/tracing/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterAsyncNotifier(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue[metrics.Provider](i, nil)
		do.ProvideValue(i, &Config{})

		RegisterAsyncNotifier(i)

		notifier, err := do.Invoke[async.AsyncNotifier](i)
		must.NoError(t, err)
		test.NotNil(t, notifier)
	})

	T.Run("with fan-out enabled", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue[metrics.Provider](i, nil)
		do.ProvideValue(i, &Config{Provider: ProviderSSE, FanOut: &fanout.Config{Enabled: true}})
		do.ProvideValue(i, newPublisherProviderForTest())
		do.ProvideValue(i, newConsumerProviderForTest())

		RegisterAsyncNotifier(i)

		notifier, err := do.Invoke[async.AsyncNotifier](i)
		must.NoError(t, err)

		_, wrapped := notifier.(*fanout.Notifier)
		must.True(t, wrapped)

		test.NoError(t, notifier.Close())
	})

	T.Run("with no messagequeue registered", func(t *testing.T) {
		t.Parallel()

		// A container that registers no message queue still wires up, because a
		// config that has not asked for fan-out never reaches one.
		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, &Config{Provider: ProviderSSE})

		RegisterAsyncNotifier(i)

		notifier, err := do.Invoke[async.AsyncNotifier](i)
		must.NoError(t, err)
		test.NotNil(t, notifier)
	})
}

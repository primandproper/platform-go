package msgconfig

import (
	"testing"

	"github.com/primandproper/platform-go/v3/messagequeue"
	loggingnoop "github.com/primandproper/platform-go/v3/observability/logging/noop"
	"github.com/primandproper/platform-go/v3/observability/metrics"
	tracingnoop "github.com/primandproper/platform-go/v3/observability/tracing/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterMessageQueue(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue[metrics.Provider](i, nil)
		do.ProvideValue(i, &Config{})

		RegisterMessageQueue(i)

		consumer, err := do.Invoke[messagequeue.ConsumerProvider](i)
		must.NoError(t, err)
		test.NotNil(t, consumer)

		publisher, err := do.Invoke[messagequeue.PublisherProvider](i)
		must.NoError(t, err)
		test.NotNil(t, publisher)
	})
}

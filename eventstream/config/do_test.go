package config

import (
	"testing"

	"github.com/primandproper/platform-go/v6/eventstream"
	loggingnoop "github.com/primandproper/platform-go/v6/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v6/observability/tracing/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterEventStreamUpgrader(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue(i, &Config{Provider: ProviderSSE})

		RegisterEventStreamUpgrader(i)

		upgrader, err := do.Invoke[eventstream.EventStreamUpgrader](i)
		must.NoError(t, err)
		test.NotNil(t, upgrader)
	})
}

func TestRegisterBidirectionalEventStreamUpgrader(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue(i, &Config{Provider: ProviderWebSocket})

		RegisterBidirectionalEventStreamUpgrader(i)

		upgrader, err := do.Invoke[eventstream.BidirectionalEventStreamUpgrader](i)
		must.NoError(t, err)
		test.NotNil(t, upgrader)
	})
}

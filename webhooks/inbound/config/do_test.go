package inboundcfg

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v13/messagequeue"
	messagequeuecfg "github.com/primandproper/platform-go/v13/messagequeue/config"
	"github.com/primandproper/platform-go/v13/observability"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"
	"github.com/primandproper/platform-go/v13/webhooks/inbound"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// noopPublishers is a PublisherProvider that discards, so a DI test needs no broker.
func noopPublishers(t *testing.T) messagequeue.PublisherProvider {
	t.Helper()

	publishers, err := messagequeuecfg.NewPublisherProvider(t.Context(), &messagequeuecfg.Config{
		Publisher: messagequeuecfg.MessageQueueConfig{Provider: messagequeuecfg.ProviderNoop},
	})
	must.NoError(t, err)

	return publishers
}

func TestRegisterVerifier(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, validConfig())

		RegisterVerifier(i)

		verifier, err := do.Invoke[inbound.Verifier](i)
		must.NoError(t, err)
		test.EqOp(t, "github", verifier.Provider())
	})

	T.Run("reports a config it cannot build a scheme from", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, &Config{})

		RegisterVerifier(i)

		verifier, err := do.Invoke[inbound.Verifier](i)
		test.Error(t, err)
		test.Nil(t, verifier)
	})
}

func TestRegisterReceiver(T *testing.T) {
	T.Parallel()

	// A container that registers no observability still wires up: absent means noop.
	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, validConfig())
		do.ProvideValue(i, noopPublishers(t))

		RegisterReceiver(i)

		receiver, err := do.Invoke[*inbound.Receiver](i)
		must.NoError(t, err)
		test.NotNil(t, receiver)
	})

	T.Run("reads the registered pillars", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, validConfig())
		do.ProvideValue(i, noopPublishers(t))
		do.ProvideValue(i, &observability.Pillars{
			Logger:          loggingnoop.NewLogger(),
			TracerProvider:  tracingnoop.NewTracerProvider(),
			MetricsProvider: metricsnoop.NewMetricsProvider(),
		})

		RegisterReceiver(i)

		receiver, err := do.Invoke[*inbound.Receiver](i)
		must.NoError(t, err)
		test.NotNil(t, receiver)
	})
}

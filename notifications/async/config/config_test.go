package asynccfg

import (
	"fmt"
	"testing"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/notifications/async/ably"
	"github.com/primandproper/platform-go/v13/notifications/async/pusher"
	asyncsse "github.com/primandproper/platform-go/v13/notifications/async/sse"
	asyncws "github.com/primandproper/platform-go/v13/notifications/async/websocket"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/metrics/metricstest"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderNoop,
		}

		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("with invalid provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: "invalid",
		}

		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("with unset provider", func(t *testing.T) {
		t.Parallel()

		// Notifying nobody is selected by naming noop. An unset provider used to
		// do it silently, which is indistinguishable from a deployment that
		// meant to notify somebody and typed the name in the wrong variable.
		must.Error(t, (&Config{}).ValidateWithContext(t.Context()))
	})

	T.Run("pusher requires config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderPusher,
		}

		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("ably requires config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderAbly,
		}

		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("self-hosted providers require a topology declaration", func(t *testing.T) {
		t.Parallel()

		// The failure: a config that looks complete, loads without complaint,
		// and silently drops notifications the moment a second replica exists.
		// Nothing here can count replicas, so the only way to tell the correct
		// deployment from the broken one is to make the operator say which it
		// is.
		for _, provider := range []string{ProviderSSE, ProviderWebSocket} {
			cfg := &Config{Provider: provider, SSE: &asyncsse.Config{}, WebSocket: &asyncws.Config{}}

			test.ErrorIs(t, cfg.ValidateWithContext(t.Context()), ErrTopologyRequired)

			_, err := cfg.NewAsyncNotifier(t.Context())
			test.ErrorIs(t, err, ErrTopologyRequired)
		}
	})

	T.Run("self-hosted providers refuse a fleet", func(t *testing.T) {
		t.Parallel()

		for _, provider := range []string{ProviderSSE, ProviderWebSocket} {
			cfg := &Config{Provider: provider, SSE: &asyncsse.Config{}, WebSocket: &asyncws.Config{}, Topology: TopologyFleet}

			test.ErrorIs(t, cfg.ValidateWithContext(t.Context()), ErrFleetUnsupportedForSelfHostedProvider)

			_, err := cfg.NewAsyncNotifier(t.Context())
			test.ErrorIs(t, err, ErrFleetUnsupportedForSelfHostedProvider)
		}
	})

	T.Run("hosted and noop providers ignore topology", func(t *testing.T) {
		t.Parallel()

		// A hosted broker holds the connections, so replica count is not
		// load-bearing and an undeclared topology is not a mistake.
		for _, topology := range []string{"", TopologySingleReplica, TopologyFleet} {
			cfg := &Config{Provider: ProviderNoop, Topology: topology}
			must.NoError(t, cfg.ValidateWithContext(t.Context()))

			actual, err := cfg.NewAsyncNotifier(t.Context())
			test.NotNil(t, actual)
			test.NoError(t, err)
		}
	})

	T.Run("with an unrecognized topology", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderSSE, Topology: "two_ish"}

		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("websocket requires config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderWebSocket,
		}

		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestConfig_NewAsyncNotifier(T *testing.T) {
	T.Parallel()

	T.Run("with websocket", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider:  ProviderWebSocket,
			WebSocket: &asyncws.Config{},
			Topology:  TopologySingleReplica,
		}

		actual, err := cfg.NewAsyncNotifier(t.Context())
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with sse", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderSSE,
			SSE:      &asyncsse.Config{},
			Topology: TopologySingleReplica,
		}

		actual, err := cfg.NewAsyncNotifier(t.Context())
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with pusher", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderPusher,
			Pusher: &pusher.Config{
				AppID:   "123",
				Key:     "key",
				Secret:  "secret",
				Cluster: "us2",
			},
		}

		actual, err := cfg.NewAsyncNotifier(t.Context())
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with ably", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderAbly,
			Ably: &ably.Config{
				APIKey: "appid.keyid:keysecret",
			},
		}

		actual, err := cfg.NewAsyncNotifier(t.Context())
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	noopProviders := []string{ProviderNoop}
	for _, provider := range noopProviders {
		T.Run(fmt.Sprintf("with noop provider %q", provider), func(t *testing.T) {
			t.Parallel()

			cfg := &Config{
				Provider: provider,
			}

			actual, err := cfg.NewAsyncNotifier(t.Context())
			test.NotNil(t, actual)
			test.NoError(t, err)
		})
	}

	T.Run("with unknown provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: "unknown",
		}

		actual, err := cfg.NewAsyncNotifier(t.Context())
		test.Nil(t, actual)
		test.ErrorIs(t, err, errors.ErrUnknownProvider)
	})

	T.Run("with unset provider", func(t *testing.T) {
		t.Parallel()

		actual, err := (&Config{}).NewAsyncNotifier(t.Context())
		test.Nil(t, actual)
		test.ErrorIs(t, err, errors.ErrUnknownProvider)
	})

	T.Run("a failed provider yields a nil interface, not a typed nil", func(t *testing.T) {
		t.Parallel()

		// The config is complete and the metrics provider is what fails, so
		// this reaches pusher.NewNotifier and fails inside it. A config that is
		// merely incomplete would be refused by ValidateWithContext first and
		// never exercise the conversion this is about.
		cfg := &Config{
			Provider: ProviderPusher,
			Pusher: &pusher.Config{
				AppID:   "123",
				Key:     "key",
				Secret:  "secret",
				Cluster: "us2",
			},
		}

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metricstest.Int64Counter(t, "x"), errors.New("arbitrary")
			},
		}

		// Compared against nil directly rather than with test.Nil, which is
		// satisfied by a nil pointer inside a non-nil interface — the exact
		// value this asserts is absent. Returning pusher.NewNotifier's
		// (*Notifier, error) straight through produced one, and a caller's
		// `if n != nil` accepted it and panicked on the first publish.
		actual, err := cfg.NewAsyncNotifier(t.Context(), WithMetricsProvider(mp))
		test.Error(t, err)
		test.True(t, actual == nil)
	})
}

func TestNewAsyncNotifierFromConfig(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderNoop,
		}

		actual, err := NewAsyncNotifier(t.Context(), cfg, nil)
		test.NoError(t, err)
		test.NotNil(t, actual)
	})

	T.Run("with unknown provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: "unknown",
		}

		actual, err := NewAsyncNotifier(t.Context(), cfg, nil)
		test.Nil(t, actual)
		test.Error(t, err)
	})
}

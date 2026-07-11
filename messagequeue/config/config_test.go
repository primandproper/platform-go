package msgconfig

import (
	"testing"

	"github.com/primandproper/platform-go/v4/messagequeue/kafka"
	"github.com/primandproper/platform-go/v4/messagequeue/pubsub"
	"github.com/primandproper/platform-go/v4/messagequeue/sqs"
	loggingnoop "github.com/primandproper/platform-go/v4/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v4/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func Test_cleanString(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.NotEq(t, "", cleanString(t.Name()))
	})
}

func TestQueuesConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("valid", func(t *testing.T) {
		t.Parallel()

		cfg := &QueuesConfig{
			DataChangesTopicName:              "data-changes",
			OutboundEmailsTopicName:           "outbound-emails",
			SearchIndexRequestsTopicName:      "search-index-requests",
			MobileNotificationsTopicName:      "mobile-notifications",
			UserDataAggregationTopicName:      "user-data-aggregation",
			WebhookExecutionRequestsTopicName: "webhook-execution-requests",
		}

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("missing fields", func(t *testing.T) {
		t.Parallel()

		cfg := &QueuesConfig{}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestNewConsumerProvider(T *testing.T) {
	T.Parallel()

	T.Run("with nil config", func(t *testing.T) {
		t.Parallel()

		p, err := NewConsumerProvider(t.Context(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, nil)
		test.Nil(t, p)
		test.ErrorIs(t, err, ErrNilConfig)
	})

	T.Run("with redis provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Consumer: MessageQueueConfig{
				Provider: ProviderRedis,
			},
		}

		p, err := NewConsumerProvider(t.Context(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, cfg)
		test.NoError(t, err)
		test.NotNil(t, p)
	})

	T.Run("with SQS provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Consumer: MessageQueueConfig{
				Provider: ProviderSQS,
				SQS:      sqs.Config{},
			},
		}

		p, err := NewConsumerProvider(t.Context(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, cfg)
		test.NoError(t, err)
		test.NotNil(t, p)
	})

	T.Run("with kafka provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Consumer: MessageQueueConfig{
				Provider: ProviderKafka,
				Kafka:    kafka.Config{Brokers: []string{"localhost:9092"}},
			},
		}

		p, err := NewConsumerProvider(t.Context(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, cfg)
		test.NoError(t, err)
		test.NotNil(t, p)
	})

	T.Run("with pubsub provider and empty project ID", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Consumer: MessageQueueConfig{
				Provider: ProviderPubSub,
				PubSub:   pubsub.Config{},
			},
		}

		p, err := NewConsumerProvider(t.Context(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, cfg)
		test.Nil(t, p)
		test.Error(t, err)
	})

	T.Run("with unknown provider falls back to noop", func(t *testing.T) {
		t.Parallel()

		p, err := NewConsumerProvider(t.Context(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, &Config{})
		test.NoError(t, err)
		test.NotNil(t, p)
	})
}

// TestNewConsumerProvider_PubSubEmulator covers the pubsub success branch.
// It must not run in parallel because it relies on PUBSUB_EMULATOR_HOST.
func TestNewConsumerProvider_PubSubEmulator(t *testing.T) {
	t.Setenv("PUBSUB_EMULATOR_HOST", "127.0.0.1:0")

	cfg := &Config{
		Consumer: MessageQueueConfig{
			Provider: ProviderPubSub,
			PubSub:   pubsub.Config{ProjectID: "test-project"},
		},
	}

	p, err := NewConsumerProvider(t.Context(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, cfg)
	must.NoError(t, err)
	test.NotNil(t, p)
}

func TestNewPublisherProvider(T *testing.T) {
	T.Parallel()

	T.Run("with nil config", func(t *testing.T) {
		t.Parallel()

		p, err := NewPublisherProvider(t.Context(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, nil)
		test.Nil(t, p)
		test.ErrorIs(t, err, ErrNilConfig)
	})

	T.Run("with redis provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Publisher: MessageQueueConfig{
				Provider: ProviderRedis,
			},
		}

		p, err := NewPublisherProvider(t.Context(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, cfg)
		test.NoError(t, err)
		test.NotNil(t, p)
	})

	T.Run("with SQS provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Publisher: MessageQueueConfig{
				Provider: ProviderSQS,
				SQS:      sqs.Config{},
			},
		}

		p, err := NewPublisherProvider(t.Context(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, cfg)
		test.NoError(t, err)
		test.NotNil(t, p)
	})

	T.Run("with kafka provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Publisher: MessageQueueConfig{
				Provider: ProviderKafka,
				Kafka:    kafka.Config{Brokers: []string{"localhost:9092"}},
			},
		}

		p, err := NewPublisherProvider(t.Context(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, cfg)
		test.NoError(t, err)
		test.NotNil(t, p)
	})

	T.Run("with pubsub provider and empty project ID", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Publisher: MessageQueueConfig{
				Provider: ProviderPubSub,
				PubSub:   pubsub.Config{},
			},
		}

		p, err := NewPublisherProvider(t.Context(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, cfg)
		test.Nil(t, p)
		test.Error(t, err)
	})

	T.Run("with unknown provider falls back to noop", func(t *testing.T) {
		t.Parallel()

		p, err := NewPublisherProvider(t.Context(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, &Config{})
		test.NoError(t, err)
		test.NotNil(t, p)
	})
}

// TestNewPublisherProvider_PubSubEmulator covers the pubsub success branch.
// It must not run in parallel because it relies on PUBSUB_EMULATOR_HOST.
func TestNewPublisherProvider_PubSubEmulator(t *testing.T) {
	t.Setenv("PUBSUB_EMULATOR_HOST", "127.0.0.1:0")

	cfg := &Config{
		Publisher: MessageQueueConfig{
			Provider: ProviderPubSub,
			PubSub:   pubsub.Config{ProjectID: "test-project"},
		},
	}

	p, err := NewPublisherProvider(t.Context(), loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, cfg)
	must.NoError(t, err)
	test.NotNil(t, p)
}

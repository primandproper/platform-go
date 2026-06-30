package msgconfig

import (
	"context"
	"strings"

	"github.com/primandproper/platform-go/v2/errors"
	"github.com/primandproper/platform-go/v2/messagequeue"
	"github.com/primandproper/platform-go/v2/messagequeue/kafka"
	"github.com/primandproper/platform-go/v2/messagequeue/noop"
	"github.com/primandproper/platform-go/v2/messagequeue/pubsub"
	"github.com/primandproper/platform-go/v2/messagequeue/redis"
	"github.com/primandproper/platform-go/v2/messagequeue/sqs"
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	"github.com/primandproper/platform-go/v2/observability/tracing"

	ps "cloud.google.com/go/pubsub/v2"
	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderRedis is used to refer to redis.
	ProviderRedis provider = "redis"
	// ProviderSQS is used to refer to sqs.
	ProviderSQS provider = "sqs"
	// ProviderPubSub is used to refer to GCP Pub/Sub.
	ProviderPubSub provider = "pubsub"
	// ProviderKafka is used to refer to Kafka.
	ProviderKafka provider = "kafka"
)

var (
	ErrNilConfig = errors.New("nil config provided")
)

type (
	// provider is used to indicate what messaging provider we'll use.
	provider string

	// MessageQueueConfig is used to indicate how the messaging provider should be configured.
	MessageQueueConfig struct {
		_        struct{}      `json:"-"`
		Kafka    kafka.Config  `envPrefix:"KAFKA_"  json:"kafka"`
		Provider provider      `env:"PROVIDER"      json:"provider,omitempty"`
		SQS      sqs.Config    `envPrefix:"SQS_"    json:"sqs"`
		PubSub   pubsub.Config `envPrefix:"PUBSUB_" json:"pubSub"`
		Redis    redis.Config  `envPrefix:"REDIS_"  json:"redis"`
	}

	// Config is used to indicate how the messaging provider should be configured.
	Config struct {
		_ struct{} `json:"-"`

		Consumer  MessageQueueConfig `envPrefix:"CONSUMER_"  json:"consumer"`
		Publisher MessageQueueConfig `envPrefix:"PUBLISHER_" json:"publisher"`
	}

	// QueuesConfig contains the various queue names.
	QueuesConfig struct {
		_ struct{} `json:"-"`

		DataChangesTopicName              string `env:"DATA_CHANGES_TOPIC_NAME"               json:"dataChangesTopicName"`
		OutboundEmailsTopicName           string `env:"OUTBOUND_EMAILS_TOPIC_NAME"            json:"outboundEmailsTopicName"`
		SearchIndexRequestsTopicName      string `env:"SEARCH_INDEX_REQUESTS_TOPIC_NAME"      json:"searchIndexRequestsTopicName"`
		MobileNotificationsTopicName      string `env:"MOBILE_NOTIFICATIONS_TOPIC_NAME"       json:"mobileNotificationsTopicName"`
		UserDataAggregationTopicName      string `env:"USER_DATA_AGGREGATION_TOPIC_NAME"      json:"userDataAggregationTopicName"`
		WebhookExecutionRequestsTopicName string `env:"WEBHOOK_EXECUTION_REQUESTS_TOPIC_NAME" json:"webhookExecutionRequestsTopicName"`
	}
)

var _ validation.ValidatableWithContext = (*QueuesConfig)(nil)

// ValidateWithContext validates a QueuesConfig struct.
func (c *QueuesConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.DataChangesTopicName, validation.Required),
		validation.Field(&c.OutboundEmailsTopicName, validation.Required),
		validation.Field(&c.SearchIndexRequestsTopicName, validation.Required),
		validation.Field(&c.MobileNotificationsTopicName, validation.Required),
		validation.Field(&c.UserDataAggregationTopicName, validation.Required),
		validation.Field(&c.WebhookExecutionRequestsTopicName, validation.Required),
	)
}

func cleanString(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// ProvideConsumerProvider provides a ConsumerProvider.
func ProvideConsumerProvider(ctx context.Context, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, c *Config) (messagequeue.ConsumerProvider, error) {
	if c == nil {
		return nil, ErrNilConfig
	}

	switch cleanString(string(c.Consumer.Provider)) {
	case string(ProviderRedis):
		return redis.ProvideRedisConsumerProvider(logger, tracerProvider, metricsProvider, c.Consumer.Redis), nil
	case string(ProviderSQS):
		return sqs.ProvideSQSConsumerProvider(ctx, logger, tracerProvider, metricsProvider, c.Consumer.SQS)
	case string(ProviderKafka):
		return kafka.ProvideKafkaConsumerProvider(logger, tracerProvider, metricsProvider, c.Consumer.Kafka), nil
	case string(ProviderPubSub):
		client, err := ps.NewClientWithConfig(ctx, c.Consumer.PubSub.ProjectID, &ps.ClientConfig{
			EnableOpenTelemetryTracing: true,
		})
		if err != nil {
			return nil, errors.Wrap(err, "establishing PubSub client")
		}

		return pubsub.ProvidePubSubConsumerProvider(logger, tracerProvider, metricsProvider, client), nil
	default:
		logger.Info("Using noop consumer provider")
		return noop.NewConsumerProvider(), nil
	}
}

// ProvidePublisherProvider provides a PublisherProvider.
func ProvidePublisherProvider(ctx context.Context, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider, c *Config) (messagequeue.PublisherProvider, error) {
	if c == nil {
		return nil, ErrNilConfig
	}

	switch cleanString(string(c.Publisher.Provider)) {
	case string(ProviderRedis):
		return redis.ProvideRedisPublisherProvider(logger, tracerProvider, metricsProvider, c.Publisher.Redis), nil
	case string(ProviderSQS):
		return sqs.ProvideSQSPublisherProvider(ctx, logger, tracerProvider, metricsProvider), nil
	case string(ProviderKafka):
		return kafka.ProvideKafkaPublisherProvider(logger, tracerProvider, metricsProvider, c.Publisher.Kafka), nil
	case string(ProviderPubSub):
		client, err := ps.NewClientWithConfig(ctx, c.Publisher.PubSub.ProjectID, &ps.ClientConfig{
			EnableOpenTelemetryTracing: true,
		})
		if err != nil {
			return nil, errors.Wrap(err, "establishing PubSub client")
		}

		return pubsub.ProvidePubSubPublisherProvider(logger, tracerProvider, metricsProvider, client, c.Publisher.PubSub.ProjectID), nil
	default:
		logger.Info("Using noop publisher provider")
		return noop.NewPublisherProvider(), nil
	}
}

// Package messagequeuecfg selects and builds messagequeue publisher and consumer
// providers from configuration, over Redis, SQS, GCP Pub/Sub, Kafka, or noop.
//
// The publishing and the consuming halves are configured independently — Config
// holds a MessageQueueConfig for each, with its own provider and credentials —
// so a service that reads from Kafka and writes to SQS is expressible, and so a
// process that only publishes never has to name a consumer it will not build.
package messagequeuecfg

import (
	"context"
	"slices"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/cfgnorm"
	"github.com/primandproper/platform-go/v13/messagequeue"
	"github.com/primandproper/platform-go/v13/messagequeue/kafka"
	"github.com/primandproper/platform-go/v13/messagequeue/noop"
	"github.com/primandproper/platform-go/v13/messagequeue/pubsub"
	"github.com/primandproper/platform-go/v13/messagequeue/redis"
	"github.com/primandproper/platform-go/v13/messagequeue/sqs"

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
	// ProviderNoop discards published messages and consumes nothing. It must be
	// selected deliberately — it is never what an unrecognized provider falls
	// back to.
	ProviderNoop provider = "noop"
)

// providers are every provider this package implements. Validation and both
// factories read it.
var providers = []string{
	string(ProviderRedis),
	string(ProviderSQS),
	string(ProviderPubSub),
	string(ProviderKafka),
	string(ProviderNoop),
}

var (
	ErrNilConfig = errors.New("nil config provided")
)

type (
	// provider is used to indicate what messaging provider we'll use.
	provider string

	// MessageQueueConfig is used to indicate how the messaging provider should be configured.
	MessageQueueConfig struct {
		_        struct{}      `json:"-"            yaml:"-"`
		Kafka    kafka.Config  `envPrefix:"KAFKA_"  json:"kafka,omitzero"     yaml:"kafka,omitempty"`
		Provider provider      `env:"PROVIDER"      json:"provider,omitempty" yaml:"provider,omitempty"`
		SQS      sqs.Config    `envPrefix:"SQS_"    json:"sqs,omitzero"       yaml:"sqs,omitempty"`
		PubSub   pubsub.Config `envPrefix:"PUBSUB_" json:"pubSub,omitzero"    yaml:"pubSub,omitempty"`
		Redis    redis.Config  `envPrefix:"REDIS_"  json:"redis,omitzero"     yaml:"redis,omitempty"`
	}

	// Config is used to indicate how the messaging provider should be configured.
	Config struct {
		_ struct{} `json:"-" yaml:"-"`

		Consumer  MessageQueueConfig `envPrefix:"CONSUMER_"  json:"consumer,omitzero"  yaml:"consumer,omitempty"`
		Publisher MessageQueueConfig `envPrefix:"PUBLISHER_" json:"publisher,omitzero" yaml:"publisher,omitempty"`
	}
)

var (
	_ validation.ValidatableWithContext = (*Config)(nil)
	_ validation.ValidatableWithContext = (*MessageQueueConfig)(nil)
)

// ValidateWithContext validates a MessageQueueConfig struct.
//
// The selected provider's own block is validated and the others are skipped.
// Naming the provider used to be the whole of it, which left every leaf
// provider's rules unreachable: a redis queue with no addresses, a kafka one
// with no brokers and a pubsub one with no project all validated clean and
// then failed — or, for redis, did not fail, and returned a provider holding a
// nil client.
//
// The sub-configs here are values rather than pointers, and each is validated
// through an explicit validation.By rather than left to ozzo. ValidateStruct
// dereferences the field pointer it is handed before looking for a Validatable,
// so `validation.Field(&c.Kafka)` offers ozzo a kafka.Config value — and every
// ValidateWithContext in this module has a pointer receiver, which a value does
// not satisfy. Naming the field was therefore indistinguishable from not naming
// it. The pointer sub-configs elsewhere in this module do not have the problem,
// because dereferencing a **Config leaves a *Config.
func (c *MessageQueueConfig) ValidateWithContext(ctx context.Context) error {
	provider := cfgnorm.Provider(string(c.Provider))

	// selected returns a rule that validates sub, for the one provider that
	// names it, and does nothing for the rest.
	selected := func(name string, sub validation.ValidatableWithContext) validation.Rule {
		return validation.By(func(any) error {
			if provider != name {
				return nil
			}

			return sub.ValidateWithContext(ctx)
		})
	}

	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.Provider, validation.Required, validation.By(func(any) error {
			// Checked normalized, matching dispatch: validating the raw string
			// rejected "Redis" and " kafka " while the factories built them.
			if !slices.Contains(providers, provider) {
				return errors.Wrapf(errors.ErrUnknownProvider, "messagequeue provider %q", c.Provider)
			}

			return nil
		})),
		validation.Field(&c.Redis, selected(string(ProviderRedis), &c.Redis)),
		validation.Field(&c.SQS, selected(string(ProviderSQS), &c.SQS)),
		validation.Field(&c.PubSub, selected(string(ProviderPubSub), &c.PubSub)),
		validation.Field(&c.Kafka, selected(string(ProviderKafka), &c.Kafka)),
	)
}

// ValidateWithContext validates a Config struct.
//
// Both halves are invoked explicitly, for the reason MessageQueueConfig's own
// validation gives: naming a struct-valued field to ozzo is indistinguishable
// from not naming it, because it dereferences the field pointer before looking
// for the pointer-receiver Validatable. A zero Config validated clean.
func (c *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.Consumer, validation.By(func(any) error { return c.Consumer.ValidateWithContext(ctx) })),
		validation.Field(&c.Publisher, validation.By(func(any) error { return c.Publisher.ValidateWithContext(ctx) })),
	)
}

// NewConsumerProvider provides a ConsumerProvider.
//
// Each provider is built into a variable and returned only once its error is
// known to be nil. The provider constructors return their own concrete types,
// so returning one straight through would convert a nil *redis.ConsumerProvider into a
// non-nil messagequeue.ConsumerProvider on the error path, and a caller testing the
// result against nil would find a value that panics on first use.
func NewConsumerProvider(ctx context.Context, c *Config, opts ...Option) (messagequeue.ConsumerProvider, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if c == nil {
		return nil, ErrNilConfig
	}

	provider, err := cfgnorm.SelectProvider(string(c.Consumer.Provider), providers, "messagequeue consumer provider")
	if err != nil {
		return nil, err
	}

	if err = c.Consumer.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating messagequeue consumer config")
	}

	switch provider {
	case string(ProviderRedis):
		p, provErr := redis.NewRedisConsumerProvider(ctx, c.Consumer.Redis, redis.WithLogger(logger), redis.WithTracerProvider(tracerProvider), redis.WithMetricsProvider(metricsProvider))
		if provErr != nil {
			return nil, provErr
		}

		return p, nil
	case string(ProviderSQS):
		p, provErr := sqs.NewSQSConsumerProvider(ctx, c.Consumer.SQS, sqs.WithLogger(logger), sqs.WithTracerProvider(tracerProvider), sqs.WithMetricsProvider(metricsProvider))
		if provErr != nil {
			return nil, provErr
		}

		return p, nil
	case string(ProviderKafka):
		return kafka.NewKafkaConsumerProvider(c.Consumer.Kafka, kafka.WithLogger(logger), kafka.WithTracerProvider(tracerProvider), kafka.WithMetricsProvider(metricsProvider)), nil
	case string(ProviderPubSub):
		client, clientErr := ps.NewClientWithConfig(ctx, c.Consumer.PubSub.ProjectID, &ps.ClientConfig{
			EnableOpenTelemetryTracing: true,
		})
		if clientErr != nil {
			return nil, errors.Wrap(clientErr, "establishing PubSub client")
		}

		return pubsub.NewPubSubConsumerProvider(client, pubsub.WithLogger(logger), pubsub.WithTracerProvider(tracerProvider), pubsub.WithMetricsProvider(metricsProvider)), nil
	case string(ProviderNoop):
		return noop.NewConsumerProvider(), nil
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "messagequeue consumer provider %q", c.Consumer.Provider)
	}
}

// NewPublisherProvider provides a PublisherProvider.
//
// Each provider is built into a variable and returned only once its error is
// known to be nil. The provider constructors return their own concrete types,
// so returning one straight through would convert a nil *redis.PublisherProvider into a
// non-nil messagequeue.PublisherProvider on the error path, and a caller testing the
// result against nil would find a value that panics on first use.
func NewPublisherProvider(ctx context.Context, c *Config, opts ...Option) (messagequeue.PublisherProvider, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if c == nil {
		return nil, ErrNilConfig
	}

	provider, err := cfgnorm.SelectProvider(string(c.Publisher.Provider), providers, "messagequeue publisher provider")
	if err != nil {
		return nil, err
	}

	if err = c.Publisher.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating messagequeue publisher config")
	}

	switch provider {
	case string(ProviderRedis):
		p, provErr := redis.NewRedisPublisherProvider(ctx, c.Publisher.Redis, redis.WithLogger(logger), redis.WithTracerProvider(tracerProvider), redis.WithMetricsProvider(metricsProvider))
		if provErr != nil {
			return nil, provErr
		}

		return p, nil
	case string(ProviderSQS):
		p, provErr := sqs.NewSQSPublisherProvider(ctx, c.Publisher.SQS, sqs.WithLogger(logger), sqs.WithTracerProvider(tracerProvider), sqs.WithMetricsProvider(metricsProvider))
		if provErr != nil {
			return nil, provErr
		}

		return p, nil
	case string(ProviderKafka):
		return kafka.NewKafkaPublisherProvider(c.Publisher.Kafka, kafka.WithLogger(logger), kafka.WithTracerProvider(tracerProvider), kafka.WithMetricsProvider(metricsProvider)), nil
	case string(ProviderPubSub):
		client, clientErr := ps.NewClientWithConfig(ctx, c.Publisher.PubSub.ProjectID, &ps.ClientConfig{
			EnableOpenTelemetryTracing: true,
		})
		if clientErr != nil {
			return nil, errors.Wrap(clientErr, "establishing PubSub client")
		}

		return pubsub.NewPubSubPublisherProvider(client, c.Publisher.PubSub.ProjectID, pubsub.WithLogger(logger), pubsub.WithTracerProvider(tracerProvider), pubsub.WithMetricsProvider(metricsProvider)), nil
	case string(ProviderNoop):
		return noop.NewPublisherProvider(), nil
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "messagequeue publisher provider %q", c.Publisher.Provider)
	}
}

package pubsub

import (
	"errors"
	"fmt"
	"testing"

	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/messagequeue"
	"github.com/primandproper/platform-go/v13/messagequeue/internal/mqmetrics"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
)

func TestBuildPubSubPublisher(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		publisher, err := buildPubSubPublisher(loggingnoop.NewLogger(), nil, tracingnoop.NewTracerProvider(), nil, "test-topic")
		must.NoError(t, err)
		must.NotNil(t, publisher)
	})

	T.Run("returns error when first NewInt64Counter fails", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(name string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				if name == mqmetrics.MessagesPublished {
					return metricnoop.Int64Counter{}, errors.New("forced error")
				}
				t.Fatalf("unexpected NewInt64Counter call: %q", name)
				return nil, nil
			},
		}

		actual, err := buildPubSubPublisher(loggingnoop.NewLogger(), nil, tracingnoop.NewTracerProvider(), mp, "t")
		test.Error(t, err)
		test.Nil(t, actual)
		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("returns error when second NewInt64Counter fails", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(name string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				switch name {
				case mqmetrics.MessagesPublished:
					return metricnoop.Int64Counter{}, nil
				case mqmetrics.PublishErrors:
					return metricnoop.Int64Counter{}, errors.New("forced error")
				}
				t.Fatalf("unexpected NewInt64Counter call: %q", name)
				return nil, nil
			},
		}

		actual, err := buildPubSubPublisher(loggingnoop.NewLogger(), nil, tracingnoop.NewTracerProvider(), mp, "t")
		test.Error(t, err)
		test.Nil(t, actual)
		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
	})

	T.Run("returns error when NewFloat64Histogram fails", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(string, ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metricnoop.Int64Counter{}, nil
			},
			NewFloat64HistogramFunc: func(string, ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
				return metricnoop.Float64Histogram{}, errors.New("forced error")
			},
		}

		actual, err := buildPubSubPublisher(loggingnoop.NewLogger(), nil, tracingnoop.NewTracerProvider(), mp, "t")
		test.Error(t, err)
		test.Nil(t, actual)
		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
		test.SliceLen(t, 1, mp.NewFloat64HistogramCalls())
	})
}

func TestNewPubSubPublisherProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		provider := NewPubSubPublisherProvider(nil, "test-project")
		must.NotNil(t, provider)
	})
}

func TestPublisherProvider_Ping(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		p := &PublisherProvider{}
		test.NoError(t, p.Ping(t.Context()))
	})
}

func TestPublisherProvider_qualifyTopicName(T *testing.T) {
	T.Parallel()

	T.Run("already qualified", func(t *testing.T) {
		t.Parallel()

		p := &PublisherProvider{projectID: "my-project"}
		result := p.qualifyTopicName("projects/my-project/topics/my-topic")
		test.EqOp(t, "projects/my-project/topics/my-topic", result)
	})

	T.Run("unqualified", func(t *testing.T) {
		t.Parallel()

		p := &PublisherProvider{projectID: "my-project"}
		result := p.qualifyTopicName("my-topic")
		test.EqOp(t, "projects/my-project/topics/my-topic", result)
	})
}

func TestPublisherProvider_NewPublisher(T *testing.T) {
	T.Parallel()

	T.Run("with empty topic", func(t *testing.T) {
		t.Parallel()

		provider := NewPubSubPublisherProvider(nil, "test-project")

		pub, err := provider.NewPublisher(t.Context(), "")
		test.Nil(t, pub)
		test.ErrorIs(t, err, messagequeue.ErrEmptyTopicName)
	})
}

// TestPubSubPublisher_Container holds the publisher subtests that need a real
// emulator container to drive Publish end to end so we can assert the data it
// observes. It boots its own container (the shared one in TestPubSub_Container
// lives in another file); runWithContainerBackedPubSub owns its lifecycle.
func TestPubSubPublisher_Container(T *testing.T) {
	T.Parallel()

	runWithContainerBackedPubSub(T, func(infra *pubsubTestInfra) {
		T.Run("observes topic and length on publish", func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			topicName := infra.newTopic(t)

			provider := NewPubSubPublisherProvider(infra.client, infra.projectID)
			must.NotNil(t, provider)

			publisher, err := provider.NewPublisher(ctx, topicName)
			must.NoError(t, err)
			must.NotNil(t, publisher)

			// Seeded as buildPubSubPublisher seeds it: the topic is stated once at
			// construction, not at each Publish.
			obs := observability.NewRecordingObserverWithValues(map[string]any{keys.TopicKey: topicName})
			publisher.(*pubSubPublisher).o11y = obs

			inputData := &struct {
				Name string `json:"name"`
			}{
				Name: t.Name(),
			}

			test.NoError(t, publisher.Publish(ctx, inputData))

			op := obs.ObservedOperationWithData(t, map[string]any{
				keys.TopicKey: topicName,
			})
			op.Observed(t, observability.ObservedKeyFunc(keys.LengthKey, func(v any) bool {
				n, ok := v.(int)
				return ok && n > 0
			}))
			test.SliceEmpty(t, op.Errors)
		})

		// The client refuses a message carrying an ordering key unless the
		// publisher was built with EnableMessageOrdering, and it refuses it
		// locally, so this fails at Publish rather than at the server if the
		// provider ever stops setting it.
		T.Run("publishes with an ordering key", func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			topicName := infra.newTopic(t)

			provider := NewPubSubPublisherProvider(infra.client, infra.projectID)
			must.NotNil(t, provider)

			publisher, err := provider.NewPublisher(ctx, topicName)
			must.NoError(t, err)
			must.NotNil(t, publisher)

			inputData := &struct {
				Name string `json:"name"`
			}{
				Name: t.Name(),
			}

			for range 3 {
				test.NoError(t, publisher.Publish(ctx, inputData, messagequeue.WithOrderingKey("account_123")))
			}
		})

		// A failed publish on an ordering key pauses that key inside the client:
		// every later message for it is refused until ResumePublish. Publish
		// resumes the key itself, so a transient failure does not leave the key
		// permanently dead.
		T.Run("recovers an ordering key from a failed publish", func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()

			// Deliberately not created yet, so the first publish fails.
			topicName := fmt.Sprintf("projects/%s/topics/topic-%s", infra.projectID, identifiers.New())

			provider := NewPubSubPublisherProvider(infra.client, infra.projectID)
			must.NotNil(t, provider)

			publisher, err := provider.NewPublisher(ctx, topicName)
			must.NoError(t, err)

			inputData := map[string]string{"name": t.Name()}

			test.Error(t, publisher.Publish(ctx, inputData, messagequeue.WithOrderingKey("account_123")))

			created, err := infra.client.TopicAdminClient.CreateTopic(ctx, &pubsubpb.Topic{Name: topicName})
			must.NoError(t, err)
			must.NotNil(t, created)

			// Without the resume this is ErrPublishingPaused forever, whatever
			// the topic does.
			test.NoError(t, publisher.Publish(ctx, inputData, messagequeue.WithOrderingKey("account_123")))
		})

		T.Run("publishes with a deduplication key it ignores", func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			topicName := infra.newTopic(t)

			provider := NewPubSubPublisherProvider(infra.client, infra.projectID)
			must.NotNil(t, provider)

			publisher, err := provider.NewPublisher(ctx, topicName)
			must.NoError(t, err)

			test.NoError(t, publisher.Publish(ctx, map[string]string{"name": t.Name()}, messagequeue.WithDeduplicationKey("event_456")))
		})
	})
}

package pubsub

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/messagequeue"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"
	"github.com/primandproper/platform-go/v13/random"
	"github.com/primandproper/platform-go/v13/testutils/containers"

	"cloud.google.com/go/pubsub/v2"
	"cloud.google.com/go/pubsub/v2/apiv1/pubsubpb"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	tcpubsub "github.com/testcontainers/testcontainers-go/modules/gcloud/pubsub"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"google.golang.org/api/option"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const pubsubEmulatorImage = "gcr.io/google.com/cloudsdktool/cloud-sdk:emulators"

type pubsubTestInfra struct {
	client    *pubsub.Client
	projectID string
}

// runWithContainerBackedPubSub boots a single Pub/Sub emulator container and
// hands the suite a client + project ID that all of its subtests share. Subtests
// call (*pubsubTestInfra).newTopic for a unique topic + subscription inside that
// shared project. containers.Run owns the container, so it outlives the parallel
// subtests the closure registers.
func runWithContainerBackedPubSub(tb testing.TB, fn func(infra *pubsubTestInfra)) {
	tb.Helper()

	randomID, err := random.GenerateHexEncodedString(tb.Context(), 8)
	must.NoError(tb, err)
	projectID := "project-" + randomID

	containers.Run(tb,
		func(ctx context.Context) (*tcpubsub.Container, error) {
			return tcpubsub.Run(ctx, pubsubEmulatorImage, tcpubsub.WithProjectID(projectID))
		},
		func(ctx context.Context, container *tcpubsub.Container) {
			conn, connErr := grpc.NewClient(container.URI(), grpc.WithTransportCredentials(insecure.NewCredentials()))
			must.NoError(tb, connErr)
			must.NotNil(tb, conn)
			tb.Cleanup(func() { _ = conn.Close() })

			client, clientErr := pubsub.NewClient(ctx, projectID, option.WithGRPCConn(conn))
			must.NoError(tb, clientErr)
			must.NotNil(tb, client)
			tb.Cleanup(func() { _ = client.Close() })

			fn(&pubsubTestInfra{client: client, projectID: projectID})
		},
	)
}

// newTopic creates a fresh topic + subscription with a unique name inside the
// shared project and returns the fully qualified topic name. The subscription
// name is derived via subscriptionNameForTopic so that consumer.Consume can
// resolve it without extra plumbing.
func (i *pubsubTestInfra) newTopic(t *testing.T) string {
	t.Helper()

	ctx := t.Context()

	topicName := fmt.Sprintf("projects/%s/topics/topic-%s", i.projectID, identifiers.New())

	pubSubTopic, err := i.client.TopicAdminClient.CreateTopic(ctx, &pubsubpb.Topic{Name: topicName})
	must.NoError(t, err)
	must.NotNil(t, pubSubTopic)

	subscription, err := i.client.SubscriptionAdminClient.CreateSubscription(ctx, &pubsubpb.Subscription{
		Name:  subscriptionNameForTopic(i.projectID, pubSubTopic.GetName()),
		Topic: pubSubTopic.GetName(),
	})
	must.NoError(t, err)
	must.NotNil(t, subscription)

	return pubSubTopic.GetName()
}

func TestSubscriptionNameForTopic(T *testing.T) {
	T.Parallel()

	T.Run("fully qualified topic", func(t *testing.T) {
		t.Parallel()

		result := subscriptionNameForTopic("my-project", "projects/my-project/topics/my-topic")
		test.EqOp(t, "projects/my-project/subscriptions/my-topic", result)
	})

	T.Run("short topic name is qualified with the project", func(t *testing.T) {
		t.Parallel()

		result := subscriptionNameForTopic("my-project", "my-topic")
		test.EqOp(t, "projects/my-project/subscriptions/my-topic", result)
	})
}

func TestPubSubConsumer_Consume_nilErrorChannel(T *testing.T) {
	T.Parallel()

	T.Run("subscription lookup failure with nil errs channel does not hang", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()

		// A lazy client pointed at an unroutable address; the cancelled context
		// below makes GetSubscription fail before any dial occurs.
		conn, err := grpc.NewClient("localhost:0", grpc.WithTransportCredentials(insecure.NewCredentials()))
		must.NoError(t, err)
		t.Cleanup(func() { _ = conn.Close() })

		client, err := pubsub.NewClient(ctx, "test-project", option.WithGRPCConn(conn))
		must.NoError(t, err)
		t.Cleanup(func() { _ = client.Close() })

		consumer, err := buildPubSubConsumer(
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			nil,
			client,
			"some-topic",
			func(context.Context, []byte) error { return nil },
		)
		must.NoError(t, err)

		cancelledCtx, cancel := context.WithCancel(ctx)
		cancel()

		done := make(chan struct{})
		go func() {
			consumer.Consume(cancelledCtx, nil)
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Consume hung on subscription failure with a nil errs channel")
		}
	})
}

func TestBuildPubSubConsumer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		handler := func(_ context.Context, _ []byte) error { return nil }

		consumer, err := buildPubSubConsumer(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, nil, "test-topic", handler)
		must.NoError(t, err)
		must.NotNil(t, consumer)
	})

	T.Run("returns error when NewInt64Counter fails", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(string, ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metricnoop.Int64Counter{}, errors.New("forced error")
			},
		}

		actual, err := buildPubSubConsumer(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), mp, nil, "t", nil)
		test.Error(t, err)
		test.Nil(t, actual)
		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})
}

func TestNewPubSubConsumerProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		provider := NewPubSubConsumerProvider(nil)
		must.NotNil(t, provider)
	})
}

func TestPubSubConsumerProvider_NewConsumer(T *testing.T) {
	T.Parallel()

	T.Run("returns error for empty topic", func(t *testing.T) {
		t.Parallel()

		provider := NewPubSubConsumerProvider(nil)

		consumer, err := provider.NewConsumer(t.Context(), "", func(_ context.Context, _ []byte) error { return nil })
		test.Nil(t, consumer)
		test.ErrorIs(t, err, messagequeue.ErrEmptyTopicName)
	})
}

// TestPubSub_Container holds every pubsub subtest that needs a real emulator
// container. They all share one container so we pay the pull/start cost once
// per package run, mirroring the qdrant/pgvector pattern. Each subtest creates
// its own topic + subscription via infra.newTopic to stay isolated.
func TestPubSub_Container(T *testing.T) {
	T.Parallel()

	runWithContainerBackedPubSub(T, func(infra *pubsubTestInfra) {
		T.Run("publisher publishes message", func(t *testing.T) {
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

			test.NoError(t, publisher.Publish(ctx, inputData))
		})

		T.Run("consumer provider caches consumers for same topic", func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			topicName := infra.newTopic(t)

			provider := NewPubSubConsumerProvider(infra.client)

			handler := func(_ context.Context, _ []byte) error { return nil }

			c1, err := provider.NewConsumer(ctx, topicName, handler)
			must.NoError(t, err)
			must.NotNil(t, c1)

			c2, err := provider.NewConsumer(ctx, topicName, handler)
			must.NoError(t, err)
			test.True(t, c1 == c2)
		})

		T.Run("consumer receives published message", func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			topicName := infra.newTopic(t)

			var called atomic.Bool
			handler := func(_ context.Context, _ []byte) error {
				called.Store(true)
				return nil
			}

			provider := NewPubSubConsumerProvider(infra.client)
			consumer, err := provider.NewConsumer(ctx, topicName, handler)
			must.NoError(t, err)

			// Seeded as buildPubSubConsumer seeds it: a consumer is bound to one
			// topic, so the topic is stated once at construction.
			obs := observability.NewRecordingObserverWithValues(map[string]any{keys.TopicKey: topicName})
			consumer.(*pubSubConsumer).o11y = obs

			messageData := []byte(`{"name":"test"}`)

			errChan := make(chan error, 1)
			done := make(chan struct{})
			consumeCtx, stopConsuming := context.WithCancel(ctx)
			defer stopConsuming()
			go func() {
				consumer.Consume(consumeCtx, errChan)
				close(done)
			}()

			// Publish a message.
			publisher := infra.client.Publisher(topicName)
			result := publisher.Publish(ctx, &pubsub.Message{Data: messageData})
			<-result.Ready()
			_, err = result.Get(ctx)
			must.NoError(t, err)

			// Wait for handler to be called.
			deadline := time.Now().Add(10 * time.Second)
			for !called.Load() && time.Now().Before(deadline) {
				time.Sleep(100 * time.Millisecond)
			}
			test.True(t, called.Load())

			stopConsuming()
			// Wait for Consume to return so the background message callback (and its
			// deferred op.End) has completed before reading the recorded observations.
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("timed out waiting for Consume to return after stop signal")
			}

			select {
			case err = <-errChan:
				t.Fatalf("unexpected error: %v", err)
			default:
			}

			op := obs.ObservedOperationWithData(t, map[string]any{
				keys.TopicKey:  topicName,
				keys.LengthKey: len(messageData),
			})
			test.SliceEmpty(t, op.Errors)
		})

		T.Run("consumer handler error is sent to error channel", func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			topicName := infra.newTopic(t)

			expectedErr := fmt.Errorf("handler failure")
			handler := func(_ context.Context, _ []byte) error {
				return expectedErr
			}

			provider := NewPubSubConsumerProvider(infra.client)
			consumer, err := provider.NewConsumer(ctx, topicName, handler)
			must.NoError(t, err)

			// Seeded as buildPubSubConsumer seeds it: a consumer is bound to one
			// topic, so the topic is stated once at construction.
			obs := observability.NewRecordingObserverWithValues(map[string]any{keys.TopicKey: topicName})
			consumer.(*pubSubConsumer).o11y = obs

			messageData := []byte(`{"name":"test"}`)

			errChan := make(chan error, 1)
			done := make(chan struct{})
			consumeCtx, stopConsuming := context.WithCancel(ctx)
			defer stopConsuming()
			go func() {
				consumer.Consume(consumeCtx, errChan)
				close(done)
			}()

			// Publish a message.
			publisher := infra.client.Publisher(topicName)
			result := publisher.Publish(ctx, &pubsub.Message{Data: messageData})
			<-result.Ready()
			_, err = result.Get(ctx)
			must.NoError(t, err)

			// Wait for the error to appear.
			select {
			case receivedErr := <-errChan:
				test.ErrorIs(t, receivedErr, expectedErr)
			case <-time.After(10 * time.Second):
				t.Fatal("timed out waiting for handler error")
			}

			stopConsuming()
			// Wait for Consume to return so the background message callback (and its
			// deferred op.End) has completed before reading the recorded observations.
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("timed out waiting for Consume to return after stop signal")
			}

			op := obs.ObservedOperationWithData(t, map[string]any{
				keys.TopicKey:  topicName,
				keys.LengthKey: len(messageData),
			})
			must.SliceLen(t, 1, op.Errors)
			test.ErrorIs(t, op.Errors[0], expectedErr)
		})

		T.Run("consumer stops when stop channel is signaled", func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			topicName := infra.newTopic(t)

			handler := func(_ context.Context, _ []byte) error { return nil }

			provider := NewPubSubConsumerProvider(infra.client)
			consumer, err := provider.NewConsumer(ctx, topicName, handler)
			must.NoError(t, err)

			errChan := make(chan error, 1)

			done := make(chan struct{})
			consumeCtx, stopConsuming := context.WithCancel(ctx)
			defer stopConsuming()
			go func() {
				consumer.Consume(consumeCtx, errChan)
				close(done)
			}()

			stopConsuming()

			select {
			case <-done:
				// Consume returned, success.
			case <-time.After(10 * time.Second):
				t.Fatal("timed out waiting for Consume to return after stop signal")
			}
		})

		T.Run("consumer with nil stop channel does not panic", func(t *testing.T) {
			t.Parallel()

			ctx := t.Context()
			topicName := infra.newTopic(t)

			var called atomic.Bool
			handler := func(_ context.Context, _ []byte) error {
				called.Store(true)
				return nil
			}

			provider := NewPubSubConsumerProvider(infra.client)
			consumer, err := provider.NewConsumer(ctx, topicName, handler)
			must.NoError(t, err)

			errChan := make(chan error, 1)

			// Pass nil stopChan — should create its own internally.
			done := make(chan struct{})
			go func() {
				consumer.Consume(ctx, errChan)
				close(done)
			}()

			// Publish a message to verify it still works.
			publisher := infra.client.Publisher(topicName)
			result := publisher.Publish(ctx, &pubsub.Message{Data: []byte(`{"name":"test"}`)})
			<-result.Ready()
			_, err = result.Get(ctx)
			must.NoError(t, err)

			deadline := time.Now().Add(10 * time.Second)
			for !called.Load() && time.Now().Before(deadline) {
				time.Sleep(100 * time.Millisecond)
			}
			test.True(t, called.Load())
		})
	})
}

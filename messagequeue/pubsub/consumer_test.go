package pubsub

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/identifiers"
	"github.com/primandproper/platform-go/messagequeue"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	"github.com/primandproper/platform-go/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"
	"github.com/primandproper/platform-go/random"
	"github.com/primandproper/platform-go/testutils/containers"

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
	shutdown  func(context.Context) error
	projectID string
}

// buildPubSubTestInfra boots a single Pub/Sub emulator container and returns a
// client + project ID that can be reused across many subtests. Subtests should
// call (*pubsubTestInfra).newTopic to get a unique topic + subscription within
// the shared project, mirroring the qdrant/pgvector pattern.
func buildPubSubTestInfra(t *testing.T) *pubsubTestInfra {
	t.Helper()

	ctx := t.Context()

	randomID, err := random.GenerateHexEncodedString(ctx, 8)
	must.NoError(t, err)
	projectID := "project-" + randomID

	pubsubContainer, err := containers.StartWithRetry(ctx, func(ctx context.Context) (*tcpubsub.Container, error) {
		return tcpubsub.Run(
			ctx,
			pubsubEmulatorImage,
			tcpubsub.WithProjectID(projectID),
		)
	})
	must.NoError(t, err)
	must.NotNil(t, pubsubContainer)

	conn, err := grpc.NewClient(pubsubContainer.URI(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	must.NoError(t, err)
	must.NotNil(t, conn)

	client, err := pubsub.NewClient(ctx, projectID, option.WithGRPCConn(conn))
	must.NoError(t, err)
	must.NotNil(t, client)

	return &pubsubTestInfra{
		client:    client,
		projectID: projectID,
		shutdown:  func(ctx context.Context) error { return pubsubContainer.Terminate(ctx) },
	}
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
		Name:  subscriptionNameForTopic(pubSubTopic.GetName()),
		Topic: pubSubTopic.GetName(),
	})
	must.NoError(t, err)
	must.NotNil(t, subscription)

	return pubSubTopic.GetName()
}

func TestSubscriptionNameForTopic(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		result := subscriptionNameForTopic("projects/my-project/topics/my-topic")
		test.EqOp(t, "projects/my-project/subscriptions/my-topic", result)
	})

	T.Run("no match", func(t *testing.T) {
		t.Parallel()

		result := subscriptionNameForTopic("some-other-string")
		test.EqOp(t, "some-other-string", result)
	})
}

func TestBuildPubSubConsumer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()
		handler := func(_ context.Context, _ []byte) error { return nil }

		consumer := buildPubSubConsumer(logger, tracingnoop.NewTracerProvider(), nil, nil, "test-topic", handler)
		must.NotNil(t, consumer)
	})

	T.Run("panics when NewInt64Counter fails", func(t *testing.T) {
		t.Parallel()

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(string, ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metricnoop.Int64Counter{}, errors.New("forced error")
			},
		}

		test.Panic(t, func() {
			buildPubSubConsumer(loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), mp, nil, "t", nil)
		})
		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})
}

func TestProvidePubSubConsumerProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()
		provider := ProvidePubSubConsumerProvider(logger, tracingnoop.NewTracerProvider(), nil, nil)
		must.NotNil(t, provider)
	})
}

func TestPubSubConsumerProvider_ProvideConsumer(T *testing.T) {
	T.Parallel()

	T.Run("returns error for empty topic", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()
		provider := ProvidePubSubConsumerProvider(logger, tracingnoop.NewTracerProvider(), nil, nil)

		consumer, err := provider.ProvideConsumer(t.Context(), "", func(_ context.Context, _ []byte) error { return nil })
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

	containers.SkipIfNotRunning(T)

	infra := buildPubSubTestInfra(T)
	T.Cleanup(func() { _ = infra.shutdown(context.Background()) })

	T.Run("publisher publishes message", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		topicName := infra.newTopic(t)

		logger := loggingnoop.NewLogger()
		provider := ProvidePubSubPublisherProvider(logger, tracingnoop.NewTracerProvider(), nil, infra.client, infra.projectID)
		must.NotNil(t, provider)

		publisher, err := provider.ProvidePublisher(ctx, topicName)
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

		logger := loggingnoop.NewLogger()
		provider := ProvidePubSubConsumerProvider(logger, tracingnoop.NewTracerProvider(), nil, infra.client)

		handler := func(_ context.Context, _ []byte) error { return nil }

		c1, err := provider.ProvideConsumer(ctx, topicName, handler)
		must.NoError(t, err)
		must.NotNil(t, c1)

		c2, err := provider.ProvideConsumer(ctx, topicName, handler)
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

		logger := loggingnoop.NewLogger()
		provider := ProvidePubSubConsumerProvider(logger, tracingnoop.NewTracerProvider(), nil, infra.client)
		consumer, err := provider.ProvideConsumer(ctx, topicName, handler)
		must.NoError(t, err)

		obs := observability.NewRecordingObserver()
		consumer.(*pubSubConsumer).o11y = obs

		messageData := []byte(`{"name":"test"}`)

		stopChan := make(chan bool, 1)
		errChan := make(chan error, 1)
		done := make(chan struct{})
		go func() {
			consumer.Consume(ctx, stopChan, errChan)
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

		stopChan <- true
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

		logger := loggingnoop.NewLogger()
		provider := ProvidePubSubConsumerProvider(logger, tracingnoop.NewTracerProvider(), nil, infra.client)
		consumer, err := provider.ProvideConsumer(ctx, topicName, handler)
		must.NoError(t, err)

		obs := observability.NewRecordingObserver()
		consumer.(*pubSubConsumer).o11y = obs

		messageData := []byte(`{"name":"test"}`)

		stopChan := make(chan bool, 1)
		errChan := make(chan error, 1)
		done := make(chan struct{})
		go func() {
			consumer.Consume(ctx, stopChan, errChan)
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

		stopChan <- true
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

		logger := loggingnoop.NewLogger()
		provider := ProvidePubSubConsumerProvider(logger, tracingnoop.NewTracerProvider(), nil, infra.client)
		consumer, err := provider.ProvideConsumer(ctx, topicName, handler)
		must.NoError(t, err)

		stopChan := make(chan bool, 1)
		errChan := make(chan error, 1)

		done := make(chan struct{})
		go func() {
			consumer.Consume(ctx, stopChan, errChan)
			close(done)
		}()

		stopChan <- true

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

		logger := loggingnoop.NewLogger()
		provider := ProvidePubSubConsumerProvider(logger, tracingnoop.NewTracerProvider(), nil, infra.client)
		consumer, err := provider.ProvideConsumer(ctx, topicName, handler)
		must.NoError(t, err)

		errChan := make(chan error, 1)

		// Pass nil stopChan — should create its own internally.
		done := make(chan struct{})
		go func() {
			consumer.Consume(ctx, nil, errChan)
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
}

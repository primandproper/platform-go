package fanout

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v10/errors"
	"github.com/primandproper/platform-go/v10/messagequeue"
	messagequeuemock "github.com/primandproper/platform-go/v10/messagequeue/mock"
	"github.com/primandproper/platform-go/v10/notifications/async"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var errArbitrary = platformerrors.New("arbitrary")

// localNotifier stands in for the sse or websocket notifier holding a replica's
// connections. It records what the consume loop delivered to it.
type localNotifier struct {
	publishErr error
	closeErr   error

	published []delivery
	mu        sync.Mutex
	closes    int
}

type delivery struct {
	event   *async.Event
	channel string
}

func (n *localNotifier) Publish(_ context.Context, channel string, event *async.Event) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.published = append(n.published, delivery{channel: channel, event: event})

	return n.publishErr
}

func (n *localNotifier) Close() error {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.closes++

	return n.closeErr
}

func (n *localNotifier) deliveries() []delivery {
	n.mu.Lock()
	defer n.mu.Unlock()

	return append([]delivery(nil), n.published...)
}

func (n *localNotifier) closeCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()

	return n.closes
}

// acceptingNotifier is a localNotifier that also manages connections, as the
// self-hosted providers do.
type acceptingNotifier struct {
	*localNotifier

	acceptErr error

	accepted []delivery
	mu       sync.Mutex
}

func (n *acceptingNotifier) AcceptConnection(_ http.ResponseWriter, _ *http.Request, channel, memberID string) error {
	n.mu.Lock()
	defer n.mu.Unlock()

	n.accepted = append(n.accepted, delivery{channel: channel, event: &async.Event{Type: memberID}})

	return n.acceptErr
}

// harness is one fully wired Notifier plus the seams to drive it: the handler
// the backplane registered, and whatever it published.
type harness struct {
	notifier *Notifier
	local    *localNotifier

	handler messagequeue.ConsumerFunc

	publisherTop string
	consumerTop  string
	published    []any
	mu           sync.Mutex
	stops        int
}

type harnessOption func(*harnessConfig)

type harnessConfig struct {
	local      async.AsyncNotifier
	cfg        *Config
	publishErr error
}

func withLocal(local async.AsyncNotifier) harnessOption {
	return func(c *harnessConfig) { c.local = local }
}

func withConfig(cfg *Config) harnessOption {
	return func(c *harnessConfig) { c.cfg = cfg }
}

func withPublishErr(err error) harnessOption {
	return func(c *harnessConfig) { c.publishErr = err }
}

func newHarness(t *testing.T, opts ...harnessOption) *harness {
	t.Helper()

	hc := &harnessConfig{cfg: &Config{Enabled: true}}
	for _, opt := range opts {
		opt(hc)
	}

	local, _ := hc.local.(*localNotifier)
	if hc.local == nil {
		local = &localNotifier{}
		hc.local = local
	}

	h := &harness{local: local}

	publisher := &messagequeuemock.PublisherMock{
		PublishFunc: func(_ context.Context, data any) error {
			h.mu.Lock()
			h.published = append(h.published, data)
			h.mu.Unlock()

			return hc.publishErr
		},
		StopFunc: func() {
			h.mu.Lock()
			defer h.mu.Unlock()

			h.stops++
		},
	}

	publisherProvider := &messagequeuemock.PublisherProviderMock{
		NewPublisherFunc: func(_ context.Context, topic string) (messagequeue.Publisher, error) {
			h.mu.Lock()
			h.publisherTop = topic
			h.mu.Unlock()

			return publisher, nil
		},
	}

	consumerProvider := &messagequeuemock.ConsumerProviderMock{
		NewConsumerFunc: func(_ context.Context, topic string, handlerFunc messagequeue.ConsumerFunc) (messagequeue.Consumer, error) {
			h.mu.Lock()
			h.consumerTop = topic
			h.handler = handlerFunc
			h.mu.Unlock()

			return &messagequeuemock.ConsumerMock{
				ConsumeFunc: func(ctx context.Context, _ chan<- error) { <-ctx.Done() },
			}, nil
		},
	}

	n, err := New(t.Context(), hc.cfg, hc.local, publisherProvider, consumerProvider)
	must.NoError(t, err)

	h.notifier = n
	t.Cleanup(func() { _ = n.Close() })

	return h
}

func (h *harness) sent() []any {
	h.mu.Lock()
	defer h.mu.Unlock()

	return append([]any(nil), h.published...)
}

// consume feeds the backplane's own handler whatever it published, which is what
// the broker does for every replica including this one.
func (h *harness) consume(t *testing.T, data any) error {
	t.Helper()

	h.mu.Lock()
	handler := h.handler
	h.mu.Unlock()

	must.NotNil(t, handler)

	payload, err := json.Marshal(data)
	must.NoError(t, err)

	return handler(t.Context(), payload)
}

func TestNew(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		test.NotNil(t, h.notifier)

		h.mu.Lock()
		defer h.mu.Unlock()

		// The publisher and the consumer have to agree on the topic, or a
		// Publish reaches nobody and nothing reports it.
		test.EqOp(t, DefaultTopic, h.publisherTop)
		test.EqOp(t, DefaultTopic, h.consumerTop)
	})

	T.Run("with configured topic", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, withConfig(&Config{Enabled: true, Topic: "custom_topic"}))

		h.mu.Lock()
		defer h.mu.Unlock()

		test.EqOp(t, "custom_topic", h.publisherTop)
		test.EqOp(t, "custom_topic", h.consumerTop)
	})

	T.Run("with nil config", func(t *testing.T) {
		t.Parallel()

		actual, err := New(t.Context(), nil, &localNotifier{}, &messagequeuemock.PublisherProviderMock{}, &messagequeuemock.ConsumerProviderMock{})
		test.Nil(t, actual)
		test.ErrorIs(t, err, ErrNilConfig)
	})

	T.Run("with nil local notifier", func(t *testing.T) {
		t.Parallel()

		actual, err := New(t.Context(), &Config{}, nil, &messagequeuemock.PublisherProviderMock{}, &messagequeuemock.ConsumerProviderMock{})
		test.Nil(t, actual)
		test.ErrorIs(t, err, ErrNilLocalNotifier)
	})

	T.Run("with nil publisher provider", func(t *testing.T) {
		t.Parallel()

		actual, err := New(t.Context(), &Config{}, &localNotifier{}, nil, &messagequeuemock.ConsumerProviderMock{})
		test.Nil(t, actual)
		test.ErrorIs(t, err, ErrNilPublisherProvider)
	})

	T.Run("with nil consumer provider", func(t *testing.T) {
		t.Parallel()

		actual, err := New(t.Context(), &Config{}, &localNotifier{}, &messagequeuemock.PublisherProviderMock{}, nil)
		test.Nil(t, actual)
		test.ErrorIs(t, err, ErrNilConsumerProvider)
	})

	T.Run("with failing publisher provider", func(t *testing.T) {
		t.Parallel()

		publisherProvider := &messagequeuemock.PublisherProviderMock{
			NewPublisherFunc: func(context.Context, string) (messagequeue.Publisher, error) {
				return nil, errArbitrary
			},
		}

		actual, err := New(t.Context(), &Config{}, &localNotifier{}, publisherProvider, &messagequeuemock.ConsumerProviderMock{})
		test.Nil(t, actual)
		test.ErrorIs(t, err, errArbitrary)
	})

	T.Run("with failing consumer provider", func(t *testing.T) {
		t.Parallel()

		stopped := false
		publisherProvider := &messagequeuemock.PublisherProviderMock{
			NewPublisherFunc: func(context.Context, string) (messagequeue.Publisher, error) {
				return &messagequeuemock.PublisherMock{StopFunc: func() { stopped = true }}, nil
			},
		}
		consumerProvider := &messagequeuemock.ConsumerProviderMock{
			NewConsumerFunc: func(context.Context, string, messagequeue.ConsumerFunc) (messagequeue.Consumer, error) {
				return nil, messagequeue.ErrConsumerAlreadyRegistered
			},
		}

		actual, err := New(t.Context(), &Config{}, &localNotifier{}, publisherProvider, consumerProvider)
		test.Nil(t, actual)
		test.ErrorIs(t, err, messagequeue.ErrConsumerAlreadyRegistered)
		// The publisher was already built; a constructor that returns an error
		// has to leave nothing running behind it.
		test.True(t, stopped)
	})
}

func TestNotifier_Publish(T *testing.T) {
	T.Parallel()

	T.Run("enqueues rather than delivering locally", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		event := &async.Event{Type: "thing.created", Data: json.RawMessage(`{"id":"1"}`)}
		must.NoError(t, h.notifier.Publish(t.Context(), "org_1", event))

		sent := h.sent()
		must.SliceLen(t, 1, sent)

		env, ok := sent[0].(*envelope)
		must.True(t, ok)
		test.EqOp(t, "org_1", env.Channel)
		test.EqOp(t, "thing.created", env.Event.Type)

		// The invariant: a Publish that also delivered locally would hand every
		// subscriber on this replica a second copy once the event came back
		// around off the topic.
		test.SliceEmpty(t, h.local.deliveries())
	})

	T.Run("with nil event", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		test.ErrorIs(t, h.notifier.Publish(t.Context(), "org_1", nil), ErrNilEvent)
		test.SliceEmpty(t, h.sent())
	})

	T.Run("with failing publisher", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, withPublishErr(errArbitrary))

		err := h.notifier.Publish(t.Context(), "org_1", &async.Event{Type: "thing.created"})
		test.ErrorIs(t, err, errArbitrary)
	})
}

func TestNotifier_deliver(T *testing.T) {
	T.Parallel()

	T.Run("round trip", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		event := &async.Event{Type: "thing.created", Data: json.RawMessage(`{"id":"1"}`)}
		must.NoError(t, h.notifier.Publish(t.Context(), "org_1", event))

		must.NoError(t, h.consume(t, h.sent()[0]))

		delivered := h.local.deliveries()
		must.SliceLen(t, 1, delivered)
		test.EqOp(t, "org_1", delivered[0].channel)
		test.EqOp(t, "thing.created", delivered[0].event.Type)
		test.EqOp(t, `{"id":"1"}`, string(delivered[0].event.Data))
	})

	T.Run("with undecodable payload", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		h.mu.Lock()
		handler := h.handler
		h.mu.Unlock()

		test.Error(t, handler(t.Context(), []byte("not json")))
		test.SliceEmpty(t, h.local.deliveries())
	})

	T.Run("with no event in the envelope", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		test.ErrorIs(t, h.consume(t, &envelope{Channel: "org_1"}), ErrNilEvent)
		test.SliceEmpty(t, h.local.deliveries())
	})

	T.Run("with failing local notifier", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, withLocal(&localNotifier{publishErr: errArbitrary}))

		err := h.consume(t, &envelope{Channel: "org_1", Event: &async.Event{Type: "thing.created"}})
		test.ErrorIs(t, err, errArbitrary)
	})
}

func TestNotifier_AcceptConnection(T *testing.T) {
	T.Parallel()

	T.Run("delegates to the wrapped notifier", func(t *testing.T) {
		t.Parallel()

		local := &acceptingNotifier{localNotifier: &localNotifier{}}

		h := newHarness(t, withLocal(local))

		must.NoError(t, h.notifier.AcceptConnection(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody), "org_1", "member_1"))

		local.mu.Lock()
		defer local.mu.Unlock()

		must.SliceLen(t, 1, local.accepted)
		test.EqOp(t, "org_1", local.accepted[0].channel)
	})

	T.Run("with a notifier that accepts no connections", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		err := h.notifier.AcceptConnection(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", http.NoBody), "org_1", "member_1")
		test.ErrorIs(t, err, ErrLocalNotConnectionAcceptor)
	})
}

func TestNotifier_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		must.NoError(t, h.notifier.Close())

		h.mu.Lock()
		defer h.mu.Unlock()

		test.EqOp(t, 1, h.stops)
		test.EqOp(t, 1, h.local.closeCount())
	})

	T.Run("is idempotent", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		must.NoError(t, h.notifier.Close())
		must.NoError(t, h.notifier.Close())

		h.mu.Lock()
		defer h.mu.Unlock()

		test.EqOp(t, 1, h.stops)
		test.EqOp(t, 1, h.local.closeCount())
	})

	T.Run("reports the wrapped notifier's error", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t, withLocal(&localNotifier{closeErr: errArbitrary}))

		test.ErrorIs(t, h.notifier.Close(), errArbitrary)
	})
}

// TestNotifier_fleetDelivery is the property the package exists for: an event
// published on one replica reaches the connections held by another, exactly
// once, and reaches the publishing replica's own connections the same way.
func TestNotifier_fleetDelivery(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		replicaA := newHarness(t)
		replicaB := newHarness(t)

		must.NoError(t, replicaA.notifier.Publish(t.Context(), "org_1", &async.Event{Type: "thing.created"}))

		// One topic, so every replica sees the same payload — including the one
		// that published it.
		payload := replicaA.sent()[0]
		must.NoError(t, replicaA.consume(t, payload))
		must.NoError(t, replicaB.consume(t, payload))

		test.SliceLen(t, 1, replicaA.local.deliveries())
		test.SliceLen(t, 1, replicaB.local.deliveries())
	})
}

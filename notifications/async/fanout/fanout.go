package fanout

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"

	platformerrors "github.com/primandproper/platform-go/v10/errors"
	"github.com/primandproper/platform-go/v10/messagequeue"
	"github.com/primandproper/platform-go/v10/notifications/async"
	"github.com/primandproper/platform-go/v10/observability"
	"github.com/primandproper/platform-go/v10/observability/keys"
	"github.com/primandproper/platform-go/v10/observability/logging"
	"github.com/primandproper/platform-go/v10/observability/metrics"
)

const o11yName = "async_notifications_fanout"

// channelKey names the notification channel on spans and log lines.
//
// It is not keys.TopicKey, which the observer already carries for the
// messagequeue topic. Both exist here and they are different things — one topic
// carries every channel — so collapsing them onto one key would make a trace
// claim the backplane published to a topic named after a channel.
const channelKey = "notification.channel"

var (
	_ async.AsyncNotifier      = (*Notifier)(nil)
	_ async.ConnectionAcceptor = (*Notifier)(nil)

	// ErrNilConfig is returned when New is given no config.
	ErrNilConfig = platformerrors.New("nil fanout config provided")

	// ErrNilLocalNotifier is returned when New is given no notifier to deliver
	// consumed events through.
	ErrNilLocalNotifier = platformerrors.New("nil local async notifier provided")

	// ErrNilPublisherProvider is returned when New is given no publisher provider.
	ErrNilPublisherProvider = platformerrors.New("nil messagequeue publisher provider provided")

	// ErrNilConsumerProvider is returned when New is given no consumer provider.
	ErrNilConsumerProvider = platformerrors.New("nil messagequeue consumer provider provided")

	// ErrNilEvent is returned by Publish when handed no event.
	ErrNilEvent = platformerrors.New("nil event provided")

	// ErrLocalNotConnectionAcceptor is returned by AcceptConnection when the
	// wrapped notifier does not manage server-side connections.
	//
	// Fanning out over a hosted provider is a configuration mistake rather than
	// a shape this package supports — the hosted broker already does this job,
	// and every replica consuming the topic would republish to it — but the
	// mistake is not detectable here, because a notifier that accepts no
	// connections is a legitimate thing to wrap in a test.
	ErrLocalNotConnectionAcceptor = platformerrors.New("wrapped notifier does not accept connections")
)

// Notifier is an AsyncNotifier whose Publish enqueues onto a messagequeue topic
// and whose delivery happens on every replica consuming that topic. See the
// package documentation for the shape and the invariant.
//
// It owns two goroutines, started by New and stopped by Close.
type Notifier struct {
	local     async.AsyncNotifier
	acceptor  async.ConnectionAcceptor
	publisher messagequeue.Publisher
	o11y      observability.Observer
	logger    logging.Logger

	cancel context.CancelFunc
	// done closes once the consume loop and the error drain have both returned.
	done chan struct{}

	publishedCounter metrics.Int64Counter
	consumedCounter  metrics.Int64Counter
	deliveredCounter metrics.Int64Counter
	droppedCounter   metrics.Int64Counter

	closeErr  error
	closeOnce sync.Once
}

// New builds a Notifier and starts it.
//
// local is the notifier consumed events are delivered through — the sse or
// websocket notifier holding this replica's connections. Its Publish is never
// reached by a caller's Publish: it is invoked later, from the consume loop, as
// a local delivery sink.
//
// The publisher and consumer are built here from their providers rather than
// taken ready-made, so the topic they agree on cannot drift. A publisher on one
// topic and a consumer on another is the same silent no-delivery failure this
// package exists to remove.
//
// ctx bounds the consume loop and must outlive the notifier: a request-scoped
// context stops delivery the moment its request ends. Close cancels the loop
// and waits for it.
func New(
	ctx context.Context,
	cfg *Config,
	local async.AsyncNotifier,
	publisherProvider messagequeue.PublisherProvider,
	consumerProvider messagequeue.ConsumerProvider,
	opts ...Option,
) (*Notifier, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}
	if local == nil {
		return nil, ErrNilLocalNotifier
	}
	if publisherProvider == nil {
		return nil, ErrNilPublisherProvider
	}
	if consumerProvider == nil {
		return nil, ErrNilConsumerProvider
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating fanout config")
	}

	o := newOptions(opts)

	n := &Notifier{
		local: local,
		done:  make(chan struct{}),
		// A notifier that accepts no connections is still publishable-through;
		// AcceptConnection is what reports the absence, and only if it is called.
		o11y: observability.NewObserverWithValues(o11yName, o.logger, o.tracerProvider, map[string]any{
			keys.TopicKey: cfg.Topic,
		}),
	}
	if acceptor, ok := local.(async.ConnectionAcceptor); ok {
		n.acceptor = acceptor
	}

	n.logger = n.o11y.Logger()

	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

	var err error
	if n.publishedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_events_published", o11yName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating events published counter")
	}
	if n.consumedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_events_consumed", o11yName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating events consumed counter")
	}
	if n.deliveredCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_events_delivered", o11yName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating events delivered counter")
	}
	// Published minus delivered is the number that matters and it spans
	// replicas, so it cannot be read off one process. This counter is what makes
	// an undecodable envelope visible without correlating two other series.
	if n.droppedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_events_dropped", o11yName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating events dropped counter")
	}

	if n.publisher, err = publisherProvider.NewPublisher(ctx, cfg.Topic); err != nil {
		return nil, platformerrors.Wrapf(err, "building fanout publisher for topic %q", cfg.Topic)
	}

	loopCtx, cancel := context.WithCancel(ctx)
	n.cancel = cancel

	consumer, err := consumerProvider.NewConsumer(loopCtx, cfg.Topic, n.deliver)
	if err != nil {
		cancel()
		n.publisher.Stop()

		return nil, platformerrors.Wrapf(err, "building fanout consumer for topic %q", cfg.Topic)
	}

	n.run(loopCtx, consumer)

	return n, nil
}

// run starts the consume loop and the drain that keeps its errors from being
// discarded, and closes done once both have returned.
func (n *Notifier) run(ctx context.Context, consumer messagequeue.Consumer) {
	// Buffered so a handler error raised as the loop is being cancelled does not
	// block the consumer against a drain that has already returned.
	errs := make(chan error, 1)

	var wg sync.WaitGroup

	wg.Add(2)

	go func() {
		defer wg.Done()

		consumer.Consume(ctx, errs)
	}()

	go func() {
		defer wg.Done()

		for {
			select {
			case err := <-errs:
				if err != nil {
					// There is no caller to hand these to. Delivery is
					// at-most-once by design, so a failed event is not retried:
					// the client reconciles on reconnect.
					n.logger.Error("delivering fanned-out notification", err)
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	go func() {
		wg.Wait()
		close(n.done)
	}()
}

// Publish enqueues an event onto the backplane topic. It does not deliver to
// this replica's connections — that happens when this replica consumes what it
// just published, on the same path every other replica takes.
func (n *Notifier) Publish(ctx context.Context, channel string, event *async.Event) error {
	ctx, op := n.o11y.Begin(ctx, observability.WithValue(channelKey, channel))
	defer op.End()

	if event == nil {
		return op.Error(ErrNilEvent, "publishing notification")
	}

	op.SetValues(map[string]any{
		"event.type":   event.Type,
		keys.LengthKey: len(event.Data),
	})

	if err := n.publisher.Publish(ctx, &envelope{Channel: channel, Event: event}); err != nil {
		return op.Error(err, "publishing notification to fanout topic")
	}

	n.publishedCounter.Add(ctx, 1)

	return nil
}

// deliver is the consumer handler: it hands one consumed event to this
// replica's local connections. It is the only place local.Publish is called.
func (n *Notifier) deliver(ctx context.Context, payload []byte) error {
	ctx, op := n.o11y.Begin(ctx, observability.WithValue(keys.LengthKey, len(payload)))
	defer op.End()

	n.consumedCounter.Add(ctx, 1)

	var env envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		n.droppedCounter.Add(ctx, 1)

		return op.Error(err, "decoding fanout envelope")
	}

	op.Set(channelKey, env.Channel)

	if env.Event == nil {
		n.droppedCounter.Add(ctx, 1)

		return op.Error(ErrNilEvent, "decoding fanout envelope")
	}

	if err := n.local.Publish(ctx, env.Channel, env.Event); err != nil {
		n.droppedCounter.Add(ctx, 1)

		return op.Error(err, "delivering notification to local connections")
	}

	n.deliveredCounter.Add(ctx, 1)

	return nil
}

// AcceptConnection registers an inbound client with the wrapped notifier, which
// is what actually holds the connection. The backplane adds nothing here: a
// connection is local by definition, and making it reachable fleet-wide is what
// Publish and deliver are for.
func (n *Notifier) AcceptConnection(w http.ResponseWriter, r *http.Request, channel, memberID string) error {
	if n.acceptor == nil {
		return platformerrors.Wrapf(ErrLocalNotConnectionAcceptor, "%T", n.local)
	}

	return n.acceptor.AcceptConnection(w, r, channel, memberID)
}

// Close stops the consume loop, waits for it and the error drain to exit,
// releases the publisher, and closes the wrapped notifier. Safe to call more
// than once; it returns the wrapped notifier's Close error.
//
// The providers are left alone: they were built by whoever passed them in, and
// they may well be serving something else.
func (n *Notifier) Close() error {
	n.closeOnce.Do(func() {
		n.cancel()
		<-n.done

		n.publisher.Stop()

		n.closeErr = n.local.Close()
	})

	return n.closeErr
}

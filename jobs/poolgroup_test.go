package jobs_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/jobs"
	"github.com/primandproper/platform-go/v13/messagequeue"
	messagequeuemock "github.com/primandproper/platform-go/v13/messagequeue/mock"
	lognoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// errBroker is what the broker refuses a subscription with, so the partial
// start tests can name the failure they expect Start to surface.
var errBroker = errors.New("broker refused the subscription")

// fakeBroker is a ConsumerProvider with one consumer per topic, which is the
// difference that matters for a PoolGroup: fakeQueue has a single handler
// field, so several pools over it would all end up wired to whichever
// subscribed last.
//
// It can also refuse a named topic, which is the only way to reach the partial
// start this type exists for — the pools before it are already consuming when
// the refusal arrives.
type fakeBroker struct {
	topics map[string]*fakeSubscription

	// failOn holds a function rather than an error so a test can arrange for
	// the world to change while the refusal is being decided — which is the
	// only deterministic way to have a message in flight on an earlier pool at
	// the moment a later one fails.
	failOn map[string]func() error

	subscribed []string

	mu sync.Mutex
}

// fakeSubscription is one topic's wire. stopped closes when Consume returns,
// which is how a test observes that a pool was actually drained rather than
// merely asked to stop.
type fakeSubscription struct {
	messages chan []byte
	handler  messagequeue.ConsumerFunc
	stopped  chan struct{}
}

func newFakeBroker() *fakeBroker {
	return &fakeBroker{
		topics: map[string]*fakeSubscription{},
		failOn: map[string]func() error{},
	}
}

// refuse arranges for the subscription to topic to fail, standing in for a
// broker that is briefly unreachable while the process is starting.
func (b *fakeBroker) refuse(topic string, err error) {
	b.refuseWith(topic, func() error { return err })
}

// refuseWith is refuse for a test that has to do something first — the hook
// runs on the goroutine calling Start, with none of the broker's locks held.
func (b *fakeBroker) refuseWith(topic string, fn func() error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.failOn[topic] = fn
}

func (b *fakeBroker) provider() messagequeue.ConsumerProvider {
	return &messagequeuemock.ConsumerProviderMock{
		CloseFunc:       func() {},
		NewConsumerFunc: b.newConsumer,
	}
}

func (b *fakeBroker) newConsumer(_ context.Context, topic string, handler messagequeue.ConsumerFunc) (messagequeue.Consumer, error) {
	b.mu.Lock()
	fail := b.failOn[topic]
	b.mu.Unlock()

	// Called outside the lock, because a hook's whole purpose is to reach back
	// into the broker before it answers.
	if fail != nil {
		if err := fail(); err != nil {
			return nil, err
		}
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	sub := &fakeSubscription{
		messages: make(chan []byte, 16),
		handler:  handler,
		stopped:  make(chan struct{}),
	}
	b.topics[topic] = sub
	b.subscribed = append(b.subscribed, topic)

	return &messagequeuemock.ConsumerMock{
		ConsumeFunc: func(ctx context.Context, errs chan<- error) {
			defer close(sub.stopped)

			for {
				select {
				case <-ctx.Done():
					return
				case msg := <-sub.messages:
					if err := sub.handler(ctx, msg); err != nil {
						select {
						case errs <- err:
						default:
						}
					}
				}
			}
		},
	}, nil
}

func (b *fakeBroker) subscription(t *testing.T, topic string) *fakeSubscription {
	t.Helper()

	b.mu.Lock()
	defer b.mu.Unlock()

	sub, ok := b.topics[topic]
	must.True(t, ok, must.Sprintf("nothing subscribed to topic %q", topic))

	return sub
}

// topicsSubscribed reports the topics the group asked for, in the order it
// asked, so a test can assert that a rejected spec list reached the broker not
// at all.
func (b *fakeBroker) topicsSubscribed() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return append([]string(nil), b.subscribed...)
}

func (b *fakeBroker) publish(t *testing.T, topic, payload string) {
	t.Helper()

	b.subscription(t, topic).messages <- []byte(payload)
}

// send is publish without the assertion, for the one caller that runs off the
// test's goroutine and therefore must not reach for must.
func (b *fakeBroker) send(topic, payload string) {
	b.mu.Lock()
	sub := b.topics[topic]
	b.mu.Unlock()

	if sub != nil {
		sub.messages <- []byte(payload)
	}
}

// awaitStopped asserts that the topic's consumer has returned, which for a
// partially started group is the whole claim: the pools that did come up are no
// longer pulling messages off their topics.
func (b *fakeBroker) awaitStopped(t *testing.T, topic string) {
	t.Helper()

	select {
	case <-b.subscription(t, topic).stopped:
	case <-time.After(waitFor):
		must.Unreachable(t, must.Sprintf("consumer for topic %q is still running", topic))
	}
}

// groupSpec is the spec every happy-path test uses: a named topic whose handler
// forwards what it received onto handled.
func groupSpec(topic string, handled chan<- string) jobs.PoolSpec {
	return jobs.PoolSpec{
		Topic:  topic,
		Config: &jobs.PoolConfig{Concurrency: 1, Retry: fastRetry(1)},
		Handler: func(_ context.Context, payload []byte) error {
			handled <- topic + ":" + string(payload)

			return nil
		},
	}
}

// closeGroup registers a bounded Close, so a test that fails partway through
// does not leave a group consuming for the rest of the run.
func closeGroup(t *testing.T, group *jobs.PoolGroup) {
	t.Helper()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitFor)
		defer cancel()

		_ = group.Close(ctx)
	})
}

func TestNewPoolGroup(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		handled := make(chan string, 2)

		group, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			groupSpec("orders", handled),
			groupSpec("invoices", handled),
		}, newFakeBroker().provider())
		must.NoError(t, err)
		must.NotNil(t, group)

		test.Eq(t, []string{"orders", "invoices"}, group.Topics())
	})

	T.Run("with nil consumer provider", func(t *testing.T) {
		t.Parallel()

		_, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			groupSpec("orders", make(chan string, 1)),
		}, nil)
		test.ErrorIs(t, err, jobs.ErrNilConsumerProvider)
	})

	T.Run("with no specs", func(t *testing.T) {
		t.Parallel()

		_, err := jobs.NewPoolGroup(t.Context(), nil, newFakeBroker().provider())
		test.ErrorIs(t, err, jobs.ErrNoPoolSpecs)
	})

	T.Run("with nil handler", func(t *testing.T) {
		t.Parallel()

		_, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			{Topic: "orders"},
		}, newFakeBroker().provider())
		test.ErrorIs(t, err, jobs.ErrNilHandler)
	})

	T.Run("with duplicate topics", func(t *testing.T) {
		t.Parallel()

		handled := make(chan string, 1)

		_, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			groupSpec("orders", handled),
			groupSpec("orders", handled),
		}, newFakeBroker().provider())
		test.ErrorIs(t, err, jobs.ErrDuplicateTopic)
	})

	T.Run("with no topic anywhere", func(t *testing.T) {
		t.Parallel()

		_, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			{Handler: func(context.Context, []byte) error { return nil }},
		}, newFakeBroker().provider())
		test.Error(t, err)
	})

	T.Run("with an invalid config", func(t *testing.T) {
		t.Parallel()

		broker := newFakeBroker()
		handled := make(chan string, 1)

		bad := groupSpec("invoices", handled)
		bad.Config.HandlerTimeout = -time.Second

		// The first spec is fine, which is the point: a config the group can
		// see is wrong fails construction, so the pool ahead of it is never
		// built and there is no partial start to unwind.
		_, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			groupSpec("orders", handled),
			bad,
		}, broker.provider())
		test.Error(t, err)
		test.SliceEmpty(t, broker.topicsSubscribed())
	})

	T.Run("with a nil config", func(t *testing.T) {
		t.Parallel()

		group, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			{Topic: "orders", Handler: func(context.Context, []byte) error { return nil }},
		}, newFakeBroker().provider())
		must.NoError(t, err)

		test.Eq(t, []string{"orders"}, group.Topics())
	})

	T.Run("with the topic on the config", func(t *testing.T) {
		t.Parallel()

		group, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			{
				Config:  &jobs.PoolConfig{Topic: "orders"},
				Handler: func(context.Context, []byte) error { return nil },
			},
		}, newFakeBroker().provider())
		must.NoError(t, err)

		test.Eq(t, []string{"orders"}, group.Topics())
	})

	T.Run("with the spec topic overriding the config topic", func(t *testing.T) {
		t.Parallel()

		group, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			{
				Topic:   "orders",
				Config:  &jobs.PoolConfig{Topic: "stale"},
				Handler: func(context.Context, []byte) error { return nil },
			},
		}, newFakeBroker().provider())
		must.NoError(t, err)

		test.Eq(t, []string{"orders"}, group.Topics())
	})

	// The failure mode this guards is the one a shared *PoolConfig invites:
	// writing each spec's topic onto the caller's struct would leave every pool
	// consuming whichever topic was assigned last.
	T.Run("with one config shared by every spec", func(t *testing.T) {
		t.Parallel()

		broker := newFakeBroker()
		handled := make(chan string, 2)
		shared := &jobs.PoolConfig{Concurrency: 2, Retry: fastRetry(1)}

		handler := func(topic string) jobs.Handler {
			return func(_ context.Context, payload []byte) error {
				handled <- topic + ":" + string(payload)

				return nil
			}
		}

		group, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			{Topic: "orders", Config: shared, Handler: handler("orders")},
			{Topic: "invoices", Config: shared, Handler: handler("invoices")},
		}, broker.provider())
		must.NoError(t, err)

		test.Eq(t, []string{"orders", "invoices"}, group.Topics())

		// Untouched: neither the topic each pool resolved nor the defaults each
		// one filled in were written back onto what the caller still holds.
		test.EqOp(t, "", shared.Topic)
		test.EqOp(t, 2, shared.Concurrency)

		must.NoError(t, group.Start(t.Context()))
		closeGroup(t, group)

		test.Eq(t, []string{"orders", "invoices"}, broker.topicsSubscribed())

		broker.publish(t, "orders", "o-1")
		test.EqOp(t, "orders:o-1", recv(t, handled, "the orders handler"))

		broker.publish(t, "invoices", "i-1")
		test.EqOp(t, "invoices:i-1", recv(t, handled, "the invoices handler"))
	})
}

func TestPoolGroup_Start(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		broker := newFakeBroker()
		handled := make(chan string, 3)

		group, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			groupSpec("orders", handled),
			groupSpec("invoices", handled),
			groupSpec("emails", handled),
		}, broker.provider())
		must.NoError(t, err)

		must.NoError(t, group.Start(t.Context()))
		closeGroup(t, group)

		test.Eq(t, []string{"orders", "invoices", "emails"}, broker.topicsSubscribed())

		broker.publish(t, "emails", "e-1")
		test.EqOp(t, "emails:e-1", recv(t, handled, "the emails handler"))
	})

	// The whole point of the type: the pools that came up before the failure
	// are drained rather than left consuming a process that is about to exit.
	T.Run("with a later pool failing to build", func(t *testing.T) {
		t.Parallel()

		broker := newFakeBroker()
		broker.refuse("emails", errBroker)

		handled := make(chan string, 3)

		group, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			groupSpec("orders", handled),
			groupSpec("invoices", handled),
			groupSpec("emails", handled),
		}, broker.provider())
		must.NoError(t, err)

		err = group.Start(t.Context())
		test.ErrorIs(t, err, errBroker)
		test.StrContains(t, err.Error(), "emails")

		// Start returns only once the drain has finished, so these are already
		// closed rather than about to be.
		broker.awaitStopped(t, "orders")
		broker.awaitStopped(t, "invoices")
	})

	T.Run("with the first pool failing to build", func(t *testing.T) {
		t.Parallel()

		broker := newFakeBroker()
		broker.refuse("orders", errBroker)

		handled := make(chan string, 2)

		group, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			groupSpec("orders", handled),
			groupSpec("invoices", handled),
		}, broker.provider())
		must.NoError(t, err)

		test.ErrorIs(t, group.Start(t.Context()), errBroker)

		// Nothing came up, so nothing was drained — and, more to the point, the
		// spec after the failure was never reached.
		test.SliceEmpty(t, broker.topicsSubscribed())
	})

	// A misconfigured meter fails NewPool rather than the first message, which
	// makes it the other way a start goes partial.
	T.Run("with an instrument that will not build", func(t *testing.T) {
		t.Parallel()

		broker := newFakeBroker()
		handled := make(chan string, 2)

		group, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			groupSpec("orders", handled),
			{
				Topic:   "invoices",
				Config:  &jobs.PoolConfig{Concurrency: 1, Retry: fastRetry(1)},
				Handler: func(context.Context, []byte) error { return nil },
				Options: []jobs.PoolOption{
					jobs.WithPoolMetricsProvider(failingInstruments("jobs_pool_in_flight")),
				},
			},
		}, broker.provider())
		must.NoError(t, err)

		test.ErrorIs(t, group.Start(t.Context()), errInstrument)

		broker.awaitStopped(t, "orders")
	})

	// The drain runs on a context stripped of the caller's cancellation, so a
	// start that failed because its context was canceled still drains rather
	// than giving up immediately on an already-expired deadline.
	T.Run("with the start context already canceled", func(t *testing.T) {
		t.Parallel()

		broker := newFakeBroker()
		broker.refuse("invoices", errBroker)

		handled := make(chan string, 2)

		group, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			groupSpec("orders", handled),
			groupSpec("invoices", handled),
		}, broker.provider())
		must.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		test.ErrorIs(t, group.Start(ctx), errBroker)

		broker.awaitStopped(t, "orders")
	})

	// A handler that will not finish must not hold the error path open forever.
	// The drain is bounded precisely because the process is on its way out.
	T.Run("with a partial start whose drain times out", func(t *testing.T) {
		t.Parallel()

		broker := newFakeBroker()

		entered := make(chan struct{}, 1)
		release := make(chan struct{})

		t.Cleanup(func() { close(release) })

		// The wedge has to be in place at the moment the second subscription is
		// refused, which is why the refusal is what puts it there: it publishes
		// to the pool that is already running and waits for that pool's handler
		// to block before failing. Anything else races the start.
		broker.refuseWith("invoices", func() error {
			broker.send("orders", "o-1")

			select {
			case <-entered:
			case <-time.After(waitFor):
			}

			return errBroker
		})

		group, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			{
				Topic:  "orders",
				Config: &jobs.PoolConfig{Concurrency: 1, Retry: fastRetry(1)},
				Handler: func(context.Context, []byte) error {
					entered <- struct{}{}
					<-release

					return nil
				},
			},
			groupSpec("invoices", make(chan string, 1)),
		}, broker.provider(), jobs.WithPoolGroupDrainTimeout(50*time.Millisecond))
		must.NoError(t, err)

		started := make(chan error, 1)
		go func() { started <- group.Start(t.Context()) }()

		// Without the bound this never arrives: the orders handler is blocked
		// until the cleanup below, and the drain would wait for it.
		select {
		case startErr := <-started:
			test.ErrorIs(t, startErr, errBroker)
		case <-time.After(2 * time.Second):
			must.Unreachable(t, must.Sprintf("Start waited past the drain timeout"))
		}
	})

	T.Run("called twice", func(t *testing.T) {
		t.Parallel()

		handled := make(chan string, 1)

		group, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			groupSpec("orders", handled),
		}, newFakeBroker().provider())
		must.NoError(t, err)

		must.NoError(t, group.Start(t.Context()))
		closeGroup(t, group)

		test.ErrorIs(t, group.Start(t.Context()), jobs.ErrPoolGroupStarted)
	})

	T.Run("after a failed start", func(t *testing.T) {
		t.Parallel()

		broker := newFakeBroker()
		broker.refuse("orders", errBroker)

		group, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			groupSpec("orders", make(chan string, 1)),
		}, broker.provider())
		must.NoError(t, err)

		test.ErrorIs(t, group.Start(t.Context()), errBroker)

		// A group that failed to start is spent too. Retrying it would rebuild
		// pools whose stop channels the drain already closed.
		test.ErrorIs(t, group.Start(t.Context()), jobs.ErrPoolGroupStarted)
	})

	T.Run("after close", func(t *testing.T) {
		t.Parallel()

		group, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			groupSpec("orders", make(chan string, 1)),
		}, newFakeBroker().provider())
		must.NoError(t, err)

		must.NoError(t, group.Close(t.Context()))
		test.ErrorIs(t, group.Start(t.Context()), jobs.ErrPoolGroupStarted)
	})
}

func TestPoolGroup_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		broker := newFakeBroker()
		handled := make(chan string, 2)

		group, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			groupSpec("orders", handled),
			groupSpec("invoices", handled),
		}, broker.provider())
		must.NoError(t, err)

		must.NoError(t, group.Start(t.Context()))
		must.NoError(t, group.Close(t.Context()))

		broker.awaitStopped(t, "orders")
		broker.awaitStopped(t, "invoices")
	})

	T.Run("called twice", func(t *testing.T) {
		t.Parallel()

		group, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			groupSpec("orders", make(chan string, 1)),
		}, newFakeBroker().provider())
		must.NoError(t, err)

		must.NoError(t, group.Start(t.Context()))
		must.NoError(t, group.Close(t.Context()))
		must.NoError(t, group.Close(t.Context()))
	})

	// A single unstarted Pool waits out its whole context here, because nothing
	// will ever close the channel its Close is waiting on. A group knows it
	// never started and says so immediately.
	T.Run("before start", func(t *testing.T) {
		t.Parallel()

		group, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			groupSpec("orders", make(chan string, 1)),
		}, newFakeBroker().provider())
		must.NoError(t, err)

		ctx, cancel := context.WithTimeout(t.Context(), waitFor)
		defer cancel()

		closed := make(chan error, 1)
		go func() { closed <- group.Close(ctx) }()

		select {
		case closeErr := <-closed:
			test.NoError(t, closeErr)
		case <-time.After(time.Second):
			must.Unreachable(t, must.Sprintf("Close waited on a group that never started"))
		}
	})

	T.Run("with a pool that will not drain", func(t *testing.T) {
		t.Parallel()

		broker := newFakeBroker()

		entered := make(chan struct{}, 1)
		release := make(chan struct{})

		group, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			groupSpec("orders", make(chan string, 1)),
			{
				Topic:  "invoices",
				Config: &jobs.PoolConfig{Concurrency: 1, Retry: fastRetry(1)},
				Handler: func(context.Context, []byte) error {
					entered <- struct{}{}
					<-release

					return nil
				},
			},
		}, broker.provider())
		must.NoError(t, err)

		must.NoError(t, group.Start(t.Context()))

		broker.publish(t, "invoices", "i-1")
		recv(t, entered, "the invoices handler to start")

		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		defer cancel()

		// The topic is named, because a shutdown that would not finish has no
		// request to attribute the failure to and no caller to hand it back to.
		closeErr := group.Close(ctx)
		test.Error(t, closeErr)
		test.StrContains(t, closeErr.Error(), "invoices")

		// The pool that had nothing in flight still drained cleanly: the bound
		// is on the group, and the pools close concurrently rather than in turn.
		broker.awaitStopped(t, "orders")

		close(release)
	})
}

// TestPoolGroup_Options covers what the group hands down to the pools it
// builds, since a group that quietly dropped the dead-letter destination would
// look exactly like one that had none.
func TestPoolGroup_Options(T *testing.T) {
	T.Parallel()

	T.Run("group dead letter reaches every pool", func(t *testing.T) {
		t.Parallel()

		broker := newFakeBroker()
		deadLetters := make(chan jobs.DeadLetter, 2)

		failing := func(topic string) jobs.PoolSpec {
			return jobs.PoolSpec{
				Topic:   topic,
				Config:  &jobs.PoolConfig{Concurrency: 1, Retry: fastRetry(1)},
				Handler: func(context.Context, []byte) error { return errHandler },
			}
		}

		group, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			failing("orders"),
			failing("invoices"),
		}, broker.provider(), jobs.WithPoolGroupDeadLetter(func(_ context.Context, msg jobs.DeadLetter) error {
			deadLetters <- msg

			return nil
		}))
		must.NoError(t, err)

		must.NoError(t, group.Start(t.Context()))
		closeGroup(t, group)

		broker.publish(t, "orders", "o-1")
		test.EqOp(t, "orders", recv(t, deadLetters, "the orders dead letter").Topic)

		broker.publish(t, "invoices", "i-1")
		test.EqOp(t, "invoices", recv(t, deadLetters, "the invoices dead letter").Topic)
	})

	T.Run("spec options override the group's", func(t *testing.T) {
		t.Parallel()

		broker := newFakeBroker()
		groupDeadLetters := make(chan jobs.DeadLetter, 1)
		specDeadLetters := make(chan jobs.DeadLetter, 1)

		group, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			{
				Topic:   "orders",
				Config:  &jobs.PoolConfig{Concurrency: 1, Retry: fastRetry(1)},
				Handler: func(context.Context, []byte) error { return errHandler },
				Options: []jobs.PoolOption{
					jobs.WithPoolDeadLetter(func(_ context.Context, msg jobs.DeadLetter) error {
						specDeadLetters <- msg

						return nil
					}),
				},
			},
		}, broker.provider(), jobs.WithPoolGroupDeadLetter(func(_ context.Context, msg jobs.DeadLetter) error {
			groupDeadLetters <- msg

			return nil
		}))
		must.NoError(t, err)

		must.NoError(t, group.Start(t.Context()))
		closeGroup(t, group)

		broker.publish(t, "orders", "o-1")
		test.EqOp(t, "orders", recv(t, specDeadLetters, "the spec's dead letter").Topic)
		notYet(t, groupDeadLetters, "the group's dead letter")
	})

	T.Run("group observability reaches every pool", func(t *testing.T) {
		t.Parallel()

		broker := newFakeBroker()
		spy := newCounterSpy()
		handled := make(chan string, 2)

		group, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			groupSpec("orders", handled),
			groupSpec("invoices", handled),
		}, broker.provider(),
			jobs.WithPoolGroupLogger(lognoop.NewLogger()),
			jobs.WithPoolGroupTracerProvider(tracingnoop.NewTracerProvider()),
			jobs.WithPoolGroupMetricsProvider(spy.provider()))
		must.NoError(t, err)

		must.NoError(t, group.Start(t.Context()))
		closeGroup(t, group)

		broker.publish(t, "orders", "o-1")
		test.EqOp(t, "orders:o-1", recv(t, handled, "the orders handler"))

		broker.publish(t, "invoices", "i-1")
		test.EqOp(t, "invoices:i-1", recv(t, handled, "the invoices handler"))

		// Two pools, one provider: the instruments are per-pool but the counts
		// land on the same names, told apart by the topic attribute.
		awaitCount(t, spy, "jobs_pool_messages_processed", 2)
	})

	T.Run("nil options are ignored", func(t *testing.T) {
		t.Parallel()

		group, err := jobs.NewPoolGroup(t.Context(), []jobs.PoolSpec{
			groupSpec("orders", make(chan string, 1)),
		}, newFakeBroker().provider(), nil, jobs.WithPoolGroupDrainTimeout(0), jobs.WithPoolGroupDeadLetter(nil))
		must.NoError(t, err)
		must.NotNil(t, group)
	})
}

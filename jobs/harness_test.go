package jobs_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	clockmock "github.com/primandproper/platform-go/v13/clock/mock"
	"github.com/primandproper/platform-go/v13/messagequeue"
	messagequeuemock "github.com/primandproper/platform-go/v13/messagequeue/mock"
	"github.com/primandproper/platform-go/v13/observability/logging"
	lognoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"

	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// testTopic is the topic every Pool fixture consumes.
const testTopic = "test-topic"

// waitFor is the ceiling on any "this should have happened by now" wait. The
// tests synchronize on channels rather than sleeping, so it is only ever
// reached when something is actually wrong.
const waitFor = 10 * time.Second

// fakeQueue is a ConsumerProvider whose consumer reads from a channel a test
// controls. It stands in for a broker: publish() puts a payload on the wire,
// and the Pool's own handler is whatever it registered at construction.
type fakeQueue struct {
	provider messagequeue.ConsumerProvider
	messages chan []byte
	stopped  chan struct{}

	// transportErrs carries what the broker reports as a transport-level
	// failure, which is a different channel from a handler error: the Pool
	// hands messages off before running them, so nothing a handler does ever
	// reaches the consumer's error channel.
	transportErrs chan error

	handler messagequeue.ConsumerFunc
	errs    []error
	atStop  []error
	mu      sync.Mutex
}

// newFakeQueue builds a queue whose consumer runs until its context is done,
// mirroring what every real implementation in messagequeue does.
func newFakeQueue() *fakeQueue {
	q := &fakeQueue{
		messages:      make(chan []byte, 64),
		transportErrs: make(chan error),
		stopped:       make(chan struct{}),
	}

	consumer := &messagequeuemock.ConsumerMock{
		ConsumeFunc: func(ctx context.Context, errs chan<- error) {
			defer close(q.stopped)

			for {
				select {
				case <-ctx.Done():
					q.flushErrorsAtStop(errs)

					return
				case err := <-q.transportErrs:
					errs <- err
				case msg := <-q.messages:
					if err := q.handlerFunc()(ctx, msg); err != nil {
						q.recordErr(err)

						select {
						case errs <- err:
						default:
						}
					}
				}
			}
		},
	}

	q.provider = &messagequeuemock.ConsumerProviderMock{
		CloseFunc: func() {},
		NewConsumerFunc: func(_ context.Context, _ string, handlerFunc messagequeue.ConsumerFunc) (messagequeue.Consumer, error) {
			q.mu.Lock()
			defer q.mu.Unlock()
			q.handler = handlerFunc

			return consumer, nil
		},
	}

	return q
}

func (q *fakeQueue) handlerFunc() messagequeue.ConsumerFunc {
	q.mu.Lock()
	defer q.mu.Unlock()

	return q.handler
}

func (q *fakeQueue) recordErr(err error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.errs = append(q.errs, err)
}

// handlerErrors returns the errors the Pool's handler returned to the consumer.
// In steady state this is empty: the Pool hands messages off and reports
// success immediately, so only a shutdown rejection shows up here.
func (q *fakeQueue) handlerErrors() []error {
	q.mu.Lock()
	defer q.mu.Unlock()

	return append([]error(nil), q.errs...)
}

func (q *fakeQueue) publish(payloads ...string) {
	for _, payload := range payloads {
		q.messages <- []byte(payload)
	}
}

// reportTransportError makes the broker surface err on the consumer's error
// channel while the Pool is still running, and returns once the consumer has
// forwarded it.
func (q *fakeQueue) reportTransportError(err error) {
	q.transportErrs <- err
}

// stageErrorsAtStop arranges for errs to be pushed onto the consumer's error
// channel immediately before Consume returns. That is the only way to leave
// errors buffered for the drain the Pool runs after Consume has finished —
// reporting them any earlier lets the drain take them while it is still in its
// steady-state loop.
func (q *fakeQueue) stageErrorsAtStop(errs ...error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.atStop = append(q.atStop, errs...)
}

// flushErrorsAtStop pushes the staged errors without blocking, so a test that
// stages more than the channel holds cannot wedge the consumer.
func (q *fakeQueue) flushErrorsAtStop(errs chan<- error) {
	q.mu.Lock()
	defer q.mu.Unlock()

	for _, err := range q.atStop {
		select {
		case errs <- err:
		default:
		}
	}
}

// counterSpy is a metrics.Provider that totals Int64Counter increments by
// instrument name, so a test can assert on the counters that are the only
// externally visible record of a dropped or dead-lettered message.
type counterSpy struct {
	counts map[string]int64
	mu     sync.Mutex
}

func newCounterSpy() *counterSpy {
	return &counterSpy{counts: map[string]int64{}}
}

func (c *counterSpy) add(name string, incr int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.counts[name] += incr
}

func (c *counterSpy) count(name string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.counts[name]
}

// provider delegates everything but Int64Counter to the noop implementation:
// only the counters carry assertions, and the histograms would otherwise need a
// double each.
func (c *counterSpy) provider() metrics.Provider {
	fallback := metricsnoop.NewMetricsProvider()

	return &metricsmock.ProviderMock{
		NewFloat64HistogramFunc:   fallback.NewFloat64Histogram,
		NewInt64UpDownCounterFunc: fallback.NewInt64UpDownCounter,
		NewInt64CounterFunc: func(name string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			return &metricsmock.Int64CounterMock{
				AddFunc: func(context.Context, int64, ...metric.AddOption) { c.add(name, 1) },
			}, nil
		},
	}
}

// errInstrument is what a misconfigured meter returns, so the constructor tests
// can name the error they expect to surface from NewPool and NewScheduler.
var errInstrument = errors.New("meter is misconfigured")

// failingInstruments is a metrics.Provider that refuses to build one named
// instrument and delegates the rest to the noop implementation. Every
// instrument a component creates is a constructor that can fail, and the claim
// under test is that each failure fails the constructor rather than the first
// message or the first tick.
func failingInstruments(failOn string) metrics.Provider {
	fallback := metricsnoop.NewMetricsProvider()

	return &metricsmock.ProviderMock{
		NewInt64CounterFunc: func(name string, options ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			if name == failOn {
				return nil, errInstrument
			}

			return fallback.NewInt64Counter(name, options...)
		},
		NewInt64UpDownCounterFunc: func(name string, options ...metric.Int64UpDownCounterOption) (metrics.Int64UpDownCounter, error) {
			if name == failOn {
				return nil, errInstrument
			}

			return fallback.NewInt64UpDownCounter(name, options...)
		},
		NewFloat64HistogramFunc: func(name string, options ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
			if name == failOn {
				return nil, errInstrument
			}

			return fallback.NewFloat64Histogram(name, options...)
		},
	}
}

// gatedLogger blocks inside Error until the test opens its gate, and keeps what
// it was asked to log. It exists so a test can hold the Pool's consumer-error
// drain mid-report while the consumer finishes — which is the only way to leave
// errors buffered for the drain the Pool runs once Consume has returned, rather
// than racing the steady-state drain for them.
//
// The With* methods return the same logger, because the Pool names and
// decorates its logger before using it and the gate has to survive that.
type gatedLogger struct {
	logging.Logger

	gate    chan struct{}
	entered chan struct{}

	logged []error
	mu     sync.Mutex
}

func newGatedLogger() *gatedLogger {
	return &gatedLogger{
		Logger:  lognoop.NewLogger(),
		gate:    make(chan struct{}),
		entered: make(chan struct{}, 1),
	}
}

func (l *gatedLogger) Error(_ string, err error) {
	select {
	case l.entered <- struct{}{}:
	default:
	}

	<-l.gate

	l.mu.Lock()
	defer l.mu.Unlock()
	l.logged = append(l.logged, err)
}

func (l *gatedLogger) WithName(string) logging.Logger { return l }

func (l *gatedLogger) WithValue(string, any) logging.Logger { return l }

// WithValues must return l for the same reason WithValue does: the Pool's
// observer is built with seeded values, so the logger it derives goes through
// here — and the embedded noop's version would hand back a logger with no gate.
func (l *gatedLogger) WithValues(map[string]any) logging.Logger { return l }

func (l *gatedLogger) errors() []error {
	l.mu.Lock()
	defer l.mu.Unlock()

	return append([]error(nil), l.logged...)
}

// fixedClock reads at forever, so a test can assert on a timestamp the code
// under test stamped rather than on "some time near now".
func fixedClock(at time.Time) clock.Clock {
	return &clockmock.ClockMock{
		NowFunc:   func() time.Time { return at },
		SinceFunc: func(time.Time) time.Duration { return 0 },
	}
}

// awaitCount blocks until the named counter reaches at least want. Polling is
// the only option — the counter is written from a worker goroutine with no
// channel to wait on — but the poll interval never matters, because the assert
// below it only runs once the value has arrived or waitFor has elapsed.
func awaitCount(t *testing.T, spy *counterSpy, name string, want int64) {
	t.Helper()

	deadline := time.Now().Add(waitFor)
	for time.Now().Before(deadline) {
		if spy.count(name) >= want {
			return
		}

		time.Sleep(time.Millisecond)
	}

	must.Unreachable(t, must.Sprintf("counter %q reached %d, wanted %d", name, spy.count(name), want))
}

// recv takes one value from ch or fails, so a test that would otherwise hang
// reports which wait it was stuck on.
func recv[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()

	select {
	case v := <-ch:
		return v
	case <-time.After(waitFor):
		must.Unreachable(t, must.Sprintf("timed out waiting for %s", what))

		var zero T

		return zero
	}
}

// notYet asserts nothing has arrived on ch. It is deliberately impatient: it is
// used to show that a Close has not returned yet, and a long wait there would
// only slow the test down without strengthening the claim.
func notYet[T any](t *testing.T, ch <-chan T, what string) {
	t.Helper()

	select {
	case <-ch:
		must.Unreachable(t, must.Sprintf("%s happened earlier than it should have", what))
	case <-time.After(50 * time.Millisecond):
	}
}

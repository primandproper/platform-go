package jobs_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/jobs"
	"github.com/primandproper/platform-go/v13/messagequeue"
	messagequeuemock "github.com/primandproper/platform-go/v13/messagequeue/mock"
	lognoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"
	"github.com/primandproper/platform-go/v13/retry"
	retrycfg "github.com/primandproper/platform-go/v13/retry/config"
	retrynoop "github.com/primandproper/platform-go/v13/retry/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// errHandler is the failure every retry test returns, so the assertions can
// name the error they expect to see reach the dead-letter envelope.
var errHandler = errors.New("handler exploded")

// fastRetry keeps the backoff real — attempts are still spaced — while costing
// nothing in wall time. MaxAttempts is what the tests actually vary.
func fastRetry(maxAttempts uint) retrycfg.Config {
	return retrycfg.Config{
		MaxAttempts:  maxAttempts,
		InitialDelay: time.Millisecond,
		MaxDelay:     2 * time.Millisecond,
		Multiplier:   1,
	}
}

func poolConfig(concurrency int, maxAttempts uint) *jobs.PoolConfig {
	return &jobs.PoolConfig{
		Topic:       testTopic,
		Concurrency: concurrency,
		Retry:       fastRetry(maxAttempts),
	}
}

// startPool builds a Pool over a fake queue, runs it, and registers the Close.
// Every test that gets this far wants all three.
func startPool(t *testing.T, cfg *jobs.PoolConfig, handler jobs.Handler, opts ...jobs.PoolOption) (*jobs.Pool, *fakeQueue) {
	t.Helper()

	return startPoolOn(t, newFakeQueue(), cfg, handler, opts...)
}

// startPoolOn is startPool over a queue the test has already configured, for
// the cases that have to arrange the broker's behavior before anything
// consumes from it.
func startPoolOn(t *testing.T, q *fakeQueue, cfg *jobs.PoolConfig, handler jobs.Handler, opts ...jobs.PoolOption) (*jobs.Pool, *fakeQueue) {
	t.Helper()

	pool, err := jobs.NewPool(t.Context(), cfg, q.provider, handler, opts...)
	must.NoError(t, err)
	must.NotNil(t, pool)

	go pool.Run()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), waitFor)
		defer cancel()

		_ = pool.Close(ctx)
	})

	return pool, q
}

func TestNewPool(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		q := newFakeQueue()

		pool, err := jobs.NewPool(t.Context(), poolConfig(2, 1), q.provider, func(context.Context, []byte) error {
			return nil
		})
		must.NoError(t, err)
		must.NotNil(t, pool)
	})

	T.Run("with nil config", func(t *testing.T) {
		t.Parallel()

		_, err := jobs.NewPool(t.Context(), nil, newFakeQueue().provider, func(context.Context, []byte) error {
			return nil
		})
		test.Error(t, err)
	})

	T.Run("with nil consumer provider", func(t *testing.T) {
		t.Parallel()

		_, err := jobs.NewPool(t.Context(), poolConfig(1, 1), nil, func(context.Context, []byte) error {
			return nil
		})
		test.ErrorIs(t, err, jobs.ErrNilConsumerProvider)
	})

	T.Run("with nil handler", func(t *testing.T) {
		t.Parallel()

		_, err := jobs.NewPool(t.Context(), poolConfig(1, 1), newFakeQueue().provider, nil)
		test.ErrorIs(t, err, jobs.ErrNilHandler)
	})

	T.Run("with empty topic", func(t *testing.T) {
		t.Parallel()

		cfg := poolConfig(1, 1)
		cfg.Topic = ""

		_, err := jobs.NewPool(t.Context(), cfg, newFakeQueue().provider, func(context.Context, []byte) error {
			return nil
		})
		test.Error(t, err)
	})

	T.Run("with negative handler timeout", func(t *testing.T) {
		t.Parallel()

		cfg := poolConfig(1, 1)
		cfg.HandlerTimeout = -time.Second

		_, err := jobs.NewPool(t.Context(), cfg, newFakeQueue().provider, func(context.Context, []byte) error {
			return nil
		})
		test.Error(t, err)
	})

	T.Run("with failing consumer provider", func(t *testing.T) {
		t.Parallel()

		expected := errors.New("no broker")
		provider := &messagequeuemock.ConsumerProviderMock{
			NewConsumerFunc: func(context.Context, string, messagequeue.ConsumerFunc) (messagequeue.Consumer, error) {
				return nil, expected
			},
		}

		_, err := jobs.NewPool(t.Context(), poolConfig(1, 1), provider, func(context.Context, []byte) error {
			return nil
		})
		test.ErrorIs(t, err, expected)
	})

	T.Run("with a failing instrument", func(t *testing.T) {
		t.Parallel()

		// Every instrument the Pool creates, so that adding one without a
		// matching error path shows up here rather than as a nil counter on the
		// first message.
		instruments := []string{
			"jobs_pool_messages_received",
			"jobs_pool_messages_processed",
			"jobs_pool_attempts_failed",
			"jobs_pool_messages_dead_lettered",
			"jobs_pool_messages_dropped",
			"jobs_pool_handler_panics",
			"jobs_pool_dead_letter_failures",
			"jobs_pool_consumer_errors",
			"jobs_pool_in_flight",
			"jobs_pool_handler_latency_ms",
			"jobs_pool_message_latency_ms",
			"jobs_pool_queue_wait_ms",
		}

		for _, instrument := range instruments {
			t.Run(instrument, func(t *testing.T) {
				t.Parallel()

				q := newFakeQueue()

				_, err := jobs.NewPool(t.Context(), poolConfig(1, 1), q.provider, func(context.Context, []byte) error {
					return nil
				}, jobs.WithPoolMetricsProvider(failingInstruments(instrument)))
				test.ErrorIs(t, err, errInstrument)

				// The constructor failed before subscribing, so nothing was left
				// consuming a topic the caller believes it never attached to.
				test.SliceEmpty(t, q.provider.(*messagequeuemock.ConsumerProviderMock).NewConsumerCalls())
			})
		}
	})
}

func TestPoolOptions(T *testing.T) {
	T.Parallel()

	T.Run("WithPoolClock stamps the dead-letter envelope", func(t *testing.T) {
		t.Parallel()

		at := time.Date(2020, time.March, 4, 5, 6, 7, 0, time.UTC)
		deadLetters := make(chan jobs.DeadLetter, 1)

		_, q := startPool(t, poolConfig(1, 1), func(context.Context, []byte) error {
			return errHandler
		},
			jobs.WithPoolClock(fixedClock(at)),
			jobs.WithPoolDeadLetter(func(_ context.Context, msg jobs.DeadLetter) error {
				deadLetters <- msg

				return nil
			}),
		)

		q.publish("doomed")

		test.EqOp(t, at, recv(t, deadLetters, "dead letter").FailedAt)
	})

	T.Run("WithPoolRetryPolicy overrides the configured attempts", func(t *testing.T) {
		t.Parallel()

		var attempts atomic.Int64
		deadLetters := make(chan jobs.DeadLetter, 1)

		// The config asks for five attempts and the policy gives one. The policy
		// wins, because it is the thing that actually runs the handler — which is
		// the whole hazard the option's documentation calls out.
		_, q := startPool(t, poolConfig(1, 5), func(context.Context, []byte) error {
			attempts.Add(1)

			return errHandler
		},
			jobs.WithPoolRetryPolicy(retrynoop.NewPolicy()),
			jobs.WithPoolDeadLetter(func(_ context.Context, msg jobs.DeadLetter) error {
				deadLetters <- msg

				return nil
			}),
		)

		q.publish("doomed")

		test.EqOp(t, uint(1), recv(t, deadLetters, "dead letter").Attempts)
		test.EqOp(t, int64(1), attempts.Load())
	})

	T.Run("WithPoolLogger and WithPoolTracerProvider are wired without disturbing the pool", func(t *testing.T) {
		t.Parallel()

		handled := make(chan string, 1)
		_, q := startPool(t, poolConfig(1, 1), func(_ context.Context, payload []byte) error {
			handled <- string(payload)

			return nil
		},
			jobs.WithPoolLogger(lognoop.NewLogger()),
			jobs.WithPoolTracerProvider(tracingnoop.NewTracerProvider()),
		)

		q.publish("observed")

		test.EqOp(t, "observed", recv(t, handled, "handled message"))
	})

	T.Run("ignores nil options and nil values", func(t *testing.T) {
		t.Parallel()

		handled := make(chan string, 1)

		// A nil value is dropped rather than installed, so a caller wiring an
		// option from a config that turned out to be empty gets the default
		// instead of a nil-dereference on the first message.
		_, q := startPool(t, poolConfig(1, 1), func(_ context.Context, payload []byte) error {
			handled <- string(payload)

			return nil
		},
			nil,
			jobs.WithPoolClock(nil),
			jobs.WithPoolDeadLetter(nil),
			jobs.WithPoolRetryPolicy(nil),
		)

		q.publish("fine")

		test.EqOp(t, "fine", recv(t, handled, "handled message"))
	})
}

func TestPoolConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("fills unset knobs", func(t *testing.T) {
		t.Parallel()

		cfg := &jobs.PoolConfig{Topic: testTopic}
		cfg.EnsureDefaults()

		test.EqOp(t, jobs.DefaultConcurrency, cfg.Concurrency)
		test.Positive(t, cfg.Retry.MaxAttempts)
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("leaves set knobs alone", func(t *testing.T) {
		t.Parallel()

		cfg := &jobs.PoolConfig{Topic: testTopic, Concurrency: 3}
		cfg.EnsureDefaults()

		test.EqOp(t, 3, cfg.Concurrency)
	})
}

func TestPool_Run(T *testing.T) {
	T.Parallel()

	T.Run("handles every published message", func(t *testing.T) {
		t.Parallel()

		handled := make(chan string, 3)
		_, q := startPool(t, poolConfig(2, 1), func(_ context.Context, payload []byte) error {
			handled <- string(payload)

			return nil
		})

		q.publish("a", "b", "c")

		seen := map[string]bool{}
		for range 3 {
			seen[recv(t, handled, "handled message")] = true
		}

		test.MapContainsKeys(t, seen, []string{"a", "b", "c"})
		test.SliceEmpty(t, q.handlerErrors())
	})

	T.Run("retries a failing message until it succeeds", func(t *testing.T) {
		t.Parallel()

		var attempts atomic.Int64
		succeeded := make(chan struct{}, 1)

		_, q := startPool(t, poolConfig(1, 5), func(context.Context, []byte) error {
			if attempts.Add(1) < 3 {
				return errHandler
			}

			succeeded <- struct{}{}

			return nil
		})

		q.publish("retry-me")
		recv(t, succeeded, "successful attempt")

		test.EqOp(t, int64(3), attempts.Load())
	})

	T.Run("runs messages concurrently up to Concurrency", func(t *testing.T) {
		t.Parallel()

		const concurrency = 4

		var (
			mu      sync.Mutex
			peak    int
			current int
		)

		release := make(chan struct{})
		entered := make(chan struct{}, concurrency)

		_, q := startPool(t, poolConfig(concurrency, 1), func(context.Context, []byte) error {
			mu.Lock()
			current++
			peak = max(peak, current)
			mu.Unlock()

			entered <- struct{}{}
			<-release

			mu.Lock()
			current--
			mu.Unlock()

			return nil
		})

		q.publish("1", "2", "3", "4")
		for range concurrency {
			recv(t, entered, "handler entry")
		}
		close(release)

		mu.Lock()
		defer mu.Unlock()
		test.EqOp(t, concurrency, peak)
	})

	T.Run("bounds an attempt with HandlerTimeout", func(t *testing.T) {
		t.Parallel()

		observed := make(chan error, 1)
		deadLetters := make(chan jobs.DeadLetter, 1)

		cfg := poolConfig(1, 1)
		cfg.HandlerTimeout = 50 * time.Millisecond

		_, q := startPool(t, cfg, func(ctx context.Context, _ []byte) error {
			<-ctx.Done()
			observed <- ctx.Err()

			return ctx.Err()
		}, jobs.WithPoolDeadLetter(func(_ context.Context, msg jobs.DeadLetter) error {
			deadLetters <- msg

			return nil
		}))

		q.publish("wedged")

		test.ErrorIs(t, recv(t, observed, "the attempt's context expiring"), context.DeadlineExceeded)

		// The timeout ends the attempt rather than the Pool: the message still
		// takes the ordinary terminal path.
		test.EqOp(t, "wedged", string(recv(t, deadLetters, "dead letter").Payload))
	})

	T.Run("reports a consumer error without stopping", func(t *testing.T) {
		t.Parallel()

		spy := newCounterSpy()
		handled := make(chan string, 1)
		q := newFakeQueue()

		startPoolOn(t, q, poolConfig(1, 1), func(_ context.Context, payload []byte) error {
			handled <- string(payload)

			return nil
		}, jobs.WithPoolMetricsProvider(spy.provider()))

		q.reportTransportError(errors.New("broker hiccup"))
		awaitCount(t, spy, "jobs_pool_consumer_errors", 1)

		// A transport failure is not terminal — the consumer is still attached,
		// and the next message proves it.
		q.publish("fine")
		test.EqOp(t, "fine", recv(t, handled, "message after the consumer error"))
	})

	T.Run("contains a panicking handler and keeps working", func(t *testing.T) {
		t.Parallel()

		spy := newCounterSpy()
		deadLetters := make(chan jobs.DeadLetter, 1)
		handled := make(chan string, 1)

		_, q := startPool(t, poolConfig(1, 1), func(_ context.Context, payload []byte) error {
			if string(payload) == "boom" {
				panic("handler blew up")
			}

			handled <- string(payload)

			return nil
		},
			jobs.WithPoolMetricsProvider(spy.provider()),
			jobs.WithPoolDeadLetter(func(_ context.Context, msg jobs.DeadLetter) error {
				deadLetters <- msg

				return nil
			}),
		)

		q.publish("boom")

		dead := recv(t, deadLetters, "dead-lettered panic")
		test.EqOp(t, "boom", string(dead.Payload))
		test.StrContains(t, dead.Error, "handler blew up")
		test.EqOp(t, int64(1), spy.count("jobs_pool_handler_panics"))

		// The worker that caught the panic is still the one serving the topic.
		q.publish("fine")
		test.EqOp(t, "fine", recv(t, handled, "message after the panic"))
	})
}

func TestPool_DeadLettering(T *testing.T) {
	T.Parallel()

	T.Run("dead-letters a message that exhausts its attempts", func(t *testing.T) {
		t.Parallel()

		const maxAttempts = 3

		var attempts atomic.Int64
		deadLetters := make(chan jobs.DeadLetter, 1)

		_, q := startPool(t, poolConfig(1, maxAttempts), func(context.Context, []byte) error {
			attempts.Add(1)

			return errHandler
		}, jobs.WithPoolDeadLetter(func(_ context.Context, msg jobs.DeadLetter) error {
			deadLetters <- msg

			return nil
		}))

		q.publish("doomed")

		dead := recv(t, deadLetters, "dead letter")
		test.EqOp(t, testTopic, dead.Topic)
		test.EqOp(t, "doomed", string(dead.Payload))
		test.EqOp(t, uint(maxAttempts), dead.Attempts)
		test.StrContains(t, dead.Error, errHandler.Error())
		test.False(t, dead.FailedAt.IsZero())
		test.EqOp(t, int64(maxAttempts), attempts.Load())
	})

	T.Run("an unretryable error skips the remaining attempts", func(t *testing.T) {
		t.Parallel()

		var attempts atomic.Int64
		deadLetters := make(chan jobs.DeadLetter, 1)

		_, q := startPool(t, poolConfig(1, 5), func(context.Context, []byte) error {
			attempts.Add(1)

			return retry.Unretryable(errHandler)
		}, jobs.WithPoolDeadLetter(func(_ context.Context, msg jobs.DeadLetter) error {
			deadLetters <- msg

			return nil
		}))

		q.publish("malformed")

		dead := recv(t, deadLetters, "dead letter")
		test.EqOp(t, uint(1), dead.Attempts)
		test.EqOp(t, int64(1), attempts.Load())
	})

	T.Run("drops the message when no dead-letter destination is configured", func(t *testing.T) {
		t.Parallel()

		spy := newCounterSpy()
		handled := make(chan string, 1)

		_, q := startPool(t, poolConfig(1, 2), func(_ context.Context, payload []byte) error {
			if string(payload) == "doomed" {
				return errHandler
			}

			handled <- string(payload)

			return nil
		}, jobs.WithPoolMetricsProvider(spy.provider()))

		q.publish("doomed")
		awaitCount(t, spy, "jobs_pool_messages_dropped", 1)

		test.EqOp(t, int64(0), spy.count("jobs_pool_messages_dead_lettered"))

		// A dropped message is terminal for that message alone.
		q.publish("fine")
		test.EqOp(t, "fine", recv(t, handled, "message after the drop"))
	})

	T.Run("counts a dead-letter destination that itself fails", func(t *testing.T) {
		t.Parallel()

		spy := newCounterSpy()

		_, q := startPool(t, poolConfig(1, 1), func(context.Context, []byte) error {
			return errHandler
		},
			jobs.WithPoolMetricsProvider(spy.provider()),
			jobs.WithPoolDeadLetter(func(context.Context, jobs.DeadLetter) error {
				return errors.New("dead-letter topic is also down")
			}),
		)

		q.publish("doomed")
		awaitCount(t, spy, "jobs_pool_dead_letter_failures", 1)

		test.EqOp(t, int64(0), spy.count("jobs_pool_messages_dead_lettered"))
	})
}

func TestPool_Close(T *testing.T) {
	T.Parallel()

	T.Run("drains the in-flight message before returning", func(t *testing.T) {
		t.Parallel()

		entered := make(chan struct{}, 1)
		release := make(chan struct{})
		finished := make(chan struct{})

		pool, q := startPool(t, poolConfig(1, 1), func(context.Context, []byte) error {
			entered <- struct{}{}
			<-release
			close(finished)

			return nil
		})

		q.publish("slow")
		recv(t, entered, "handler entry")

		closed := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), waitFor)
			defer cancel()

			closed <- pool.Close(ctx)
		}()

		// Close must not report a clean shutdown while the handler is running.
		notYet(t, closed, "Close returning")
		notYet(t, finished, "the handler finishing")

		close(release)

		test.NoError(t, recv(t, closed, "Close returning"))
		recv(t, finished, "the handler finishing")
	})

	T.Run("reports the deadline when the drain outlasts it", func(t *testing.T) {
		t.Parallel()

		entered := make(chan struct{}, 1)
		observed := make(chan error, 1)

		// The handler blocks on its own context, which is exactly what Close
		// cancels once its deadline passes.
		pool, q := startPool(t, poolConfig(1, 1), func(ctx context.Context, _ []byte) error {
			entered <- struct{}{}
			<-ctx.Done()
			observed <- ctx.Err()

			return ctx.Err()
		})

		q.publish("wedged")
		recv(t, entered, "handler entry")

		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		test.ErrorIs(t, pool.Close(ctx), context.DeadlineExceeded)
		test.ErrorIs(t, recv(t, observed, "the handler's context being canceled"), context.Canceled)
	})

	T.Run("reports the consumer errors still buffered when Consume returns", func(t *testing.T) {
		t.Parallel()

		// The Pool sizes the consumer's error channel by Concurrency, so this is
		// how many errors can be sitting in it when Consume gives up.
		const buffered = 16

		spy := newCounterSpy()
		logger := newGatedLogger()
		q := newFakeQueue()

		staged := make([]error, buffered)
		for i := range staged {
			staged[i] = errors.New("broker went away")
		}
		q.stageErrorsAtStop(staged...)

		pool, _ := startPoolOn(t, q, poolConfig(buffered, 1), func(context.Context, []byte) error {
			return nil
		},
			jobs.WithPoolMetricsProvider(spy.provider()),
			jobs.WithPoolLogger(logger),
		)

		closed := make(chan error, 1)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), waitFor)
			defer cancel()

			closed <- pool.Close(ctx)
		}()

		// Hold the drain inside its first report until the consumer has
		// finished, so the rest of the errors are still buffered when the Pool
		// goes looking for what Consume left behind.
		recv(t, logger.entered, "the drain reporting a consumer error")
		<-q.stopped
		close(logger.gate)

		must.NoError(t, recv(t, closed, "Close returning"))

		// Close waits out the drain, so every staged error has been reported by
		// the time it returns — a shutdown does not swallow the errors that
		// explain it.
		test.EqOp(t, int64(buffered), spy.count("jobs_pool_consumer_errors"))
		test.SliceLen(t, buffered, logger.errors())
	})

	T.Run("is safe to call more than once", func(t *testing.T) {
		t.Parallel()

		pool, _ := startPool(t, poolConfig(1, 1), func(context.Context, []byte) error {
			return nil
		})

		ctx, cancel := context.WithTimeout(context.Background(), waitFor)
		defer cancel()

		test.NoError(t, pool.Close(ctx))
		test.NoError(t, pool.Close(ctx))
	})

	T.Run("rejects a message the consumer reads during shutdown", func(t *testing.T) {
		t.Parallel()

		pool, q := startPool(t, poolConfig(1, 1), func(context.Context, []byte) error {
			return nil
		})

		ctx, cancel := context.WithTimeout(context.Background(), waitFor)
		defer cancel()

		must.NoError(t, pool.Close(ctx))

		// Run has returned, so nothing is left to hand a message to. The
		// rejection is what tells the transport not to treat it as handled —
		// and the reason the work channel is never closed, since a send on a
		// closed channel panics from inside a select that offers a ready
		// alternative. Repeated because that failure was a coin flip.
		for range 100 {
			test.Error(t, q.handlerFunc()(t.Context(), []byte("too late")))
		}
	})
}

func TestNewTopicDeadLetter(T *testing.T) {
	T.Parallel()

	T.Run("publishes the envelope to the topic", func(t *testing.T) {
		t.Parallel()

		published := make(chan any, 1)
		provider := &messagequeuemock.PublisherProviderMock{
			NewPublisherFunc: func(context.Context, string) (messagequeue.Publisher, error) {
				return &messagequeuemock.PublisherMock{
					PublishFunc: func(_ context.Context, data any, _ ...messagequeue.PublishOption) error {
						published <- data

						return nil
					},
				}, nil
			},
		}

		deadLetter, err := jobs.NewTopicDeadLetter(t.Context(), provider, "dead-letters")
		must.NoError(t, err)

		must.NoError(t, deadLetter(t.Context(), jobs.DeadLetter{Topic: testTopic, Payload: []byte("x")}))

		sent, ok := recv(t, published, "published envelope").(jobs.DeadLetter)
		must.True(t, ok)
		test.EqOp(t, testTopic, sent.Topic)

		test.SliceLen(t, 1, provider.NewPublisherCalls())
		test.EqOp(t, "dead-letters", provider.NewPublisherCalls()[0].Topic)
	})

	T.Run("with nil publisher provider", func(t *testing.T) {
		t.Parallel()

		_, err := jobs.NewTopicDeadLetter(t.Context(), nil, "dead-letters")
		test.ErrorIs(t, err, jobs.ErrNilPublisherProvider)
	})

	T.Run("with empty topic", func(t *testing.T) {
		t.Parallel()

		_, err := jobs.NewTopicDeadLetter(t.Context(), &messagequeuemock.PublisherProviderMock{}, "")
		test.ErrorIs(t, err, messagequeue.ErrEmptyTopicName)
	})

	T.Run("with failing publisher provider", func(t *testing.T) {
		t.Parallel()

		expected := platformerrors.New("no broker")
		provider := &messagequeuemock.PublisherProviderMock{
			NewPublisherFunc: func(context.Context, string) (messagequeue.Publisher, error) {
				return nil, expected
			},
		}

		_, err := jobs.NewTopicDeadLetter(t.Context(), provider, "dead-letters")
		test.ErrorIs(t, err, expected)
	})
}

package jobs

// These tests live in the package rather than in jobs_test because the only way
// to see what a Pool or Scheduler observed is to swap its Observer for a
// recording one, and the field is unexported — the same seam
// distributedlock/memory's tests use.
//
// They call process and tick directly instead of running the loops. What is
// under test is which values reach the pillars, and the goroutines add nothing
// to that but scheduling.

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/distributedlock"
	distributedlockmock "github.com/primandproper/platform-go/v13/distributedlock/mock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/messagequeue"
	messagequeuemock "github.com/primandproper/platform-go/v13/messagequeue/mock"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	retrycfg "github.com/primandproper/platform-go/v13/retry/config"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/trace"
)

const observedTopic = "observed-topic"

// newObservedPool builds a Pool with a RecordingObserver in place of the real
// one. The consumer is never started — these tests drive process directly.
func newObservedPool(t *testing.T, handler Handler) (*Pool, *observability.RecordingObserver) {
	t.Helper()

	provider := &messagequeuemock.ConsumerProviderMock{
		NewConsumerFunc: func(context.Context, string, messagequeue.ConsumerFunc) (messagequeue.Consumer, error) {
			return &messagequeuemock.ConsumerMock{}, nil
		},
	}

	pool, err := NewPool(t.Context(), &PoolConfig{
		Topic:       observedTopic,
		Concurrency: 1,
		Retry: retrycfg.Config{
			MaxAttempts:  2,
			InitialDelay: time.Millisecond,
			MaxDelay:     time.Millisecond,
			Multiplier:   1,
		},
	}, provider, handler)
	must.NoError(t, err)

	// Seeded as NewPool seeds it: a pool consumes one topic, so the topic is
	// stated once at construction.
	obs := observability.NewRecordingObserverWithValues(map[string]any{keys.TopicKey: observedTopic})
	pool.o11y = obs

	return pool, obs
}

func TestPool_observability(T *testing.T) {
	T.Parallel()

	T.Run("a handled message observes its topic, size, and attempt count", func(t *testing.T) {
		t.Parallel()

		pool, obs := newObservedPool(t, func(context.Context, []byte) error { return nil })

		pool.process(&message{payload: []byte("hello")})

		obs.ObservedOperationWithData(t, map[string]any{
			keys.TopicKey:  observedTopic,
			payloadSizeKey: 5,
			attemptsKey:    uint(1),
		})
	})

	T.Run("each attempt is its own operation", func(t *testing.T) {
		t.Parallel()

		pool, obs := newObservedPool(t, func(context.Context, []byte) error {
			return platformerrors.New("nope")
		})

		pool.process(&message{payload: []byte("x")})

		// Two attempts means two attempt operations plus the message's own, and
		// the second one has to name itself the second — a single shared span
		// would only ever record the last.
		obs.ObservedInOrder(t,
			observability.ObservedKeyValue(attemptsKey, uint(1)),
			observability.ObservedKeyValue(attemptsKey, uint(2)),
		)
	})

	T.Run("a panic stack goes to the span and not the log", func(t *testing.T) {
		t.Parallel()

		pool, obs := newObservedPool(t, func(context.Context, []byte) error {
			panic("boom")
		})

		pool.process(&message{payload: []byte("x")})

		// The stack is unbounded and useless in a log line, but it is the only
		// description of where the panic came from — so it belongs on the span
		// alone.
		attempt := obs.ObservedOperationWithKeys(t, panicStackKey)
		attempt.Observed(t, observability.ObservedKey(panicStackKey).OnSpan())
	})
}

func TestMessage_linkToOrigin(T *testing.T) {
	T.Parallel()

	T.Run("carries a valid consume span as a link", func(t *testing.T) {
		t.Parallel()

		origin := trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: trace.TraceID{1},
			SpanID:  trace.SpanID{2},
		})
		must.True(t, origin.IsValid())

		test.SliceLen(t, 1, (&message{origin: origin}).linkToOrigin())
	})

	T.Run("omits an untraced consume rather than linking to nothing", func(t *testing.T) {
		t.Parallel()

		// An invalid link is not inert: it produces a span carrying a dangling
		// reference, which is worse than no link at all.
		test.SliceEmpty(t, (&message{}).linkToOrigin())
	})
}

// newObservedScheduler builds a Scheduler with a RecordingObserver and a locker
// the test supplies, so both the acquired and the contended paths are drivable.
func newObservedScheduler(t *testing.T, locker distributedlock.Locker) (*Scheduler, *observability.RecordingObserver) {
	t.Helper()

	scheduler, err := NewScheduler(t.Context(), &SchedulerConfig{}, locker)
	must.NoError(t, err)

	obs := observability.NewRecordingObserver()
	scheduler.o11y = obs

	return scheduler, obs
}

func TestScheduler_observability(T *testing.T) {
	T.Parallel()

	job := Job{
		Name:     "observed-job",
		Interval: time.Minute,
		LeaseTTL: time.Minute,
		Run:      func(context.Context) error { return nil },
	}

	T.Run("a run observes the job, its lease, and that it ran", func(t *testing.T) {
		t.Parallel()

		locker := &distributedlockmock.LockerMock{
			AcquireFunc: func(context.Context, string, time.Duration) (distributedlock.Lock, error) {
				return &distributedlockmock.LockMock{
					ReleaseFunc: func(context.Context) error { return nil },
				}, nil
			},
		}

		scheduler, obs := newObservedScheduler(t, locker)
		scheduler.tick(t.Context(), &job, job.Interval)

		obs.ObservedOperationWithData(t, map[string]any{
			jobNameKey:      job.Name,
			jobIntervalKey:  job.Interval,
			keys.LockKeyKey: DefaultLockKeyPrefix + job.Name,
			keys.LockTTLKey: job.LeaseTTL,
			jobRanKey:       true,
		})
	})

	T.Run("a skipped tick is still traced, and says it did not run", func(t *testing.T) {
		t.Parallel()

		locker := &distributedlockmock.LockerMock{
			AcquireFunc: func(context.Context, string, time.Duration) (distributedlock.Lock, error) {
				return nil, distributedlock.ErrLockNotAcquired
			},
		}

		scheduler, obs := newObservedScheduler(t, locker)
		scheduler.tick(t.Context(), &job, job.Interval)

		// "Did this replica decline, or did nobody run it" is the question a
		// missed job raises, and only a traced skip can answer it.
		obs.ObservedOperationWithData(t, map[string]any{
			jobNameKey: job.Name,
			jobRanKey:  false,
		})
	})

	T.Run("a calendar job's run reports the schedule it is on", func(t *testing.T) {
		t.Parallel()

		const spec = "CRON_TZ=America/Chicago 0 3 * * *"

		scheduled := Job{
			Name:     "observed-calendar-job",
			Schedule: MustCron(spec),
			LeaseTTL: time.Minute,
			Run:      func(context.Context) error { return nil },
		}

		locker := &distributedlockmock.LockerMock{
			AcquireFunc: func(context.Context, string, time.Duration) (distributedlock.Lock, error) {
				return &distributedlockmock.LockMock{
					ReleaseFunc: func(context.Context) error { return nil },
				}, nil
			},
		}

		scheduler, obs := newObservedScheduler(t, locker)
		scheduler.tick(t.Context(), &scheduled, 24*time.Hour)

		// The expression as written, so the trace answers "when is this
		// supposed to run" without the reader deriving it from the window.
		obs.ObservedOperationWithData(t, map[string]any{
			jobNameKey:      scheduled.Name,
			jobScheduleKey:  spec,
			jobIntervalKey:  24 * time.Hour,
			keys.LockKeyKey: DefaultLockKeyPrefix + scheduled.Name,
			keys.LockTTLKey: scheduled.LeaseTTL,
			jobRanKey:       true,
		})
	})
}

func TestDescribeSchedule(T *testing.T) {
	T.Parallel()

	T.Run("a parsed cron spec reports itself", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "CRON_TZ=UTC */15 * * * *", describeSchedule(MustCron("*/15 * * * *")))
	})

	T.Run("a caller's schedule falls back to its type", func(t *testing.T) {
		t.Parallel()

		// A pointer address would say nothing about when the job runs, so the
		// type name is the most a Schedule that cannot describe itself gets.
		test.EqOp(t, "jobs.namelessSchedule", describeSchedule(namelessSchedule{}))
	})
}

// namelessSchedule is a Schedule that cannot describe itself, which is every
// Schedule this package did not build.
type namelessSchedule struct{}

func (namelessSchedule) Next(after time.Time) time.Time {
	return after.Add(time.Hour)
}

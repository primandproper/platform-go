package workqueue

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newWakeQueue builds a queue with a wakeup and no database behind it. Wait
// touches neither, which is the point of taking the wakeup as a bare channel.
func newWakeQueue(t *testing.T, wakeup <-chan struct{}, mutate func(*Config)) *Queue[string] {
	t.Helper()

	cfg := validConfig()
	cfg.MinWakeInterval = 100 * time.Millisecond

	if mutate != nil {
		mutate(cfg)
	}

	q, err := New[string](t.Context(), cfg, clientFor(dialect.Postgres), WithWakeup(wakeup))
	must.NoError(t, err)

	t.Cleanup(func() { _ = q.Close(context.WithoutCancel(t.Context())) })

	return q
}

func TestWithWakeup(T *testing.T) {
	T.Parallel()

	T.Run("sets the channel", func(t *testing.T) {
		t.Parallel()

		o := newQueueOptions([]Option{WithWakeup(make(chan struct{}))})

		test.NotNil(t, o.wakeup)
	})

	T.Run("New carries it onto the queue", func(t *testing.T) {
		t.Parallel()

		q := newWakeQueue(t, make(chan struct{}), nil)

		test.NotNil(t, q.wakeup)
	})

	T.Run("a queue without one has no wakeup", func(t *testing.T) {
		t.Parallel()

		q, err := New[string](t.Context(), validConfig(), clientFor(dialect.Postgres))
		must.NoError(t, err)
		t.Cleanup(func() { _ = q.Close(context.WithoutCancel(t.Context())) })

		test.Nil(t, q.wakeup)
	})
}

func TestNew_notifyChannel(T *testing.T) {
	T.Parallel()

	T.Run("accepts an identifier", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.NotifyChannel = "work"

		q, err := New[string](t.Context(), cfg, clientFor(dialect.Postgres))
		must.NoError(t, err)
		t.Cleanup(func() { _ = q.Close(context.WithoutCancel(t.Context())) })
	})

	// The name is bound as text by the statement this package emits, but a
	// listener has to render it into a LISTEN, which takes no parameters.
	T.Run("rejects a channel that is not an identifier", func(t *testing.T) {
		t.Parallel()

		for _, channel := range []string{"work; DROP TABLE users", "work queue", "1work"} {
			cfg := validConfig()
			cfg.NotifyChannel = channel

			_, err := New[string](t.Context(), cfg, clientFor(dialect.Postgres))
			test.ErrorIs(t, err, dialect.ErrInvalidIdentifier, test.Sprintf("channel %q", channel))
		}
	})
}

func TestConfig_wakeFields(T *testing.T) {
	T.Parallel()

	T.Run("EnsureDefaults fills the wake floor and leaves the channel empty", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.EnsureDefaults()

		test.EqOp(t, DefaultMinWakeInterval, cfg.MinWakeInterval)
		test.EqOp(t, "", cfg.NotifyChannel)
	})

	T.Run("EnsureDefaults leaves a set wake floor alone", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.MinWakeInterval = time.Second
		cfg.EnsureDefaults()

		test.EqOp(t, time.Second, cfg.MinWakeInterval)
	})

	T.Run("rejects a wake floor below a millisecond", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.EnsureDefaults()
		cfg.MinWakeInterval = time.Microsecond

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestQueue_Wait(T *testing.T) {
	T.Parallel()

	// The poll is what makes a lost wake survivable, and wakes are lost as a
	// matter of course — so a loop cannot opt out of having one.
	T.Run("rejects a non-positive poll", func(t *testing.T) {
		t.Parallel()

		q := newWakeQueue(t, make(chan struct{}), nil)

		test.ErrorIs(t, q.Wait(t.Context(), 0), ErrInvalidPollInterval)
		test.ErrorIs(t, q.Wait(t.Context(), -time.Second), ErrInvalidPollInterval)
	})

	T.Run("returns when the poll elapses", func(t *testing.T) {
		t.Parallel()

		q := newWakeQueue(t, nil, nil)

		startTime := time.Now()

		must.NoError(t, q.Wait(t.Context(), 50*time.Millisecond))

		test.GreaterEq(t, 50*time.Millisecond, time.Since(startTime))
	})

	T.Run("returns on a wake long before the poll", func(t *testing.T) {
		t.Parallel()

		wakeup := make(chan struct{}, 1)
		q := newWakeQueue(t, wakeup, func(cfg *Config) { cfg.MinWakeInterval = time.Millisecond })

		wakeup <- struct{}{}

		startTime := time.Now()

		must.NoError(t, q.Wait(t.Context(), time.Minute))

		test.Less(t, 10*time.Second, time.Since(startTime))
	})

	T.Run("returns the context's error when it is done", func(t *testing.T) {
		t.Parallel()

		q := newWakeQueue(t, nil, nil)

		ctx, cancel := context.WithCancel(t.Context())
		cancel()

		test.ErrorIs(t, q.Wait(ctx, time.Minute), context.Canceled)
	})

	// A queue taking thousands of enqueues a second would otherwise drive
	// thousands of claim round trips a second, which is more load than polling,
	// not less.
	T.Run("floors how often a wake can return", func(t *testing.T) {
		t.Parallel()

		const (
			floor = 50 * time.Millisecond
			waits = 10
		)

		// Always ready, so nothing but the floor paces the loop.
		wakeup := make(chan struct{}, 1)
		ctx := t.Context()

		go func() {
			for {
				select {
				case wakeup <- struct{}{}:
				case <-ctx.Done():
					return
				}
			}
		}()

		q := newWakeQueue(t, wakeup, func(cfg *Config) { cfg.MinWakeInterval = floor })

		startTime := time.Now()

		for range waits {
			must.NoError(t, q.Wait(t.Context(), time.Minute))
		}

		// Ten wake-driven returns cannot happen faster than nine intervals; the
		// poll is a minute away, so nothing else could have paced them.
		test.GreaterEq(t, (waits-1)*floor, time.Since(startTime))
	})

	// Held, not dropped. A wake inside the floor still returns as soon as the
	// floor elapses, so the last enqueue of a burst is not left for the poll.
	T.Run("holds a wake that lands inside the floor rather than discarding it", func(t *testing.T) {
		t.Parallel()

		const floor = 100 * time.Millisecond

		wakeup := make(chan struct{}, 1)
		q := newWakeQueue(t, wakeup, func(cfg *Config) { cfg.MinWakeInterval = floor })

		wakeup <- struct{}{}
		must.NoError(t, q.Wait(t.Context(), time.Minute))

		wakeup <- struct{}{}

		startTime := time.Now()

		must.NoError(t, q.Wait(t.Context(), time.Minute))

		elapsed := time.Since(startTime)

		// It waited out the floor, and it did not wait out the poll.
		test.GreaterEq(t, floor/2, elapsed)
		test.Less(t, 10*time.Second, elapsed)
	})

	T.Run("a held wake gives up when the context does", func(t *testing.T) {
		t.Parallel()

		wakeup := make(chan struct{}, 1)
		q := newWakeQueue(t, wakeup, func(cfg *Config) { cfg.MinWakeInterval = time.Hour })

		wakeup <- struct{}{}
		must.NoError(t, q.Wait(t.Context(), time.Minute))

		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()

		wakeup <- struct{}{}

		test.ErrorIs(t, q.Wait(ctx, time.Minute), context.DeadlineExceeded)
	})
}

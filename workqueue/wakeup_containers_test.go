package workqueue

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database/postgres"
	"github.com/primandproper/platform-go/v13/database/postgres/pgnotify"
	"github.com/primandproper/platform-go/v13/testutils/containers/pgtest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// Wait is unit-tested against a bare channel, which is the whole argument for
// taking one. What only a real server can show is the other half: that the
// pg_notify Enqueue emits reaches a listener, and that a claim loop waiting on
// it stops paying its poll interval.

const (
	// wakeupPoll is the poll both waits below run on. It is long enough that a
	// Wait returning inside the deadline cannot have been the poll.
	wakeupPoll = 30 * time.Second

	// wakeupDeadline is what "fast" means here — two orders of magnitude below
	// the poll, and still generous for a loopback socket.
	wakeupDeadline = 3 * time.Second
)

// startListener runs a listener against the container and waits out the
// catch-up wake every session opens with, so a wake seen after this returns is
// one an enqueue caused.
func startListener(t *testing.T, dsn, channel string) *pgnotify.Listener {
	t.Helper()

	l, err := pgnotify.NewListener(t.Context(), &pgnotify.Config{ConnectionString: dsn, Channel: channel})
	must.NoError(t, err)

	go l.Run()

	t.Cleanup(func() {
		must.NoError(t, l.Close(context.WithoutCancel(t.Context())))
	})

	select {
	case <-l.Signal():
	case <-time.After(wakeupDeadline):
		t.Fatal("the listener never established a session")
	}

	return l
}

func TestWorkQueue_NotifyWakeup_Containers(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx, &testClientConfig{connectionString: pg.ConnectionString})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		createTable(T, client, "wakeup")

		T.Run("an enqueue wakes a waiting claim loop", func(t *testing.T) {
			t.Parallel()

			const channel = "workqueue_wakeup"

			listener := startListener(t, pg.ConnectionString, channel)

			q := newQueue(t, client, func(cfg *Config) {
				cfg.TablePrefix = "wakeup"
				cfg.NotifyChannel = channel
				cfg.MinWakeInterval = time.Millisecond
			})

			// The Queue under test does the enqueueing as well as the waiting,
			// which is the ordinary single-process case; what matters is that
			// the notification goes out over the wire and comes back.
			must.NoError(t, q.EnqueueKeys(t.Context(), "a"))

			waiter, waiterErr := New[string](t.Context(), &Config{
				Name:            q.Name(),
				TablePrefix:     "wakeup",
				MinWakeInterval: time.Millisecond,
			}, client, WithWakeup(listener.Signal()))
			must.NoError(t, waiterErr)
			t.Cleanup(func() { _ = waiter.Close(context.WithoutCancel(t.Context())) })

			startTime := time.Now()

			must.NoError(t, waiter.Wait(t.Context(), wakeupPoll))

			elapsed := time.Since(startTime)

			t.Logf("wake-driven wait: %s (poll interval is %s)", elapsed, wakeupPoll)

			test.Less(t, wakeupDeadline, elapsed)

			// And the wake was not a false one: the work really is claimable.
			items, claimErr := waiter.Claim(t.Context(), 10, time.Minute)
			must.NoError(t, claimErr)
			must.SliceLen(t, 1, items)
			test.EqOp(t, "a", items[0].Key)
		})

		// The control. Without a channel configured, Enqueue emits nothing and
		// the same loop waits out its poll — which is the latency the wakeup
		// removes, and has to still be there or the measurement above proves
		// nothing.
		T.Run("an enqueue with no channel configured wakes nobody", func(t *testing.T) {
			t.Parallel()

			listener := startListener(t, pg.ConnectionString, "workqueue_unused")

			q := newQueue(t, client, func(cfg *Config) {
				cfg.TablePrefix = "wakeup"
				cfg.MinWakeInterval = time.Millisecond
			})

			must.NoError(t, q.EnqueueKeys(t.Context(), "b"))

			waiter, waiterErr := New[string](t.Context(), &Config{
				Name:            q.Name(),
				TablePrefix:     "wakeup",
				MinWakeInterval: time.Millisecond,
			}, client, WithWakeup(listener.Signal()))
			must.NoError(t, waiterErr)
			t.Cleanup(func() { _ = waiter.Close(context.WithoutCancel(t.Context())) })

			const poll = time.Second

			startTime := time.Now()

			must.NoError(t, waiter.Wait(t.Context(), poll))

			test.GreaterEq(t, poll, time.Since(startTime))

			// The item is there regardless — nothing about the durable path
			// depends on a notification.
			items, claimErr := waiter.Claim(t.Context(), 10, time.Minute)
			must.NoError(t, claimErr)
			test.SliceLen(t, 1, items)
		})
	})
}

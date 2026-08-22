package pgnotify

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/testutils/containers/pgtest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The unit tests above cover the parts that are just Go: the coalescing
// channel, the identifier vetting, the backoff. Nothing there can tell whether
// LISTEN is actually issued, whether a notification crosses the wire, or
// whether a dropped session comes back listening — which is the whole of what
// this file covers, and the only place that can.

// signalTimeout is generous: a notification crosses a loopback socket in
// microseconds, so anything approaching this is a failure rather than a slow
// machine.
const signalTimeout = 15 * time.Second

// startListener builds a listener against the container, runs it, and waits out
// the catch-up wake every session opens with — so a test that returns from this
// knows the LISTEN is standing and a notification sent now cannot be missed.
func startListener(t *testing.T, dsn, channel string) *Listener {
	t.Helper()

	l, err := NewListener(t.Context(), &Config{ConnectionString: dsn, Channel: channel})
	must.NoError(t, err)

	go l.Run()

	t.Cleanup(func() {
		must.NoError(t, l.Close(context.WithoutCancel(t.Context())))
	})

	waitForSignal(t, l, "the catch-up wake that opens every session")

	return l
}

// waitForSignal fails the test unless a wake arrives.
func waitForSignal(t *testing.T, l *Listener, what string) {
	t.Helper()

	select {
	case <-l.Signal():
	case <-time.After(signalTimeout):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// assertNoSignal fails the test if a wake arrives within d.
func assertNoSignal(t *testing.T, l *Listener, d time.Duration, what string) {
	t.Helper()

	select {
	case <-l.Signal():
		t.Fatalf("unexpected wake: %s", what)
	case <-time.After(d):
	}
}

// notify sends one payload-free notification, running the same statement the
// producing packages run. It takes an executor rather than the pool, so the
// tests that notify inside a transaction go through it too — which is where it
// matters most, since that is the property the outbox depends on.
func notify(t *testing.T, q database.SQLQueryExecutor, channel string) {
	t.Helper()

	_, err := q.ExecContext(t.Context(), dialect.PostgresNotifyStatement, channel)
	must.NoError(t, err)
}

func TestListener_Containers(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(_ context.Context, pg *pgtest.Instance) {
		// Each subtest listens on its own channel: they share one server, and a
		// notification is broadcast to every session listening on the name.
		T.Run("wakes on a notification", func(t *testing.T) {
			t.Parallel()

			l := startListener(t, pg.ConnectionString, "wake_basic")

			notify(t, pg.DB, "wake_basic")

			waitForSignal(t, l, "a wake after NOTIFY")
		})

		// Both halves of the coalescing are exercised here: Postgres collapses
		// duplicate (channel, payload) pairs inside one transaction, and the
		// capacity-one channel collapses whatever survives that.
		T.Run("a burst in one transaction becomes one wake", func(t *testing.T) {
			t.Parallel()

			l := startListener(t, pg.ConnectionString, "wake_burst")

			tx, err := pg.DB.BeginTx(t.Context(), nil)
			must.NoError(t, err)

			for range 500 {
				notify(t, tx, "wake_burst")
			}

			must.NoError(t, tx.Commit())

			waitForSignal(t, l, "a wake after a burst")
			assertNoSignal(t, l, time.Second, "a burst produced more than one wake")
		})

		// Nothing is delivered before the commit that sent it, which is the
		// property the outbox is built on: a woken relay cannot look for a row
		// that is not yet visible.
		T.Run("an uncommitted notification is not delivered", func(t *testing.T) {
			t.Parallel()

			l := startListener(t, pg.ConnectionString, "wake_uncommitted")

			tx, err := pg.DB.BeginTx(t.Context(), nil)
			must.NoError(t, err)

			notify(t, tx, "wake_uncommitted")

			assertNoSignal(t, l, time.Second, "a notification arrived before its transaction committed")

			must.NoError(t, tx.Rollback())

			assertNoSignal(t, l, time.Second, "a rolled back notification was delivered")
		})

		// The gap is the reason a wake can only ever be a hint: everything sent
		// between the drop and the re-LISTEN is gone. The listener answers that
		// by waking unconditionally on every new session.
		T.Run("reconnects, re-listens, and wakes for the gap", func(t *testing.T) {
			t.Parallel()

			const channel = "wake_reconnect"

			l := startListener(t, pg.ConnectionString, channel)

			terminateListener(t, pg.DB, channel)

			waitForSignal(t, l, "the catch-up wake after a reconnect")

			// The LISTEN is standing again on the new session, which is only
			// observable by sending to it.
			notify(t, pg.DB, channel)

			waitForSignal(t, l, "a wake after NOTIFY on the reconnected session")
		})

		// A channel nobody notifies stays quiet: a listener does not wake on
		// traffic that is not its own.
		T.Run("ignores notifications on other channels", func(t *testing.T) {
			t.Parallel()

			l := startListener(t, pg.ConnectionString, "wake_mine")

			notify(t, pg.DB, "wake_someone_elses")

			assertNoSignal(t, l, time.Second, "a wake arrived for another channel")
		})
	})
}

// terminateListener kills the backend holding the LISTEN for channel, which is
// how this suite simulates a failover or an idle-connection reaper without
// touching the container itself.
//
// It retries: pg_stat_activity reports the listening session only once its
// LISTEN has been recorded as the current query, which is a moment after the
// wake this test already waited for.
func terminateListener(t *testing.T, db *sql.DB, channel string) {
	t.Helper()

	const query = `
		SELECT count(pg_terminate_backend(pid))
		FROM pg_stat_activity
		WHERE pid <> pg_backend_pid()
		  AND query = $1
	`

	listen := `LISTEN "` + channel + `"`

	for range 100 {
		var terminated int

		must.NoError(t, db.QueryRowContext(t.Context(), query, listen).Scan(&terminated))

		if terminated > 0 {
			return
		}

		time.Sleep(100 * time.Millisecond)
	}

	test.Unreachable(t, test.Sprintf("no backend was listening on %q to terminate", channel))
}

package timers

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/database/postgres"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/testutils/containers/pgtest"
	"github.com/primandproper/platform-go/v13/timers/migrations"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The unit tests above render SQL and sort batches; nothing there can tell
// whether the statements are accepted, whether SKIP LOCKED actually keeps two
// claimants apart, whether a lease really lapses on the server's clock, or
// whether the run_at fence really stops a stale Complete. That is the whole of
// what this file covers, and it is the only place that can.

// testClientConfig is the minimum database.ClientConfig a Postgres client needs.
// The pool is deliberately larger than one connection: the properties worth
// testing here are all concurrent.
type testClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 1 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Millisecond }
func (c *testClientConfig) GetMaxIdleConns() int              { return 8 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 16 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// setCounter names a fresh logical set per subtest. Subtests share one table, so
// they must not share a set — one test's backlog would be another's. That they
// can share the table at all is itself the property Config.Name exists for.
var setCounter atomic.Uint64

// createTable renders and executes the shipped DDL under a namespace.
func createTable(t *testing.T, client database.Client, prefix string) {
	t.Helper()

	stmts, err := migrations.Statements(dialect.Postgres, prefix)
	must.NoError(t, err)
	must.SliceNotEmpty(t, stmts)

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}
}

// newSet builds a Timers on its own logical set.
func newSet(t *testing.T, client database.Client, mutate func(*Config), opts ...Option) *Timers[string] {
	t.Helper()

	cfg := &Config{Name: fmt.Sprintf("s%d", setCounter.Add(1))}
	if mutate != nil {
		mutate(cfg)
	}

	set, err := New[string](t.Context(), cfg, client, opts...)
	must.NoError(t, err)

	return set
}

// dueKeys collects the keys out of a claim, for the assertions that care about
// which timers came back rather than about their metadata.
func dueKeys(fired []Due[string]) []string {
	keys := make([]string, 0, len(fired))
	for i := range fired {
		keys = append(keys, fired[i].Key)
	}

	return keys
}

// past and future are instants far enough either side of now that no test's
// runtime can cross them.
func past() time.Time   { return time.Now().Add(-time.Hour) }
func future() time.Time { return time.Now().Add(24 * time.Hour) }

func TestTimers_Postgres(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx, &testClientConfig{connectionString: pg.ConnectionString})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		createTable(T, client, DefaultTablePrefix)

		runTimerSuite(T, client)
	})
}

//nolint:maintidx // one behavioral contract per subtest; splitting it would only hide the list.
func runTimerSuite(t *testing.T, client database.Client) {
	t.Helper()

	t.Run("schedule, claim, complete", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, nil)

		must.NoError(t, set.ScheduleAt(t.Context(), "a", past(), nil))
		must.NoError(t, set.ScheduleAt(t.Context(), "b", past(), []byte("note")))

		fired, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 2, fired)
		test.Eq(t, []string{"a", "b"}, dueKeys(fired))

		for i := range fired {
			test.EqOp(t, 1, fired[i].Attempts)
			test.False(t, fired[i].Reclaimed)
			test.True(t, fired[i].Late > 0)
		}

		must.NoError(t, set.Complete(t.Context(), fired...))

		stats, err := set.Stats(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), stats.Outstanding)
		test.EqOp(t, int64(2), stats.Fired)
	})

	// The whole durability claim: the schedule is a row, so a payload written by
	// one process comes back byte-for-byte to another.
	t.Run("a payload round-trips, and nil stays nil", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, nil)

		must.NoError(t, set.Schedule(t.Context(),
			Timer[string]{Key: "with", RunAt: past(), Payload: []byte("hello \x00 bytes")},
			Timer[string]{Key: "without", RunAt: past()},
			Timer[string]{Key: "empty", RunAt: past(), Payload: []byte{}},
		))

		fired, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 3, fired)

		byKey := map[string][]byte{}
		for i := range fired {
			byKey[fired[i].Key] = fired[i].Payload
		}

		test.Eq(t, []byte("hello \x00 bytes"), byKey["with"])
		test.Nil(t, byKey["without"])
		test.NotNil(t, byKey["empty"])
		test.SliceEmpty(t, byKey["empty"])
	})

	// The instant is the schedule. A timer that is not owed yet is invisible to
	// every claimant, however deep the backlog.
	t.Run("a timer scheduled for the future is not claimable", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, nil)

		must.NoError(t, set.ScheduleAt(t.Context(), "later", future(), nil))
		must.NoError(t, set.ScheduleAt(t.Context(), "now", past(), nil))

		fired, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 1, fired)
		test.EqOp(t, "now", fired[0].Key)
	})

	// The lease is the whole point: a firing handed to one worker is invisible to
	// every other until it lapses or is given back.
	t.Run("a leased timer is not claimed again", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, nil)
		must.NoError(t, set.ScheduleAt(t.Context(), "only", past(), nil))

		first, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 1, first)

		second, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		test.SliceEmpty(t, second)
	})

	// Failure recovery, in full: nothing detects the dead worker, the lease
	// simply runs out on the server's clock and somebody else picks it up.
	t.Run("a lapsed lease returns the firing, flagged", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, nil)
		must.NoError(t, set.ScheduleAt(t.Context(), "abandoned", past(), nil))

		first, err := set.Claim(t.Context(), 10, 200*time.Millisecond)
		must.NoError(t, err)
		must.SliceLen(t, 1, first)

		time.Sleep(400 * time.Millisecond)

		second, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 1, second)
		test.EqOp(t, "abandoned", second[0].Key)
		test.EqOp(t, 2, second[0].Attempts)
		test.True(t, second[0].Reclaimed)
	})

	t.Run("the oldest debt fires first", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, nil)

		must.NoError(t, set.Schedule(t.Context(),
			Timer[string]{Key: "recent", RunAt: time.Now().Add(-time.Minute)},
			Timer[string]{Key: "ancient", RunAt: time.Now().Add(-24 * time.Hour)},
		))

		fired, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 2, fired)
		test.Eq(t, []string{"ancient", "recent"}, dueKeys(fired))
	})

	// The rule that separates this from a work queue's enqueue. A merge that only
	// moved things earlier could not express the case the feature exists for.
	t.Run("rescheduling moves a timer in either direction", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, nil)

		must.NoError(t, set.ScheduleAt(t.Context(), "trial", past(), nil))
		must.NoError(t, set.ScheduleAt(t.Context(), "trial", future(), nil))

		fired, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceEmpty(t, fired)

		must.NoError(t, set.ScheduleAt(t.Context(), "trial", past(), nil))

		fired, err = set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		test.SliceLen(t, 1, fired)
	})

	t.Run("rescheduling replaces the payload and resets the attempt count", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, nil)

		must.NoError(t, set.ScheduleAt(t.Context(), "k", past(), []byte("first")))

		first, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 1, first)
		test.EqOp(t, 1, first[0].Attempts)

		must.NoError(t, set.ScheduleAt(t.Context(), "k", past(), []byte("second")))

		second, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 1, second)
		test.Eq(t, []byte("second"), second[0].Payload)
		test.EqOp(t, 1, second[0].Attempts)
	})

	// The fence, which is this package's answer to the one race a work queue
	// does not have. The first worker is still firing when the schedule moves;
	// its Complete must not retire the new schedule.
	t.Run("a stale Complete cannot retire a rescheduled timer", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, nil)
		must.NoError(t, set.ScheduleAt(t.Context(), "moved", past(), nil))

		inFlight, err := set.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)
		must.SliceLen(t, 1, inFlight)

		// The trial gets extended while the expiry job is mid-flight.
		must.NoError(t, set.ScheduleAt(t.Context(), "moved", past().Add(time.Minute), nil))

		// The worker finishes and reports the instant it was handed.
		must.NoError(t, set.Complete(t.Context(), inFlight...))

		stats, err := set.Stats(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), stats.Fired)
		test.EqOp(t, int64(1), stats.Outstanding)

		// And the new schedule is still live.
		fired, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		test.SliceLen(t, 1, fired)
	})

	// The other half of the rule. An at-least-once upstream redelivering "start
	// trial" must not free a row somebody is firing right now, or a second
	// worker fires it too.
	t.Run("rescheduling to the same instant leaves a live lease alone", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, nil)

		instant := past()
		must.NoError(t, set.ScheduleAt(t.Context(), "redelivered", instant, nil))

		inFlight, err := set.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)
		must.SliceLen(t, 1, inFlight)

		must.NoError(t, set.ScheduleAt(t.Context(), "redelivered", instant, nil))

		// Still held, so nobody else picks it up.
		again, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceEmpty(t, again)

		// And the instant still matches, so the worker in flight retires it.
		must.NoError(t, set.Complete(t.Context(), inFlight...))

		stats, err := set.Stats(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(1), stats.Fired)
		test.EqOp(t, int64(0), stats.Outstanding)
	})

	t.Run("a matching Complete retires the timer for good", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, nil)
		must.NoError(t, set.ScheduleAt(t.Context(), "done", past(), nil))

		fired, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 1, fired)
		must.NoError(t, set.Complete(t.Context(), fired...))

		again, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		test.SliceEmpty(t, again)
	})

	// Completing releases the lease too, so a rescheduled timer is immediately
	// claimable rather than waiting out the lease it was completed under.
	t.Run("completing a timer releases its lease", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, nil)
		must.NoError(t, set.ScheduleAt(t.Context(), "recycled", past(), nil))

		fired, err := set.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)
		must.NoError(t, set.Complete(t.Context(), fired...))
		must.NoError(t, set.ScheduleAt(t.Context(), "recycled", past(), nil))

		again, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 1, again)
		test.EqOp(t, 1, again[0].Attempts)
	})

	t.Run("release with a delay pushes the timer out", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, nil)
		must.NoError(t, set.ScheduleAt(t.Context(), "backed-off", past(), nil))

		fired, err := set.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)
		must.SliceLen(t, 1, fired)

		must.NoError(t, set.Release(t.Context(), time.Hour, platformerrors.New("try later"), fired...))

		again, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		test.SliceEmpty(t, again)
	})

	t.Run("release with no delay hands the firing straight back", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, nil)
		must.NoError(t, set.ScheduleAt(t.Context(), "handed-back", past(), nil))

		fired, err := set.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)

		must.NoError(t, set.Release(t.Context(), 0, platformerrors.New("not my problem"), fired...))

		again, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 1, again)
		// The attempt count survives the release, so a repeatedly failing timer
		// still walks toward its ceiling.
		test.EqOp(t, 2, again[0].Attempts)
	})

	// A late release arriving after somebody else finished the work would
	// otherwise resurrect it, and the pair would loop forever.
	t.Run("release cannot resurrect a fired timer", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, nil)
		must.NoError(t, set.ScheduleAt(t.Context(), "finished", past(), nil))

		fired, err := set.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)
		must.NoError(t, set.Complete(t.Context(), fired...))
		must.NoError(t, set.Release(t.Context(), 0, nil, fired...))

		again, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		test.SliceEmpty(t, again)
	})

	// The count is the answer to the question a cancel actually asks: did I beat
	// the firing?
	t.Run("cancel reports whether it beat the firing", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, nil)
		must.NoError(t, set.ScheduleAt(t.Context(), "called-off", future(), nil))

		cancelled, err := set.Cancel(t.Context(), "called-off")
		must.NoError(t, err)
		test.EqOp(t, int64(1), cancelled)

		cancelled, err = set.Cancel(t.Context(), "called-off")
		must.NoError(t, err)
		test.EqOp(t, int64(0), cancelled)

		cancelled, err = set.Cancel(t.Context(), "never-scheduled")
		must.NoError(t, err)
		test.EqOp(t, int64(0), cancelled)
	})

	t.Run("cancel removes a timer whatever its state", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, nil)

		must.NoError(t, set.Schedule(t.Context(),
			Timer[string]{Key: "waiting", RunAt: future()},
			Timer[string]{Key: "leased", RunAt: past()},
		))

		fired, err := set.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)
		must.SliceLen(t, 1, fired)

		cancelled, err := set.Cancel(t.Context(), "waiting", "leased")
		must.NoError(t, err)
		test.EqOp(t, int64(2), cancelled)

		stats, err := set.Stats(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), stats.Outstanding)
	})

	// Once a timer has stalled out of attempts it is invisible to every claim,
	// so one broken row cannot spend the whole fleet's firing budget.
	t.Run("a timer out of attempts stalls rather than spinning", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, func(cfg *Config) { cfg.MaxAttempts = 2 })
		must.NoError(t, set.ScheduleAt(t.Context(), "poison", past(), nil))

		for range 2 {
			fired, claimErr := set.Claim(t.Context(), 10, time.Minute)
			must.NoError(t, claimErr)
			must.SliceLen(t, 1, fired)
			must.NoError(t, set.Release(t.Context(), 0, platformerrors.New("boom"), fired...))
		}

		fired, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceEmpty(t, fired)

		stats, err := set.Stats(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(1), stats.Stalled)
		test.EqOp(t, int64(0), stats.Due)
	})

	t.Run("an unlimited ceiling never stalls", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, func(cfg *Config) { cfg.MaxAttempts = -1 })
		must.NoError(t, set.ScheduleAt(t.Context(), "forever", past(), nil))

		for range 3 {
			fired, claimErr := set.Claim(t.Context(), 10, time.Minute)
			must.NoError(t, claimErr)
			must.SliceLen(t, 1, fired)
			must.NoError(t, set.Release(t.Context(), 0, nil, fired...))
		}

		stats, err := set.Stats(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), stats.Stalled)
		test.EqOp(t, int64(1), stats.Due)
	})

	t.Run("reap removes fired timers past their retention", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, func(cfg *Config) { cfg.Retention = time.Second })
		must.NoError(t, set.ScheduleAt(t.Context(), "old", past(), nil))

		fired, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.NoError(t, set.Complete(t.Context(), fired...))

		reaped, err := set.Reap(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), reaped)

		time.Sleep(1100 * time.Millisecond)

		reaped, err = set.Reap(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(1), reaped)
	})

	t.Run("stats separate what is outstanding from what is owed", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, nil)

		must.NoError(t, set.Schedule(t.Context(),
			Timer[string]{Key: "soon", RunAt: past()},
			Timer[string]{Key: "later", RunAt: future()},
			Timer[string]{Key: "leased", RunAt: past()},
		))

		fired, err := set.Claim(t.Context(), 1, time.Hour)
		must.NoError(t, err)
		must.SliceLen(t, 1, fired)

		stats, err := set.Stats(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(3), stats.Outstanding)
		test.EqOp(t, int64(1), stats.Due)
		test.EqOp(t, int64(1), stats.Leased)
		test.True(t, stats.OldestDueLateness > 0)
	})

	t.Run("next due reports the nearest outstanding instant", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, nil)

		_, found, err := set.NextDue(t.Context())
		must.NoError(t, err)
		test.False(t, found)

		must.NoError(t, set.ScheduleAt(t.Context(), "far", time.Now().Add(24*time.Hour), nil))
		must.NoError(t, set.ScheduleAt(t.Context(), "near", time.Now().Add(time.Hour), nil))

		next, found, err := set.NextDue(t.Context())
		must.NoError(t, err)
		must.True(t, found)
		test.True(t, next > 50*time.Minute)
		test.True(t, next < 70*time.Minute)
	})

	// A poller that measured to the long-past instant of a leased row would wake
	// at once and claim nothing until the lease lapsed.
	t.Run("next due measures a leased timer to its lease expiry", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, nil)
		must.NoError(t, set.ScheduleAt(t.Context(), "held", past(), nil))

		next, found, err := set.NextDue(t.Context())
		must.NoError(t, err)
		must.True(t, found)
		test.True(t, next < 0)

		fired, err := set.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)
		must.SliceLen(t, 1, fired)

		next, found, err = set.NextDue(t.Context())
		must.NoError(t, err)
		must.True(t, found)
		test.True(t, next > 30*time.Minute)
	})

	// A stalled timer will never be claimed again, so counting it would keep a
	// poller awake forever for work that is never coming.
	t.Run("next due ignores stalled timers", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, func(cfg *Config) { cfg.MaxAttempts = 1 })
		must.NoError(t, set.ScheduleAt(t.Context(), "poison", past(), nil))

		fired, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 1, fired)
		must.NoError(t, set.Release(t.Context(), 0, platformerrors.New("boom"), fired...))

		_, found, err := set.NextDue(t.Context())
		must.NoError(t, err)
		test.False(t, found)
	})

	// Wait's whole reason to exist: sleep until the instant, not through it.
	t.Run("wait returns when the next timer is nearer than the poll", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, func(cfg *Config) { cfg.MinWakeInterval = 10 * time.Millisecond })
		must.NoError(t, set.ScheduleAt(t.Context(), "soon", time.Now().Add(300*time.Millisecond), nil))

		start := time.Now()
		must.NoError(t, set.Wait(t.Context(), time.Hour))
		elapsed := time.Since(start)

		test.True(t, elapsed < 5*time.Second, test.Sprintf("waited %s, expected to wake for the timer", elapsed))

		fired, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		test.SliceLen(t, 1, fired)
	})

	// A due timer that this claimant cannot get — a fleet-mate has it — must not
	// turn Wait into a spin.
	t.Run("wait floors at the wake interval when something is already due", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, func(cfg *Config) { cfg.MinWakeInterval = 200 * time.Millisecond })
		must.NoError(t, set.ScheduleAt(t.Context(), "due", past(), nil))

		start := time.Now()
		must.NoError(t, set.Wait(t.Context(), time.Hour))

		test.True(t, time.Since(start) >= 150*time.Millisecond)
	})

	t.Run("a wakeup cuts a long wait short", func(t *testing.T) {
		t.Parallel()

		wakeup := make(chan struct{}, 1)
		set := newSet(t, client, nil, WithWakeup(wakeup))

		wakeup <- struct{}{}

		start := time.Now()
		must.NoError(t, set.Wait(t.Context(), time.Hour))

		test.True(t, time.Since(start) < 5*time.Second)
	})

	// The property the whole design rests on. Twelve claimants, one hundred
	// timers, all due: every timer must be handed out exactly once.
	t.Run("a fleet of claimants never sees the same firing twice", func(t *testing.T) {
		t.Parallel()

		const (
			claimants = 12
			total     = 100
		)

		set := newSet(t, client, nil)

		scheduled := make([]Timer[string], 0, total)
		for i := range total {
			scheduled = append(scheduled, Timer[string]{Key: fmt.Sprintf("k%03d", i), RunAt: past()})
		}

		must.NoError(t, set.Schedule(t.Context(), scheduled...))

		var (
			mu   sync.Mutex
			seen = map[string]int{}
			wg   sync.WaitGroup
		)

		for range claimants {
			wg.Go(func() {
				for {
					fired, err := set.Claim(t.Context(), 7, time.Minute)
					if err != nil || len(fired) == 0 {
						return
					}

					mu.Lock()
					for i := range fired {
						seen[fired[i].Key]++
					}
					mu.Unlock()

					_ = set.Complete(t.Context(), fired...)
				}
			})
		}

		wg.Wait()

		must.MapLen(t, total, seen)
		for key, count := range seen {
			test.EqOp(t, 1, count, test.Sprintf("key %q was claimed %d times", key, count))
		}
	})

	// Postgres applies the LIMIT above the lock, so a row a competitor holds is
	// skipped and replaced rather than counted against the batch. Pushed into a
	// subquery beneath the lock this would return short batches under contention
	// — correct, and quietly half the throughput.
	t.Run("a claim gets a full batch despite a competitor holding rows", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, nil)

		scheduled := make([]Timer[string], 0, 20)
		for i := range 20 {
			scheduled = append(scheduled, Timer[string]{Key: fmt.Sprintf("b%02d", i), RunAt: past()})
		}

		must.NoError(t, set.Schedule(t.Context(), scheduled...))

		held, err := set.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)
		must.SliceLen(t, 10, held)

		rest, err := set.Claim(t.Context(), 10, time.Hour)
		must.NoError(t, err)
		test.SliceLen(t, 10, rest)
	})

	// The batch ceiling belongs to the set, not to the caller. An unspecified
	// limit means "whatever the set allows" rather than "everything owed", and a
	// limit above the ceiling is brought back down to it — so no caller can
	// lease an unbounded batch by asking for one.
	t.Run("a claim limit is clamped to the configured ceiling", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, func(cfg *Config) { cfg.MaxClaimBatch = 3 })

		scheduled := make([]Timer[string], 0, 8)
		for i := range 8 {
			scheduled = append(scheduled, Timer[string]{Key: fmt.Sprintf("l%02d", i), RunAt: past()})
		}

		must.NoError(t, set.Schedule(t.Context(), scheduled...))

		unspecified, err := set.Claim(t.Context(), 0, time.Hour)
		must.NoError(t, err)
		test.SliceLen(t, 3, unspecified)

		overTheCeiling, err := set.Claim(t.Context(), 100, time.Hour)
		must.NoError(t, err)
		test.SliceLen(t, 3, overTheCeiling)
	})

	// Two sets over one table share nothing but storage, which is the property
	// Config.Name exists for.
	t.Run("sets are isolated from each other", func(t *testing.T) {
		t.Parallel()

		first := newSet(t, client, nil)
		second := newSet(t, client, nil)

		must.NoError(t, first.ScheduleAt(t.Context(), "shared-key", past(), nil))

		fired, err := second.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		test.SliceEmpty(t, fired)

		fired, err = first.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		test.SliceLen(t, 1, fired)
	})

	t.Run("a custom codec decides what lands in the primary key", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, nil, WithKeyCodec[string](upperCodec{}))

		must.NoError(t, set.ScheduleAt(t.Context(), "lower", past(), nil))

		fired, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 1, fired)
		test.EqOp(t, "lower", fired[0].Key)

		var stored string

		must.NoError(t, client.Reader().QueryRowContext(t.Context(),
			"SELECT timer_key FROM scheduled_timers WHERE timer_set = $1", set.Name()).Scan(&stored))
		test.EqOp(t, "LOWER", stored)
	})

	t.Run("ScheduleIn resolves a delay against the clock", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, nil)

		must.NoError(t, set.ScheduleIn(t.Context(), "soon", -time.Minute, nil))
		must.NoError(t, set.ScheduleIn(t.Context(), "later", 24*time.Hour, nil))

		fired, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 1, fired)
		test.EqOp(t, "soon", fired[0].Key)
	})

	t.Run("the last word wins inside one batch", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, nil)

		must.NoError(t, set.Schedule(t.Context(),
			Timer[string]{Key: "k", RunAt: past(), Payload: []byte("first")},
			Timer[string]{Key: "k", RunAt: past(), Payload: []byte("last")},
		))

		fired, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 1, fired)
		test.Eq(t, []byte("last"), fired[0].Payload)
	})

	t.Run("a namespaced table is a different table", func(t *testing.T) {
		t.Parallel()

		createTable(t, client, "ddb")

		set := newSet(t, client, func(cfg *Config) { cfg.TablePrefix = "ddb" })
		must.NoError(t, set.ScheduleAt(t.Context(), "namespaced", past(), nil))

		fired, err := set.Claim(t.Context(), 10, time.Minute)
		must.NoError(t, err)
		must.SliceLen(t, 1, fired)

		var count int

		must.NoError(t, client.Reader().QueryRowContext(t.Context(),
			"SELECT COUNT(*) FROM ddb_scheduled_timers WHERE timer_set = $1", set.Name()).Scan(&count))
		test.EqOp(t, 1, count)
	})
}

func TestWorker_Postgres(T *testing.T) {
	T.Parallel()

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		client, err := postgres.NewDatabaseClient(ctx, &testClientConfig{connectionString: pg.ConnectionString})
		must.NoError(T, err)
		T.Cleanup(func() { _ = client.Close() })

		createTable(T, client, DefaultTablePrefix)

		runWorkerSuite(T, client)
	})
}

func runWorkerSuite(t *testing.T, client database.Client) {
	t.Helper()

	// The end-to-end claim: a timer written by one call is fired by a loop that
	// was already asleep when it landed.
	t.Run("fires due timers and retires them", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, func(cfg *Config) { cfg.MinWakeInterval = 10 * time.Millisecond })

		handled := make(chan string, 4)

		worker, err := NewWorker(t.Context(), &WorkerConfig{
			Poll:  50 * time.Millisecond,
			Lease: time.Minute,
			Batch: 5,
		}, set, func(_ context.Context, due Due[string]) error {
			handled <- due.Key

			return nil
		})
		must.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		done := make(chan error, 1)
		go func() { done <- worker.Run(ctx) }()

		must.NoError(t, set.ScheduleAt(t.Context(), "fire-me", past(), []byte("payload")))

		select {
		case key := <-handled:
			test.EqOp(t, "fire-me", key)
		case <-time.After(10 * time.Second):
			t.Fatal("the worker never fired the timer")
		}

		cancel()
		<-done

		stats, err := set.Stats(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(0), stats.Outstanding)
		test.EqOp(t, int64(1), stats.Fired)
	})

	// A full batch means more is owed right now, so the loop goes straight round
	// instead of waiting. Without that, a backlog drains at one batch per wake
	// and a thousand overdue timers take a thousand sleeps to clear.
	//
	// The wake floor is what makes this observable: at ten seconds a single wait
	// would outlast the deadline below by itself, so finishing at all is the
	// proof that no wait happened between the full batches.
	t.Run("drains a backlog without waiting between full batches", func(t *testing.T) {
		t.Parallel()

		const backlog = 10

		set := newSet(t, client, func(cfg *Config) { cfg.MinWakeInterval = 10 * time.Second })

		scheduled := make([]Timer[string], 0, backlog)
		for i := range backlog {
			scheduled = append(scheduled, Timer[string]{Key: fmt.Sprintf("d%02d", i), RunAt: past()})
		}

		must.NoError(t, set.Schedule(t.Context(), scheduled...))

		handled := make(chan string, backlog)

		worker, err := NewWorker(t.Context(), &WorkerConfig{
			Poll:  time.Minute,
			Lease: time.Minute,
			Batch: 2,
		}, set, func(_ context.Context, due Due[string]) error {
			handled <- due.Key

			return nil
		})
		must.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		done := make(chan error, 1)
		go func() { done <- worker.Run(ctx) }()

		deadline := time.After(5 * time.Second)
		for range backlog {
			select {
			case <-handled:
			case <-deadline:
				t.Fatal("the worker paced the backlog instead of going straight round")
			}
		}

		cancel()
		<-done
	})

	// A failing handler must put the firing back rather than retiring it, and
	// must record why on the row.
	t.Run("a failing handler releases the firing with its cause", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, func(cfg *Config) { cfg.MinWakeInterval = 10 * time.Millisecond })
		must.NoError(t, set.ScheduleAt(t.Context(), "fails", past(), nil))

		attempts := make(chan int, 8)

		worker, err := NewWorker(t.Context(), &WorkerConfig{
			Poll:       50 * time.Millisecond,
			Lease:      time.Minute,
			RetryDelay: time.Hour,
			Batch:      5,
		}, set, func(_ context.Context, due Due[string]) error {
			attempts <- due.Attempts

			return platformerrors.New("downstream is down")
		})
		must.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		done := make(chan error, 1)
		go func() { done <- worker.Run(ctx) }()

		select {
		case <-attempts:
		case <-time.After(10 * time.Second):
			t.Fatal("the worker never fired the timer")
		}

		// The retry delay is an hour, so nothing should come back around.
		select {
		case <-attempts:
			t.Fatal("the worker fired a timer it had just backed off for an hour")
		case <-time.After(500 * time.Millisecond):
		}

		cancel()
		<-done

		var lastError string

		must.NoError(t, client.Reader().QueryRowContext(t.Context(),
			"SELECT COALESCE(last_error, '') FROM scheduled_timers WHERE timer_set = $1", set.Name()).
			Scan(&lastError))
		test.True(t, strings.Contains(lastError, "downstream is down"))

		stats, err := set.Stats(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(1), stats.Outstanding)
		test.EqOp(t, int64(0), stats.Fired)
	})

	// One bad timer must not take the loop down, and the firing must come back
	// rather than being silently retired.
	t.Run("a panicking handler is contained and the firing survives", func(t *testing.T) {
		t.Parallel()

		set := newSet(t, client, func(cfg *Config) { cfg.MinWakeInterval = 10 * time.Millisecond })
		must.NoError(t, set.ScheduleAt(t.Context(), "explodes", past(), nil))

		reached := make(chan struct{}, 4)

		worker, err := NewWorker(t.Context(), &WorkerConfig{
			Poll:       50 * time.Millisecond,
			Lease:      time.Minute,
			RetryDelay: time.Hour,
			Batch:      5,
		}, set, func(context.Context, Due[string]) error {
			reached <- struct{}{}

			panic("handler exploded")
		})
		must.NoError(t, err)

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		done := make(chan error, 1)
		go func() { done <- worker.Run(ctx) }()

		select {
		case <-reached:
		case <-time.After(10 * time.Second):
			t.Fatal("the worker never fired the timer")
		}

		cancel()

		// The loop is still the one that exits, rather than the panic taking it.
		test.ErrorIs(t, <-done, context.Canceled)

		stats, err := set.Stats(t.Context())
		must.NoError(t, err)
		test.EqOp(t, int64(1), stats.Outstanding)
	})
}

package jobs_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	clockmock "github.com/primandproper/platform-go/v13/clock/mock"
	"github.com/primandproper/platform-go/v13/distributedlock"
	"github.com/primandproper/platform-go/v13/distributedlock/memory"
	distributedlockmock "github.com/primandproper/platform-go/v13/distributedlock/mock"
	"github.com/primandproper/platform-go/v13/jobs"
	lognoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// testInterval is long enough that no assertion below could be satisfied by an
// accidental second tick, and free either way: every scheduler test runs inside
// a synctest bubble, where the wait costs no wall time.
const testInterval = time.Minute

// errJob is the failure the scheduler tests return from a job.
var errJob = errors.New("job exploded")

func newTestLocker(t *testing.T) distributedlock.Locker {
	t.Helper()

	locker, err := memory.NewLocker()
	must.NoError(t, err)

	return locker
}

// newTestScheduler builds a Scheduler over a real in-memory lock. It is a real
// lock rather than a noop precisely because mutual exclusion is the behavior
// under test.
func newTestScheduler(t *testing.T, opts ...jobs.SchedulerOption) *jobs.Scheduler {
	t.Helper()

	scheduler, err := jobs.NewScheduler(t.Context(), &jobs.SchedulerConfig{}, newTestLocker(t), opts...)
	must.NoError(t, err)
	must.NotNil(t, scheduler)

	return scheduler
}

// runScheduler starts a Scheduler inside a bubble and closes it when the test
// ends, so a bubble never exits with a goroutine still running in it.
func runScheduler(t *testing.T, scheduler *jobs.Scheduler) {
	t.Helper()

	go scheduler.Run()

	t.Cleanup(func() {
		must.NoError(t, scheduler.Close(context.Background()))
	})
}

func TestNewScheduler(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, newTestScheduler(t))
	})

	T.Run("with nil config", func(t *testing.T) {
		t.Parallel()

		_, err := jobs.NewScheduler(t.Context(), nil, newTestLocker(t))
		test.Error(t, err)
	})

	T.Run("with nil locker", func(t *testing.T) {
		t.Parallel()

		_, err := jobs.NewScheduler(t.Context(), &jobs.SchedulerConfig{}, nil)
		test.ErrorIs(t, err, jobs.ErrNilLocker)
	})

	T.Run("with a sub-second lease TTL", func(t *testing.T) {
		t.Parallel()

		_, err := jobs.NewScheduler(t.Context(), &jobs.SchedulerConfig{DefaultLeaseTTL: time.Millisecond}, newTestLocker(t))
		test.Error(t, err)
	})

	T.Run("with a failing instrument", func(t *testing.T) {
		t.Parallel()

		// Every instrument the Scheduler creates, so that adding one without a
		// matching error path shows up here rather than as a nil counter on the
		// first tick.
		instruments := []string{
			"jobs_scheduler_runs",
			"jobs_scheduler_failures",
			"jobs_scheduler_skipped",
			"jobs_scheduler_panics",
			"jobs_scheduler_lock_errors",
			"jobs_scheduler_leases_expired",
			"jobs_scheduler_overruns",
			"jobs_scheduler_run_latency_ms",
		}

		for _, instrument := range instruments {
			t.Run(instrument, func(t *testing.T) {
				t.Parallel()

				_, err := jobs.NewScheduler(t.Context(), &jobs.SchedulerConfig{}, newTestLocker(t),
					jobs.WithSchedulerMetricsProvider(failingInstruments(instrument)))
				test.ErrorIs(t, err, errInstrument)
			})
		}
	})
}

func TestSchedulerOptions(T *testing.T) {
	T.Parallel()

	T.Run("WithSchedulerClock drives the tickers", func(t *testing.T) {
		t.Parallel()

		ticks := make(chan time.Time, 1)
		ran := make(chan struct{}, 1)

		c := &clockmock.ClockMock{
			NowFunc:   time.Now,
			SinceFunc: time.Since,
			NewTickerFunc: func(time.Duration) clock.Ticker {
				return &clockmock.TickerMock{
					ChanFunc: func() <-chan time.Time { return ticks },
					StopFunc: func() {},
				}
			},
		}

		scheduler := newTestScheduler(t, jobs.WithSchedulerClock(c))
		must.NoError(t, scheduler.Register(jobs.Job{
			// An interval no test could wait out, so the only thing that can
			// fire this job is the injected ticker.
			Name:     "hand-cranked",
			Interval: 24 * time.Hour,
			Run: func(context.Context) error {
				ran <- struct{}{}

				return nil
			},
		}))

		runScheduler(t, scheduler)

		ticks <- time.Now()
		recv(t, ran, "the job firing on the injected tick")
	})

	T.Run("WithSchedulerLogger and WithSchedulerTracerProvider are wired without disturbing the scheduler", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ran := make(chan struct{}, 1)

			scheduler := newTestScheduler(t,
				jobs.WithSchedulerLogger(lognoop.NewLogger()),
				jobs.WithSchedulerTracerProvider(tracingnoop.NewTracerProvider()),
			)
			must.NoError(t, scheduler.Register(jobs.Job{
				Name:       "observed",
				Interval:   time.Hour,
				RunOnStart: true,
				Run: func(context.Context) error {
					ran <- struct{}{}

					return nil
				},
			}))

			runScheduler(t, scheduler)

			<-ran
		})
	})

	T.Run("ignores nil options and nil values", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ran := make(chan struct{}, 1)

			// A nil clock is dropped rather than installed, so the Scheduler
			// still has the wall clock its tickers need.
			scheduler := newTestScheduler(t, nil, jobs.WithSchedulerClock(nil))
			must.NoError(t, scheduler.Register(jobs.Job{
				Name:       "defaulted",
				Interval:   time.Hour,
				RunOnStart: true,
				Run: func(context.Context) error {
					ran <- struct{}{}

					return nil
				},
			}))

			runScheduler(t, scheduler)

			<-ran
		})
	})
}

func TestSchedulerConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("fills unset knobs", func(t *testing.T) {
		t.Parallel()

		cfg := &jobs.SchedulerConfig{}
		cfg.EnsureDefaults()

		test.EqOp(t, jobs.DefaultLockKeyPrefix, cfg.LockKeyPrefix)
		test.EqOp(t, jobs.DefaultLeaseTTL, cfg.DefaultLeaseTTL)
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestScheduler_Register(T *testing.T) {
	T.Parallel()

	noop := func(context.Context) error { return nil }

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, newTestScheduler(t).Register(
			jobs.Job{Name: "one", Interval: testInterval, Run: noop},
			jobs.Job{Name: "two", Interval: testInterval, Run: noop},
		))
	})

	T.Run("with an empty name", func(t *testing.T) {
		t.Parallel()

		err := newTestScheduler(t).Register(jobs.Job{Interval: testInterval, Run: noop})
		test.ErrorIs(t, err, jobs.ErrInvalidJob)
	})

	T.Run("with no function", func(t *testing.T) {
		t.Parallel()

		err := newTestScheduler(t).Register(jobs.Job{Name: "one", Interval: testInterval})
		test.ErrorIs(t, err, jobs.ErrInvalidJob)
	})

	T.Run("with a non-positive interval", func(t *testing.T) {
		t.Parallel()

		err := newTestScheduler(t).Register(jobs.Job{Name: "one", Run: noop})
		test.ErrorIs(t, err, jobs.ErrInvalidJob)
	})

	T.Run("with a schedule instead of an interval", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, newTestScheduler(t).Register(
			jobs.Job{Name: "one", Schedule: jobs.MustCron("0 3 * * *"), Run: noop},
		))
	})

	T.Run("with both an interval and a schedule", func(t *testing.T) {
		t.Parallel()

		// Rejected rather than resolved by precedence: a job carrying both is a
		// mistake, and picking one silently is how it survives to production.
		err := newTestScheduler(t).Register(jobs.Job{
			Name:     "one",
			Interval: testInterval,
			Schedule: jobs.MustCron("0 3 * * *"),
			Run:      noop,
		})
		test.ErrorIs(t, err, jobs.ErrInvalidJob)
	})

	T.Run("with a schedule that will never fire", func(t *testing.T) {
		t.Parallel()

		// The 30th of February parses cleanly. Caught here, it is a startup
		// error; accepted, it is a job nobody notices never ran.
		err := newTestScheduler(t).Register(jobs.Job{
			Name:     "never",
			Schedule: jobs.MustCron("0 0 30 2 *"),
			Run:      noop,
		})
		test.ErrorIs(t, err, jobs.ErrInvalidJob)
	})

	T.Run("with a duplicate name", func(t *testing.T) {
		t.Parallel()

		scheduler := newTestScheduler(t)
		must.NoError(t, scheduler.Register(jobs.Job{Name: "one", Interval: testInterval, Run: noop}))

		err := scheduler.Register(jobs.Job{Name: "one", Interval: testInterval, Run: noop})
		test.ErrorIs(t, err, jobs.ErrDuplicateJob)
	})

	T.Run("with a name duplicated within one batch", func(t *testing.T) {
		t.Parallel()

		err := newTestScheduler(t).Register(
			jobs.Job{Name: "one", Interval: testInterval, Run: noop},
			jobs.Job{Name: "one", Interval: testInterval, Run: noop},
		)
		test.ErrorIs(t, err, jobs.ErrDuplicateJob)
	})

	T.Run("rejects the whole batch when one job is invalid", func(t *testing.T) {
		t.Parallel()

		scheduler := newTestScheduler(t)

		test.Error(t, scheduler.Register(
			jobs.Job{Name: "good", Interval: testInterval, Run: noop},
			jobs.Job{Name: "bad", Run: noop},
		))

		// "good" was not kept, so registering it again is not a duplicate.
		test.NoError(t, scheduler.Register(jobs.Job{Name: "good", Interval: testInterval, Run: noop}))
	})

	T.Run("after Run", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			scheduler := newTestScheduler(t)
			runScheduler(t, scheduler)
			synctest.Wait()

			err := scheduler.Register(jobs.Job{Name: "late", Interval: testInterval, Run: noop})
			test.ErrorIs(t, err, jobs.ErrSchedulerRunning)
		})
	})
}

func TestScheduler_Run(T *testing.T) {
	T.Parallel()

	T.Run("fires on the interval", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ran := make(chan time.Time, 4)

			scheduler := newTestScheduler(t)
			must.NoError(t, scheduler.Register(jobs.Job{
				Name:     "ticker",
				Interval: testInterval,
				Run: func(context.Context) error {
					ran <- time.Now()

					return nil
				},
			}))

			start := time.Now()
			runScheduler(t, scheduler)

			first := <-ran
			second := <-ran

			test.EqOp(t, testInterval, first.Sub(start))
			test.EqOp(t, testInterval, second.Sub(first))
		})
	})

	T.Run("RunOnStart fires without waiting an interval", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ran := make(chan time.Time, 1)

			scheduler := newTestScheduler(t)
			must.NoError(t, scheduler.Register(jobs.Job{
				Name:       "eager",
				Interval:   testInterval,
				RunOnStart: true,
				Run: func(context.Context) error {
					ran <- time.Now()

					return nil
				},
			}))

			start := time.Now()
			runScheduler(t, scheduler)

			test.EqOp(t, start, <-ran)
		})
	})

	T.Run("runs registered jobs independently", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			fast := make(chan struct{}, 8)
			slow := make(chan struct{}, 8)

			scheduler := newTestScheduler(t)
			must.NoError(t, scheduler.Register(
				jobs.Job{Name: "fast", Interval: time.Second, Run: func(context.Context) error {
					fast <- struct{}{}

					return nil
				}},
				jobs.Job{Name: "slow", Interval: time.Hour, Run: func(context.Context) error {
					slow <- struct{}{}

					return nil
				}},
			))

			runScheduler(t, scheduler)

			<-fast
			<-fast
			test.SliceEmpty(t, drain(slow))
		})
	})

	T.Run("a failing job keeps its schedule", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			spy := newCounterSpy()
			ran := make(chan struct{}, 4)

			scheduler := newTestScheduler(t, jobs.WithSchedulerMetricsProvider(spy.provider()))
			must.NoError(t, scheduler.Register(jobs.Job{
				Name:     "flaky",
				Interval: testInterval,
				Run: func(context.Context) error {
					ran <- struct{}{}

					return errJob
				},
			}))

			runScheduler(t, scheduler)

			// A second tick is the claim: an error is not terminal, and the
			// next tick is the retry.
			<-ran
			<-ran
			synctest.Wait()

			test.EqOp(t, int64(2), spy.count("jobs_scheduler_failures"))
			test.EqOp(t, int64(2), spy.count("jobs_scheduler_runs"))
		})
	})

	T.Run("contains a panicking job and keeps its schedule", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			spy := newCounterSpy()
			var runs atomic.Int64
			ran := make(chan struct{}, 4)

			scheduler := newTestScheduler(t, jobs.WithSchedulerMetricsProvider(spy.provider()))
			must.NoError(t, scheduler.Register(jobs.Job{
				Name:     "exploder",
				Interval: testInterval,
				Run: func(context.Context) error {
					defer func() { ran <- struct{}{} }()

					if runs.Add(1) == 1 {
						panic("job blew up")
					}

					return nil
				},
			}))

			runScheduler(t, scheduler)

			<-ran
			// The second tick is the proof: an uncontained panic would have
			// unwound this job's goroutine and stopped it for good.
			<-ran

			test.EqOp(t, int64(1), spy.count("jobs_scheduler_panics"))
			test.EqOp(t, int64(1), spy.count("jobs_scheduler_failures"))
		})
	})

	T.Run("counts a run that outran its interval", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			spy := newCounterSpy()
			var runs atomic.Int64
			ran := make(chan struct{}, 4)

			scheduler := newTestScheduler(t, jobs.WithSchedulerMetricsProvider(spy.provider()))
			must.NoError(t, scheduler.Register(jobs.Job{
				Name:       "sluggish",
				Interval:   time.Second,
				RunOnStart: true,
				Run: func(context.Context) error {
					// Only the first run overruns, so the count below is stable
					// no matter how many ticks the bubble gets through.
					if runs.Add(1) == 1 {
						time.Sleep(2 * time.Second)
					}

					ran <- struct{}{}

					return nil
				},
			}))

			runScheduler(t, scheduler)

			// The second run is what shows an overrun is not terminal: ticks are
			// coalesced rather than queued, so the job simply fires again.
			<-ran
			<-ran
			synctest.Wait()

			test.EqOp(t, int64(1), spy.count("jobs_scheduler_overruns"))
		})
	})

	T.Run("a scheduler closed before its goroutines start does not fire RunOnStart", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			var runs atomic.Int64

			scheduler := newTestScheduler(t)
			must.NoError(t, scheduler.Register(jobs.Job{
				Name:       "eager",
				Interval:   time.Hour,
				RunOnStart: true,
				Run: func(context.Context) error {
					runs.Add(1)

					return nil
				},
			}))

			// Closing first leaves the stop signal already delivered, which is
			// the state the guard in front of the RunOnStart fire exists for.
			// Close cannot report a clean stop here because Run has not run.
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			test.ErrorIs(t, scheduler.Close(ctx), context.DeadlineExceeded)

			scheduler.Run()

			test.EqOp(t, int64(0), runs.Load())
		})
	})

	T.Run("bounds a run with its timeout", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			observed := make(chan error, 1)

			scheduler := newTestScheduler(t)
			must.NoError(t, scheduler.Register(jobs.Job{
				Name:       "wedged",
				Interval:   time.Hour,
				Timeout:    testInterval,
				LeaseTTL:   time.Hour,
				RunOnStart: true,
				Run: func(ctx context.Context) error {
					<-ctx.Done()
					observed <- ctx.Err()

					return ctx.Err()
				},
			}))

			runScheduler(t, scheduler)

			test.ErrorIs(t, <-observed, context.DeadlineExceeded)
		})
	})
}

// expiringSchedule fires every minute until its cutoff and reports the zero
// time after it — a Schedule that runs itself out mid-flight, which is the case
// Register's never-fires check cannot catch up front.
type expiringSchedule struct {
	until time.Time
}

func (s expiringSchedule) Next(after time.Time) time.Time {
	next := after.Add(time.Minute)
	if next.After(s.until) {
		return time.Time{}
	}

	return next
}

// TestScheduler_Timezone pins the four-level precedence a cron schedule's zone
// is decided by: the spec's own prefix, then CronIn, then the Scheduler's
// configured zone, then UTC.
// hourlySchedule is a Schedule this package did not build, and so has no zone
// for a configured default to reach into.
type hourlySchedule struct{}

func (hourlySchedule) Next(after time.Time) time.Time {
	return after.Add(time.Hour)
}

// sharedSchedule is registered by more than one test below, standing in for the
// package-level schedule var a real service would declare once. Two Schedulers
// in different zones must each get their own reading of it.
var sharedSchedule = jobs.MustCron("0 3 * * *")

// firesAt runs a one-job Scheduler until the job fires, and reports when in
// UTC. Which zone won is not visible from outside the package, and does not
// need to be: the answer that matters is when the job actually runs.
//
// It must be called inside a synctest bubble, where waiting until 03:00 costs
// no wall time.
func firesAt(t *testing.T, timezone string, schedule jobs.Schedule) time.Time {
	t.Helper()

	fired := make(chan time.Time, 1)

	scheduler, err := jobs.NewScheduler(t.Context(),
		&jobs.SchedulerConfig{Timezone: timezone}, newTestLocker(t))
	must.NoError(t, err)

	must.NoError(t, scheduler.Register(jobs.Job{
		Name:     "zoned",
		Schedule: schedule,
		Run: func(context.Context) error {
			fired <- time.Now()

			return nil
		},
	}))

	runScheduler(t, scheduler)

	return (<-fired).UTC()
}

// TestScheduler_Timezone pins the four-level precedence a cron schedule's zone
// is decided by: the spec's own prefix, then CronIn, then the Scheduler's
// configured zone, then UTC.
//
// Each case states its expectation as "the same instant CronIn would have
// produced", computed from the bubble's own clock — so the assertions do not
// depend on what instant a bubble starts at, or on whether that date happens to
// fall inside daylight saving.
func TestScheduler_Timezone(T *testing.T) {
	T.Parallel()

	const spec = "0 3 * * *"

	T.Run("the configured zone reaches a spec that named none", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			loc := chicago(t)

			want := jobs.MustCronIn(loc, spec).Next(time.Now()).UTC()

			// Guards the assertion against passing vacuously: if Chicago and
			// UTC agreed here, the case would prove nothing about which won.
			must.NotEq(t, jobs.MustCron(spec).Next(time.Now()).UTC(), want)

			test.EqOp(t, want, firesAt(t, "America/Chicago", jobs.MustCron(spec)))
		})
	})

	T.Run("a spec that named its own zone beats the configured one", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			tokyo, err := time.LoadLocation("Asia/Tokyo")
			must.NoError(t, err)

			want := jobs.MustCronIn(tokyo, spec).Next(time.Now()).UTC()

			test.EqOp(t, want, firesAt(t, "America/Chicago", jobs.MustCron("CRON_TZ=Asia/Tokyo "+spec)))
		})
	})

	T.Run("a CronIn location beats the configured zone", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			tokyo, err := time.LoadLocation("Asia/Tokyo")
			must.NoError(t, err)

			want := jobs.MustCronIn(tokyo, spec).Next(time.Now()).UTC()

			test.EqOp(t, want, firesAt(t, "America/Chicago", jobs.MustCronIn(tokyo, spec)))
		})
	})

	T.Run("nothing configured leaves the schedule in UTC", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			want := jobs.MustCronIn(time.UTC, spec).Next(time.Now()).UTC()

			test.EqOp(t, want, firesAt(t, "", jobs.MustCron(spec)))
		})
	})

	T.Run("a caller's own schedule is left alone", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			// The configured zone reaches cron schedules and nothing else. A
			// Schedule this package did not build owns its own time semantics,
			// and there is no field to reach into.
			start := time.Now()

			test.EqOp(t, start.Add(time.Hour).UTC(), firesAt(t, "America/Chicago", hourlySchedule{}))
		})
	})

	T.Run("one schedule value read by two differently-zoned schedulers", func(t *testing.T) {
		t.Parallel()

		// Registering resolves a zone onto a copy rather than onto the schedule
		// it was handed, so a package-level var shared across Schedulers does
		// not carry the first one's zone to the second. These two run in
		// parallel over the same value, so a mutating implementation would
		// either race or hand one of them the other's answer.
		for _, zone := range []string{"America/Chicago", "Asia/Tokyo"} {
			t.Run(zone, func(t *testing.T) {
				t.Parallel()

				synctest.Test(t, func(t *testing.T) {
					loc, err := time.LoadLocation(zone)
					must.NoError(t, err)

					want := jobs.MustCronIn(loc, spec).Next(time.Now()).UTC()

					test.EqOp(t, want, firesAt(t, zone, sharedSchedule))
				})
			})
		}
	})
}

func TestNewScheduler_Timezone(T *testing.T) {
	T.Parallel()

	T.Run("with a zone the runtime cannot load", func(t *testing.T) {
		t.Parallel()

		// Caught at construction rather than at the first fire, because the
		// usual cause is an image without the zoneinfo database and that is a
		// deployment problem worth learning about at startup.
		_, err := jobs.NewScheduler(t.Context(),
			&jobs.SchedulerConfig{Timezone: "Mars/Olympus"}, newTestLocker(t))

		test.Error(t, err)
	})

	T.Run("an empty zone defaults without needing the zoneinfo database", func(t *testing.T) {
		t.Parallel()

		cfg := &jobs.SchedulerConfig{}
		cfg.EnsureDefaults()

		test.EqOp(t, jobs.DefaultTimezone, cfg.Timezone)

		_, err := jobs.NewScheduler(t.Context(), cfg, newTestLocker(t))
		test.NoError(t, err)
	})
}

// presentSchedule answers with a fire time that is not in the future, which is
// the one thing Schedule.Next is documented not to do. It does so twice — once
// for Register's validation and once for the wait the Scheduler then performs —
// and then gets out of the way, so the job under test runs exactly once.
type presentSchedule struct {
	answers atomic.Int64
}

func (s *presentSchedule) Next(after time.Time) time.Time {
	if s.answers.Add(1) <= 2 {
		return after
	}

	return after.Add(24 * time.Hour)
}

func TestScheduler_Schedule(T *testing.T) {
	T.Parallel()

	T.Run("fires on the schedule", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ran := make(chan time.Time, 4)

			scheduler := newTestScheduler(t)
			must.NoError(t, scheduler.Register(jobs.Job{
				Name:     "calendar",
				Schedule: jobs.MustCron("* * * * *"),
				Run: func(context.Context) error {
					ran <- time.Now()

					return nil
				},
			}))

			start := time.Now()
			runScheduler(t, scheduler)

			first := <-ran
			second := <-ran

			// The claim a calendar makes that a stopwatch does not: fires land
			// on the minute rather than an interval after whenever this replica
			// happened to start.
			test.EqOp(t, 0, first.Second())
			test.True(t, first.After(start))
			test.True(t, first.Sub(start) <= time.Minute)
			test.EqOp(t, time.Minute, second.Sub(first))
		})
	})

	T.Run("RunOnStart fires without waiting for the next fire time", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ran := make(chan time.Time, 1)

			scheduler := newTestScheduler(t)
			must.NoError(t, scheduler.Register(jobs.Job{
				Name:       "eager-calendar",
				Schedule:   jobs.MustCron("0 3 * * *"),
				RunOnStart: true,
				Run: func(context.Context) error {
					ran <- time.Now()

					return nil
				},
			}))

			start := time.Now()
			runScheduler(t, scheduler)

			test.EqOp(t, start, <-ran)
		})
	})

	T.Run("counts a run that outran its schedule", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			spy := newCounterSpy()
			var runs atomic.Int64
			ran := make(chan struct{}, 4)

			scheduler := newTestScheduler(t, jobs.WithSchedulerMetricsProvider(spy.provider()))
			must.NoError(t, scheduler.Register(jobs.Job{
				Name: "sluggish-calendar",
				// A minute of headroom per fire, and a first run that takes
				// ninety seconds of it.
				Schedule:   jobs.MustCron("* * * * *"),
				RunOnStart: true,
				Run: func(context.Context) error {
					if runs.Add(1) == 1 {
						time.Sleep(90 * time.Second)
					}

					ran <- struct{}{}

					return nil
				},
			}))

			runScheduler(t, scheduler)

			// The second run is what shows the overrun is not terminal: the
			// fires that passed during the first are skipped, not queued.
			<-ran
			<-ran
			synctest.Wait()

			test.EqOp(t, int64(1), spy.count("jobs_scheduler_overruns"))
		})
	})

	T.Run("a scheduler closed before its goroutines start does not fire RunOnStart", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			var runs atomic.Int64

			scheduler := newTestScheduler(t)
			must.NoError(t, scheduler.Register(jobs.Job{
				Name:       "eager-calendar",
				Schedule:   jobs.MustCron("0 3 * * *"),
				RunOnStart: true,
				Run: func(context.Context) error {
					runs.Add(1)

					return nil
				},
			}))

			// The same guard the interval path has, on the other side of the
			// dispatch: Close cannot report a clean stop here because Run has
			// not run.
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			test.ErrorIs(t, scheduler.Close(ctx), context.DeadlineExceeded)

			scheduler.Run()

			test.EqOp(t, int64(0), runs.Load())
		})
	})

	T.Run("a job waiting for a distant fire time stops without waiting it out", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			var runs atomic.Int64

			scheduler := newTestScheduler(t)
			must.NoError(t, scheduler.Register(jobs.Job{
				Name:     "nightly",
				Schedule: jobs.MustCron("0 3 * * *"),
				Run: func(context.Context) error {
					runs.Add(1)

					return nil
				},
			}))

			start := time.Now()

			go scheduler.Run()
			synctest.Wait()

			must.NoError(t, scheduler.Close(context.Background()))

			// No run, and no bubble time elapsed: the wait was cut short by the
			// stop rather than slept through to 03:00. A wait that ignored the
			// stop would show hours here and a run at the end of them.
			test.EqOp(t, int64(0), runs.Load())
			test.EqOp(t, start, time.Now())
		})
	})

	T.Run("a schedule that answers with a time in the present fires rather than panicking", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			ran := make(chan time.Time, 1)

			scheduler := newTestScheduler(t)
			must.NoError(t, scheduler.Register(jobs.Job{
				Name:     "impatient",
				Schedule: &presentSchedule{},
				Run: func(context.Context) error {
					ran <- time.Now()

					return nil
				},
			}))

			start := time.Now()
			runScheduler(t, scheduler)

			// A zero wait is the duration NewTicker panics on, so without the
			// guard in front of it a caller's arithmetic mistake would take the
			// process down rather than costing one early run.
			test.EqOp(t, start, <-ran)
		})
	})

	T.Run("a schedule that retires mid-flight stops its job and not the others", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			var retired, survivor atomic.Int64
			ran := make(chan struct{}, 4)

			scheduler := newTestScheduler(t, jobs.WithSchedulerLogger(lognoop.NewLogger()))
			must.NoError(t, scheduler.Register(
				jobs.Job{
					Name: "retiring",
					// Room for exactly one fire before it reports that it will
					// never fire again.
					Schedule: expiringSchedule{until: time.Now().Add(90 * time.Second)},
					Run: func(context.Context) error {
						retired.Add(1)

						return nil
					},
				},
				jobs.Job{
					Name:     "surviving",
					Schedule: jobs.MustCron("* * * * *"),
					Run: func(context.Context) error {
						survivor.Add(1)
						ran <- struct{}{}

						return nil
					},
				},
			))

			runScheduler(t, scheduler)

			// Three fires of the survivor is two minutes of bubble time, long
			// past the point where the retired job would have fired again.
			<-ran
			<-ran
			<-ran
			synctest.Wait()

			test.EqOp(t, int64(1), retired.Load())
			test.EqOp(t, int64(3), survivor.Load())
		})
	})
}

func TestScheduler_Leasing(T *testing.T) {
	T.Parallel()

	T.Run("only one replica runs a job per tick", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			spy := newCounterSpy()

			// One locker shared by two schedulers is what "two replicas" means
			// here: the lock, not the process boundary, is what coordinates.
			locker := newTestLocker(t)

			started := make(chan string, 4)
			release := make(chan struct{})

			for _, replica := range []string{"a", "b"} {
				scheduler, err := jobs.NewScheduler(t.Context(), &jobs.SchedulerConfig{}, locker,
					jobs.WithSchedulerMetricsProvider(spy.provider()))
				must.NoError(t, err)

				must.NoError(t, scheduler.Register(jobs.Job{
					Name:     "singleton",
					Interval: testInterval,
					Run: func(context.Context) error {
						started <- replica
						<-release

						return nil
					},
				}))

				runScheduler(t, scheduler)
			}

			// The winner is now inside the job, holding the lease.
			<-started

			// Wait for the loser to finish its tick, which it can only do by
			// failing to acquire and giving up.
			synctest.Wait()
			test.SliceEmpty(t, drain(started))
			test.EqOp(t, int64(1), spy.count("jobs_scheduler_skipped"))
			test.EqOp(t, int64(1), spy.count("jobs_scheduler_runs"))

			close(release)
		})
	})

	T.Run("counts a lease that expired mid-run", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			spy := newCounterSpy()
			ran := make(chan struct{}, 1)

			locker := &distributedlockmock.LockerMock{
				AcquireFunc: func(context.Context, string, time.Duration) (distributedlock.Lock, error) {
					return &distributedlockmock.LockMock{
						ReleaseFunc: func(context.Context) error {
							return distributedlock.ErrLockNotHeld
						},
					}, nil
				},
			}

			scheduler, err := jobs.NewScheduler(t.Context(), &jobs.SchedulerConfig{}, locker,
				jobs.WithSchedulerMetricsProvider(spy.provider()))
			must.NoError(t, err)

			must.NoError(t, scheduler.Register(jobs.Job{
				Name:       "overrunner",
				Interval:   time.Hour,
				RunOnStart: true,
				Run: func(context.Context) error {
					ran <- struct{}{}

					return nil
				},
			}))

			runScheduler(t, scheduler)

			<-ran
			synctest.Wait()

			test.EqOp(t, int64(1), spy.count("jobs_scheduler_leases_expired"))
		})
	})

	T.Run("counts a release that fails for its own reasons", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			spy := newCounterSpy()
			ran := make(chan struct{}, 1)

			// Not ErrLockNotHeld: the lease did not expire, the backend simply
			// could not be told to let go of it. That is a lock error rather
			// than a "this job may have run twice" warning.
			locker := &distributedlockmock.LockerMock{
				AcquireFunc: func(context.Context, string, time.Duration) (distributedlock.Lock, error) {
					return &distributedlockmock.LockMock{
						ReleaseFunc: func(context.Context) error {
							return errors.New("lock backend is down")
						},
					}, nil
				},
			}

			scheduler, err := jobs.NewScheduler(t.Context(), &jobs.SchedulerConfig{}, locker,
				jobs.WithSchedulerMetricsProvider(spy.provider()))
			must.NoError(t, err)

			must.NoError(t, scheduler.Register(jobs.Job{
				Name:       "unreleasable",
				Interval:   time.Hour,
				RunOnStart: true,
				Run: func(context.Context) error {
					ran <- struct{}{}

					return nil
				},
			}))

			runScheduler(t, scheduler)

			<-ran
			synctest.Wait()

			test.EqOp(t, int64(1), spy.count("jobs_scheduler_lock_errors"))
			test.EqOp(t, int64(0), spy.count("jobs_scheduler_leases_expired"))
			test.EqOp(t, int64(0), spy.count("jobs_scheduler_failures"))
		})
	})

	T.Run("a lock failure skips the tick without running the job", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			spy := newCounterSpy()
			var runs atomic.Int64

			locker := &distributedlockmock.LockerMock{
				AcquireFunc: func(context.Context, string, time.Duration) (distributedlock.Lock, error) {
					return nil, errors.New("lock backend is down")
				},
			}

			scheduler, err := jobs.NewScheduler(t.Context(), &jobs.SchedulerConfig{}, locker,
				jobs.WithSchedulerMetricsProvider(spy.provider()))
			must.NoError(t, err)

			must.NoError(t, scheduler.Register(jobs.Job{
				Name:       "unreachable",
				Interval:   time.Hour,
				RunOnStart: true,
				Run: func(context.Context) error {
					runs.Add(1)

					return nil
				},
			}))

			runScheduler(t, scheduler)
			synctest.Wait()

			test.EqOp(t, int64(0), runs.Load())
			test.EqOp(t, int64(1), spy.count("jobs_scheduler_lock_errors"))
			test.EqOp(t, int64(0), spy.count("jobs_scheduler_skipped"))
		})
	})
}

func TestScheduler_Close(T *testing.T) {
	T.Parallel()

	T.Run("waits for the in-flight run", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			finished := make(chan struct{})

			scheduler := newTestScheduler(t)
			must.NoError(t, scheduler.Register(jobs.Job{
				Name:       "slow",
				Interval:   time.Hour,
				LeaseTTL:   time.Hour,
				RunOnStart: true,
				Run: func(context.Context) error {
					entered <- struct{}{}
					<-release
					close(finished)

					return nil
				},
			}))

			go scheduler.Run()
			<-entered

			closed := make(chan error, 1)
			go func() { closed <- scheduler.Close(context.Background()) }()

			synctest.Wait()
			test.SliceEmpty(t, drain(closed))

			close(release)

			must.NoError(t, <-closed)
			<-finished
		})
	})

	T.Run("reports the deadline when a run outlasts it", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			entered := make(chan struct{}, 1)
			release := make(chan struct{})

			scheduler := newTestScheduler(t)
			must.NoError(t, scheduler.Register(jobs.Job{
				Name:       "wedged",
				Interval:   time.Hour,
				LeaseTTL:   time.Hour,
				RunOnStart: true,
				Run: func(context.Context) error {
					entered <- struct{}{}
					<-release

					return nil
				},
			}))

			go scheduler.Run()
			<-entered

			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()

			test.ErrorIs(t, scheduler.Close(ctx), context.DeadlineExceeded)

			// Release afterwards so the bubble is not left with a parked
			// goroutine in it.
			close(release)
			must.NoError(t, scheduler.Close(context.Background()))
		})
	})

	T.Run("does not start the tick that came due during a run it is stopping", func(t *testing.T) {
		t.Parallel()

		// Hand-cranked rather than bubbled, because the claim is about a tick
		// that is already pending when the stop signal lands — and that is a
		// state to put the ticker in, not a moment in time to reach.
		ticks := make(chan time.Time, 1)
		entered := make(chan struct{}, 1)
		release := make(chan struct{})

		var runs atomic.Int64

		c := &clockmock.ClockMock{
			NowFunc:   time.Now,
			SinceFunc: time.Since,
			NewTickerFunc: func(time.Duration) clock.Ticker {
				return &clockmock.TickerMock{
					ChanFunc: func() <-chan time.Time { return ticks },
					StopFunc: func() {},
				}
			},
		}

		scheduler := newTestScheduler(t, jobs.WithSchedulerClock(c))
		must.NoError(t, scheduler.Register(jobs.Job{
			Name:     "slow",
			Interval: time.Hour,
			LeaseTTL: time.Hour,
			Run: func(context.Context) error {
				runs.Add(1)
				entered <- struct{}{}
				<-release

				return nil
			},
		}))

		go scheduler.Run()

		ticks <- time.Now()
		recv(t, entered, "the job starting")

		closed := make(chan error, 1)
		go func() { closed <- scheduler.Close(context.Background()) }()

		// Close has delivered the stop signal by now, and cannot return while
		// the job is still inside its run.
		notYet(t, closed, "Close returning")

		// The next tick comes due while the run that outlasted the stop signal
		// is still going.
		ticks <- time.Now()

		close(release)
		must.NoError(t, recv(t, closed, "Close returning"))

		// The loop rechecks the stop signal after the run rather than returning
		// to its select, so the pending tick is dropped rather than started.
		test.EqOp(t, int64(1), runs.Load())
	})

	T.Run("is safe to call more than once", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			scheduler := newTestScheduler(t)
			go scheduler.Run()
			synctest.Wait()

			test.NoError(t, scheduler.Close(context.Background()))
			test.NoError(t, scheduler.Close(context.Background()))
		})
	})
}

// drain empties ch without blocking, so a test can assert that nothing arrived
// on it. A blocking receive would let a synctest bubble idle forward to the
// next tick and pass regardless.
func drain[T any](ch <-chan T) []T {
	var received []T

	for {
		select {
		case v := <-ch:
			received = append(received, v)
		default:
			return received
		}
	}
}

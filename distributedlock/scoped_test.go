package distributedlock_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	clockmock "github.com/primandproper/platform-go/v13/clock/mock"
	"github.com/primandproper/platform-go/v13/distributedlock"
	"github.com/primandproper/platform-go/v13/distributedlock/distributedlocktest"
	"github.com/primandproper/platform-go/v13/distributedlock/memory"
	distributedlockmock "github.com/primandproper/platform-go/v13/distributedlock/mock"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// discardHistogram satisfies metrics.Float64Histogram for constructor tests
// that only need some histograms to succeed. The metrics mock package
// generates no histogram double.
type discardHistogram struct{}

func (discardHistogram) Record(context.Context, float64, ...metric.RecordOption) {}

const (
	scopedTestKey = "scoped-test"
	scopedLockTTL = time.Minute

	// scopedPollInterval is the contention poll cadence the fixture configures.
	// Bubble time makes it free, so the value is arbitrary — but the contention
	// test steps to either side of it, so it fails if polling ignores it.
	scopedPollInterval = 100 * time.Millisecond
)

// noJitter pins the jitter draw to its maximum, so a contended wait is exactly
// the current poll interval. Without it the adapter sleeps somewhere in
// [interval/2, interval] and the tests below could not name a deadline.
func noJitter() float64 { return 1 }

// newScopedFixture wires the generic adapter around a real memory locker. The
// contention tests build it inside a synctest bubble, where the production
// clock reads bubble time, so WithLock's poll sleeps cost no wall time.
func newScopedFixture(t *testing.T, opts ...distributedlock.ScopedOption) (distributedlock.ScopedLocker, distributedlock.Locker) {
	t.Helper()

	raw, err := memory.NewLocker()
	must.NoError(t, err)

	scoped, err := distributedlock.NewScopedLocker(
		raw,
		append([]distributedlock.ScopedOption{
			distributedlock.WithScopedLockTTL(scopedLockTTL),
			distributedlock.WithScopedPollInterval(scopedPollInterval),
			distributedlock.WithScopedRand(noJitter),
		}, opts...)...,
	)
	must.NoError(t, err)

	return scoped, raw
}

// startWaiter parks a WithLock call on an already-held key and returns the
// channel its result lands on, once the adapter is asleep between polls.
func startWaiter(t *testing.T, ctx context.Context, scoped distributedlock.ScopedLocker) <-chan error {
	t.Helper()

	done := make(chan error, 1)
	go func() {
		done <- scoped.WithLock(ctx, scopedTestKey, func(context.Context) error {
			return nil
		})
	}()
	synctest.Wait()

	return done
}

// requireWaiting asserts the waiter has not acquired yet. Both this and
// requireAcquired use non-blocking receives: a blocking one would let the
// bubble idle forward to any later deadline and pass regardless of schedule.
func requireWaiting(t *testing.T, done <-chan error, when string) {
	t.Helper()

	synctest.Wait()
	select {
	case <-done:
		t.Fatalf("WithLock acquired %s, before its next poll came due", when)
	default:
	}
}

func requireAcquired(t *testing.T, done <-chan error, when string) {
	t.Helper()

	synctest.Wait()
	select {
	case err := <-done:
		must.NoError(t, err)
	default:
		t.Fatalf("WithLock had not acquired %s", when)
	}
}

func TestNewScopedLocker(T *testing.T) {
	T.Parallel()

	T.Run("rejects a nil locker", func(t *testing.T) {
		t.Parallel()

		_, err := distributedlock.NewScopedLocker(nil)
		test.Error(t, err)
	})

	T.Run("rejects poll settings that would spin or shrink", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			opt  distributedlock.ScopedOption
			name string
		}{
			// A zero or negative interval is the dangerous one: clock.Sleep
			// returns immediately for it, so WithLock would busy-loop on a
			// contended lock rather than wait.
			{name: "zero poll interval", opt: distributedlock.WithScopedPollInterval(0)},
			{name: "negative poll interval", opt: distributedlock.WithScopedPollInterval(-time.Second)},
			{name: "shrinking backoff", opt: distributedlock.WithScopedPollBackoff(0.5, time.Second)},
			{name: "max below the poll interval", opt: distributedlock.WithScopedPollBackoff(2, time.Millisecond)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				raw, err := memory.NewLocker()
				must.NoError(t, err)

				_, err = distributedlock.NewScopedLocker(raw, distributedlock.WithScopedPollInterval(scopedPollInterval), tc.opt)
				test.Error(t, err)
			})
		}
	})

	T.Run("accepts a backoff factor of exactly one", func(t *testing.T) {
		t.Parallel()

		raw, err := memory.NewLocker()
		must.NoError(t, err)

		_, err = distributedlock.NewScopedLocker(
			raw,
			distributedlock.WithScopedPollBackoff(1, distributedlock.DefaultScopedMaxPollInterval),
		)
		test.NoError(t, err)
	})

	T.Run("nil options are skipped", func(t *testing.T) {
		t.Parallel()

		raw, err := memory.NewLocker()
		must.NoError(t, err)

		_, err = distributedlock.NewScopedLocker(
			raw,
			nil,
			distributedlock.WithScopedRand(nil),
			distributedlock.WithScopedClock(nil),
		)
		test.NoError(t, err)
	})

	T.Run("an instrument that cannot be built fails construction", func(t *testing.T) {
		t.Parallel()

		counters := []string{
			"scoped_lock_acquires",
			"scoped_lock_contended",
			"scoped_lock_errors",
			"scoped_lock_release_failures",
		}

		for i, failing := range counters {
			t.Run(failing, func(t *testing.T) {
				t.Parallel()

				raw, err := memory.NewLocker()
				must.NoError(t, err)

				mp := &metricsmock.ProviderMock{
					NewInt64CounterFunc: func(name string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
						if name == failing {
							return nil, errors.New("instrument unavailable")
						}

						return &metricsmock.Int64CounterMock{}, nil
					},
				}

				_, err = distributedlock.NewScopedLocker(raw, distributedlock.WithMetricsProvider(mp))
				must.Error(t, err)
				test.SliceLen(t, i+1, mp.NewInt64CounterCalls())
			})
		}

		// Both histograms come from the same constructor, so failing the Nth
		// call is what separates their two error paths.
		for _, failAtCall := range []int{1, 2} {
			t.Run("histogram", func(t *testing.T) {
				t.Parallel()

				raw, err := memory.NewLocker()
				must.NoError(t, err)

				var calls int
				mp := &metricsmock.ProviderMock{
					NewInt64CounterFunc: func(string, ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
						return &metricsmock.Int64CounterMock{}, nil
					},
					NewFloat64HistogramFunc: func(string, ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
						calls++
						if calls == failAtCall {
							return nil, errors.New("instrument unavailable")
						}

						return discardHistogram{}, nil
					},
				}

				_, err = distributedlock.NewScopedLocker(raw, distributedlock.WithMetricsProvider(mp))
				must.Error(t, err)
				test.EqOp(t, failAtCall, calls)
			})
		}
	})
}

func TestScopedLocker_WithScopedClock(T *testing.T) {
	T.Parallel()

	T.Run("contended polling sleeps on the supplied clock", func(t *testing.T) {
		t.Parallel()

		// A clock whose Sleep returns instantly: the wait between polls is the
		// test's to control, not the wall's.
		c := &clockmock.ClockMock{
			// Now is read too: the locker times its own latency against the
			// clock it was given rather than against the wall.
			NowFunc:   time.Now,
			SleepFunc: func(context.Context, time.Duration) error { return nil },
		}

		var attempts int
		raw := &distributedlockmock.LockerMock{
			AcquireFunc: func(context.Context, string, time.Duration) (distributedlock.Lock, error) {
				attempts++
				if attempts == 1 {
					return nil, distributedlock.ErrLockNotAcquired
				}

				return &distributedlockmock.LockMock{
					ReleaseFunc: func(context.Context) error { return nil },
				}, nil
			},
		}

		scoped, err := distributedlock.NewScopedLocker(
			raw,
			distributedlock.WithScopedPollInterval(scopedPollInterval),
			distributedlock.WithScopedRand(noJitter),
			distributedlock.WithScopedClock(c),
		)
		must.NoError(t, err)

		must.NoError(t, scoped.WithLock(t.Context(), scopedTestKey, func(context.Context) error { return nil }))

		// One refusal, one sleep on the injected clock, one success.
		test.EqOp(t, 2, attempts)
		must.SliceLen(t, 1, c.SleepCalls())
		test.EqOp(t, scopedPollInterval, c.SleepCalls()[0].D)
	})

	T.Run("a growth factor that overflows clamps to the max poll interval", func(t *testing.T) {
		t.Parallel()

		c := &clockmock.ClockMock{
			// Now is read too: the locker times its own latency against the
			// clock it was given rather than against the wall.
			NowFunc:   time.Now,
			SleepFunc: func(context.Context, time.Duration) error { return nil },
		}

		var attempts int
		raw := &distributedlockmock.LockerMock{
			AcquireFunc: func(context.Context, string, time.Duration) (distributedlock.Lock, error) {
				attempts++
				if attempts <= 2 {
					return nil, distributedlock.ErrLockNotAcquired
				}

				return &distributedlockmock.LockMock{
					ReleaseFunc: func(context.Context) error { return nil },
				}, nil
			},
		}

		const maxPoll = time.Hour

		// A factor this large pushes interval*factor past what a Duration can
		// hold. The stepwise growth has to notice and clamp, not wrap around
		// into a negative wait that would turn the poller hot.
		scoped, err := distributedlock.NewScopedLocker(
			raw,
			distributedlock.WithScopedPollInterval(scopedPollInterval),
			distributedlock.WithScopedPollBackoff(1e300, maxPoll),
			distributedlock.WithScopedRand(noJitter),
			distributedlock.WithScopedClock(c),
		)
		must.NoError(t, err)

		must.NoError(t, scoped.WithLock(t.Context(), scopedTestKey, func(context.Context) error { return nil }))

		must.SliceLen(t, 2, c.SleepCalls())
		test.EqOp(t, scopedPollInterval, c.SleepCalls()[0].D)
		test.EqOp(t, maxPoll, c.SleepCalls()[1].D)
	})
}

func TestScopedLocker_LockerFailures(T *testing.T) {
	T.Parallel()

	errAcquireBroken := errors.New("lock backend unreachable")

	T.Run("WithLock surfaces a non-contention acquire error immediately", func(t *testing.T) {
		t.Parallel()

		raw := &distributedlockmock.LockerMock{
			AcquireFunc: func(context.Context, string, time.Duration) (distributedlock.Lock, error) {
				return nil, errAcquireBroken
			},
		}

		scoped, err := distributedlock.NewScopedLocker(raw)
		must.NoError(t, err)

		// Not ErrLockNotAcquired, so there is nothing to wait out: it returns
		// on the first attempt rather than polling a broken backend.
		test.ErrorIs(t, scoped.WithLock(t.Context(), scopedTestKey, func(context.Context) error { return nil }), errAcquireBroken)
		test.SliceLen(t, 1, raw.AcquireCalls())
	})

	T.Run("TryWithLock surfaces a non-contention acquire error", func(t *testing.T) {
		t.Parallel()

		raw := &distributedlockmock.LockerMock{
			AcquireFunc: func(context.Context, string, time.Duration) (distributedlock.Lock, error) {
				return nil, errAcquireBroken
			},
		}

		scoped, err := distributedlock.NewScopedLocker(raw)
		must.NoError(t, err)

		acquired, err := scoped.TryWithLock(t.Context(), scopedTestKey, func(context.Context) error { return nil })
		test.False(t, acquired)
		test.ErrorIs(t, err, errAcquireBroken)
	})

	T.Run("a release failure is joined onto fn's result", func(t *testing.T) {
		t.Parallel()

		errReleaseBroken := errors.New("release rejected")

		raw := &distributedlockmock.LockerMock{
			AcquireFunc: func(context.Context, string, time.Duration) (distributedlock.Lock, error) {
				return &distributedlockmock.LockMock{
					ReleaseFunc: func(context.Context) error { return errReleaseBroken },
				}, nil
			},
		}

		scoped, err := distributedlock.NewScopedLocker(raw)
		must.NoError(t, err)

		errFn := errors.New("fn failed")
		err = scoped.WithLock(t.Context(), scopedTestKey, func(context.Context) error { return errFn })

		// Both survive: the caller's own failure and the fact that the lock
		// could not be handed back.
		test.ErrorIs(t, err, errFn)
		test.ErrorIs(t, err, errReleaseBroken)
	})

	T.Run("an expired lock is reported distinctly", func(t *testing.T) {
		t.Parallel()

		// ErrLockNotHeld on release means the TTL elapsed while fn was still
		// running, so mutual exclusion was not actually held throughout.
		raw := &distributedlockmock.LockerMock{
			AcquireFunc: func(context.Context, string, time.Duration) (distributedlock.Lock, error) {
				return &distributedlockmock.LockMock{
					ReleaseFunc: func(context.Context) error { return distributedlock.ErrLockNotHeld },
				}, nil
			},
		}

		scoped, err := distributedlock.NewScopedLocker(raw)
		must.NoError(t, err)

		err = scoped.WithLock(t.Context(), scopedTestKey, func(context.Context) error { return nil })
		test.ErrorIs(t, err, distributedlock.ErrLockNotHeld)
	})
}

func TestScopedLocker_WithLock_Backoff(T *testing.T) {
	T.Parallel()

	// Each case holds the lock past the first poll so a second wait is
	// scheduled, releases it, then steps the bubble to either side of the
	// second wait's deadline. Acquiring early or late fails, which pins the
	// exact interval rather than merely asserting "it eventually acquired".
	T.Run("the second wait is the first grown by the backoff factor", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			scoped, raw := newScopedFixture(t,
				distributedlock.WithScopedPollBackoff(2, time.Minute))

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			held, err := raw.Acquire(ctx, scopedTestKey, scopedLockTTL)
			must.NoError(t, err)

			done := startWaiter(t, ctx, scoped)

			// Burn the first wait against a still-held lock, scheduling a
			// second one of 2 x scopedPollInterval.
			time.Sleep(scopedPollInterval)
			requireWaiting(t, done, "on a held lock")

			must.NoError(t, held.Release(ctx))

			// One interval in, the un-grown schedule would already have fired.
			time.Sleep(scopedPollInterval)
			requireWaiting(t, done, "one poll interval after release")

			time.Sleep(scopedPollInterval)
			requireAcquired(t, done, "two poll intervals after release")
		})
	})

	T.Run("growth is clamped to the max poll interval", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			// A factor of 10 would make the second wait 1s; the max pins it to
			// 2 x scopedPollInterval instead.
			scoped, raw := newScopedFixture(t,
				distributedlock.WithScopedPollBackoff(10, 2*scopedPollInterval))

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			held, err := raw.Acquire(ctx, scopedTestKey, scopedLockTTL)
			must.NoError(t, err)

			done := startWaiter(t, ctx, scoped)

			time.Sleep(scopedPollInterval)
			requireWaiting(t, done, "on a held lock")

			must.NoError(t, held.Release(ctx))

			time.Sleep(scopedPollInterval)
			requireWaiting(t, done, "one poll interval after release")

			// Unclamped this would still be waiting at 2 intervals, with 8 to go.
			time.Sleep(scopedPollInterval)
			requireAcquired(t, done, "at the clamped max poll interval")
		})
	})

	T.Run("a factor of one keeps the interval fixed", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			scoped, raw := newScopedFixture(t,
				distributedlock.WithScopedPollBackoff(1, time.Minute))

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			held, err := raw.Acquire(ctx, scopedTestKey, scopedLockTTL)
			must.NoError(t, err)

			done := startWaiter(t, ctx, scoped)

			time.Sleep(scopedPollInterval)
			requireWaiting(t, done, "on a held lock")

			must.NoError(t, held.Release(ctx))

			// Ungrown, so the very next interval is the one that acquires.
			time.Sleep(scopedPollInterval)
			requireAcquired(t, done, "one poll interval after release")
		})
	})

	T.Run("jitter keeps each wait within half the interval", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			// The floor of the jitter range: every wait is exactly half the
			// interval, so the lock is taken half an interval after release.
			scoped, raw := newScopedFixture(t,
				distributedlock.WithScopedRand(func() float64 { return 0 }),
				distributedlock.WithScopedPollBackoff(1, time.Minute))

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			held, err := raw.Acquire(ctx, scopedTestKey, scopedLockTTL)
			must.NoError(t, err)

			done := startWaiter(t, ctx, scoped)
			must.NoError(t, held.Release(ctx))

			time.Sleep(scopedPollInterval / 2)
			requireAcquired(t, done, "at half the poll interval with jitter at its floor")
		})
	})
}

func TestScopedLocker_TryWithLock(T *testing.T) {
	T.Parallel()

	T.Run("runs fn under the lock and releases after", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		scoped, raw := newScopedFixture(t)

		ran := false
		acquired, err := scoped.TryWithLock(ctx, scopedTestKey, func(context.Context) error {
			ran = true

			// The lock is genuinely held while fn runs.
			_, acquireErr := raw.Acquire(ctx, scopedTestKey, time.Minute)
			test.ErrorIs(t, acquireErr, distributedlock.ErrLockNotAcquired)

			return nil
		})

		must.NoError(t, err)
		test.True(t, acquired)
		test.True(t, ran)

		// And released once fn returns.
		held, err := raw.Acquire(ctx, scopedTestKey, time.Minute)
		must.NoError(t, err)
		must.NoError(t, held.Release(ctx))
	})
}

func TestScopedLocker_WithLock(T *testing.T) {
	T.Parallel()

	T.Run("waits out contention by polling", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			scoped, raw := newScopedFixture(t)

			ctx, cancel := context.WithCancel(t.Context())
			// Unwind the waiter on the way out so a failed assertion reports
			// itself rather than stranding the bubble in a deadlock.
			defer cancel()

			held, err := raw.Acquire(ctx, scopedTestKey, scopedLockTTL)
			must.NoError(t, err)

			done := make(chan error, 1)
			go func() {
				done <- scoped.WithLock(ctx, scopedTestKey, func(context.Context) error {
					return nil
				})
			}()

			// Wait returns once the adapter is asleep between polls, so the
			// release below is what the next poll observes.
			synctest.Wait()
			must.NoError(t, held.Release(ctx))

			// Releasing alone must not wake it. Wait parks everything without
			// moving the clock, so a result here would mean the adapter acquired
			// outside its poll schedule.
			synctest.Wait()
			select {
			case <-done:
				t.Fatal("WithLock returned before its next poll came due")
			default:
			}

			// Crossing the poll deadline is what lets it through. Both checks are
			// non-blocking: a blocking receive would let the bubble idle forward
			// to any later deadline and pass regardless of the interval.
			time.Sleep(scopedPollInterval)
			synctest.Wait()
			select {
			case err = <-done:
				must.NoError(t, err)
			default:
				t.Fatal("WithLock did not acquire on the first poll after the release")
			}
		})
	})

	T.Run("a canceled context ends the wait", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			scoped, raw := newScopedFixture(t)

			held, err := raw.Acquire(t.Context(), scopedTestKey, time.Minute)
			must.NoError(t, err)
			t.Cleanup(func() { _ = held.Release(t.Context()) })

			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() {
				done <- scoped.WithLock(ctx, scopedTestKey, func(context.Context) error {
					t.Error("fn must not run; the lock is never released")
					return nil
				})
			}()

			// Cancel only once the adapter is parked in its poll sleep, so the
			// wake has to come from the context rather than from a poll tick.
			synctest.Wait()
			cancel()

			// Non-blocking, for the same reason as above: a blocking receive
			// would idle the bubble forward to the next poll and pass even if
			// cancellation were ignored.
			synctest.Wait()
			select {
			case err = <-done:
				test.ErrorIs(t, err, context.Canceled)
			default:
				t.Fatal("WithLock did not observe cancellation")
			}
		})
	})
}

// TestScopedLocker_Conformance runs the shared distributedlock.ScopedLocker
// suite against the generic adapter. The cases above describe how this
// implementation reaches the contract — the poll schedule, the backoff, what a
// canceled context does to a waiter — and none of that is visible to a caller
// holding the interface. What the suite pins is what is: that a contended
// TryWithLock is a false rather than an error, and that the postgres
// implementation, which waits in the database instead of polling, answers the
// same way.
func TestScopedLocker_Conformance(T *testing.T) {
	T.Parallel()

	distributedlocktest.RunScoped(T, func(tb testing.TB) distributedlock.ScopedLocker {
		tb.Helper()

		raw, err := memory.NewLocker()
		must.NoError(tb, err)
		tb.Cleanup(func() { must.NoError(tb, raw.Close()) })

		scoped, err := distributedlock.NewScopedLocker(raw)
		must.NoError(tb, err)

		return scoped
	})
}

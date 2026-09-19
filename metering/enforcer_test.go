package metering

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/cache"
	cachemock "github.com/primandproper/primitives-go/v2/cache/mock"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/observability/logging"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// enforcerEnv is one enforcer with the pieces a test needs to reach around it.
type enforcerEnv struct {
	enforcer *QuotaEnforcer
	db       *storeEnv
	store    Store
	totals   cache.Cache[CachedTotal]
	clock    *stubClock
}

// newTestEnforcer builds an enforcer over a fresh store and an in-memory cache.
func newTestEnforcer(tb testing.TB, behavior QuotaBehavior, limit int64, opts ...EnforcerOption) *enforcerEnv {
	tb.Helper()

	db := newSQLiteEnv(tb)
	store := db.newStore(tb)
	c := newStubClock()

	totals := newStubCache(c)

	enforcer, err := db.newEnforcer(tb, &EnforcerConfig{},
		store, newTestRegistry(tb, behavior, limit),
		append([]EnforcerOption{WithEnforcerClock(c), WithEnforcerCache(totals)}, opts...)...)
	must.NoError(tb, err)

	return &enforcerEnv{enforcer: enforcer, db: db, store: store, totals: totals, clock: c}
}

// consume decides and records in a transaction of its own.
func (e *enforcerEnv) consume(tb testing.TB, subject, meter string, quantity int64) (*Decision, error) {
	tb.Helper()

	return consumeIn(tb, e.db, e.enforcer, subject, meter, quantity)
}

// consumeUsage is consume over a full Usage record.
//
//nolint:gocritic // hugeParam: Usage is taken by value to match ConsumeUsage
func (e *enforcerEnv) consumeUsage(tb testing.TB, u Usage) (*Decision, error) {
	tb.Helper()

	return inTx(tb, e.db.client, func(tx database.Tx) (*Decision, error) {
		return e.enforcer.ConsumeUsage(tb.Context(), tx, testScope, u)
	})
}

func TestNewQuotaEnforcer(T *testing.T) {
	T.Parallel()

	db := newSQLiteEnv(T)
	store := db.newStore(T)

	T.Run("refuses a nil config, store, or registry", func(t *testing.T) {
		t.Parallel()

		registry := newTestRegistry(t, BehaviorBlock, 10)

		_, err := db.newEnforcer(t, nil, store, registry)
		test.Error(t, err)

		_, err = db.newEnforcer(t, &EnforcerConfig{}, nil, registry)
		test.ErrorIs(t, err, ErrNilStore)

		_, err = db.newEnforcer(t, &EnforcerConfig{}, store, nil)
		test.ErrorIs(t, err, ErrNilRegistry)

		// The reader is not optional the way the cache is. An enforcer that
		// cannot read the durable total answers a cache miss with nothing.
		_, err = NewQuotaEnforcer(t.Context(), &EnforcerConfig{}, store, registry, nil)
		test.ErrorIs(t, err, ErrNilExecutor)
	})

	T.Run("fills defaults and ignores nil options", func(t *testing.T) {
		t.Parallel()

		enforcer, err := db.newEnforcer(t, &EnforcerConfig{}, store,
			newTestRegistry(t, BehaviorBlock, 10), nil,
			WithEnforcerClock(nil), WithEnforcerCache(nil),
			WithEnforcerQuotaSource(nil), WithEnforcerPeriodResolver(nil),
			WithEnforcerLogger(nil), WithEnforcerTracerProvider(nil), WithEnforcerMetricsProvider(nil))
		must.NoError(t, err)

		test.EqOp(t, DefaultStaleness, enforcer.cfg.Staleness)
		test.EqOp(t, DefaultCachePrefix, enforcer.cfg.CachePrefix)
		test.NotNil(t, enforcer.clock)
		test.NotNil(t, enforcer.quotas)
		test.NotNil(t, enforcer.resolver)
		// A nil cache is honored rather than replaced: it is a supported, and
		// loudly announced, configuration.
		test.Nil(t, enforcer.totals)
	})
}

func TestQuotaEnforcer_Check(T *testing.T) {
	T.Parallel()

	T.Run("allows under the limit and writes nothing", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		decision, err := env.enforcer.Check(t.Context(), testScope, testSubject, testMeter, 10)
		must.NoError(t, err)

		test.True(t, decision.Allowed)
		test.EqOp(t, int64(10), decision.Used)
		test.EqOp(t, int64(100), decision.Limit)
		test.EqOp(t, monthBounds.End, decision.ResetsAt)
		// A Check that recorded would be a Consume, and every read path would pay
		// for a durable write.
		test.EqOp(t, int64(0), totalOf(t, env.db, env.store))
	})

	T.Run("counts the caller's quantity against the total", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		must.NoError(t, mustRecord(t, env.db, env.store, newEntry("seed", 95, AggregationSum)))

		decision, err := env.enforcer.Check(t.Context(), testScope, testSubject, testMeter, 10)
		must.NoError(t, err)

		// Not "are they under the limit now" but "would this take them over",
		// which is the question a gate in front of an operation is asking.
		test.False(t, decision.Allowed)
		test.EqOp(t, int64(105), decision.Used)
		test.EqOp(t, int64(5), decision.Overage)
	})

	T.Run("serves the first read from the durable store, not stale", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		must.NoError(t, mustRecord(t, env.db, env.store, newEntry("seed", 40, AggregationSum)))

		decision, err := env.enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1)
		must.NoError(t, err)

		test.False(t, decision.Stale)
		test.EqOp(t, int64(41), decision.Used)
	})

	T.Run("serves a subsequent read from cache, marked stale", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		must.NoError(t, mustRecord(t, env.db, env.store, newEntry("seed", 40, AggregationSum)))

		_, err := env.enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1)
		must.NoError(t, err)

		// Usage the cache does not know about yet. The staleness budget is
		// exactly this window, and Decision.Stale says so rather than pretending
		// the number is fresh.
		must.NoError(t, mustRecord(t, env.db, env.store, newEntry("more", 50, AggregationSum)))

		decision, err := env.enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1)
		must.NoError(t, err)

		test.True(t, decision.Stale)
		test.EqOp(t, int64(41), decision.Used)
	})

	// The quota is cached beside the total, under the same key and the same
	// budget, because the source behind it is not necessarily cheap: the wiring
	// this module recommends resolves a plan and reads a subscription row to
	// answer QuotaFor, and a Check that asked per call put that on every request
	// path a quota guards.
	T.Run("resolves no quota on a read the cache answers", func(t *testing.T) {
		t.Parallel()

		quotas := &countingQuotaSource{quota: testQuota(100)}
		env := newTestEnforcer(t, BehaviorBlock, 100, WithEnforcerQuotaSource(quotas))

		first, err := env.enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1)
		must.NoError(t, err)
		test.EqOp(t, int64(1), quotas.calls.Load())

		second, err := env.enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1)
		must.NoError(t, err)

		// Still one. And the limit the cached quota carries is the one the
		// decision is made against, rather than the zero a missing quota would
		// have made it — which would refuse every request for the whole budget.
		test.EqOp(t, int64(1), quotas.calls.Load())
		test.EqOp(t, first.Limit, second.Limit)
		test.EqOp(t, int64(100), second.Limit)
		test.True(t, second.Allowed)
		test.True(t, second.Stale)
	})

	T.Run("issues no statement on a read the cache answers", func(t *testing.T) {
		t.Parallel()

		db := newSQLiteEnv(t)
		store := db.newStore(t)
		c := newStubClock()

		// The reader both halves would go through: metering's own total read, and
		// — wired as entitlements.QuotaSource is — the quota source's plan lookup.
		counter := &countingExecutor{q: db.client.Reader()}

		enforcer, err := NewQuotaEnforcer(t.Context(), &EnforcerConfig{}, store,
			newTestRegistry(t, BehaviorBlock, 100), counter,
			WithEnforcerClock(c), WithEnforcerCache(newStubCache(c)))
		must.NoError(t, err)

		_, err = enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1)
		must.NoError(t, err)
		test.Greater(t, int64(0), counter.statements.Load())

		counter.statements.Store(0)

		_, err = enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1)
		must.NoError(t, err)

		// None at all. This is the promise "Check is fast and slightly stale"
		// makes, and it is a promise about the database rather than about one
		// store's querier: a source that reached for a row of its own would show
		// up here.
		test.EqOp(t, int64(0), counter.statements.Load())
	})

	// An entry written before the quota was cached beside the total decodes with
	// no error and no quota, and a zero Quota is a limit of zero under an empty
	// behavior — every request refused for the rest of the budget.
	T.Run("treats an entry carrying no quota as a miss", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		must.NoError(t, mustRecord(t, env.db, env.store, newEntry("seed", 40, AggregationSum)))
		must.NoError(t, env.totals.Set(t.Context(),
			env.enforcer.cacheKey(testScope, testSubject, testMeter, monthBounds),
			&CachedTotal{Quantity: 9_000, PeriodEnd: monthBounds.End}))

		decision, err := env.enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1)
		must.NoError(t, err)

		// Read afresh, not answered from the half-entry: the total is the durable
		// one and the limit is the source's.
		test.EqOp(t, int64(41), decision.Used)
		test.EqOp(t, int64(100), decision.Limit)
		test.False(t, decision.Stale)

		// And rewritten whole, so the next Check pays nothing.
		entry, err := env.totals.Get(t.Context(),
			env.enforcer.cacheKey(testScope, testSubject, testMeter, monthBounds))
		must.NoError(t, err)
		must.NotNil(t, entry.Quota)
	})

	T.Run("re-reads once the staleness budget expires", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		must.NoError(t, mustRecord(t, env.db, env.store, newEntry("seed", 40, AggregationSum)))

		_, err := env.enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1)
		must.NoError(t, err)

		must.NoError(t, mustRecord(t, env.db, env.store, newEntry("more", 50, AggregationSum)))

		// The TTL is the whole staleness mechanism: an entry that expires is an
		// entry re-read from the durable total, which bounds staleness by
		// construction rather than by everybody remembering to invalidate.
		env.clock.advance(DefaultStaleness + time.Second)

		decision, err := env.enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1)
		must.NoError(t, err)

		test.False(t, decision.Stale)
		test.EqOp(t, int64(91), decision.Used)
	})

	T.Run("honors a per-meter staleness budget", func(t *testing.T) {
		t.Parallel()

		db := newSQLiteEnv(t)
		store := db.newStore(t)
		c := newStubClock()

		totals := newStubCache(c)

		registry := NewRegistry()
		must.NoError(t, registry.RegisterMeter(Meter{
			Name: testMeter, Aggregation: AggregationSum, Period: PeriodMonth,
			// Tighter than the enforcer default, which is what a meter whose unit
			// is worth real money asks for.
			Staleness: time.Second,
		}))
		must.NoError(t, registry.RegisterQuota(Quota{
			Meter: testMeter, Limit: 100, Behavior: BehaviorBlock, Period: PeriodMonth,
		}))

		enforcer, err := db.newEnforcer(t, &EnforcerConfig{}, store, registry,
			WithEnforcerClock(c), WithEnforcerCache(totals))
		must.NoError(t, err)

		must.NoError(t, mustRecord(t, db, store, newEntry("seed", 40, AggregationSum)))

		_, err = enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1)
		must.NoError(t, err)

		must.NoError(t, mustRecord(t, db, store, newEntry("more", 50, AggregationSum)))

		c.advance(2 * time.Second)

		decision, err := enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1)
		must.NoError(t, err)

		test.False(t, decision.Stale)
		test.EqOp(t, int64(91), decision.Used)
	})

	T.Run("never caches past the end of a period", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		must.NoError(t, mustRecord(t, env.db, env.store, newEntry("seed", 40, AggregationSum)))

		// Five seconds before the month ends, with a ten-second budget. An entry
		// that outlived its window would answer the next period's first Check
		// with the last period's total — a quota that starts full on the first.
		env.clock.advance(monthBounds.End.Sub(baseTime) - 5*time.Second)

		_, err := env.enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1)
		must.NoError(t, err)

		env.clock.advance(6 * time.Second)

		decision, err := env.enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1)
		must.NoError(t, err)

		test.False(t, decision.Stale)
		test.EqOp(t, int64(1), decision.Used)
	})

	T.Run("works with no cache at all", func(t *testing.T) {
		t.Parallel()

		db := newSQLiteEnv(t)
		store := db.newStore(t)

		enforcer, err := db.newEnforcer(t, &EnforcerConfig{}, store,
			newTestRegistry(t, BehaviorBlock, 100), WithEnforcerClock(newStubClock()))
		must.NoError(t, err)

		must.NoError(t, mustRecord(t, db, store, newEntry("seed", 40, AggregationSum)))

		decision, err := enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1)
		must.NoError(t, err)

		// Correct, and a durable read every time — which is the thing the package
		// docs warn about at length.
		test.EqOp(t, int64(41), decision.Used)
		test.False(t, decision.Stale)
	})

	// Running without a cache is a supported configuration and an expensive one,
	// and the difference between the two is a line at construction. Nobody reads
	// a Check's latency and infers the cache was never wired; they read this.
	T.Run("says so at construction when it has no cache", func(t *testing.T) {
		t.Parallel()

		db := newSQLiteEnv(t)
		store := db.newStore(t)
		logger := newRecordingLogger()

		_, err := db.newEnforcer(t, &EnforcerConfig{}, store,
			newTestRegistry(t, BehaviorBlock, 100),
			WithEnforcerClock(newStubClock()), WithEnforcerLogger(logger))
		must.NoError(t, err)

		test.SliceContains(t, logger.messages(logging.InfoLevel),
			"metering enforcer has no cache; every Check will read the durable total")
	})

	T.Run("says nothing at construction when it has one", func(t *testing.T) {
		t.Parallel()

		db := newSQLiteEnv(t)
		store := db.newStore(t)
		logger := newRecordingLogger()
		c := newStubClock()

		_, err := db.newEnforcer(t, &EnforcerConfig{}, store,
			newTestRegistry(t, BehaviorBlock, 100),
			WithEnforcerClock(c), WithEnforcerCache(newStubCache(c)), WithEnforcerLogger(logger))
		must.NoError(t, err)

		test.SliceEmpty(t, logger.at(logging.InfoLevel))
	})

	T.Run("carries on through a broken cache", func(t *testing.T) {
		t.Parallel()

		db := newSQLiteEnv(t)
		store := db.newStore(t)

		broken := &cachemock.CacheMock[CachedTotal]{
			GetFunc: func(context.Context, string) (*CachedTotal, error) { return nil, errArbitrary },
			SetFunc: func(context.Context, string, *CachedTotal, ...cache.WriteOption) error { return errArbitrary },
		}

		instruments := newRecordingInstruments()

		enforcer, err := db.newEnforcer(t, &EnforcerConfig{}, store,
			newTestRegistry(t, BehaviorBlock, 100),
			WithEnforcerClock(newStubClock()), WithEnforcerCache(broken),
			WithEnforcerMetricsProvider(instruments.provider()))
		must.NoError(t, err)

		must.NoError(t, mustRecord(t, db, store, newEntry("seed", 40, AggregationSum)))

		// A cache that is down turns Check into a durable read, which is slow and
		// correct. The wrong response to a degraded cache is to stop answering.
		decision, err := enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1)
		must.NoError(t, err)

		test.EqOp(t, int64(41), decision.Used)

		// Carrying on is the behavior; counting it is what keeps the cost
		// visible. Every Check is now a durable read, and the counter is the
		// only thing that says why. Twice for one Check: the read that could
		// not be served and the refresh that could not be written are separate
		// failures of the same cache, and a Check that stopped counting the
		// second would under-report a cache that is up for reads and down for
		// writes.
		test.Eq(t, []int64{1, 1}, instruments.recorded("_cache_errors"))
	})

	T.Run("counts no cache error when the write lands", func(t *testing.T) {
		t.Parallel()

		instruments := newRecordingInstruments()
		env := newTestEnforcer(t, BehaviorBlock, 100, WithEnforcerMetricsProvider(instruments.provider()))

		must.NoError(t, mustRecord(t, env.db, env.store, newEntry("seed", 40, AggregationSum)))

		_, err := env.enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1)
		must.NoError(t, err)

		test.SliceEmpty(t, instruments.recorded("_cache_errors"))
	})

	T.Run("treats a cache miss as a miss, not a failure", func(t *testing.T) {
		t.Parallel()

		db := newSQLiteEnv(t)
		store := db.newStore(t)

		missing := &cachemock.CacheMock[CachedTotal]{
			GetFunc: func(context.Context, string) (*CachedTotal, error) { return nil, cache.ErrNotFound },
			SetFunc: func(context.Context, string, *CachedTotal, ...cache.WriteOption) error { return nil },
		}

		enforcer, err := db.newEnforcer(t, &EnforcerConfig{}, store,
			newTestRegistry(t, BehaviorBlock, 100),
			WithEnforcerClock(newStubClock()), WithEnforcerCache(missing))
		must.NoError(t, err)

		decision, err := enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1)
		must.NoError(t, err)

		test.EqOp(t, int64(1), decision.Used)
	})

	T.Run("reports an unknown meter", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		_, err := env.enforcer.Check(t.Context(), testScope, testSubject, "not_registered", 1)

		test.ErrorIs(t, err, ErrUnknownMeter)
	})

	T.Run("reports a meter with no quota", func(t *testing.T) {
		t.Parallel()

		db := newSQLiteEnv(t)
		store := db.newStore(t)

		registry := NewRegistry()
		must.NoError(t, registry.RegisterMeter(Meter{
			Name: testMeter, Aggregation: AggregationSum, Period: PeriodMonth,
		}))

		enforcer, err := db.newEnforcer(t, &EnforcerConfig{}, store, registry,
			WithEnforcerClock(newStubClock()))
		must.NoError(t, err)

		// Unmetered is not unlimited, and this package will not pretend
		// otherwise.
		_, err = enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1)

		test.ErrorIs(t, err, ErrNoQuota)
	})

	T.Run("answers an unlimited quota without a limit", func(t *testing.T) {
		t.Parallel()

		db := newSQLiteEnv(t)
		store := db.newStore(t)

		registry := NewRegistry()
		must.NoError(t, registry.RegisterMeter(Meter{
			Name: testMeter, Aggregation: AggregationSum, Period: PeriodMonth,
		}))
		must.NoError(t, registry.RegisterUnlimitedQuota(testMeter))

		enforcer, err := db.newEnforcer(t, &EnforcerConfig{}, store, registry,
			WithEnforcerClock(newStubClock()))
		must.NoError(t, err)

		// The alternative this exists to replace: no quota at all, which is
		// ErrNoQuota on every check rather than a decision anybody made.
		decision, err := enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1_000_000_000)
		must.NoError(t, err)

		test.True(t, decision.Allowed)
		test.EqOp(t, int64(0), decision.Overage)
		test.EqOp(t, Unlimited, decision.Limit)

		// And through the durable path, where the limit is bound into SQL rather
		// than compared in Go: the largest int64 there is still a limit the
		// engine can hold and nobody can reach.
		decision, err = consumeIn(t, db, enforcer, testSubject, testMeter, 1_000_000_000)
		must.NoError(t, err)

		test.True(t, decision.Allowed)
		test.EqOp(t, int64(0), decision.Overage)
		test.EqOp(t, Unlimited, decision.Limit)
		test.EqOp(t, int64(1_000_000_000), decision.Used)
	})

	T.Run("reports a quota over the wrong period", func(t *testing.T) {
		t.Parallel()

		// A QuotaSource is application code and the registry cannot vet what it
		// returns at wiring time, so the check RegisterQuota runs once runs again
		// here — a quota over the wrong window reads a total nothing writes to.
		env := newTestEnforcer(t, BehaviorBlock, 100, WithEnforcerQuotaSource(QuotaSourceFunc(
			func(context.Context, string, string) (Quota, error) {
				return Quota{Meter: testMeter, Limit: 10, Behavior: BehaviorBlock, Period: PeriodDay}, nil
			})))

		_, err := env.enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1)

		test.ErrorIs(t, err, ErrPeriodMismatch)
	})

	T.Run("propagates a quota source failure", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100, WithEnforcerQuotaSource(QuotaSourceFunc(
			func(context.Context, string, string) (Quota, error) {
				return Quota{}, errArbitrary
			})))

		_, err := env.enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1)

		test.ErrorIs(t, err, errArbitrary)
	})

	T.Run("propagates a period resolution failure", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100, WithEnforcerPeriodResolver(PeriodResolverFunc(
			func(context.Context, string, Period, time.Time) (Bounds, error) {
				return Bounds{}, errArbitrary
			})))

		_, err := env.enforcer.Check(t.Context(), testScope, testSubject, testMeter, 1)

		test.ErrorIs(t, err, errArbitrary)
	})

	T.Run("honors a per-subject quota source", func(t *testing.T) {
		t.Parallel()

		// Where plans live. Two customers on different plans have different
		// limits for the same meter, and that mapping is the application's.
		env := newTestEnforcer(t, BehaviorBlock, 100, WithEnforcerQuotaSource(QuotaSourceFunc(
			func(_ context.Context, subject, meter string) (Quota, error) {
				limit := int64(10)
				if subject == "enterprise" {
					limit = 1_000_000
				}

				return Quota{Meter: meter, Limit: limit, Behavior: BehaviorBlock, Period: PeriodMonth}, nil
			})))

		free, err := env.enforcer.Check(t.Context(), testScope, testSubject, testMeter, 50)
		must.NoError(t, err)
		test.False(t, free.Allowed)

		enterprise, err := env.enforcer.Check(t.Context(), testScope, "enterprise", testMeter, 50)
		must.NoError(t, err)
		test.True(t, enterprise.Allowed)
	})
}

func TestQuotaEnforcer_CheckFailurePolicy(T *testing.T) {
	T.Parallel()

	newFailing := func(t *testing.T, failOpen bool) *QuotaEnforcer {
		t.Helper()

		db := newSQLiteEnv(t)
		store := db.newStore(t)

		enforcer, err := db.newEnforcer(t, &EnforcerConfig{FailOpen: failOpen},
			&failingTotalStore{Store: store}, newTestRegistry(t, BehaviorBlock, 100),
			WithEnforcerClock(newStubClock()))
		must.NoError(t, err)

		return enforcer
	}

	T.Run("fails closed by default", func(t *testing.T) {
		t.Parallel()

		// The right answer whenever the quota guards something that costs money:
		// an outage that lets every subject past every limit bills the operator.
		_, err := newFailing(t, false).Check(t.Context(), testScope, testSubject, testMeter, 1)

		test.ErrorIs(t, err, errArbitrary)
	})

	T.Run("fails open when configured to", func(t *testing.T) {
		t.Parallel()

		decision, err := newFailing(t, true).Check(t.Context(), testScope, testSubject, testMeter, 1)
		must.NoError(t, err)

		test.True(t, decision.Allowed)
		// Used is left at zero and Stale is set, which is the honest description
		// of an answer derived from nothing.
		test.EqOp(t, int64(0), decision.Used)
		test.True(t, decision.Stale)
	})
}

// The instruments a decision moves are how an overage is noticed at all: the
// decision itself is returned to one caller and forgotten, and the counter is
// what an invoice line is reconciled against.
func TestQuotaEnforcer_observeDecision(T *testing.T) {
	T.Parallel()

	T.Run("counts the units let past the limit, and only those", func(t *testing.T) {
		t.Parallel()

		instruments := newRecordingInstruments()
		env := newTestEnforcer(t, BehaviorAllowOverage, 100,
			WithEnforcerMetricsProvider(instruments.provider()))

		// Exactly at the limit is allowed and is not an overage. It is the one
		// quantity that separates a counter of excess units from a counter of
		// decisions: an overage of zero posted to the same series would put an
		// invoice line on every subject who used their allowance exactly.
		decision, err := env.consume(t, testSubject, testMeter, 100)
		must.NoError(t, err)

		test.True(t, decision.Allowed)
		test.EqOp(t, int64(0), decision.Overage)
		test.SliceEmpty(t, instruments.recorded("_overage"))
		test.SliceEmpty(t, instruments.recorded("_denied"))

		// One unit past it is one unit of overage, in the meter's unit rather
		// than in events.
		decision, err = env.consume(t, testSubject, testMeter, 1)
		must.NoError(t, err)

		test.EqOp(t, int64(1), decision.Overage)
		test.Eq(t, []int64{1}, instruments.recorded("_overage"))
	})

	T.Run("counts a refusal", func(t *testing.T) {
		t.Parallel()

		instruments := newRecordingInstruments()
		env := newTestEnforcer(t, BehaviorBlock, 100,
			WithEnforcerMetricsProvider(instruments.provider()))

		decision, err := env.consume(t, testSubject, testMeter, 101)
		must.NoError(t, err)

		test.False(t, decision.Allowed)
		test.Eq(t, []int64{1}, instruments.recorded("_denied"))
	})
}

func TestQuotaEnforcer_Consume(T *testing.T) {
	T.Parallel()

	T.Run("records against the durable total", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		decision, err := env.consume(t, testSubject, testMeter, 30)
		must.NoError(t, err)

		test.True(t, decision.Allowed)
		test.EqOp(t, int64(30), decision.Used)
		// Never stale: an exact answer is the whole promise.
		test.False(t, decision.Stale)
		test.EqOp(t, int64(30), totalOf(t, env.db, env.store))
	})

	T.Run("counts a retried Consume twice", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		// Documented and unavoidable: the signature has no key that could be
		// stable across retries. Any path that can retry uses ConsumeUsage.
		for range 2 {
			_, err := env.consume(t, testSubject, testMeter, 30)
			must.NoError(t, err)
		}

		test.EqOp(t, int64(60), totalOf(t, env.db, env.store))
	})

	T.Run("blocks past the limit", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		must.NoError(t, mustRecord(t, env.db, env.store, newEntry("seed", 95, AggregationSum)))

		decision, err := env.consume(t, testSubject, testMeter, 10)
		must.NoError(t, err)

		test.False(t, decision.Allowed)
		test.EqOp(t, int64(95), totalOf(t, env.db, env.store))
	})

	T.Run("evicts the cache it just made stale", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		_, err := env.consume(t, testSubject, testMeter, 30)
		must.NoError(t, err)

		// Evicted rather than written through, so the next Check pays a durable
		// read and gets a number that is true rather than one that was true if
		// the caller's transaction committed. It did here, and the durable read
		// says so.
		decision, err := env.enforcer.Check(t.Context(), testScope, testSubject, testMeter, 0)
		must.NoError(t, err)

		test.False(t, decision.Stale)
		test.EqOp(t, int64(30), decision.Used)
	})

	T.Run("caches nothing for a consume the caller rolled back", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		// Warm the cache, so there is an entry for the consume to invalidate and
		// a hit for the Check afterwards to find if it does not.
		_, err := env.enforcer.Check(t.Context(), testScope, testSubject, testMeter, 0)
		must.NoError(t, err)

		// The shape this port exists for, failing: the work the consume
		// authorized did not commit, so neither did the usage.
		test.ErrorIs(t, env.db.client.WithTransaction(t.Context(), func(tx database.Tx) error {
			decision, consumeErr := env.enforcer.Consume(t.Context(), tx, testScope, testSubject, testMeter, 30)
			must.NoError(t, consumeErr)
			test.True(t, decision.Allowed)

			return errArbitrary
		}), errArbitrary)

		// A written-through total would report 30 here for the whole staleness
		// budget — refusing requests against usage nobody incurred.
		decision, err := env.enforcer.Check(t.Context(), testScope, testSubject, testMeter, 0)
		must.NoError(t, err)

		test.EqOp(t, int64(0), decision.Used)
		test.EqOp(t, int64(0), totalOf(t, env.db, env.store))
	})

	T.Run("propagates a store failure with no fail-open", func(t *testing.T) {
		t.Parallel()

		db := newSQLiteEnv(t)
		store := db.newStore(t)

		enforcer, err := db.newEnforcer(t, &EnforcerConfig{FailOpen: true},
			&failingConsumeStore{Store: store}, newTestRegistry(t, BehaviorBlock, 100),
			WithEnforcerClock(newStubClock()))
		must.NoError(t, err)

		// Even with FailOpen set. An exact answer has nowhere to fail open to:
		// allowing usage the store could not record is allowing usage nobody will
		// ever be billed for.
		_, err = consumeIn(t, db, enforcer, testSubject, testMeter, 1)

		test.ErrorIs(t, err, errArbitrary)
	})

	T.Run("serializes concurrent consumes", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 5)

		var (
			mu      sync.Mutex
			allowed int
			wg      sync.WaitGroup
		)

		for range 8 {
			wg.Go(func() {
				decision, err := env.consume(t, testSubject, testMeter, 1)
				if err != nil || !decision.Allowed {
					return
				}

				mu.Lock()
				defer mu.Unlock()

				allowed++
			})
		}

		wg.Wait()

		test.EqOp(t, 5, allowed)
	})
}

// The quota Check caches is not Consume's to use. Consume's whole promise is
// that the answer is exact, and an exact answer made against a limit resolved a
// staleness budget ago is a durable write against a plan the customer may no
// longer be on.
func TestQuotaEnforcer_Consume_resolvesAFreshQuota(T *testing.T) {
	T.Parallel()

	quotas := &countingQuotaSource{quota: testQuota(100)}
	env := newTestEnforcer(T, BehaviorBlock, 100, WithEnforcerQuotaSource(quotas))

	_, err := env.enforcer.Check(T.Context(), testScope, testSubject, testMeter, 1)
	must.NoError(T, err)
	test.EqOp(T, int64(1), quotas.calls.Load())

	// The entry a Check just wrote is sitting there, and Consume reads the source
	// anyway.
	_, err = env.consume(T, testSubject, testMeter, 1)
	must.NoError(T, err)
	test.EqOp(T, int64(2), quotas.calls.Load())

	// And evicted it on the way out, so the next Check resolves both again rather
	// than deciding against a total a rolled-back transaction never wrote.
	_, err = env.totals.Get(T.Context(), env.enforcer.cacheKey(testScope, testSubject, testMeter, monthBounds))
	test.ErrorIs(T, err, cache.ErrNotFound)
}

func TestQuotaEnforcer_ConsumeUsage(T *testing.T) {
	T.Parallel()

	T.Run("dedupes on the caller's key", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		usage := Usage{Subject: testSubject, Meter: testMeter, Quantity: 30, IdempotencyKey: "completion-1"}

		first, err := env.consumeUsage(t, usage)
		must.NoError(t, err)
		must.False(t, first.Duplicate)

		second, err := env.consumeUsage(t, usage)
		must.NoError(t, err)

		test.True(t, second.Duplicate)
		test.EqOp(t, int64(30), second.Used)
		test.EqOp(t, int64(30), totalOf(t, env.db, env.store))
	})

	T.Run("validates the usage", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		_, err := env.consumeUsage(t, Usage{Subject: testSubject, Meter: testMeter, Quantity: 1})

		test.ErrorIs(t, err, ErrEmptyIdempotencyKey)
	})

	T.Run("refuses a nil transaction", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		_, err := env.enforcer.Consume(t.Context(), nil, testScope, testSubject, testMeter, 1)
		test.ErrorIs(t, err, ErrNilExecutor)

		_, err = env.enforcer.ConsumeUsage(t.Context(), nil, testScope, Usage{
			Subject: testSubject, Meter: testMeter, Quantity: 1, IdempotencyKey: "req-1",
		})
		test.ErrorIs(t, err, ErrNilExecutor)
	})

	T.Run("honors an explicit event time", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		lastMonth := baseTime.AddDate(0, -1, 0)

		_, err := env.consumeUsage(t, Usage{
			Subject: testSubject, Meter: testMeter, Quantity: 30,
			IdempotencyKey: "completion-1", OccurredAt: lastMonth,
		})
		must.NoError(t, err)

		// Filed in the period it happened in, not the one it was ingested in.
		test.EqOp(t, int64(0), totalOf(t, env.db, env.store))
	})

	T.Run("reports an unknown meter", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		_, err := env.consumeUsage(t, Usage{
			Subject: testSubject, Meter: "not_registered", Quantity: 1, IdempotencyKey: "k",
		})

		test.ErrorIs(t, err, ErrUnknownMeter)
	})
}

func TestQuotaEnforcer_cacheKey(T *testing.T) {
	T.Parallel()

	env := newTestEnforcer(T, BehaviorBlock, 100)

	key := env.enforcer.cacheKey(testScope, testSubject, testMeter, monthBounds)

	test.EqOp(T, DefaultCachePrefix+testScope.Owner()+":"+testSubject+":"+testMeter+":"+
		strconv.FormatInt(monthBounds.Start.Unix(), 10), key)

	// The period start is part of the key rather than something the entry is
	// checked against, so a new period is a new key and cannot be answered by the
	// old one's entry.
	next := Bounds{Start: monthBounds.End, End: monthBounds.End.AddDate(0, 1, 0)}
	test.NotEqOp(T, key, env.enforcer.cacheKey(testScope, testSubject, testMeter, next))

	// And neither is the scope, for the same reason: two tenants may name the
	// same subject, and one entry answering for both would serve one tenant's
	// quota question with the other's usage for the whole staleness budget.
	test.NotEqOp(T, key, env.enforcer.cacheKey(otherScope, testSubject, testMeter, monthBounds))
}

func TestQuotaEnforcer_writeThrough(T *testing.T) {
	T.Parallel()

	T.Run("does nothing without a cache", func(t *testing.T) {
		t.Parallel()

		db := newSQLiteEnv(t)
		store := db.newStore(t)

		enforcer, err := db.newEnforcer(t, &EnforcerConfig{}, store,
			newTestRegistry(t, BehaviorBlock, 100), WithEnforcerClock(newStubClock()))
		must.NoError(t, err)

		_, op := enforcer.o11y.Begin(t.Context())
		defer op.End()

		enforcer.writeThrough(t.Context(), op, testMeterOf(t, enforcer), testScope, testSubject,
			monthBounds, testQuota(100), 5)
	})

	T.Run("stores the quota beside the total", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		_, op := env.enforcer.o11y.Begin(t.Context())
		defer op.End()

		env.enforcer.writeThrough(t.Context(), op, testMeterOf(t, env.enforcer), testScope, testSubject,
			monthBounds, testQuota(100), 5)

		entry, err := env.totals.Get(t.Context(), env.enforcer.cacheKey(testScope, testSubject, testMeter, monthBounds))
		must.NoError(t, err)
		must.NotNil(t, entry)

		// One entry holding both halves of a decision, so there is one expiry
		// and one eviction rather than two of each.
		test.EqOp(t, int64(5), entry.Quantity)
		must.NotNil(t, entry.Quota)
		test.EqOp(t, int64(100), entry.Quota.Limit)
		test.EqOp(t, BehaviorBlock, entry.Quota.Behavior)
	})

	T.Run("does nothing for a period that has already ended", func(t *testing.T) {
		t.Parallel()

		env := newTestEnforcer(t, BehaviorBlock, 100)

		// A closed period has no remaining budget to cache under, and an entry
		// with a non-positive TTL would either never expire or be refused.
		env.clock.advance(monthBounds.End.Sub(baseTime))

		_, op := env.enforcer.o11y.Begin(t.Context())
		defer op.End()

		env.enforcer.writeThrough(t.Context(), op, testMeterOf(t, env.enforcer), testScope, testSubject,
			monthBounds, testQuota(100), 5)

		_, err := env.totals.Get(t.Context(), env.enforcer.cacheKey(testScope, testSubject, testMeter, monthBounds))
		test.ErrorIs(t, err, cache.ErrNotFound)
	})
}

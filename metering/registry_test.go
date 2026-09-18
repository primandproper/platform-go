package metering

import (
	"context"
	"testing"
	"time"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegistry_RegisterMeter(T *testing.T) {
	T.Parallel()

	T.Run("registers and looks up", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()

		must.NoError(t, registry.RegisterMeter(Meter{
			Name:        testMeter,
			Unit:        "requests",
			Aggregation: AggregationSum,
			Period:      PeriodMonth,
		}))

		m, ok := registry.Meter(testMeter)
		must.True(t, ok)
		test.EqOp(t, "requests", m.Unit)
		test.EqOp(t, AggregationSum, m.Aggregation)

		_, ok = registry.Meter("nothing_registered")
		test.False(t, ok)
	})

	T.Run("refuses a duplicate", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()
		meter := Meter{Name: testMeter, Aggregation: AggregationSum, Period: PeriodMonth}

		must.NoError(t, registry.RegisterMeter(meter))

		// An overwrite would reinterpret every stored total for the meter — the
		// same rows, read as a different quantity, with nothing to say when the
		// reading changed.
		test.ErrorIs(t, registry.RegisterMeter(meter), ErrDuplicateMeter)
	})

	T.Run("propagates meter validation", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()

		test.ErrorIs(t, registry.RegisterMeter(Meter{Aggregation: AggregationSum, Period: PeriodMonth}),
			ErrInvalidMeterName)
		test.ErrorIs(t, registry.RegisterMeter(Meter{Name: "seats", Aggregation: AggregationUniqueCount, Period: PeriodMonth}),
			ErrUnsupportedAggregation)
	})
}

func TestRegistry_RegisterQuota(T *testing.T) {
	T.Parallel()

	newRegistryWithMeter := func(t *testing.T) *Registry {
		t.Helper()

		registry := NewRegistry()
		must.NoError(t, registry.RegisterMeter(Meter{
			Name: testMeter, Aggregation: AggregationSum, Period: PeriodMonth,
		}))

		return registry
	}

	T.Run("registers and looks up", func(t *testing.T) {
		t.Parallel()

		registry := newRegistryWithMeter(t)

		must.NoError(t, registry.RegisterQuota(Quota{
			Meter: testMeter, Limit: 100, Behavior: BehaviorBlock, Period: PeriodMonth,
		}))

		q, ok := registry.Quota(testMeter)
		must.True(t, ok)
		test.EqOp(t, int64(100), q.Limit)

		_, ok = registry.Quota("nothing_registered")
		test.False(t, ok)
	})

	T.Run("requires the meter to exist first", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()

		// A quota over an unknown meter is a typo in one of the two names, and
		// accepting it produces a limit nothing ever consults.
		test.ErrorIs(t, registry.RegisterQuota(Quota{
			Meter: testMeter, Limit: 100, Behavior: BehaviorBlock, Period: PeriodMonth,
		}), ErrUnknownMeter)
	})

	T.Run("refuses a duplicate", func(t *testing.T) {
		t.Parallel()

		registry := newRegistryWithMeter(t)
		quota := Quota{Meter: testMeter, Limit: 100, Behavior: BehaviorBlock, Period: PeriodMonth}

		must.NoError(t, registry.RegisterQuota(quota))
		test.ErrorIs(t, registry.RegisterQuota(quota), ErrDuplicateQuota)
	})

	T.Run("propagates quota validation", func(t *testing.T) {
		t.Parallel()

		registry := newRegistryWithMeter(t)

		test.ErrorIs(t, registry.RegisterQuota(Quota{
			Meter: testMeter, Limit: 100, Behavior: BehaviorBlock, Period: PeriodDay,
		}), ErrPeriodMismatch)
	})
}

func TestRegistry_RegisterUnlimitedQuota(T *testing.T) {
	T.Parallel()

	T.Run("registers the meter's own period", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()
		must.NoError(t, registry.RegisterMeter(Meter{
			Name: testMeter, Aggregation: AggregationSum, Period: PeriodDay,
		}))

		must.NoError(t, registry.RegisterUnlimitedQuota(testMeter))

		q, ok := registry.Quota(testMeter)
		must.True(t, ok)
		test.EqOp(t, Unlimited, q.Limit)
		test.EqOp(t, BehaviorAllowOverage, q.Behavior)
		// Read off the meter rather than defaulted: a period spelled a second
		// time is the one part of an unlimited quota a caller can get wrong, and
		// what it produces is a limit that never fills.
		test.EqOp(t, PeriodDay, q.Period)
	})

	T.Run("requires the meter to exist first", func(t *testing.T) {
		t.Parallel()

		test.ErrorIs(t, NewRegistry().RegisterUnlimitedQuota(testMeter), ErrUnknownMeter)
	})

	T.Run("refuses a duplicate", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()
		must.NoError(t, registry.RegisterMeter(Meter{
			Name: testMeter, Aggregation: AggregationSum, Period: PeriodMonth,
		}))

		must.NoError(t, registry.RegisterUnlimitedQuota(testMeter))
		test.ErrorIs(t, registry.RegisterUnlimitedQuota(testMeter), ErrDuplicateQuota)
		test.ErrorIs(t, registry.RegisterQuota(Quota{
			Meter: testMeter, Limit: 100, Behavior: BehaviorBlock, Period: PeriodMonth,
		}), ErrDuplicateQuota)
	})
}

func TestRegistry_Listings(T *testing.T) {
	T.Parallel()

	registry := NewRegistry()

	// Registered out of order, so the sort is doing something.
	for _, name := range []string{"storage_bytes", "api_requests", "llm_tokens"} {
		must.NoError(T, registry.RegisterMeter(Meter{
			Name: name, Aggregation: AggregationSum, Period: PeriodMonth,
		}))
	}

	must.NoError(T, registry.RegisterQuota(Quota{
		Meter: "llm_tokens", Limit: 10, Behavior: BehaviorBlock, Period: PeriodMonth,
	}))
	must.NoError(T, registry.RegisterQuota(Quota{
		Meter: "api_requests", Limit: 10, Behavior: BehaviorBlock, Period: PeriodMonth,
	}))

	test.Eq(T, []string{"api_requests", "llm_tokens", "storage_bytes"}, registry.MeterNames())
	test.Eq(T, []string{"api_requests", "llm_tokens"}, registry.QuotaMeters())

	test.SliceEmpty(T, NewRegistry().MeterNames())
	test.SliceEmpty(T, NewRegistry().QuotaMeters())
}

func TestRegistryQuotaSource(T *testing.T) {
	T.Parallel()

	T.Run("serves the registry's static quotas", func(t *testing.T) {
		t.Parallel()

		registry := newTestRegistry(t, BehaviorBlock, 100)
		source := NewRegistryQuotaSource(registry)

		q, err := source.QuotaFor(t.Context(), testSubject, testMeter)
		must.NoError(t, err)
		test.EqOp(t, int64(100), q.Limit)
	})

	T.Run("reports a meter with no quota", func(t *testing.T) {
		t.Parallel()

		registry := NewRegistry()
		must.NoError(t, registry.RegisterMeter(Meter{
			Name: testMeter, Aggregation: AggregationSum, Period: PeriodMonth,
		}))

		// Unmetered is not the same as unlimited. A caller who wants "allow
		// everything" registers a quota that says so, so the decision is visible
		// in the registry rather than implied by an absence.
		_, err := NewRegistryQuotaSource(registry).QuotaFor(t.Context(), testSubject, testMeter)

		test.ErrorIs(t, err, ErrNoQuota)
	})

	T.Run("reports a nil registry", func(t *testing.T) {
		t.Parallel()

		_, err := NewRegistryQuotaSource(nil).QuotaFor(t.Context(), testSubject, testMeter)

		test.ErrorIs(t, err, ErrNilRegistry)
	})
}

func TestFuncAdapters(T *testing.T) {
	T.Parallel()

	T.Run("QuotaSourceFunc", func(t *testing.T) {
		t.Parallel()

		var sawSubject, sawMeter string

		source := QuotaSourceFunc(func(_ context.Context, subject, meter string) (Quota, error) {
			sawSubject, sawMeter = subject, meter

			return Quota{Meter: meter, Limit: 7}, nil
		})

		q, err := source.QuotaFor(t.Context(), testSubject, testMeter)
		must.NoError(t, err)

		test.EqOp(t, testSubject, sawSubject)
		test.EqOp(t, testMeter, sawMeter)
		test.EqOp(t, int64(7), q.Limit)
	})

	T.Run("ProviderMapperFunc", func(t *testing.T) {
		t.Parallel()

		var sawSubject, sawMeter string

		mapper := ProviderMapperFunc(func(_ context.Context, subject, meter string) (ProviderRef, error) {
			sawSubject, sawMeter = subject, meter

			return ProviderRef{CustomerID: "cus_123", MeterName: "api_requests"}, nil
		})

		ref, err := mapper.ProviderRefFor(t.Context(), testSubject, testMeter)
		must.NoError(t, err)

		test.EqOp(t, testSubject, sawSubject)
		test.EqOp(t, testMeter, sawMeter)
		test.EqOp(t, "cus_123", ref.CustomerID)
		test.EqOp(t, "api_requests", ref.MeterName)
	})

	T.Run("PeriodResolverFunc", func(t *testing.T) {
		t.Parallel()

		resolver := PeriodResolverFunc(func(context.Context, string, Period, time.Time) (Bounds, error) {
			return monthBounds, nil
		})

		bounds, err := resolver.Resolve(t.Context(), testSubject, PeriodMonth, baseTime)
		must.NoError(t, err)
		test.EqOp(t, monthBounds.Start, bounds.Start)
	})
}

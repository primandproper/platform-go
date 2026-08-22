package metering

import (
	"errors"
	"strings"
	"testing"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestAggregation_Valid(T *testing.T) {
	T.Parallel()

	for _, a := range []Aggregation{AggregationSum, AggregationMax, AggregationLast, AggregationUniqueCount} {
		test.True(T, a.Valid(), test.Sprintf("aggregation %q", a))
	}

	test.False(T, Aggregation("").Valid())
	test.False(T, Aggregation("median").Valid())
}

func TestAggregation_Supported(T *testing.T) {
	T.Parallel()

	for _, a := range []Aggregation{AggregationSum, AggregationMax, AggregationLast} {
		test.True(T, a.Supported(), test.Sprintf("aggregation %q", a))
	}

	// Named and refused. See AggregationUniqueCount for why the two are
	// deliberately different questions.
	test.False(T, AggregationUniqueCount.Supported())
	test.False(T, Aggregation("median").Supported())
}

func TestAggregation_Fold(T *testing.T) {
	T.Parallel()

	T.Run("sum adds", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, int64(7), AggregationSum.Fold(3, 4, true))
		// Order is irrelevant to a sum, which is exactly why a late-arriving
		// record can be folded into it without a second thought.
		test.EqOp(t, int64(7), AggregationSum.Fold(3, 4, false))
	})

	T.Run("max keeps the larger", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, int64(9), AggregationMax.Fold(9, 4, true))
		test.EqOp(t, int64(9), AggregationMax.Fold(3, 9, true))
		test.EqOp(t, int64(9), AggregationMax.Fold(9, 4, false))
	})

	T.Run("last takes the newer only", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, int64(4), AggregationLast.Fold(9, 4, true))
		// The whole point: a record that arrived late does not displace a newer
		// reading, or a queue redelivery would reset a gauge to an hour ago.
		test.EqOp(t, int64(9), AggregationLast.Fold(9, 4, false))
	})

	T.Run("unimplemented and unknown leave the total alone", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, int64(3), AggregationUniqueCount.Fold(3, 4, true))
		test.EqOp(t, int64(3), Aggregation("median").Fold(3, 4, true))
	})
}

func TestMeter_validate(T *testing.T) {
	T.Parallel()

	valid := Meter{Name: "api_requests", Aggregation: AggregationSum, Period: PeriodMonth}
	must.NoError(T, valid.validate())

	T.Run("rejects an unusable name", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"9lives", "has space", "has-dash", "has.dot", "üñî", strings.Repeat("a", MaxMeterNameLength+1)} {
			m := valid
			m.Name = name

			test.ErrorIs(t, m.validate(), ErrInvalidMeterName, test.Sprintf("name %q", name))
		}
	})

	T.Run("accepts underscores and digits after the first byte", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"a", "_leading", "llm_tokens_v2", "A"} {
			m := valid
			m.Name = name

			test.NoError(t, m.validate(), test.Sprintf("name %q", name))
		}
	})

	T.Run("rejects an unknown or unimplemented aggregation", func(t *testing.T) {
		t.Parallel()

		unknown := valid
		unknown.Aggregation = "median"
		test.ErrorIs(t, unknown.validate(), ErrUnsupportedAggregation)

		// Distinct from unknown: this one is named, documented, and still
		// refused, so a meter cannot be registered with it and silently counted
		// as something else.
		unimplemented := valid
		unimplemented.Aggregation = AggregationUniqueCount
		test.ErrorIs(t, unimplemented.validate(), ErrUnsupportedAggregation)
	})

	T.Run("rejects an unknown period", func(t *testing.T) {
		t.Parallel()

		m := valid
		m.Period = "fortnight"

		test.ErrorIs(t, m.validate(), ErrUnknownPeriod)
	})
}

func TestUsage_validate(T *testing.T) {
	T.Parallel()

	valid := Usage{Subject: testSubject, Meter: testMeter, IdempotencyKey: "req-1", Quantity: 1}
	must.NoError(T, valid.validate())

	T.Run("requires a subject", func(t *testing.T) {
		t.Parallel()

		u := valid
		u.Subject = ""

		test.ErrorIs(t, u.validate(), ErrEmptySubject)
	})

	T.Run("requires a meter", func(t *testing.T) {
		t.Parallel()

		u := valid
		u.Meter = ""

		test.ErrorIs(t, u.validate(), ErrInvalidMeterName)
	})

	T.Run("requires an idempotency key", func(t *testing.T) {
		t.Parallel()

		u := valid
		u.IdempotencyKey = ""

		test.ErrorIs(t, u.validate(), ErrEmptyIdempotencyKey)
	})

	T.Run("bounds the idempotency key", func(t *testing.T) {
		t.Parallel()

		u := valid
		u.IdempotencyKey = strings.Repeat("k", MaxIdempotencyKeyLength+1)

		// Bounded because it is the width of the primary key column; a longer
		// one would be truncated by the driver into a collision with whatever
		// shares its prefix.
		//
		// Its own sentinel rather than the empty one: a caller told "you must
		// supply an idempotency key" about a key it did supply looks for the bug
		// in the wrong place.
		test.ErrorIs(t, u.validate(), ErrIdempotencyKeyTooLong)
		test.False(t, errors.Is(u.validate(), ErrEmptyIdempotencyKey))
	})

	T.Run("accepts an idempotency key of exactly the bound", func(t *testing.T) {
		t.Parallel()

		// The bound is the width of the primary key column, and this is the
		// length that says where it sits: one byte shorter is accepted by any
		// reading of the check and one byte longer is refused by any. A bound
		// that were one out would refuse the longest key the column can hold, or
		// admit one the driver truncates into a collision.
		u := valid
		u.IdempotencyKey = strings.Repeat("k", MaxIdempotencyKeyLength)

		test.NoError(t, u.validate())
	})

	T.Run("refuses a negative quantity", func(t *testing.T) {
		t.Parallel()

		u := valid
		u.Quantity = -1

		test.ErrorIs(t, u.validate(), ErrNegativeQuantity)
	})

	T.Run("accepts a zero quantity", func(t *testing.T) {
		t.Parallel()

		// Zero is meaningful for a gauge: "you now have zero seats provisioned"
		// is a reading, not an absence of one.
		u := valid
		u.Quantity = 0

		test.NoError(t, u.validate())
	})
}

func TestQuotaBehavior(T *testing.T) {
	T.Parallel()

	for _, b := range []QuotaBehavior{BehaviorBlock, BehaviorWarn, BehaviorAllowOverage} {
		test.True(T, b.Valid(), test.Sprintf("behavior %q", b))
	}

	test.False(T, QuotaBehavior("").Valid())
	test.False(T, QuotaBehavior("throttle").Valid())

	// Only block refuses. The other two count what happened, because usage a
	// customer had is usage a customer had.
	test.False(T, BehaviorBlock.records())
	test.True(T, BehaviorWarn.records())
	test.True(T, BehaviorAllowOverage.records())
}

func TestQuota_validate(T *testing.T) {
	T.Parallel()

	meter := Meter{Name: testMeter, Aggregation: AggregationSum, Period: PeriodMonth}
	valid := Quota{Meter: testMeter, Limit: 100, Behavior: BehaviorBlock, Period: PeriodMonth}
	must.NoError(T, valid.validate(meter))

	T.Run("rejects an unknown behavior", func(t *testing.T) {
		t.Parallel()

		q := valid
		q.Behavior = "throttle"

		test.Error(t, q.validate(meter))
	})

	T.Run("rejects a negative limit", func(t *testing.T) {
		t.Parallel()

		q := valid
		q.Limit = -1

		test.Error(t, q.validate(meter))
	})

	T.Run("accepts a zero limit", func(t *testing.T) {
		t.Parallel()

		// Zero is a real configuration — a feature switched off for a plan tier —
		// and not a synonym for unlimited.
		q := valid
		q.Limit = 0

		test.NoError(t, q.validate(meter))
	})

	T.Run("rejects a period the meter does not bucket by", func(t *testing.T) {
		t.Parallel()

		q := valid
		q.Period = PeriodDay

		test.ErrorIs(t, q.validate(meter), ErrPeriodMismatch)
	})
}

func TestNewDecision(T *testing.T) {
	T.Parallel()

	resetsAt := monthBounds.End

	T.Run("under the limit is allowed with no overage", func(t *testing.T) {
		t.Parallel()

		d := newDecision(testMeter, BehaviorBlock, 40, 100, resetsAt)

		test.True(t, d.Allowed)
		test.EqOp(t, int64(40), d.Used)
		test.EqOp(t, int64(100), d.Limit)
		test.EqOp(t, int64(0), d.Overage)
		test.EqOp(t, resetsAt, d.ResetsAt)
		test.EqOp(t, testMeter, d.Meter)
		test.EqOp(t, BehaviorBlock, d.Behavior)
	})

	T.Run("exactly at the limit is allowed", func(t *testing.T) {
		t.Parallel()

		// The limit is inclusive: a plan sold as "one million requests" delivers
		// the millionth one.
		d := newDecision(testMeter, BehaviorBlock, 100, 100, resetsAt)

		test.True(t, d.Allowed)
		test.EqOp(t, int64(0), d.Overage)
	})

	T.Run("block refuses past the limit", func(t *testing.T) {
		t.Parallel()

		d := newDecision(testMeter, BehaviorBlock, 101, 100, resetsAt)

		test.False(t, d.Allowed)
		test.EqOp(t, int64(1), d.Overage)
	})

	T.Run("warn and allow_overage let it through and report the excess", func(t *testing.T) {
		t.Parallel()

		for _, behavior := range []QuotaBehavior{BehaviorWarn, BehaviorAllowOverage} {
			d := newDecision(testMeter, behavior, 150, 100, resetsAt)

			test.True(t, d.Allowed, test.Sprintf("behavior %q", behavior))
			test.EqOp(t, int64(50), d.Overage, test.Sprintf("behavior %q", behavior))
		}
	})
}

func TestOverageOf(T *testing.T) {
	T.Parallel()

	test.EqOp(T, int64(0), overageOf(40, 100))
	test.EqOp(T, int64(0), overageOf(100, 100))
	test.EqOp(T, int64(5), overageOf(105, 100))
	// A zero limit makes every unit an overage, which is what "this plan does not
	// include this feature, and here is how much they used anyway" looks like.
	test.EqOp(T, int64(3), overageOf(3, 0))
}

func TestSentinelsWrapPlatformErrors(T *testing.T) {
	T.Parallel()

	// The nil-input sentinels wrap the platform's, so a caller may check either —
	// which is what lets a handler map every nil-argument bug to one response
	// without enumerating this package's names.
	for _, err := range []error{
		ErrNilStore, ErrNilRegistry, ErrNilDatabaseClient,
		ErrNilExecutor, ErrNilProviderMapper, ErrNilUsageReporter,
	} {
		test.True(T, errors.Is(err, platformerrors.ErrNilInputParameter), test.Sprintf("error %v", err))
	}
}

func TestBounds(T *testing.T) {
	T.Parallel()

	T.Run("is half-open", func(t *testing.T) {
		t.Parallel()

		// Half-open because inclusive ends have no correct answer at the
		// boundary: an event at exactly midnight would belong to two periods.
		test.True(t, monthBounds.Contains(monthBounds.Start))
		test.True(t, monthBounds.Contains(baseTime))
		test.False(t, monthBounds.Contains(monthBounds.End))
		test.False(t, monthBounds.Contains(monthBounds.Start.Add(-time.Nanosecond)))
	})

	T.Run("validates", func(t *testing.T) {
		t.Parallel()

		test.True(t, monthBounds.Valid())
		test.False(t, Bounds{}.Valid())
		test.False(t, Bounds{Start: monthBounds.Start}.Valid())
		test.False(t, Bounds{Start: monthBounds.End, End: monthBounds.Start}.Valid())
		// Empty is not valid either: a window nothing can fall into would key
		// every period to the same row.
		test.False(t, Bounds{Start: monthBounds.Start, End: monthBounds.Start}.Valid())
	})
}

func TestTotal_PendingAndDelta(T *testing.T) {
	T.Parallel()

	T.Run("nil is neither pending nor owed", func(t *testing.T) {
		t.Parallel()

		var total *Total

		test.False(t, total.Pending())
		test.EqOp(t, int64(0), total.Delta())
	})

	T.Run("reports what the provider has not been told", func(t *testing.T) {
		t.Parallel()

		total := &Total{Quantity: 100, FlushedQuantity: 40}

		test.True(t, total.Pending())
		test.EqOp(t, int64(60), total.Delta())
	})

	T.Run("settled is not pending", func(t *testing.T) {
		t.Parallel()

		total := &Total{Quantity: 100, FlushedQuantity: 100}

		test.False(t, total.Pending())
		test.EqOp(t, int64(0), total.Delta())
	})

	T.Run("clamps a flushed quantity ahead of the total", func(t *testing.T) {
		t.Parallel()

		// Reachable for a max or last meter whose reading went down. A negative
		// delta posted to a provider is a credit nobody authorized.
		total := &Total{Quantity: 40, FlushedQuantity: 100}

		test.False(t, total.Pending())
		test.EqOp(t, int64(0), total.Delta())
	})
}

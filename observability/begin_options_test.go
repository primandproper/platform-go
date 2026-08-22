package observability

import (
	"testing"

	"github.com/primandproper/platform-go/v13/observability/tracing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestBeginOptions(T *testing.T) {
	T.Parallel()

	T.Run("WithValues seeds both pillars", func(t *testing.T) {
		t.Parallel()

		obs := NewRecordingObserver()

		_, op := obs.Begin(t.Context(), WithValues(map[string]any{
			"request_id": "req-1",
			"status":     "pending",
		}))
		op.End()

		rec := obs.Operations[0]
		test.Eq(t, map[string]any{"request_id": "req-1", "status": "pending"}, rec.Values)
		test.Eq(t, map[string]any{"request_id": "req-1", "status": "pending"}, rec.SpanValues)
		test.Eq(t, map[string]any{"request_id": "req-1", "status": "pending"}, rec.LogValues)
	})

	T.Run("WithValue seeds one value on both pillars", func(t *testing.T) {
		t.Parallel()

		obs := NewRecordingObserver()

		_, op := obs.Begin(t.Context(), WithValue("subject_id", "sub-1"))
		op.End()

		rec := obs.Operations[0]
		test.Eq(t, map[string]any{"subject_id": "sub-1"}, rec.Values)
	})

	T.Run("WithSpanValue and WithLogValue respect their pillar", func(t *testing.T) {
		t.Parallel()

		obs := NewRecordingObserver()

		_, op := obs.Begin(t.Context(),
			WithSpanValue("span_only", 1),
			WithLogValue("log_only", 2),
		)
		op.End()

		rec := obs.Operations[0]
		test.Eq(t, map[string]any{"span_only": 1}, rec.SpanValues)
		test.Eq(t, map[string]any{"log_only": 2}, rec.LogValues)

		// Values is the both-pillars view, so a single-pillar seed stays out of it.
		test.MapEmpty(t, rec.Values)
	})

	T.Run("options apply in order", func(t *testing.T) {
		t.Parallel()

		obs := NewRecordingObserver()

		// Last write wins, which is what makes a caller-supplied override after a
		// shared base option behave the way the equivalent Set calls would.
		_, op := obs.Begin(t.Context(),
			WithValue("status", "pending"),
			WithValue("status", "running"),
		)
		op.End()

		test.EqOp(t, "running", obs.Operations[0].Values["status"])
	})

	T.Run("seeded values are ordinary observations", func(t *testing.T) {
		t.Parallel()

		obs := NewRecordingObserver()

		// A test asserting on observations cannot tell whether the unit seeded the
		// value at Begin or set it afterwards, which is what lets a call site move
		// to the option form without its test changing.
		_, seeded := obs.Begin(t.Context(), WithValue("k", "v"))
		seeded.End()

		_, afterwards := obs.Begin(t.Context())
		afterwards.Set("k", "v")
		afterwards.End()

		must.SliceLen(t, 2, obs.Operations)
		test.Eq(t, obs.Operations[0].Values, obs.Operations[1].Values)
		test.EqOp(t, len(obs.Operations[0].Observations), len(obs.Operations[1].Observations))
	})

	T.Run("tolerates nil options and empty maps", func(t *testing.T) {
		t.Parallel()

		obs := NewRecordingObserver()

		// A caller building options conditionally should not have to guard the call.
		_, op := obs.Begin(t.Context(), nil, WithValues(nil), WithValues(map[string]any{}))
		op.End()

		test.MapEmpty(t, obs.Operations[0].Values)
	})

	T.Run("the production observer seeds the operation too", func(t *testing.T) {
		t.Parallel()

		logger := newRecordingLogger()
		obs := &observer{logger: logger, tracer: tracing.NewTracerForTest("begin-options")}

		_, op := obs.Begin(t.Context(), WithValues(map[string]any{"request_id": "req-1"}))
		op.End()

		// The logger half of the dual write: a value seeded at Begin is folded into
		// the span-linked logger, so every line the operation emits carries it.
		test.EqOp(t, "req-1", logger.rec.values["request_id"])
	})

	T.Run("Begin without options behaves as before", func(t *testing.T) {
		t.Parallel()

		obs := NewRecordingObserver()

		_, op := obs.Begin(t.Context())
		op.End()

		must.SliceLen(t, 1, obs.Operations)
		test.MapEmpty(t, obs.Operations[0].Values)
		test.True(t, obs.Operations[0].Ended)
	})
}

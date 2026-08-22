package observability

import (
	"context"
	"testing"
	"time"

	clockmock "github.com/primandproper/platform-go/v13/clock/mock"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// recordingHistogram captures what was recorded into it.
type recordingHistogram struct {
	metrics.Float64Histogram

	recorded []float64
}

func (h *recordingHistogram) Record(_ context.Context, value float64, _ ...metric.RecordOption) {
	h.recorded = append(h.recorded, value)
}

// steppingClock advances by step on every read, so a timed block measures a
// known duration without sleeping.
func steppingClock(start time.Time, step time.Duration) *clockmock.ClockMock {
	now := start

	return &clockmock.ClockMock{
		NowFunc: func() time.Time {
			at := now
			now = now.Add(step)

			return at
		},
	}
}

func TestOperation_Time(T *testing.T) {
	T.Parallel()

	start := time.Date(2026, time.July, 27, 12, 0, 0, 0, time.UTC)

	T.Run("records the elapsed milliseconds", func(t *testing.T) {
		t.Parallel()

		hist := &recordingHistogram{}

		_, op := NewObserver("timing_test", nil, nil).Begin(t.Context())
		defer op.End()

		op.Time(t.Context(), steppingClock(start, 250*time.Millisecond), hist)()

		must.SliceLen(t, 1, hist.recorded)
		test.EqOp(t, 250.0, hist.recorded[0])
	})

	// Milliseconds() truncates, so the hand-written loops this replaced reported
	// every sub-millisecond operation as zero. A 400µs call is not a free one.
	T.Run("keeps the sub-millisecond part", func(t *testing.T) {
		t.Parallel()

		hist := &recordingHistogram{}

		_, op := NewObserver("timing_test", nil, nil).Begin(t.Context())
		defer op.End()

		op.Time(t.Context(), steppingClock(start, 400*time.Microsecond), hist)()

		must.SliceLen(t, 1, hist.recorded)
		test.EqOp(t, 0.4, hist.recorded[0])
	})

	// The point of the parameter: a component holding an injected clock must
	// time against it, or its tests cannot control what its histogram says.
	T.Run("measures against the clock it was given", func(t *testing.T) {
		t.Parallel()

		hist := &recordingHistogram{}
		c := steppingClock(start, time.Hour)

		_, op := NewObserver("timing_test", nil, nil).Begin(t.Context())
		defer op.End()

		op.Time(t.Context(), c, hist)()

		must.SliceLen(t, 1, hist.recorded)
		test.EqOp(t, float64(time.Hour.Milliseconds()), hist.recorded[0])
		test.SliceLen(t, 2, c.NowCalls())
	})

	// A pinned clock reports a duration of zero, which is what pinning it means
	// — and is why this reads Now twice rather than Now then Since.
	T.Run("a clock pinned to an instant reports no elapsed time", func(t *testing.T) {
		t.Parallel()

		hist := &recordingHistogram{}

		_, op := NewObserver("timing_test", nil, nil).Begin(t.Context())
		defer op.End()

		op.Time(t.Context(), &clockmock.ClockMock{NowFunc: func() time.Time { return start }}, hist)()

		must.SliceLen(t, 1, hist.recorded)
		test.EqOp(t, 0.0, hist.recorded[0])
	})

	T.Run("a nil clock resolves to the wall clock", func(t *testing.T) {
		t.Parallel()

		hist := &recordingHistogram{}

		_, op := NewObserver("timing_test", nil, nil).Begin(t.Context())
		defer op.End()

		op.Time(t.Context(), nil, hist)()

		must.SliceLen(t, 1, hist.recorded)
		test.GreaterEq(t, 0.0, hist.recorded[0])
	})

	// An unmetered component needs no branch around its own timing.
	T.Run("a nil histogram records nothing rather than panicking", func(t *testing.T) {
		t.Parallel()

		_, op := NewObserver("timing_test", nil, nil).Begin(t.Context())
		defer op.End()

		op.Time(t.Context(), nil, nil)()
	})

	// A test holding a RecordingOperation measures through the same code the
	// real one does, so an assertion about latency is an assertion about
	// production behavior.
	T.Run("a RecordingOperation times the same way", func(t *testing.T) {
		t.Parallel()

		hist := &recordingHistogram{}

		_, op := NewRecordingObserver().Begin(t.Context())

		op.Time(t.Context(), steppingClock(start, time.Second), hist)()

		must.SliceLen(t, 1, hist.recorded)
		test.EqOp(t, 1000.0, hist.recorded[0])
	})
}

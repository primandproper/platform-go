package observability

import (
	"errors"
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v5/observability/logging/noop"
	"github.com/primandproper/platform-go/v5/observability/tracing"

	"github.com/shoenig/test"
	"google.golang.org/grpc/codes"
)

func TestOperation_Set(T *testing.T) {
	T.Parallel()

	T.Run("writes to both the span and the logger", func(t *testing.T) {
		t.Parallel()

		o, rec, exp := newTestObserver(t)

		_, op := o.Begin(t.Context())
		op.Set("key", "value")
		op.End()

		// logger half
		test.EqOp(t, "value", rec.values["key"].(string))

		// span half
		spans := exp.recorded()
		test.SliceLen(t, 1, spans)
		v, ok := spanAttr(spans[0], "key")
		test.True(t, ok)
		test.EqOp(t, "value", v.AsString())
	})

	T.Run("chains", func(t *testing.T) {
		t.Parallel()

		o, rec, exp := newTestObserver(t)

		_, op := o.Begin(t.Context())
		op.Set("a", "1").Set("b", "2")
		op.End()

		test.EqOp(t, "1", rec.values["a"].(string))
		test.EqOp(t, "2", rec.values["b"].(string))

		spans := exp.recorded()
		test.SliceLen(t, 1, spans)
		_, okA := spanAttr(spans[0], "a")
		_, okB := spanAttr(spans[0], "b")
		test.True(t, okA)
		test.True(t, okB)
	})
}

func TestOperation_SetValues(T *testing.T) {
	T.Parallel()

	T.Run("writes every value to both pillars", func(t *testing.T) {
		t.Parallel()

		o, rec, exp := newTestObserver(t)

		_, op := o.Begin(t.Context())
		op.SetValues(map[string]any{"x": "1", "y": "2"})
		op.End()

		test.EqOp(t, "1", rec.values["x"].(string))
		test.EqOp(t, "2", rec.values["y"].(string))

		spans := exp.recorded()
		test.SliceLen(t, 1, spans)
		_, okX := spanAttr(spans[0], "x")
		_, okY := spanAttr(spans[0], "y")
		test.True(t, okX)
		test.True(t, okY)
	})
}

func TestOperation_SpanOnly(T *testing.T) {
	T.Parallel()

	T.Run("writes to the span and not the logger", func(t *testing.T) {
		t.Parallel()

		o, rec, exp := newTestObserver(t)

		_, op := o.Begin(t.Context())
		op.SpanOnly("span_key", "value")
		op.End()

		_, inLogger := rec.values["span_key"]
		test.False(t, inLogger)

		spans := exp.recorded()
		test.SliceLen(t, 1, spans)
		_, ok := spanAttr(spans[0], "span_key")
		test.True(t, ok)
	})
}

func TestOperation_LogOnly(T *testing.T) {
	T.Parallel()

	T.Run("writes to the logger and not the span", func(t *testing.T) {
		t.Parallel()

		o, rec, exp := newTestObserver(t)

		_, op := o.Begin(t.Context())
		op.LogOnly("log_key", "value")
		op.End()

		test.EqOp(t, "value", rec.values["log_key"].(string))

		spans := exp.recorded()
		test.SliceLen(t, 1, spans)
		_, ok := spanAttr(spans[0], "log_key")
		test.False(t, ok)
	})
}

func TestOperation_Error(T *testing.T) {
	T.Parallel()

	T.Run("logs, traces, and wraps the error", func(t *testing.T) {
		t.Parallel()

		o := NewObserverForTest("test_observer")
		_, op := o.Begin(t.Context())
		defer op.End()

		err := op.Error(errors.New("boom"), "doing %s", "thing")
		test.Error(t, err)
		test.StrContains(t, err.Error(), "doing thing")
	})

	T.Run("with nil error", func(t *testing.T) {
		t.Parallel()

		o := NewObserverForTest("test_observer")
		_, op := o.Begin(t.Context())
		defer op.End()

		test.NoError(t, op.Error(nil, "doing thing"))
	})
}

func TestOperation_Acknowledge(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		o := NewObserverForTest("test_observer")
		_, op := o.Begin(t.Context())
		defer op.End()

		op.Acknowledge(errors.New("boom"), "doing thing")
	})
}

func TestOperation_GRPCStatus(T *testing.T) {
	T.Parallel()

	T.Run("returns a gRPC status error", func(t *testing.T) {
		t.Parallel()

		o := NewObserverForTest("test_observer")
		_, op := o.Begin(t.Context())
		defer op.End()

		err := op.GRPCStatus(errors.New("boom"), codes.Internal, "doing thing")
		test.Error(t, err)
	})
}

func TestOperation_End(T *testing.T) {
	T.Parallel()

	T.Run("is nil-span safe", func(t *testing.T) {
		t.Parallel()

		op := newOperation(nil, loggingnoop.NewLogger())
		op.End()
		test.NotNil(t, op.Logger())
	})

	T.Run("returns the raw span", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		_, span := tracing.StartSpan(ctx)
		op := newOperation(span, loggingnoop.NewLogger())
		test.NotNil(t, op.Span())
		op.End()
	})
}

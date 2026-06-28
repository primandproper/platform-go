package observability

import (
	"errors"
	"testing"

	"github.com/shoenig/test"
	"google.golang.org/grpc/codes"
)

func TestRecordingObserver(T *testing.T) {
	T.Parallel()

	T.Run("captures values a unit attaches", func(t *testing.T) {
		t.Parallel()

		o := NewRecordingObserver()

		_, op := o.Begin(t.Context())
		op.Set("user.id", "123").Set("feature", "beta")
		op.SpanOnly("span_only", true)
		op.LogOnly("log_only", true)
		op.End()

		test.SliceLen(t, 1, o.Operations)
		rec := o.Operations[0]

		// Set lands on both pillars.
		test.EqOp(t, "123", rec.Values["user.id"].(string))
		test.EqOp(t, "beta", rec.Values["feature"].(string))

		// SpanOnly / LogOnly land on exactly one.
		_, spanOnlyInLog := rec.LogValues["span_only"]
		test.False(t, spanOnlyInLog)
		_, ok := rec.SpanValues["span_only"]
		test.True(t, ok)

		_, logOnlyInSpan := rec.SpanValues["log_only"]
		test.False(t, logOnlyInSpan)
		_, ok = rec.LogValues["log_only"]
		test.True(t, ok)

		test.True(t, rec.Ended)
	})

	T.Run("values are observable on error paths", func(t *testing.T) {
		t.Parallel()

		o := NewRecordingObserver()

		// a unit that attaches values, then fails
		_, op := o.Begin(t.Context())
		op.Set("user.id", "123")
		failure := op.Error(errors.New("downstream blew up"), "doing the thing")
		op.End()
		test.Error(t, failure)

		// the value was still collected despite the failure...
		matched := o.ObservedOperationWithData(t, map[string]any{"user.id": "123"})
		// ...and the same operation recorded the error
		test.SliceLen(t, 1, matched.Errors)
	})

	T.Run("records errors and preserves wrapping", func(t *testing.T) {
		t.Parallel()

		o := NewRecordingObserver()
		_, op := o.Begin(t.Context())

		err := op.Error(errors.New("boom"), "doing %s", "thing")
		test.Error(t, err)
		test.StrContains(t, err.Error(), "doing thing")

		test.Error(t, op.GRPCStatus(errors.New("nope"), codes.Internal, "rpc"))

		test.SliceLen(t, 2, o.Operations[0].Errors)
	})
}

func TestRecordingObserver_assertionHelpers(T *testing.T) {
	T.Parallel()

	T.Run("ObservedOperationWithKeys", func(t *testing.T) {
		t.Parallel()

		o := NewRecordingObserver()
		_, op := o.Begin(t.Context())
		op.Set("user.id", "123").SpanOnly("only_span", true)
		op.End()

		// Set keys and SpanOnly keys both count as observed.
		o.ObservedOperationWithKeys(t, "user.id", "only_span")
	})

	T.Run("ObservedOperationWithValues", func(t *testing.T) {
		t.Parallel()

		o := NewRecordingObserver()
		_, op := o.Begin(t.Context())
		op.Set("user.id", "123").Set("feature", "beta")
		op.End()

		o.ObservedOperationWithValues(t, "123", "beta")
	})

	T.Run("ObservedOperationWithData", func(t *testing.T) {
		t.Parallel()

		o := NewRecordingObserver()
		_, op := o.Begin(t.Context())
		op.Set("user.id", "123").Set("feature", "beta")
		op.End()

		o.ObservedOperationWithData(t, map[string]any{
			"user.id": "123",
			"feature": "beta",
		})
	})

	T.Run("fails via the spy when nothing matches", func(t *testing.T) {
		t.Parallel()

		o := NewRecordingObserver()
		_, op := o.Begin(t.Context())
		op.Set("user.id", "123")
		op.End()

		spy := &spyTestingT{}
		o.ObservedOperationWithKeys(spy, "missing")
		test.True(t, spy.failed)
	})

	T.Run("fails when the matching operation did not end", func(t *testing.T) {
		t.Parallel()

		o := NewRecordingObserver()
		_, op := o.Begin(t.Context())
		op.Set("user.id", "123")
		// deliberately not ended

		spy := &spyTestingT{}
		o.ObservedOperationWithKeys(spy, "user.id")
		test.True(t, spy.failed)
	})
}

func TestRecordingObserver_orderedAssertions(T *testing.T) {
	T.Parallel()

	T.Run("asserts an ordered cross-operation sequence, value-unknown allowed", func(t *testing.T) {
		t.Parallel()

		o := NewRecordingObserver()

		// function A observes its parameter
		_, opA := o.Begin(t.Context())
		opA.Set("param.x", "X")

		// a downstream function creates and observes an object (value generated)
		_, opB := o.Begin(t.Context())
		opB.Set("created.id", "generated-9f3c")
		opB.End()

		// back in A, the returned object is observed; the test knows only the key
		opA.Set("created.object", struct{ ID string }{ID: "generated-9f3c"})
		opA.End()

		o.ObservedInOrder(t,
			ObservedKeyValue("param.x", "X"), // A saw X, exact value
			ObservedKey("created.id"),        // downstream created something, value unknown
			ObservedKey("created.object"),    // A saw the returned object, value unknown
		)
	})

	T.Run("order matters", func(t *testing.T) {
		t.Parallel()

		o := NewRecordingObserver()
		_, op := o.Begin(t.Context())
		op.Set("first", 1).Set("second", 2)
		op.End()

		rec := o.Operations[0]

		// correct order passes
		rec.ObservedInOrder(t, ObservedKey("first"), ObservedKey("second"))

		// reversed order fails
		spy := &spyTestingT{}
		rec.ObservedInOrder(spy, ObservedKey("second"), ObservedKey("first"))
		test.True(t, spy.failed)
	})

	T.Run("pillar-scoped matchers", func(t *testing.T) {
		t.Parallel()

		o := NewRecordingObserver()
		_, op := o.Begin(t.Context())
		op.Set("both", true)       // span + log
		op.SpanOnly("span_key", 1) // span only
		op.LogOnly("log_key", 2)   // log only
		op.End()

		rec := o.Operations[0]

		rec.Observed(t,
			ObservedKey("both").OnSpan(),
			ObservedKey("both").OnLog(),
			ObservedKey("span_key").OnSpan(),
			ObservedKey("log_key").OnLog(),
		)

		// span_key never reached the log pillar
		spy := &spyTestingT{}
		rec.Observed(spy, ObservedKey("span_key").OnLog())
		test.True(t, spy.failed)
	})

	T.Run("ObservedKeyFunc matches a value shape without pinning it", func(t *testing.T) {
		t.Parallel()

		o := NewRecordingObserver()
		_, op := o.Begin(t.Context())
		op.Set("count", 42)
		op.End()

		o.Operations[0].Observed(t, ObservedKeyFunc("count", func(v any) bool {
			n, ok := v.(int)
			return ok && n > 0
		}))
	})
}

// spyTestingT is a TestingT that records a failure instead of aborting, so a test
// can assert that the assertion helpers fail when they should.
type spyTestingT struct {
	failed bool
}

func (s *spyTestingT) Helper() {}

func (s *spyTestingT) Fatalf(string, ...any) {
	s.failed = true
}

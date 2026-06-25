package tracing

import (
	"context"
	"strings"
	"testing"

	"github.com/shoenig/test"
	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/embedded"
	"go.opentelemetry.io/otel/trace/noop"
)

// The functions below reconstruct the exact call stack GetCallerName is designed
// for: an instrumented method calls StartSpan, StartSpan calls GetCallerName. With
// callerSkip == 3, GetCallerName must resolve to the instrumented method (two frames
// above itself). //go:noinline pins the stack depth so the assertions are stable
// regardless of the inliner.

// openSpanStub plays the role of StartSpan: the direct caller of GetCallerName.
//
//go:noinline
func openSpanStub() string { return GetCallerName() }

// instrumentedAlpha and instrumentedBeta play the role of instrumented methods that
// open a span; GetCallerName should resolve to whichever one is on the stack.
//
//go:noinline
func instrumentedAlpha() string { return openSpanStub() }

//go:noinline
func instrumentedBeta() string { return openSpanStub() }

// outerFrame -> midFrame -> leafStub -> GetCallerName is a four-deep chain used to
// prove the skip count is exactly two frames above GetCallerName: midFrame must be
// resolved and the deeper outerFrame must be ignored.
//
//go:noinline
func leafStub() string { return GetCallerName() }

//go:noinline
func midFrame() string { return leafStub() }

//go:noinline
func outerFrame() string { return midFrame() }

func TestGetCallerName(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		test.NotEq(t, "", GetCallerName())
	})

	T.Run("resolves the function that opened the span", func(t *testing.T) {
		t.Parallel()

		// instrumentedAlpha -> openSpanStub (StartSpan) -> GetCallerName: resolves to
		// the instrumented method, exactly as it would in production.
		test.EqOp(t, "observability/tracing.instrumentedAlpha", instrumentedAlpha())
	})

	T.Run("resolves exactly two frames above, ignoring deeper callers", func(t *testing.T) {
		t.Parallel()

		// outerFrame -> midFrame -> leafStub -> GetCallerName. callerSkip lands on
		// midFrame (two frames above GetCallerName); outerFrame must not leak through.
		got := outerFrame()
		test.EqOp(t, "observability/tracing.midFrame", got)
		test.StrNotContains(t, got, "outerFrame")
	})

	T.Run("distinct callers resolve to distinct names", func(t *testing.T) {
		t.Parallel()

		alpha := instrumentedAlpha()
		beta := instrumentedBeta()

		test.EqOp(t, "observability/tracing.instrumentedAlpha", alpha)
		test.EqOp(t, "observability/tracing.instrumentedBeta", beta)
		test.NotEqOp(t, alpha, beta)
	})

	T.Run("trims the platform package prefix", func(t *testing.T) {
		t.Parallel()

		got := instrumentedAlpha()

		// The raw runtime name begins with PackagePrefix; the returned name must not.
		test.False(t, strings.HasPrefix(got, PackagePrefix))
		test.True(t, strings.HasPrefix(got, "observability/tracing."))
	})
}

// TestGetCallerName_allocations is intentionally not parallel: testing.AllocsPerRun
// panics if called from a parallel test.
//
//nolint:paralleltest // this can't be run in parallel
func TestGetCallerName_allocations(t *testing.T) {
	// Validates the documented design: the single-element stack array plus
	// runtime.FuncForPC must avoid heap allocation.
	allocs := testing.AllocsPerRun(100, func() { _ = openSpanStub() })
	test.EqOp(t, 0.0, allocs)
}

// recordingTracer is a trace.Tracer that captures the span name passed to Start,
// letting us assert what name the real otelTraceWrapper.StartSpan derived.
type recordingTracer struct {
	embedded.Tracer
	startedName string
}

func (r *recordingTracer) Start(ctx context.Context, name string, _ ...trace.SpanStartOption) (context.Context, trace.Span) {
	r.startedName = name
	return ctx, noop.Span{}
}

// openSpanThroughWrapper drives the production path: an instrumented method calling
// the Tracer interface, which dispatches to otelTraceWrapper.StartSpan -> GetCallerName.
//
//go:noinline
func openSpanThroughWrapper(tr Tracer) {
	_, span := tr.StartSpan(context.Background())
	span.End()
}

func TestGetCallerName_throughStartSpanWrapper(T *testing.T) {
	T.Parallel()

	T.Run("span is named after the calling function", func(t *testing.T) {
		t.Parallel()

		rec := &recordingTracer{}
		// Construct the real wrapper around the recording tracer and exercise it via
		// the Tracer interface, mirroring how callers open spans in production.
		var tracer Tracer = &otelTraceWrapper{tracer: rec}

		openSpanThroughWrapper(tracer)

		test.EqOp(t, "observability/tracing.openSpanThroughWrapper", rec.startedName)
	})
}

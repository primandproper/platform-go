package observability

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v13/observability/logging"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Begin is the most-executed function in this module: every instrumented method
// in every package opens with it, so whatever it costs is the floor under every
// operation the platform performs. These benchmarks price that floor.
//
// Each shape is measured twice, against two tracer providers:
//
//   - noop, which is what an unconfigured component gets and therefore the
//     lower bound — a component that traces nowhere still pays this.
//   - a real SDK provider sampling everything, which is what a traced
//     deployment pays.
//
// The pair matters because the two answer different questions. Against noop,
// Begin's own bookkeeping is the whole number. Against the SDK, span creation
// dominates, and the same bookkeeping is a rounding error. Any argument for
// making Begin cheaper has to say which of the two it is trying to improve.

// benchProviders returns the two tracer providers every shape below is measured
// against. The SDK provider gets no exporter: an exporter would measure the
// exporter, and what is wanted here is the cost of recording a span, not of
// shipping one.
func benchProviders(b *testing.B) []struct {
	provider *sdktrace.TracerProvider
	name     string
} {
	b.Helper()

	sdk := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()))
	b.Cleanup(func() { _ = sdk.Shutdown(context.Background()) })

	return []struct {
		provider *sdktrace.TracerProvider
		name     string
	}{
		// A nil provider resolves to noop inside NewObserver, which is the
		// path an unconfigured component actually takes.
		{name: "noopTracer", provider: nil},
		{name: "recordingTracer", provider: sdk},
	}
}

// observerFor builds the observer under test. The logger is left nil so it
// resolves to the noop logger: the logging providers have their own benchmarks,
// and mixing one in here would price slog rather than Begin.
func observerFor(provider *sdktrace.TracerProvider) Observer {
	if provider == nil {
		return NewObserver("bench", nil, nil)
	}

	return NewObserver("bench", nil, provider)
}

// BenchmarkObserver_Begin prices Begin at the option counts real call sites use.
//
// The noValues row is the floor. The step from it to oneValue is what a single
// BeginOption costs, and it is not free: an option is a closure, so the first
// one allocates both the variadic slice and the closure itself, and each
// further one allocates its own closure. A method that seeds three values pays
// three closures for the privilege of stating them at Begin rather than after
// it.
func BenchmarkObserver_Begin(b *testing.B) {
	ctx := b.Context()

	for _, p := range benchProviders(b) {
		o := observerFor(p.provider)

		b.Run(p.name+"/noValues", func(b *testing.B) {
			for b.Loop() {
				_, op := o.Begin(ctx)
				op.End()
			}
		})

		b.Run(p.name+"/oneValue", func(b *testing.B) {
			for b.Loop() {
				_, op := o.Begin(ctx, WithValue("name", "some-key"))
				op.End()
			}
		})

		b.Run(p.name+"/threeValues", func(b *testing.B) {
			for b.Loop() {
				_, op := o.Begin(ctx,
					WithValue("name", "some-key"),
					WithValue("length", 42),
					WithValue("scope", "acct_01HZY0000000000000"),
				)
				op.End()
			}
		})

		// WithValues carries the same three values in one option, so the delta
		// against threeValues is what the map costs versus the closures — the
		// choice a call site makes when it has more than one value to seed.
		b.Run(p.name+"/withValuesMap", func(b *testing.B) {
			values := map[string]any{
				"name":   "some-key",
				"length": 42,
				"scope":  "acct_01HZY0000000000000",
			}

			for b.Loop() {
				_, op := o.Begin(ctx, WithValues(values))
				op.End()
			}
		})
	}
}

// BenchmarkObserver_BeginVersusBeginCustom is the measurement that prices the
// span name.
//
// Begin resolves its span name from the call stack via tracing.GetCallerName;
// BeginCustom is handed one. Nothing else differs between them, so the delta is
// exactly what naming spans by reflection over the stack costs, and it is the
// number any proposal to require an explicit name on Begin has to justify
// itself against.
//
// Read it against both providers before concluding anything. The stack walk is
// a large share of Begin under a noop tracer and a small one under a recording
// tracer, so "how much does this actually save" depends entirely on whether the
// deployment in question is sampling.
func BenchmarkObserver_BeginVersusBeginCustom(b *testing.B) {
	ctx := b.Context()

	for _, p := range benchProviders(b) {
		o := observerFor(p.provider)

		b.Run(p.name+"/Begin", func(b *testing.B) {
			for b.Loop() {
				_, op := o.Begin(ctx)
				op.End()
			}
		})

		b.Run(p.name+"/BeginCustom", func(b *testing.B) {
			for b.Loop() {
				_, op := o.BeginCustom(ctx, "bench.staticName")
				op.End()
			}
		})
	}
}

// BenchmarkOperation prices the methods an instrumented body calls on the
// Operation after Begin returns it.
//
// Set writes to both pillars, so it is SpanOnly plus LogOnly and should price
// as their sum. Against a noop tracer AttachToSpan short-circuits on
// IsRecording and SpanOnly is nearly free; against a recording one it is not,
// which is the case where preferring LogOnly for a noisy value actually saves
// something.
func BenchmarkOperation(b *testing.B) {
	ctx := b.Context()

	for _, p := range benchProviders(b) {
		o := observerFor(p.provider)

		b.Run(p.name+"/Set", func(b *testing.B) {
			_, op := o.Begin(ctx)
			defer op.End()

			for b.Loop() {
				op.Set("key", "value")
			}
		})

		b.Run(p.name+"/SpanOnly", func(b *testing.B) {
			_, op := o.Begin(ctx)
			defer op.End()

			for b.Loop() {
				op.SpanOnly("key", "value")
			}
		})

		b.Run(p.name+"/LogOnly", func(b *testing.B) {
			_, op := o.Begin(ctx)
			defer op.End()

			for b.Loop() {
				op.LogOnly("key", "value")
			}
		})
	}
}

// BenchmarkNewObserver prices construction, which happens once per component
// rather than once per operation. It is here so that the two are not confused:
// a constructor doing this a handful of times is not the same expense as Begin
// doing its work on every call, and only one of the two is worth optimizing.
func BenchmarkNewObserver(b *testing.B) {
	values := map[string]any{"queue": "outbox", "shard": "3"}

	b.Run("NewObserver", func(b *testing.B) {
		for b.Loop() {
			observerSink = NewObserver("bench", nil, nil)
		}
	})

	b.Run("NewObserverWithValues", func(b *testing.B) {
		for b.Loop() {
			observerSink = NewObserverWithValues("bench", nil, nil, values)
		}
	})

	// An observer built with values seeds them onto every Operation, so the
	// cost lands on Begin rather than here. This is that cost.
	b.Run("Begin/seededObserver", func(b *testing.B) {
		o := NewObserverWithValues("bench", nil, nil, values)
		ctx := b.Context()

		for b.Loop() {
			_, op := o.Begin(ctx)
			op.End()
		}
	})
}

// BenchmarkEnsureLogger prices the nil check every constructor in the module
// performs on the way to building its observer.
func BenchmarkEnsureLogger(b *testing.B) {
	logger := logging.NewNamedLogger(nil, "bench")

	b.Run("nil", func(b *testing.B) {
		for b.Loop() {
			loggerSink = logging.EnsureLogger(nil)
		}
	})

	b.Run("present", func(b *testing.B) {
		for b.Loop() {
			loggerSink = logging.EnsureLogger(logger)
		}
	})
}

var (
	observerSink Observer
	loggerSink   logging.Logger
	_            = context.Background
)

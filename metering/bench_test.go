package metering

import (
	"strconv"
	"testing"
)

// Record runs once per billable operation and Check runs once per request that
// is subject to a quota, so between them they sit on the hot path of anything
// metered. Unlike the rest of this package's benchmarks-worth-having, both are
// backed by a real store — SQLite here — because a durable recorder that did
// not write anywhere would not be measuring the thing that makes it durable.
//
// That makes these numbers store-dominated by construction. They are still
// worth having: what they establish is the shape of the cost, in particular how
// it moves with batch size, which is the lever a caller actually has.

func BenchmarkDurableRecorder_Record(b *testing.B) {
	recorder, env, _, _ := newTestRecorder(b)

	// A distinct idempotency key per iteration, since a repeated one is
	// deduplicated at ingest and would measure the dedupe path instead.
	b.Run("single", func(b *testing.B) {
		var i int
		for b.Loop() {
			i++
			_ = recordThrough(b, env, recorder, Usage{
				Subject:        testSubject,
				Meter:          testMeter,
				Quantity:       1,
				IdempotencyKey: "bench-single-" + strconv.Itoa(i),
			})
		}
	})

	// The same total volume in batches, which is the lever a caller has: one
	// call carrying many records rather than many calls carrying one. The
	// per-record cost across these rows is what says whether batching is worth
	// the buffering it requires.
	for _, size := range []int{10, 100} {
		b.Run("batch="+strconv.Itoa(size), func(b *testing.B) {
			usages := make([]Usage, size)

			var i int
			for b.Loop() {
				i++

				for j := range usages {
					usages[j] = Usage{
						Subject:        testSubject,
						Meter:          testMeter,
						Quantity:       1,
						IdempotencyKey: "bench-batch-" + strconv.Itoa(size) + "-" + strconv.Itoa(i) + "-" + strconv.Itoa(j),
					}
				}

				_ = recordThrough(b, env, recorder, usages...)
			}

			// Per-record rather than per-call, so the rows are comparable to
			// the single row above and to each other.
			b.ReportMetric(float64(b.Elapsed().Nanoseconds())/float64(b.N*size), "ns/record")
		})
	}

	// A repeated idempotency key, which is what a retried request or a
	// redelivered queue message produces. It has to stay cheap: redelivery is
	// not rare, and the whole point of the key is that paying for it twice is
	// cheaper than counting it twice.
	b.Run("duplicate", func(b *testing.B) {
		usage := Usage{
			Subject:        testSubject,
			Meter:          testMeter,
			Quantity:       1,
			IdempotencyKey: "bench-duplicate",
		}

		_ = recordThrough(b, env, recorder, usage)

		for b.Loop() {
			_ = recordThrough(b, env, recorder, usage)
		}
	})
}

// BenchmarkQuotaEnforcer_Check prices the question asked before a metered
// operation proceeds, which entitlements delegates to on its quota path.
func BenchmarkQuotaEnforcer_Check(b *testing.B) {
	ctx := b.Context()

	// A limit high enough that the loop measures the allow path throughout;
	// the refusal path gets its own row below.
	env := newTestEnforcer(b, BehaviorBlock, 1<<62)

	b.Run("allowed", func(b *testing.B) {
		for b.Loop() {
			decisionSink, _ = env.enforcer.Check(ctx, testScope, testSubject, testMeter, 1)
		}
	})

	// Over the limit, which is where an enforcer spends its time once something
	// is actually being throttled.
	b.Run("denied", func(b *testing.B) {
		denied := newTestEnforcer(b, BehaviorBlock, 0)

		for b.Loop() {
			decisionSink, _ = denied.enforcer.Check(ctx, testScope, testSubject, testMeter, 1)
		}
	})

	// An unregistered meter, which is a configuration error rather than a
	// quota decision, and resolves without touching the store.
	b.Run("unknownMeter", func(b *testing.B) {
		for b.Loop() {
			decisionSink, _ = env.enforcer.Check(ctx, testScope, testSubject, "no_such_meter", 1)
		}
	})
}

var decisionSink *Decision

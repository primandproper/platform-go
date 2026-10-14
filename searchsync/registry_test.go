package searchsync

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"testing"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/jobs"
	"github.com/primandproper/primitives-go/v2/observability"
	nooplogging "github.com/primandproper/primitives-go/v2/observability/logging/noop"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	metricsmock "github.com/primandproper/primitives-go/v2/observability/metrics/mock"
	noopmetrics "github.com/primandproper/primitives-go/v2/observability/metrics/noop"
	textsearchmock "github.com/primandproper/primitives-go/v2/search/text/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

var _ Source[testDoc] = (*stubSource)(nil)

// orderSpec is the spec most of these cases register, with the two required
// strings filled in and everything else left to the test.
func orderSpec(source Source[testDoc], target Target[testDoc]) IndexSpec[testDoc] {
	return IndexSpec[testDoc]{
		Name:   "orders",
		Topic:  "orders-index",
		Source: source,
		Target: target,
	}
}

// registerOrders registers the ordinary spec, failing the test if it will not
// build.
func registerOrders(t *testing.T, reg *Registry, source Source[testDoc], target Target[testDoc]) *Registration[testDoc] {
	t.Helper()

	registration, err := RegisterIndex(reg, orderSpec(source, target))
	must.NoError(t, err)

	return registration
}

// eventPayload renders an event as the bytes a relay would hand a pool.
func eventPayload(t *testing.T, op Op, documentID string) []byte {
	t.Helper()

	payload, err := json.Marshal(NewEvent(op, documentID))
	must.NoError(t, err)

	return payload
}

// failingSearchMetrics serves the noop provider's instruments for every name
// except failOn, which reports an error — the one way past RegisterIndex's own
// validation into a failed Syncer or Reindexer build.
func failingSearchMetrics(failOn string) metrics.Provider {
	base := noopmetrics.NewMetricsProvider()
	boom := stderrors.New("instrument unavailable")

	return &metricsmock.ProviderMock{
		NewInt64CounterFunc: func(name string, opts ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			if name == failOn {
				return nil, boom
			}

			return base.NewInt64Counter(name, opts...)
		},
		NewFloat64HistogramFunc: func(name string, opts ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
			if name == failOn {
				return nil, boom
			}

			return base.NewFloat64Histogram(name, opts...)
		},
	}
}

func TestNewRegistry(T *testing.T) {
	T.Parallel()

	T.Run("starts empty", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()
		must.NotNil(t, reg)
		test.SliceEmpty(t, reg.Names())
		test.SliceEmpty(t, reg.PoolSpecs())
	})

	T.Run("tolerates a nil option", func(t *testing.T) {
		t.Parallel()

		must.NotNil(t, NewRegistry(nil))
	})

	T.Run("takes the pillars in one go", func(t *testing.T) {
		t.Parallel()

		logger := nooplogging.NewLogger()
		reg := NewRegistry(WithRegistryPillars(&observability.Pillars{Logger: logger}))
		test.NotNil(t, reg.logger)

		// Applied in order, so a pillar handed over may then be overridden.
		reg = NewRegistry(WithRegistryPillars(&observability.Pillars{Logger: logger}), WithRegistryLogger(nil))
		test.Nil(t, reg.logger)
	})

	T.Run("tolerates nil pillars", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry(WithRegistryPillars(nil))
		test.Nil(t, reg.logger)
		test.Nil(t, reg.tracerProvider)
		test.Nil(t, reg.metricsProvider)
	})
}

func TestRegisterIndex(T *testing.T) {
	T.Parallel()

	T.Run("builds the syncer and the reindexer together", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry(WithRegistryMetricsProvider(noopmetrics.NewMetricsProvider()))

		registration := registerOrders(t, reg, &stubSource{}, &stubTarget{})
		must.NotNil(t, registration.Syncer)
		must.NotNil(t, registration.Reindexer)
		test.EqOp(t, "orders", registration.Syncer.Name())
		test.EqOp(t, "orders", registration.Reindexer.Name())
		test.Eq(t, []string{"orders"}, reg.Names())
	})

	T.Run("refuses a nil registry", func(t *testing.T) {
		t.Parallel()

		_, err := RegisterIndex(nil, orderSpec(&stubSource{}, &stubTarget{}))
		test.ErrorIs(t, err, ErrNilRegistry)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("refuses a spec with no name", func(t *testing.T) {
		t.Parallel()

		spec := orderSpec(&stubSource{}, &stubTarget{})
		spec.Name = ""

		_, err := RegisterIndex(NewRegistry(), spec)
		test.ErrorIs(t, err, ErrEmptyName)
	})

	T.Run("refuses a spec with no topic", func(t *testing.T) {
		t.Parallel()

		spec := orderSpec(&stubSource{}, &stubTarget{})
		spec.Topic = ""

		_, err := RegisterIndex(NewRegistry(), spec)
		test.ErrorIs(t, err, ErrEmptyTopic)
		test.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)
	})

	T.Run("refuses a spec with no source", func(t *testing.T) {
		t.Parallel()

		_, err := RegisterIndex(NewRegistry(), orderSpec(nil, &stubTarget{}))
		test.ErrorIs(t, err, ErrNilSource)
	})

	T.Run("refuses a spec with no target", func(t *testing.T) {
		t.Parallel()

		_, err := RegisterIndex(NewRegistry(), orderSpec(&stubSource{}, nil))
		test.ErrorIs(t, err, ErrNilTarget)
	})

	T.Run("refuses a second index under one name", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()
		registerOrders(t, reg, &stubSource{}, &stubTarget{})

		spec := orderSpec(&stubSource{}, &stubTarget{})
		spec.Topic = "other-index"

		_, err := RegisterIndex(reg, spec)
		test.ErrorIs(t, err, ErrDuplicateIndex)
		test.StrContains(t, err.Error(), `search index "orders"`)
		test.SliceLen(t, 1, reg.Names())
	})

	T.Run("refuses a second index on one topic", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()
		registerOrders(t, reg, &stubSource{}, &stubTarget{})

		spec := orderSpec(&stubSource{}, &stubTarget{})
		spec.Name = "customers"

		_, err := RegisterIndex(reg, spec)
		test.ErrorIs(t, err, ErrDuplicateIndex)
		test.StrContains(t, err.Error(), `topic "orders-index"`)
		test.SliceLen(t, 1, reg.Names())
	})

	T.Run("reports a syncer that will not build", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry(WithRegistryMetricsProvider(failingSearchMetrics("search_sync_events_applied")))

		spec := orderSpec(&stubSource{}, &stubTarget{})
		spec.Stamp = func(context.Context, []string) error { return nil }

		_, err := RegisterIndex(reg, spec)
		test.Error(t, err)
		test.StrContains(t, err.Error(), "building orders syncer")
		test.SliceEmpty(t, reg.Names())

		// The registry holds nothing, so the stamp buffer that was already
		// running is not one Close could reach — RegisterIndex closed it on
		// the way out rather than leaving its goroutine behind.
		must.NoError(t, reg.Close(context.Background()))
	})

	T.Run("reports a reindexer that will not build", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry(WithRegistryMetricsProvider(failingSearchMetrics("search_sync_reindex_documents")))

		_, err := RegisterIndex(reg, orderSpec(&stubSource{}, &stubTarget{}))
		test.Error(t, err)
		test.StrContains(t, err.Error(), "building orders reindexer")
		test.SliceEmpty(t, reg.Names())
	})

	T.Run("indexes without a stamp rather than through a nil one", func(t *testing.T) {
		t.Parallel()

		source := &stubSource{fetchFunc: func(ids ...string) ([]Document[testDoc], error) {
			return []Document[testDoc]{{ID: ids[0], Body: &testDoc{Name: "widget"}}}, nil
		}}
		target := &stubTarget{}

		registration := registerOrders(t, NewRegistry(), source, target)

		must.NoError(t, registration.Syncer.Apply(context.Background(), NewEvent(OpUpsert, "order-1")))
		test.Eq(t, []string{"order-1"}, target.upserted)
	})

	T.Run("stamps what the index accepted", func(t *testing.T) {
		t.Parallel()

		source := &stubSource{fetchFunc: func(ids ...string) ([]Document[testDoc], error) {
			return []Document[testDoc]{{ID: ids[0], Body: &testDoc{Name: "widget"}}}, nil
		}}

		var stamped []string

		reg := NewRegistry()
		spec := orderSpec(source, &stubTarget{})
		spec.Stamp = func(_ context.Context, ids []string) error {
			stamped = append(stamped, ids...)

			return nil
		}

		registration, err := RegisterIndex(reg, spec)
		must.NoError(t, err)

		must.NoError(t, registration.Syncer.Apply(context.Background(), NewEvent(OpUpsert, "order-1")))

		// Close is what flushes: the buffer is the registry's, and closing it
		// is the one shutdown obligation the pipeline has of its own.
		must.NoError(t, reg.Close(context.Background()))
		test.Eq(t, []string{"order-1"}, stamped)
	})

	T.Run("passes the pruner through to the rebuild", func(t *testing.T) {
		t.Parallel()

		target := &stubTarget{}
		pruner := &stubEnumerator{scanFunc: func(after string, _ int) ([]string, error) {
			if after == "" {
				return []string{"order-gone"}, nil
			}

			return nil, nil
		}}

		reg := NewRegistry()
		spec := orderSpec(&stubSource{}, target)
		spec.Pruner = pruner

		registration, err := RegisterIndex(reg, spec)
		must.NoError(t, err)

		result, err := registration.Reindexer.Reindex(context.Background())
		must.NoError(t, err)
		test.EqOp(t, int64(1), result.Pruned)
		test.Eq(t, []string{"order-gone"}, target.deleted)
	})

	T.Run("applies a spec option after the registry's own", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()
		spec := orderSpec(&stubSource{}, &stubTarget{})
		spec.ReindexOptions = []ReindexOption{WithReindexBatchSize(7)}

		registration, err := RegisterIndex(reg, spec)
		must.NoError(t, err)
		test.EqOp(t, 7, registration.Reindexer.batchSize)
	})
}

func TestRegistry_PoolSpecs(T *testing.T) {
	T.Parallel()

	T.Run("returns one spec per index, in registration order", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()
		registerOrders(t, reg, &stubSource{}, &stubTarget{})

		customers := orderSpec(&stubSource{}, &stubTarget{})
		customers.Name = "customers"
		customers.Topic = "customers-index"

		_, err := RegisterIndex(reg, customers)
		must.NoError(t, err)

		specs := reg.PoolSpecs()
		must.SliceLen(t, 2, specs)
		test.EqOp(t, "orders-index", specs[0].Topic)
		test.EqOp(t, "customers-index", specs[1].Topic)
	})

	T.Run("carries the pool config and options through", func(t *testing.T) {
		t.Parallel()

		cfg := &jobs.PoolConfig{Concurrency: 4}

		reg := NewRegistry()
		spec := orderSpec(&stubSource{}, &stubTarget{})
		spec.PoolConfig = cfg
		spec.PoolOptions = []jobs.PoolOption{jobs.WithPoolLogger(nooplogging.NewLogger())}

		_, err := RegisterIndex(reg, spec)
		must.NoError(t, err)

		specs := reg.PoolSpecs()
		must.SliceLen(t, 1, specs)
		test.EqOp(t, cfg, specs[0].Config)
		test.SliceLen(t, 1, specs[0].Options)
	})

	T.Run("carries the syncer's own handler", func(t *testing.T) {
		t.Parallel()

		source := &stubSource{fetchFunc: func(ids ...string) ([]Document[testDoc], error) {
			return []Document[testDoc]{{ID: ids[0], Body: &testDoc{Name: "widget"}}}, nil
		}}
		target := &stubTarget{}

		reg := NewRegistry()
		registerOrders(t, reg, source, target)

		specs := reg.PoolSpecs()
		must.SliceLen(t, 1, specs)
		must.NotNil(t, specs[0].Handler)

		must.NoError(t, specs[0].Handler(context.Background(), eventPayload(t, OpUpsert, "order-1")))
		test.Eq(t, []string{"order-1"}, target.upserted)
	})
}

func TestRegistry_ReindexAll(T *testing.T) {
	T.Parallel()

	T.Run("rebuilds nothing when nothing is registered", func(t *testing.T) {
		t.Parallel()

		results, err := NewRegistry().ReindexAll(context.Background())
		must.NoError(t, err)
		test.MapEmpty(t, results)
	})

	T.Run("rebuilds every registered index", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()

		for _, name := range []string{"orders", "customers"} {
			spec := orderSpec(&stubSource{scanFunc: pagedDocs(name + "-1")}, &stubTarget{})
			spec.Name = name
			spec.Topic = name + "-index"

			_, err := RegisterIndex(reg, spec)
			must.NoError(t, err)
		}

		results, err := reg.ReindexAll(context.Background())
		must.NoError(t, err)
		must.MapLen(t, 2, results)
		test.EqOp(t, int64(1), results["orders"].Upserted)
		test.EqOp(t, int64(1), results["customers"].Upserted)
	})

	T.Run("carries on past a failed rebuild and joins the failures", func(t *testing.T) {
		t.Parallel()

		boom := stderrors.New("index unavailable")

		reg := NewRegistry()

		broken := orderSpec(&stubSource{scanFunc: pagedDocs("order-1")},
			&stubTarget{upsertFunc: func(...Document[testDoc]) error { return boom }})

		_, err := RegisterIndex(reg, broken)
		must.NoError(t, err)

		healthy := orderSpec(&stubSource{scanFunc: pagedDocs("customer-1")}, &stubTarget{})
		healthy.Name = "customers"
		healthy.Topic = "customers-index"

		_, err = RegisterIndex(reg, healthy)
		must.NoError(t, err)

		results, err := reg.ReindexAll(context.Background())
		test.ErrorIs(t, err, boom)
		test.StrContains(t, err.Error(), `reindexing "orders"`)

		// The healthy index was rebuilt regardless, and the failed one still
		// reports what it landed before it stopped.
		must.MapLen(t, 2, results)
		test.EqOp(t, int64(1), results["customers"].Upserted)
		test.EqOp(t, int64(0), results["orders"].Upserted)
	})

	T.Run("stops on a cancelled context", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()

		sources := make([]*stubSource, 0, 2)

		for _, name := range []string{"orders", "customers"} {
			source := &stubSource{scanFunc: pagedDocs(name + "-1")}
			sources = append(sources, source)

			spec := orderSpec(source, &stubTarget{})
			spec.Name = name
			spec.Topic = name + "-index"

			_, err := RegisterIndex(reg, spec)
			must.NoError(t, err)
		}

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		_, err := reg.ReindexAll(ctx)
		test.ErrorIs(t, err, context.Canceled)

		// One report of the cancellation rather than one per index, and the
		// second source was never walked.
		test.SliceEmpty(t, sources[1].scanned)
	})
}

// wipingTarget is a stubTarget that admits Wipe, counting the calls.
type wipingTarget struct {
	wipeFunc func() error

	stubTarget

	wipes int
}

var _ Wiper = (*wipingTarget)(nil)

func (w *wipingTarget) Wipe(context.Context) error {
	w.wipes++

	if w.wipeFunc != nil {
		return w.wipeFunc()
	}

	return nil
}

// registerNamed registers one index under name, with its topic derived from it.
func registerNamed(t *testing.T, reg *Registry, name string, source Source[testDoc], target Target[testDoc]) {
	t.Helper()

	spec := orderSpec(source, target)
	spec.Name = name
	spec.Topic = name + "-index"

	_, err := RegisterIndex(reg, spec)
	must.NoError(t, err)
}

func TestRegistry_Reindex(T *testing.T) {
	T.Parallel()

	T.Run("rebuilds only the named index", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()

		orders := &stubSource{scanFunc: pagedDocs("order-1")}
		customers := &stubSource{scanFunc: pagedDocs("customer-1")}

		registerNamed(t, reg, "orders", orders, &stubTarget{})
		registerNamed(t, reg, "customers", customers, &stubTarget{})

		results, err := reg.Reindex(context.Background(), "customers")
		must.NoError(t, err)
		must.MapLen(t, 1, results)
		test.EqOp(t, int64(1), results["customers"].Upserted)
		test.SliceEmpty(t, orders.scanned)
	})

	T.Run("rebuilds every index when none is named", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()
		registerNamed(t, reg, "orders", &stubSource{scanFunc: pagedDocs("order-1")}, &stubTarget{})
		registerNamed(t, reg, "customers", &stubSource{scanFunc: pagedDocs("customer-1")}, &stubTarget{})

		results, err := reg.Reindex(context.Background())
		must.NoError(t, err)
		must.MapLen(t, 2, results)
	})

	T.Run("refuses every unknown name before rebuilding anything", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()

		orders := &stubSource{scanFunc: pagedDocs("order-1")}
		registerNamed(t, reg, "orders", orders, &stubTarget{})

		results, err := reg.Reindex(context.Background(), "orders", "ordres", "custmers")
		test.ErrorIs(t, err, ErrUnknownIndex)
		test.StrContains(t, err.Error(), `"ordres"`)
		test.StrContains(t, err.Error(), `"custmers"`)
		test.Nil(t, results)

		// The known name listed first was not rebuilt either.
		test.SliceEmpty(t, orders.scanned)
	})

	T.Run("rebuilds a name given twice once", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()

		target := &stubTarget{}
		registerNamed(t, reg, "orders", &stubSource{scanFunc: pagedDocs("order-1")}, target)

		results, err := reg.Reindex(context.Background(), "orders", "orders")
		must.NoError(t, err)
		must.MapLen(t, 1, results)
		test.Eq(t, []string{"order-1"}, target.upserted)
	})

	T.Run("carries on past a failed rebuild and joins the failures", func(t *testing.T) {
		t.Parallel()

		boom := stderrors.New("index unavailable")

		reg := NewRegistry()
		registerNamed(t, reg, "orders", &stubSource{scanFunc: pagedDocs("order-1")},
			&stubTarget{upsertFunc: func(...Document[testDoc]) error { return boom }})
		registerNamed(t, reg, "customers", &stubSource{scanFunc: pagedDocs("customer-1")}, &stubTarget{})

		results, err := reg.Reindex(context.Background(), "orders", "customers")
		test.ErrorIs(t, err, boom)
		test.StrContains(t, err.Error(), `reindexing "orders"`)
		must.MapLen(t, 2, results)
		test.EqOp(t, int64(1), results["customers"].Upserted)
	})
}

func TestRegistry_Wipe(T *testing.T) {
	T.Parallel()

	T.Run("empties only the named index", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()

		orders, customers := &wipingTarget{}, &wipingTarget{}
		registerNamed(t, reg, "orders", &stubSource{}, orders)
		registerNamed(t, reg, "customers", &stubSource{}, customers)

		must.NoError(t, reg.Wipe(context.Background(), "customers"))
		test.EqOp(t, 0, orders.wipes)
		test.EqOp(t, 1, customers.wipes)
	})

	T.Run("empties every index when none is named", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()

		orders, customers := &wipingTarget{}, &wipingTarget{}
		registerNamed(t, reg, "orders", &stubSource{}, orders)
		registerNamed(t, reg, "customers", &stubSource{}, customers)

		must.NoError(t, reg.Wipe(context.Background()))
		test.EqOp(t, 1, orders.wipes)
		test.EqOp(t, 1, customers.wipes)
	})

	T.Run("empties nothing when nothing is registered", func(t *testing.T) {
		t.Parallel()

		must.NoError(t, NewRegistry().Wipe(context.Background()))
	})

	T.Run("refuses an unknown name before emptying anything", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()

		orders := &wipingTarget{}
		registerNamed(t, reg, "orders", &stubSource{}, orders)

		err := reg.Wipe(context.Background(), "orders", "ordres")
		test.ErrorIs(t, err, ErrUnknownIndex)
		test.StrContains(t, err.Error(), `"ordres"`)
		test.EqOp(t, 0, orders.wipes)
	})

	T.Run("refuses a target that cannot be emptied before emptying anything", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()

		orders := &wipingTarget{}
		registerNamed(t, reg, "orders", &stubSource{}, orders)
		registerNamed(t, reg, "customers", &stubSource{}, &stubTarget{})

		err := reg.Wipe(context.Background(), "orders", "customers")
		test.ErrorIs(t, err, ErrTargetCannotWipe)
		test.StrContains(t, err.Error(), `"customers"`)
		test.EqOp(t, 0, orders.wipes)
	})

	T.Run("carries on past a failed wipe and joins the failures", func(t *testing.T) {
		t.Parallel()

		boom := stderrors.New("index unavailable")

		reg := NewRegistry()

		orders := &wipingTarget{wipeFunc: func() error { return boom }}
		customers := &wipingTarget{}
		registerNamed(t, reg, "orders", &stubSource{}, orders)
		registerNamed(t, reg, "customers", &stubSource{}, customers)

		err := reg.Wipe(context.Background())
		test.ErrorIs(t, err, boom)
		test.StrContains(t, err.Error(), `wiping "orders"`)
		test.EqOp(t, 1, customers.wipes)
	})

	T.Run("stops on a cancelled context", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()

		orders, customers := &wipingTarget{}, &wipingTarget{}
		registerNamed(t, reg, "orders", &stubSource{}, orders)
		registerNamed(t, reg, "customers", &stubSource{}, customers)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := reg.Wipe(ctx)
		test.ErrorIs(t, err, context.Canceled)
		test.EqOp(t, 0, orders.wipes)
		test.EqOp(t, 0, customers.wipes)
	})

	T.Run("reaches a text index through the adapter", func(t *testing.T) {
		t.Parallel()

		index := &textsearchmock.IndexMock[testDoc]{
			WipeFunc: func(context.Context) error { return nil },
		}

		target, err := TextTarget[testDoc](index)
		must.NoError(t, err)

		reg := NewRegistry()
		registerNamed(t, reg, "orders", &stubSource{}, target)

		must.NoError(t, reg.Wipe(context.Background(), "orders"))
		test.SliceLen(t, 1, index.WipeCalls())
	})
}

func TestRegistry_Close(T *testing.T) {
	T.Parallel()

	T.Run("closes nothing when no index stamps", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()
		registerOrders(t, reg, &stubSource{}, &stubTarget{})

		must.NoError(t, reg.Close(context.Background()))
	})

	T.Run("reports a stamp that will not flush", func(t *testing.T) {
		t.Parallel()

		boom := stderrors.New("stamping unavailable")

		source := &stubSource{fetchFunc: func(ids ...string) ([]Document[testDoc], error) {
			return []Document[testDoc]{{ID: ids[0], Body: &testDoc{Name: "widget"}}}, nil
		}}

		reg := NewRegistry()
		spec := orderSpec(source, &stubTarget{})
		spec.Stamp = func(context.Context, []string) error { return boom }

		registration, err := RegisterIndex(reg, spec)
		must.NoError(t, err)

		must.NoError(t, registration.Syncer.Apply(context.Background(), NewEvent(OpUpsert, "order-1")))

		err = reg.Close(context.Background())
		test.ErrorIs(t, err, boom)
		test.StrContains(t, err.Error(), `closing "orders" stamp buffer`)
	})

	T.Run("is safe to repeat", func(t *testing.T) {
		t.Parallel()

		reg := NewRegistry()
		spec := orderSpec(&stubSource{}, &stubTarget{})
		spec.Stamp = func(context.Context, []string) error { return nil }

		_, err := RegisterIndex(reg, spec)
		must.NoError(t, err)

		must.NoError(t, reg.Close(context.Background()))
		must.NoError(t, reg.Close(context.Background()))
	})
}

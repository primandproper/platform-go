package metering

import (
	"context"
	"sync"
	"testing"

	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

// testProduct is the product most of these tests entitle a subject to.
const testProduct = "prod_pro"

// newPlanRegistry builds a registry with the meter the limits tables below name
// and a second one on a different period, so a test can prove the quota's period
// is read off the meter rather than assumed.
func newPlanRegistry(tb testing.TB) *Registry {
	tb.Helper()

	registry := NewRegistry()

	must.NoError(tb, registry.RegisterMeter(Meter{
		Name: testMeter, Unit: "requests", Aggregation: AggregationSum, Period: PeriodMonth,
	}))
	must.NoError(tb, registry.RegisterMeter(Meter{
		Name: "daily_exports", Unit: "exports", Aggregation: AggregationSum, Period: PeriodDay,
	}))

	return registry
}

// entitling returns a reader that entitles every subject to one product,
// tallying into calls how many times it was asked — which is how the tests below
// tell the ladder's short circuit from its long way round.
func entitling(product string, calls *int) EntitlementReader {
	return EntitlementReaderFunc(func(context.Context, string) (string, bool, error) {
		*calls++

		return product, true, nil
	})
}

// unentitled is a reader for a subject nothing entitles: no subscription, or one
// that is past due, cancelled, or never completed.
func unentitled(calls *int) EntitlementReader {
	return EntitlementReaderFunc(func(context.Context, string) (string, bool, error) {
		*calls++

		return "", false, nil
	})
}

func TestNewPlanLimitSource(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil registry or reader", func(t *testing.T) {
		t.Parallel()

		var calls int

		_, err := NewPlanLimitSource(nil, nil, entitling(testProduct, &calls))
		test.ErrorIs(t, err, ErrNilRegistry)

		_, err = NewPlanLimitSource(newPlanRegistry(t), nil, nil)
		test.ErrorIs(t, err, ErrNilEntitlementReader)
	})

	T.Run("refuses limits for a meter nobody registered", func(t *testing.T) {
		t.Parallel()

		var calls int

		// The worst bug this package has available: nothing records against the
		// misspelled meter, so the tier appears to work and is unlimited.
		_, err := NewPlanLimitSource(newPlanRegistry(t), map[string]PlanLimits{
			"api_reqeusts": {Behavior: BehaviorBlock, Unsubscribed: 10},
		}, entitling(testProduct, &calls))

		must.ErrorIs(t, err, ErrUnknownMeter)
		test.StrContains(t, err.Error(), "api_reqeusts")
	})

	T.Run("refuses a missing or unknown behavior", func(t *testing.T) {
		t.Parallel()

		var calls int

		for _, behavior := range []QuotaBehavior{"", "allow", "deny"} {
			_, err := NewPlanLimitSource(newPlanRegistry(t), map[string]PlanLimits{
				testMeter: {Behavior: behavior, Unsubscribed: 10},
			}, entitling(testProduct, &calls))

			must.ErrorIs(t, err, ErrInvalidPlanLimits)
			test.StrContains(t, err.Error(), testMeter)
		}
	})

	T.Run("refuses a negative limit", func(t *testing.T) {
		t.Parallel()

		var calls int

		_, err := NewPlanLimitSource(newPlanRegistry(t), map[string]PlanLimits{
			testMeter: {Behavior: BehaviorBlock, Unsubscribed: -1},
		}, entitling(testProduct, &calls))
		must.ErrorIs(t, err, ErrInvalidPlanLimits)
		test.StrContains(t, err.Error(), "unsubscribed")

		_, err = NewPlanLimitSource(newPlanRegistry(t), map[string]PlanLimits{
			testMeter: {
				Behavior:  BehaviorBlock,
				ByProduct: map[string]int64{testProduct: -5},
			},
		}, entitling(testProduct, &calls))
		must.ErrorIs(t, err, ErrInvalidPlanLimits)
		test.StrContains(t, err.Error(), testProduct)
	})

	T.Run("accepts an empty table", func(t *testing.T) {
		t.Parallel()

		var calls int

		// The honest starting position for a product with no usage data to set a
		// limit from: everything unlimited, the counting still happening.
		source, err := NewPlanLimitSource(newPlanRegistry(t), nil, entitling(testProduct, &calls))
		must.NoError(t, err)

		q, err := source.QuotaFor(t.Context(), testSubject, testMeter)
		must.NoError(t, err)
		test.EqOp(t, Unlimited, q.Limit)
	})

	T.Run("reports an instrument it cannot build", func(t *testing.T) {
		t.Parallel()

		var calls int

		_, err := NewPlanLimitSource(newPlanRegistry(t), nil, entitling(testProduct, &calls),
			WithPlanLimitMetricsProvider(failingInstrumentProvider(serviceName+"_unconfigured_products")))

		must.ErrorIs(t, err, errInstrument)
		test.StrContains(t, err.Error(), "creating")
	})
}

func TestPlanLimitSource_QuotaFor(T *testing.T) {
	T.Parallel()

	limits := func() map[string]PlanLimits {
		return map[string]PlanLimits{
			testMeter: {
				ByProduct:    map[string]int64{testProduct: 5_000, "prod_team": 50_000},
				Behavior:     BehaviorBlock,
				Unsubscribed: 100,
			},
		}
	}

	T.Run("serves the entitling product's limit", func(t *testing.T) {
		t.Parallel()

		var calls int

		source, err := NewPlanLimitSource(newPlanRegistry(t), limits(), entitling(testProduct, &calls))
		must.NoError(t, err)

		q, err := source.QuotaFor(t.Context(), testSubject, testMeter)
		must.NoError(t, err)

		test.EqOp(t, testMeter, q.Meter)
		test.EqOp(t, int64(5_000), q.Limit)
		test.EqOp(t, BehaviorBlock, q.Behavior)
		test.EqOp(t, 1, calls)
	})

	T.Run("serves the unsubscribed limit to a subject nothing entitles", func(t *testing.T) {
		t.Parallel()

		var calls int

		source, err := NewPlanLimitSource(newPlanRegistry(t), limits(), unentitled(&calls))
		must.NoError(t, err)

		q, err := source.QuotaFor(t.Context(), testSubject, testMeter)
		must.NoError(t, err)

		test.EqOp(t, int64(100), q.Limit)
		test.EqOp(t, 1, calls)
	})

	T.Run("serves the unsubscribed limit for a product nobody configured", func(t *testing.T) {
		t.Parallel()

		var (
			calls     int
			counted   int64
			announced int
		)

		source, err := NewPlanLimitSource(newPlanRegistry(t), limits(), entitling("prod_enterprise", &calls),
			WithPlanLimitLogger(countingLogger(&announced)),
			WithPlanLimitTracerProvider(tracingnoop.NewTracerProvider()),
			WithPlanLimitMetricsProvider(countingProvider(&counted)))
		must.NoError(t, err)

		q, err := source.QuotaFor(t.Context(), testSubject, testMeter)
		must.NoError(t, err)

		// Unlimited would let a new tier ship with no limits and nobody notice;
		// an error would take the endpoint down for a customer whose only mistake
		// was buying the plan somebody forgot to configure. So: the unsubscribed
		// limit, counted and said out loud.
		test.EqOp(t, int64(100), q.Limit)
		test.EqOp(t, int64(1), counted)
		test.EqOp(t, 1, announced)
	})

	T.Run("answers an ungated meter without reading anything", func(t *testing.T) {
		t.Parallel()

		var calls int

		source, err := NewPlanLimitSource(newPlanRegistry(t), limits(), entitling(testProduct, &calls))
		must.NoError(t, err)

		// QuotaFor sits on Check's path, whose reason to exist is being cheaper
		// than a durable round trip. A subscription read to reach an answer that
		// is identical for every subject would make the cheap path expensive.
		q, err := source.QuotaFor(t.Context(), testSubject, "daily_exports")
		must.NoError(t, err)

		test.EqOp(t, Unlimited, q.Limit)
		test.EqOp(t, BehaviorAllowOverage, q.Behavior)
		test.EqOp(t, 0, calls)
	})

	T.Run("reads the period off the meter", func(t *testing.T) {
		t.Parallel()

		var calls int

		table := limits()
		table["daily_exports"] = PlanLimits{Behavior: BehaviorWarn, Unsubscribed: 3}

		source, err := NewPlanLimitSource(newPlanRegistry(t), table, entitling(testProduct, &calls))
		must.NoError(t, err)

		// Deriving the period rather than taking it is what makes a mismatch with
		// the meter unrepresentable — the error the enforcer would otherwise raise
		// on the request path, for a mistake made at wiring time.
		monthly, err := source.QuotaFor(t.Context(), testSubject, testMeter)
		must.NoError(t, err)
		test.EqOp(t, PeriodMonth, monthly.Period)

		daily, err := source.QuotaFor(t.Context(), testSubject, "daily_exports")
		must.NoError(t, err)
		test.EqOp(t, PeriodDay, daily.Period)
	})

	T.Run("serves a zero limit as no usage allowed", func(t *testing.T) {
		t.Parallel()

		var calls int

		source, err := NewPlanLimitSource(newPlanRegistry(t), map[string]PlanLimits{
			testMeter: {Behavior: BehaviorBlock, ByProduct: map[string]int64{testProduct: 0}},
		}, entitling(testProduct, &calls))
		must.NoError(t, err)

		q, err := source.QuotaFor(t.Context(), testSubject, testMeter)
		must.NoError(t, err)

		// Zero is a feature switched off on a tier, not a synonym for unlimited.
		test.EqOp(t, int64(0), q.Limit)
	})

	T.Run("reports a meter nobody registered", func(t *testing.T) {
		t.Parallel()

		var calls int

		source, err := NewPlanLimitSource(newPlanRegistry(t), limits(), entitling(testProduct, &calls))
		must.NoError(t, err)

		_, err = source.QuotaFor(t.Context(), testSubject, "nothing_registered")

		test.ErrorIs(t, err, ErrUnknownMeter)
		test.EqOp(t, 0, calls)
	})

	T.Run("reports a failed entitlement lookup", func(t *testing.T) {
		t.Parallel()

		source, err := NewPlanLimitSource(newPlanRegistry(t), limits(),
			EntitlementReaderFunc(func(context.Context, string) (string, bool, error) {
				return "", false, errArbitrary
			}))
		must.NoError(t, err)

		// Not the unsubscribed limit. A lookup that failed says nothing about
		// what the subject bought, and answering it as "nothing" would downgrade
		// every paying customer the moment the subscriptions table is unreachable.
		_, err = source.QuotaFor(t.Context(), testSubject, testMeter)

		test.ErrorIs(t, err, errArbitrary)
	})

	T.Run("passes the subject through to the reader", func(t *testing.T) {
		t.Parallel()

		var seen string

		source, err := NewPlanLimitSource(newPlanRegistry(t), limits(),
			EntitlementReaderFunc(func(_ context.Context, subject string) (string, bool, error) {
				seen = subject

				return testProduct, true, nil
			}))
		must.NoError(t, err)

		_, err = source.QuotaFor(t.Context(), testSubject, testMeter)
		must.NoError(t, err)

		test.EqOp(t, testSubject, seen)
	})

	T.Run("observes what the ladder resolved", func(t *testing.T) {
		t.Parallel()

		var calls int

		source, err := NewPlanLimitSource(newPlanRegistry(t), limits(), entitling(testProduct, &calls))
		must.NoError(t, err)

		obs := observability.NewRecordingObserver()
		source.o11y = obs

		_, err = source.QuotaFor(t.Context(), testSubject, testMeter)
		must.NoError(t, err)

		// The product and the limit together, because a trace showing one
		// without the other cannot answer the question somebody opened it with:
		// whether this subject was enforced against the number their plan says.
		op := obs.ObservedOperationWithKeys(t, productKey, entitledKey, limitKey)

		test.Eq(t, any(testProduct), op.Values[productKey])
		test.Eq(t, any(true), op.Values[entitledKey])
		test.Eq(t, any(int64(5_000)), op.Values[limitKey])
		test.Eq(t, any(string(BehaviorBlock)), op.Values[behaviorKey])
	})

	T.Run("does not share the caller's table", func(t *testing.T) {
		t.Parallel()

		var calls int

		table := limits()

		source, err := NewPlanLimitSource(newPlanRegistry(t), table, entitling(testProduct, &calls))
		must.NoError(t, err)

		// A caller that goes on holding the map it passed cannot change what is
		// being enforced halfway through a request.
		table[testMeter].ByProduct[testProduct] = 1
		table["daily_exports"] = PlanLimits{Behavior: BehaviorBlock}
		delete(table, testMeter)

		q, err := source.QuotaFor(t.Context(), testSubject, testMeter)
		must.NoError(t, err)
		test.EqOp(t, int64(5_000), q.Limit)

		ungated, err := source.QuotaFor(t.Context(), testSubject, "daily_exports")
		must.NoError(t, err)
		test.EqOp(t, Unlimited, ungated.Limit)
	})
}

func TestPlanLimitSource_AgainstAnEnforcer(T *testing.T) {
	T.Parallel()

	// The ladder is only worth anything if what it returns is what gets enforced,
	// so this runs it through the enforcer rather than reading the Quota back.
	newEnv := func(t *testing.T, product string) *enforcerEnv {
		t.Helper()

		store := newSQLiteEnv(t).newStore(t)
		c := newStubClock()

		var calls int

		source, err := NewPlanLimitSource(newTestRegistry(t, BehaviorBlock, 10), map[string]PlanLimits{
			testMeter: {
				ByProduct:    map[string]int64{testProduct: 3},
				Behavior:     BehaviorBlock,
				Unsubscribed: 1,
			},
		}, entitling(product, &calls))
		must.NoError(t, err)

		enforcer, err := NewQuotaEnforcer(t.Context(), &EnforcerConfig{}, store,
			newTestRegistry(t, BehaviorBlock, 10),
			WithEnforcerClock(c), WithEnforcerQuotaSource(source))
		must.NoError(t, err)

		return &enforcerEnv{enforcer: enforcer, store: store, clock: c}
	}

	T.Run("the product's limit is the one enforced", func(t *testing.T) {
		t.Parallel()

		env := newEnv(t, testProduct)

		// The registry's own quota says 10; the plan says 3, and the plan wins.
		decision, err := env.enforcer.Check(t.Context(), testSubject, testMeter, 3)
		must.NoError(t, err)
		test.True(t, decision.Allowed)
		test.EqOp(t, int64(3), decision.Limit)

		decision, err = env.enforcer.Check(t.Context(), testSubject, testMeter, 4)
		must.NoError(t, err)
		test.False(t, decision.Allowed)
	})

	T.Run("an unsubscribed subject is enforced at the unsubscribed limit", func(t *testing.T) {
		t.Parallel()

		env := newEnv(t, "prod_enterprise")

		decision, err := env.enforcer.Consume(t.Context(), testSubject, testMeter, 2)
		must.NoError(t, err)

		test.False(t, decision.Allowed)
		test.EqOp(t, int64(1), decision.Limit)
	})
}

// TestPlanLimitSource_QuotaFor_concurrent walks the ladder from many goroutines
// at once so the race detector has something to say about the limits table.
//
// Nothing writes to that table after construction today, so this asserts a guard
// rather than reproducing a failure — which is the point of having it. The day
// somebody gives this type a writer, this is the test that refuses to let the
// write land unsynchronized.
func TestPlanLimitSource_QuotaFor_concurrent(T *testing.T) {
	T.Parallel()

	const (
		goroutines        = 16
		iterations        = 64
		entitledSubject   = "subject_entitled"
		unentitledSubject = "subject_unentitled"
		entitledLimit     = int64(5_000)
		unsubscribedLimit = int64(100)
	)

	source, err := NewPlanLimitSource(
		newPlanRegistry(T),
		map[string]PlanLimits{
			testMeter: {
				ByProduct:    map[string]int64{testProduct: entitledLimit},
				Behavior:     BehaviorBlock,
				Unsubscribed: unsubscribedLimit,
			},
		},
		EntitlementReaderFunc(func(_ context.Context, subject string) (string, bool, error) {
			if subject == entitledSubject {
				return testProduct, true, nil
			}

			return "", false, nil
		}),
	)
	must.NoError(T, err)

	ctx := T.Context()

	var wg sync.WaitGroup

	for i := range goroutines {
		wg.Go(func() {
			// Both sides of the ladder run concurrently, not just the branch that
			// reads ByProduct: half these goroutines resolve through the product
			// lookup and half fall to the unsubscribed limit.
			subject, want := entitledSubject, entitledLimit
			if i%2 == 1 {
				subject, want = unentitledSubject, unsubscribedLimit
			}

			for range iterations {
				q, quotaErr := source.QuotaFor(ctx, subject, testMeter)
				if quotaErr != nil {
					T.Error(quotaErr)

					return
				}

				if q.Limit != want {
					T.Errorf("subject %q: limit %d, want %d", subject, q.Limit, want)

					return
				}

				// The meter the table does not name short circuits before the
				// entitlement read, which is the one path that touches the table
				// and nothing else.
				if _, quotaErr = source.QuotaFor(ctx, subject, "daily_exports"); quotaErr != nil {
					T.Error(quotaErr)

					return
				}
			}
		})
	}

	wg.Wait()
}

func TestEntitlementReaderFunc(T *testing.T) {
	T.Parallel()

	var seen string

	reader := EntitlementReaderFunc(func(_ context.Context, subject string) (string, bool, error) {
		seen = subject

		return testProduct, true, nil
	})

	product, entitled, err := reader.EntitlingProduct(T.Context(), testSubject)
	must.NoError(T, err)

	test.EqOp(T, testSubject, seen)
	test.EqOp(T, testProduct, product)
	test.True(T, entitled)
}

// countingProvider builds a metrics.Provider whose counters tally into n.
func countingProvider(n *int64) metrics.Provider {
	noop := metrics.EnsureMetricsProvider(nil)

	return &metricsmock.ProviderMock{
		NewInt64CounterFunc: func(name string, opts ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			delegate, err := noop.NewInt64Counter(name, opts...)
			if err != nil {
				return nil, err
			}

			return &countingCounter{Int64Counter: delegate, total: n}, nil
		},
	}
}

// countingCounter tallies what it is asked to add.
type countingCounter struct {
	metrics.Int64Counter

	total *int64
}

func (c *countingCounter) Add(ctx context.Context, incr int64, opts ...metric.AddOption) {
	*c.total += incr

	c.Int64Counter.Add(ctx, incr, opts...)
}

// countingLogger builds a logger that tallies the Info lines written through it.
//
// Every derivation returns the same logger, because the line under test is
// written through one the operation derived — WithSpan at the operation's
// construction, WithValue for each field set on it — and a derivation that lost
// the counter would count nothing and look like a line that was never written.
func countingLogger(n *int) logging.Logger {
	return &tallyingLogger{Logger: loggingnoop.NewLogger(), infos: n}
}

// tallyingLogger counts Info lines. Built on the noop so the methods nothing
// here calls stay silent rather than needing a body each.
type tallyingLogger struct {
	logging.Logger

	infos *int
}

func (l *tallyingLogger) Info(string) { *l.infos++ }

func (l *tallyingLogger) WithName(string) logging.Logger           { return l }
func (l *tallyingLogger) WithValue(string, any) logging.Logger     { return l }
func (l *tallyingLogger) WithValues(map[string]any) logging.Logger { return l }
func (l *tallyingLogger) WithSpan(trace.Span) logging.Logger       { return l }
func (l *tallyingLogger) WithError(error) logging.Logger           { return l }
func (l *tallyingLogger) Clone() logging.Logger                    { return l }

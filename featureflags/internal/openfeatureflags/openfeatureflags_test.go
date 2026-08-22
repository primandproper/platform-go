package openfeatureflags

import (
	"testing"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	circuitbreakingnoop "github.com/primandproper/platform-go/v13/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v13/featureflags"
	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"github.com/open-feature/go-sdk/openfeature"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newEvaluator binds an Evaluator to a freshly-registered domain.
//
// The evaluation paths against a real provider are exercised by the two
// packages that embed this one; what is worth pinning here is the part they
// share and cannot easily reach — the breaker protocol and the detach.
func newEvaluator(t *testing.T, cb circuitbreaking.CircuitBreaker) *Evaluator {
	t.Helper()

	domain := "openfeatureflags_test_" + identifiers.New()

	must.NoError(t, openfeature.SetNamedProviderAndWait(domain, openfeature.NoopProvider{}))
	t.Cleanup(func() { _ = openfeature.SetNamedProvider(domain, openfeature.NoopProvider{}) })

	set, err := metrics.NewOperationSet(nil, "openfeatureflags_test")
	must.NoError(t, err)

	notFound, err := metrics.EnsureMetricsProvider(nil).NewInt64Counter("flags_not_found")
	must.NoError(t, err)

	if cb == nil {
		cb = circuitbreakingnoop.NewCircuitBreaker()
	}

	return &Evaluator{
		O11y:            observability.NewObserverForTest("openfeatureflags_test"),
		Client:          openfeature.NewClient(domain),
		CircuitBreaker:  cb,
		Domain:          domain,
		EvalCounter:     set.Requests,
		ErrorCounter:    set.Errors,
		NotFoundCounter: notFound,
		LatencyHist:     set.Latency,
	}
}

func TestContext(T *testing.T) {
	T.Parallel()

	T.Run("carries the targeting key and attributes across the boundary", func(t *testing.T) {
		t.Parallel()

		got := Context(featureflags.EvaluationContext{
			TargetingKey: "user-1",
			Attributes:   map[string]any{"plan": "pro"},
		})

		test.EqOp(t, "user-1", got.TargetingKey())
		test.EqOp(t, "pro", got.Attributes()["plan"])
	})
}

func TestEvaluator_Detach(T *testing.T) {
	T.Parallel()

	// The registry has no removal API, so without this every reload cycle left
	// a registration holding a client that had just been closed.
	T.Run("replaces the registration with the no-op provider", func(t *testing.T) {
		t.Parallel()

		e := newEvaluator(t, nil)

		must.NoError(t, e.Detach())
	})
}

// errBreaker refuses every attempt, which is what an open circuit does.
type errBreaker struct{ circuitbreaking.CircuitBreaker }

func (errBreaker) CanProceed() bool { return false }

func TestEvaluator_breaker(T *testing.T) {
	T.Parallel()

	// An open breaker is answered before the SDK is touched, and the caller gets
	// its default rather than a zero value it did not ask for.
	T.Run("an open circuit returns the default without evaluating", func(t *testing.T) {
		t.Parallel()

		e := newEvaluator(t, errBreaker{circuitbreakingnoop.NewCircuitBreaker()})
		evalCtx := featureflags.EvaluationContext{TargetingKey: "user-1"}

		allowed, err := e.CanUseFeature(t.Context(), "flag", evalCtx)
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		test.False(t, allowed)

		str, err := e.GetStringValue(t.Context(), "flag", "fallback", evalCtx)
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		test.EqOp(t, "fallback", str)

		i, err := e.GetInt64Value(t.Context(), "flag", 7, evalCtx)
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		test.EqOp(t, int64(7), i)

		f, err := e.GetFloat64Value(t.Context(), "flag", 1.5, evalCtx)
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		test.EqOp(t, 1.5, f)

		obj, err := e.GetObjectValue(t.Context(), "flag", "fallback", evalCtx)
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		test.EqOp(t, "fallback", obj)
	})
}

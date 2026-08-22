// Package openfeatureflags is the flag evaluation both of this module's
// OpenFeature-backed providers do.
//
// The PostHog and LaunchDarkly managers differ in exactly two places: how they
// build a vendor client and register it as an OpenFeature provider, and how they
// close it. Everything between — the context conversion, the breaker protocol,
// the not-found classification, and the five evaluation methods — goes through
// *openfeature.Client and is therefore the same code, which is why it had been
// written twice.
//
// That makes this a different case from the provider families this module
// deliberately leaves duplicated (see llm/doc.go). Those are two translations to
// two different vendor APIs, where the shape is shared and the details are the
// point. This is one implementation against one seam, copied — and the copies
// had already begun to differ in the error descriptions they returned.
package openfeatureflags

import (
	"context"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/featureflags"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"github.com/open-feature/go-sdk/openfeature"
)

// featureKey names the flag on every span and log line here.
const featureKey = "feature"

// Evaluator evaluates flags through an OpenFeature client, under a circuit
// breaker, with the instrumentation both providers record.
//
// It is embedded by the provider types rather than held as a field, so their
// exported surfaces stay the flag-manager interface a caller expects while the
// implementation lives once.
type Evaluator struct {
	// O11y is the provider's observer. Exported because the embedding provider
	// builds it — the instrumentation scope is the provider's name, not this
	// package's.
	O11y observability.Observer

	// CircuitBreaker guards every evaluation. Required: a nil one panics on
	// first use rather than silently evaluating unguarded.
	CircuitBreaker circuitbreaking.CircuitBreaker

	// EvalCounter counts evaluations that answered.
	EvalCounter metrics.Int64Counter

	// ErrorCounter counts evaluations that failed for a reason the breaker
	// should hear about.
	ErrorCounter metrics.Int64Counter

	// NotFoundCounter counts evaluations of a flag the provider does not have,
	// which is a correct negative answer rather than a failure.
	NotFoundCounter metrics.Int64Counter

	// LatencyHist records how long an evaluation took, in milliseconds.
	LatencyHist metrics.Float64Histogram

	// Client is the OpenFeature client bound to the provider's domain.
	Client *openfeature.Client

	// Domain is the name the provider registered under, and is what Close
	// detaches.
	Domain string
}

// Context converts a featureflags.EvaluationContext into the SDK's own
// representation. It is the only place a provider crosses the boundary between
// the platform-owned type and the OpenFeature type.
func Context(evalCtx featureflags.EvaluationContext) openfeature.EvaluationContext {
	return openfeature.NewEvaluationContext(evalCtx.TargetingKey, evalCtx.Attributes)
}

// begin opens the span and log context every evaluation here shares.
func (e *Evaluator) begin(ctx context.Context, feature string, evalCtx featureflags.EvaluationContext) (context.Context, observability.Operation) {
	return e.O11y.Begin(ctx,
		observability.WithValue(keys.UserIDKey, evalCtx.TargetingKey),
		observability.WithValue(featureKey, feature),
	)
}

// evaluationError classifies a failed evaluation into the error the caller sees
// and the verdict the circuit breaker hears.
//
// A flag the provider resolved as absent scores a success. The breaker exists to
// give a failing service breathing room, and answering "no such flag" is not
// what a failing service does — it is a correct negative answer. Counting it as
// a failure is what let a flag name shipped ahead of its flag open a breaker
// that every other flag in the process shares.
//
// Everything else is a failure the breaker should hear about. That includes the
// SDK's pre-evaluation short circuits, which return empty resolution details and
// so arrive here with an empty code: an unready or fatally broken provider is
// exactly what the breaker is for.
func (e *Evaluator) evaluationError(ctx context.Context, feature string, code openfeature.ErrorCode, err error) error {
	if code == openfeature.FlagNotFoundCode {
		e.NotFoundCounter.Add(ctx, 1)
		e.CircuitBreaker.Succeeded()

		return platformerrors.Wrapf(featureflags.ErrFlagNotFound, "feature flag %q", feature)
	}

	e.ErrorCounter.Add(ctx, 1)
	e.CircuitBreaker.Failed()

	return err
}

// evaluate runs one typed evaluation under the breaker, the span, and the
// latency histogram, returning defaultValue for anything that did not answer.
//
// resolve is the SDK call for the type, taken as a function because the
// OpenFeature client's five methods have five signatures and Go cannot express
// "whichever one matches T" without one. errCode reads the resolution details
// the SDK returns alongside its error, which is where the not-found
// classification comes from.
func evaluate[T any](
	e *Evaluator,
	ctx context.Context,
	feature string,
	defaultValue T,
	evalCtx featureflags.EvaluationContext,
	description string,
	resolve func(ctx context.Context) (value T, code openfeature.ErrorCode, err error),
) (T, error) {
	ctx, op := e.begin(ctx, feature, evalCtx)
	defer op.End()

	if !e.CircuitBreaker.CanProceed() {
		return defaultValue, circuitbreaking.ErrCircuitBroken
	}

	recordLatency := op.Time(ctx, nil, e.LatencyHist)
	value, code, err := resolve(ctx)
	recordLatency()

	if err != nil {
		return defaultValue, op.Error(e.evaluationError(ctx, feature, code, err), "%s", description)
	}

	e.EvalCounter.Add(ctx, 1)
	e.CircuitBreaker.Succeeded()

	return value, nil
}

// CanUseFeature returns whether the supplied evaluation context is permitted to
// use the named feature.
//
// This is the one method here that never reports featureflags.ErrFlagNotFound.
// A provider's API generally answers false for a boolean flag it does not know,
// which is indistinguishable from a flag that exists and is off, so the
// OpenFeature provider skips the found check for booleans rather than call every
// false a not-found. A caller therefore sees (false, nil) for a flag nobody has
// created — the same inert answer this package's distinction is there to
// produce, reached one layer lower. The four typed getters do report it, because
// a missing key is distinguishable once the flag has a value to be missing.
func (e *Evaluator) CanUseFeature(ctx context.Context, feature string, evalCtx featureflags.EvaluationContext) (bool, error) {
	return evaluate(e, ctx, feature, false, evalCtx, "checking feature flag eligibility",
		func(ctx context.Context) (bool, openfeature.ErrorCode, error) {
			details, err := e.Client.BooleanValueDetails(ctx, feature, false, Context(evalCtx))

			return details.Value, details.ErrorCode, err
		})
}

// GetStringValue returns the string value of a feature flag, falling back to
// defaultValue on error.
func (e *Evaluator) GetStringValue(ctx context.Context, feature, defaultValue string, evalCtx featureflags.EvaluationContext) (string, error) {
	return evaluate(e, ctx, feature, defaultValue, evalCtx, "checking feature flag string variation",
		func(ctx context.Context) (string, openfeature.ErrorCode, error) {
			details, err := e.Client.StringValueDetails(ctx, feature, defaultValue, Context(evalCtx))

			return details.Value, details.ErrorCode, err
		})
}

// GetInt64Value returns the integer value of a feature flag, falling back to
// defaultValue on error.
func (e *Evaluator) GetInt64Value(ctx context.Context, feature string, defaultValue int64, evalCtx featureflags.EvaluationContext) (int64, error) {
	return evaluate(e, ctx, feature, defaultValue, evalCtx, "checking feature flag integer variation",
		func(ctx context.Context) (int64, openfeature.ErrorCode, error) {
			details, err := e.Client.IntValueDetails(ctx, feature, defaultValue, Context(evalCtx))

			return details.Value, details.ErrorCode, err
		})
}

// GetFloat64Value returns the float value of a feature flag, falling back to
// defaultValue on error.
func (e *Evaluator) GetFloat64Value(ctx context.Context, feature string, defaultValue float64, evalCtx featureflags.EvaluationContext) (float64, error) {
	return evaluate(e, ctx, feature, defaultValue, evalCtx, "checking feature flag float variation",
		func(ctx context.Context) (float64, openfeature.ErrorCode, error) {
			details, err := e.Client.FloatValueDetails(ctx, feature, defaultValue, Context(evalCtx))

			return details.Value, details.ErrorCode, err
		})
}

// GetObjectValue returns the object (JSON) value of a feature flag, falling back
// to defaultValue on error.
func (e *Evaluator) GetObjectValue(ctx context.Context, feature string, defaultValue any, evalCtx featureflags.EvaluationContext) (any, error) {
	return evaluate(e, ctx, feature, defaultValue, evalCtx, "checking feature flag object variation",
		func(ctx context.Context) (any, openfeature.ErrorCode, error) {
			details, err := e.Client.ObjectValueDetails(ctx, feature, defaultValue, Context(evalCtx))

			return details.Value, details.ErrorCode, err
		})
}

// Detach replaces this evaluator's registration with the no-op provider,
// releasing the vendor client it held.
//
// Each construction registers a uniquely-named provider in OpenFeature's
// process-global registry, which has no removal API — so without this, every
// reload cycle left another registration holding a reference to a client that
// had just been closed, and the process accumulated them until it exited. The
// (small, clientless) map entry itself is not removable and is left behind.
//
// It is the provider's Close that calls this, because closing the vendor client
// is the provider's own business.
func (e *Evaluator) Detach() error {
	if err := openfeature.SetNamedProvider(e.Domain, openfeature.NoopProvider{}); err != nil {
		return platformerrors.Wrap(err, "detaching OpenFeature provider")
	}

	return nil
}

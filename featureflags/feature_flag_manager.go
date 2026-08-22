package featureflags

import (
	"context"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// ErrFlagNotFound reports that the provider resolved the evaluation and the flag
// does not exist.
//
// It is deliberately distinct from an unreachable or erroring provider. A flag
// nobody has created says nothing about whether the service that would know about
// it is healthy, and conflating the two lets the normal shape of a rollout — ship
// the code that reads the flag, then create the flag — score failures against a
// circuit breaker that every other flag in the process shares. Implementations
// therefore report this rather than a generic error, and do not count it against
// the breaker.
//
// Implementations wrap it with the flag key, so match it with errors.Is. The value
// returned alongside it is the caller's default (or false for CanUseFeature) — the
// same value an errored evaluation returns, because a caller with nothing better to
// do with either answer should not have to tell them apart.
var ErrFlagNotFound = platformerrors.New("feature flag not found")

// EvaluationContext carries targeting information for a single flag evaluation.
// TargetingKey is the primary subject identifier — typically a user ID, but it can
// be any stable string a provider's targeting rules can match against. Attributes
// carry arbitrary additional signals (tenant, plan tier, country, beta cohort,
// region, etc.) that provider rules can target on.
//
// This type is intentionally repo-owned rather than aliasing the OpenFeature SDK's
// EvaluationContext: it keeps the openfeature import out of caller code, lets the
// noop and mock implementations satisfy the signature without importing openfeature,
// and leaves room to swap providers later. Each provider converts to its own
// representation internally.
type EvaluationContext struct {
	Attributes   map[string]any
	TargetingKey string
}

type (
	// FeatureFlagManager evaluates feature flags. Implementations must be safe for
	// concurrent use.
	//
	// Every method answers one of three ways: a value the provider decided on, a
	// default alongside ErrFlagNotFound because no such flag exists, or a default
	// alongside some other error because the provider could not answer. A caller
	// that needs to tell "the flag is off" from "the flag was never created"
	// matches ErrFlagNotFound; one that treats every unusable answer alike can
	// take the returned value and ignore which it was.
	//
	// The middle answer requires the backend to be able to tell a missing flag
	// from a present one, which is not universal — see the posthog
	// implementation's CanUseFeature. Where it cannot, a missing flag is reported
	// as its default with no error, which is the same inert answer by a shorter
	// route. Nothing here promises an unresolvable flag will surface as an error;
	// what it promises is that when one does, it says which kind.
	FeatureFlagManager interface {
		// CanUseFeature evaluates a boolean flag. Returns false on error, and false
		// with ErrFlagNotFound when the provider reports no such flag.
		CanUseFeature(ctx context.Context, feature string, evalCtx EvaluationContext) (bool, error)
		// GetStringValue evaluates a string-typed flag, returning defaultValue on error
		// and defaultValue with ErrFlagNotFound when no such flag exists.
		GetStringValue(ctx context.Context, feature, defaultValue string, evalCtx EvaluationContext) (string, error)
		// GetInt64Value evaluates an int64-typed flag, returning defaultValue on error
		// and defaultValue with ErrFlagNotFound when no such flag exists.
		GetInt64Value(ctx context.Context, feature string, defaultValue int64, evalCtx EvaluationContext) (int64, error)
		// GetFloat64Value evaluates a float64-typed flag, returning defaultValue on error
		// and defaultValue with ErrFlagNotFound when no such flag exists.
		GetFloat64Value(ctx context.Context, feature string, defaultValue float64, evalCtx EvaluationContext) (float64, error)
		// GetObjectValue evaluates an object-typed (JSON) flag, returning defaultValue
		// on error and defaultValue with ErrFlagNotFound when no such flag exists. The
		// concrete type of the returned value is provider-specific — callers typically
		// type-assert or json.Marshal it back into a known struct.
		GetObjectValue(ctx context.Context, feature string, defaultValue any, evalCtx EvaluationContext) (any, error)
		// Close releases any backend resources held by the FeatureFlagManager.
		Close() error
	}
)

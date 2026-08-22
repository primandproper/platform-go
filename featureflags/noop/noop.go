// Package noop is the featureflags.FeatureFlagManager for a process with no
// flag system: each typed getter returns the default value it was handed, and
// CanUseFeature returns false.
//
// That asymmetry is the part to hold onto. The typed getters are given a
// default and give it back; CanUseFeature takes no default, so "off" is the
// only answer available, and every boolean-gated feature stays dark. A rollout
// that consists of flipping one of those on will not flip, and will not say why.
//
// Nothing here reports featureflags.ErrFlagNotFound, though every flag it is
// asked about is one it does not have — see NewFeatureFlagManager for why an
// absent provider is a resolved answer rather than a missing flag.
// featureflags/config builds it for the "noop" provider name, which its
// validation requires be spelled out.
package noop

import (
	"context"

	"github.com/primandproper/platform-go/v13/featureflags"
)

var _ featureflags.FeatureFlagManager = (*FeatureFlagManager)(nil)

// FeatureFlagManager is a no-op FeatureFlagManager.
type FeatureFlagManager struct{}

// NewFeatureFlagManager returns a FeatureFlagManager that always returns the
// supplied default values (or zero values for the boolean variant).
//
// Nothing here reports featureflags.ErrFlagNotFound, though every flag it is
// asked about is, literally, one it does not have. Selecting this implementation
// is a decision rather than an unknown: it says this process has no flag system
// and every flag takes its default, which is a resolved answer and is reported as
// one. A caller distinguishing a missing flag from a working provider is asking
// about a provider, and there is none to ask about.
func NewFeatureFlagManager() *FeatureFlagManager {
	return &FeatureFlagManager{}
}

// CanUseFeature implements the FeatureFlagManager interface.
func (*FeatureFlagManager) CanUseFeature(_ context.Context, _ string, _ featureflags.EvaluationContext) (bool, error) {
	return false, nil
}

// GetStringValue implements the FeatureFlagManager interface.
func (*FeatureFlagManager) GetStringValue(_ context.Context, _, defaultValue string, _ featureflags.EvaluationContext) (string, error) {
	return defaultValue, nil
}

// GetInt64Value implements the FeatureFlagManager interface.
func (*FeatureFlagManager) GetInt64Value(_ context.Context, _ string, defaultValue int64, _ featureflags.EvaluationContext) (int64, error) {
	return defaultValue, nil
}

// GetFloat64Value implements the FeatureFlagManager interface.
func (*FeatureFlagManager) GetFloat64Value(_ context.Context, _ string, defaultValue float64, _ featureflags.EvaluationContext) (float64, error) {
	return defaultValue, nil
}

// GetObjectValue implements the FeatureFlagManager interface.
func (*FeatureFlagManager) GetObjectValue(_ context.Context, _ string, defaultValue any, _ featureflags.EvaluationContext) (any, error) {
	return defaultValue, nil
}

// Close implements the FeatureFlagManager interface.
func (*FeatureFlagManager) Close() error {
	return nil
}

package noop

import (
	"context"

	"github.com/primandproper/platform-go/v2/featureflags"
)

var _ featureflags.FeatureFlagManager = (*featureFlagManager)(nil)

// featureFlagManager is a no-op FeatureFlagManager.
type featureFlagManager struct{}

// NewFeatureFlagManager returns a FeatureFlagManager that always returns the
// supplied default values (or zero values for the boolean variant).
func NewFeatureFlagManager() featureflags.FeatureFlagManager {
	return &featureFlagManager{}
}

// CanUseFeature implements the FeatureFlagManager interface.
func (*featureFlagManager) CanUseFeature(_ context.Context, _ string, _ featureflags.EvaluationContext) (bool, error) {
	return false, nil
}

// GetStringValue implements the FeatureFlagManager interface.
func (*featureFlagManager) GetStringValue(_ context.Context, _, defaultValue string, _ featureflags.EvaluationContext) (string, error) {
	return defaultValue, nil
}

// GetInt64Value implements the FeatureFlagManager interface.
func (*featureFlagManager) GetInt64Value(_ context.Context, _ string, defaultValue int64, _ featureflags.EvaluationContext) (int64, error) {
	return defaultValue, nil
}

// GetFloat64Value implements the FeatureFlagManager interface.
func (*featureFlagManager) GetFloat64Value(_ context.Context, _ string, defaultValue float64, _ featureflags.EvaluationContext) (float64, error) {
	return defaultValue, nil
}

// GetObjectValue implements the FeatureFlagManager interface.
func (*featureFlagManager) GetObjectValue(_ context.Context, _ string, defaultValue any, _ featureflags.EvaluationContext) (any, error) {
	return defaultValue, nil
}

// Close implements the FeatureFlagManager interface.
func (*featureFlagManager) Close() error {
	return nil
}

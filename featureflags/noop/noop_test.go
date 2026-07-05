package noop

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v4/featureflags"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func evalCtx() featureflags.EvaluationContext {
	return featureflags.EvaluationContext{TargetingKey: "user-id"}
}

func TestNewFeatureFlagManager(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		mgr := NewFeatureFlagManager()
		must.NotNil(t, mgr)
	})
}

func TestFeatureFlagManager_CanUseFeature(T *testing.T) {
	T.Parallel()

	T.Run("returns false", func(t *testing.T) {
		t.Parallel()

		result, err := NewFeatureFlagManager().CanUseFeature(context.Background(), "some-feature", evalCtx())
		test.NoError(t, err)
		test.False(t, result)
	})
}

func TestFeatureFlagManager_GetStringValue(T *testing.T) {
	T.Parallel()

	T.Run("returns default", func(t *testing.T) {
		t.Parallel()

		result, err := NewFeatureFlagManager().GetStringValue(context.Background(), "some-feature", "fallback", evalCtx())
		test.NoError(t, err)
		test.EqOp(t, "fallback", result)
	})
}

func TestFeatureFlagManager_GetInt64Value(T *testing.T) {
	T.Parallel()

	T.Run("returns default", func(t *testing.T) {
		t.Parallel()

		result, err := NewFeatureFlagManager().GetInt64Value(context.Background(), "some-feature", int64(42), evalCtx())
		test.NoError(t, err)
		test.EqOp(t, int64(42), result)
	})
}

func TestFeatureFlagManager_GetFloat64Value(T *testing.T) {
	T.Parallel()

	T.Run("returns default", func(t *testing.T) {
		t.Parallel()

		result, err := NewFeatureFlagManager().GetFloat64Value(context.Background(), "some-feature", 3.14, evalCtx())
		test.NoError(t, err)
		test.InDelta(t, 3.14, result, 1e-9)
	})
}

func TestFeatureFlagManager_GetObjectValue(T *testing.T) {
	T.Parallel()

	T.Run("returns default", func(t *testing.T) {
		t.Parallel()

		def := map[string]any{"k": "v"}
		result, err := NewFeatureFlagManager().GetObjectValue(context.Background(), "some-feature", def, evalCtx())
		test.NoError(t, err)
		test.Eq[any](t, def, result)
	})
}

func TestFeatureFlagManager_Close(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		err := NewFeatureFlagManager().Close()
		test.NoError(t, err)
	})
}

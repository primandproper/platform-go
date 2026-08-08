package entitlements

import (
	"testing"
	"time"

	"github.com/shoenig/test"
)

func TestCheckerConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("fills unset knobs", func(t *testing.T) {
		t.Parallel()

		cfg := &CheckerConfig{}
		cfg.EnsureDefaults()

		test.EqOp(t, DefaultCacheTTL, cfg.CacheTTL)
		test.EqOp(t, DefaultCachePrefix, cfg.CachePrefix)
	})

	T.Run("leaves set knobs alone", func(t *testing.T) {
		t.Parallel()

		cfg := &CheckerConfig{CacheTTL: time.Minute, CachePrefix: "ent:"}
		cfg.EnsureDefaults()

		test.EqOp(t, time.Minute, cfg.CacheTTL)
		test.EqOp(t, "ent:", cfg.CachePrefix)
	})

	T.Run("a non-positive TTL falls back to the default", func(t *testing.T) {
		t.Parallel()

		cfg := &CheckerConfig{CacheTTL: -time.Second}
		cfg.EnsureDefaults()

		test.EqOp(t, DefaultCacheTTL, cfg.CacheTTL)
	})
}

func TestCheckerConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("accepts a defaulted config", func(t *testing.T) {
		t.Parallel()

		cfg := &CheckerConfig{}
		cfg.EnsureDefaults()

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("rejects an unset TTL", func(t *testing.T) {
		t.Parallel()

		test.Error(t, (&CheckerConfig{}).ValidateWithContext(t.Context()))
	})

	T.Run("rejects a TTL past the maximum", func(t *testing.T) {
		t.Parallel()

		// Past this the cache is not accelerating the plan lookup, it is deciding
		// entitlements.
		cfg := &CheckerConfig{CacheTTL: MaxCacheTTL + time.Second}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("accepts the maximum exactly", func(t *testing.T) {
		t.Parallel()

		cfg := &CheckerConfig{CacheTTL: MaxCacheTTL}

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("does not validate the fallback plan", func(t *testing.T) {
		t.Parallel()

		// Whether it names a real plan is a question only the Catalog can answer,
		// and NewPlanChecker asks it there.
		cfg := &CheckerConfig{CacheTTL: time.Second, FallbackPlan: "not_a_plan"}

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestNewStaticPlanSource(T *testing.T) {
	T.Parallel()

	T.Run("answers the same plan for every account", func(t *testing.T) {
		t.Parallel()

		src := NewStaticPlanSource("pro")

		for _, account := range []string{"a", "b", ""} {
			plan, err := src.PlanFor(t.Context(), account)

			test.NoError(t, err)
			test.EqOp(t, "pro", plan)
		}
	})

	T.Run("an empty plan is no plan", func(t *testing.T) {
		t.Parallel()

		_, err := NewStaticPlanSource("").PlanFor(t.Context(), testAccount)

		test.ErrorIs(t, err, ErrNoPlan)
	})
}

package ratelimitingcfg

import (
	"testing"

	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/ratelimiting"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterRateLimiter(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[metrics.Provider](i, nil)
		do.ProvideValue(i, &Config{})

		RegisterRateLimiter(i)

		limiter, err := do.Invoke[ratelimiting.RateLimiter](i)
		must.NoError(t, err)
		test.NotNil(t, limiter)
	})
}

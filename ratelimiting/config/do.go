package ratelimitingcfg

import (
	"github.com/primandproper/platform-go/v5/observability/metrics"
	"github.com/primandproper/platform-go/v5/ratelimiting"

	"github.com/samber/do/v2"
)

// RegisterRateLimiter registers a RateLimiter with the injector.
func RegisterRateLimiter(i do.Injector) {
	do.Provide[ratelimiting.RateLimiter](i, func(i do.Injector) (ratelimiting.RateLimiter, error) {
		return NewRateLimiter(do.MustInvoke[*Config](i), do.MustInvoke[metrics.Provider](i))
	})
}

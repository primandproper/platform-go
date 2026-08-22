package ratelimitingcfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/ratelimiting"

	"github.com/samber/do/v2"
)

// RegisterRateLimiter registers a RateLimiter with the injector.
//
// The memory provider owns a goroutine — the sweep that reclaims the limiters
// of keys that have stopped arriving — and the injector will not stop it: do
// recognizes a Shutdown method, and this module's background components spell
// that Close. Close it from the same place you shut the rest of them down,
// after ingress is gone.
func RegisterRateLimiter(i do.Injector) {
	do.Provide(i, func(i do.Injector) (ratelimiting.RateLimiter, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewRateLimiter(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			WithPillars(pillars),
		)
	})
}

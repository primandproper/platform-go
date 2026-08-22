// Package cookiescfg registers a cookies.Manager with a do injector.
//
// Unlike its sibling config subpackages it declares no Config and no Option of
// its own: there is one cookie manager rather than a provider to select, and
// cookies.Config lives beside it in the cookies package. What remains here is
// the wiring — resolving the pillars an injector may or may not hold — which is
// the only part a leaf package cannot do for itself.
package cookiescfg

import (
	"github.com/primandproper/platform-go/v13/cookies"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterCookieManager registers a cookies.Manager with the injector.
func RegisterCookieManager(i do.Injector) {
	do.Provide(i, func(i do.Injector) (cookies.Manager, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		manager, err := cookies.NewCookieManager(
			do.MustInvoke[*cookies.Config](i),
			cookies.WithLogger(pillars.Logger),
			cookies.WithTracerProvider(pillars.TracerProvider),
		)
		if err != nil {
			return nil, err
		}

		return manager, nil
	})
}

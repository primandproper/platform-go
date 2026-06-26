package cookiescfg

import (
	"github.com/primandproper/platform-go/cookies"
	"github.com/primandproper/platform-go/observability/tracing"

	"github.com/samber/do/v2"
)

// RegisterCookieManager registers a cookies.Manager with the injector.
func RegisterCookieManager(i do.Injector) {
	do.Provide[cookies.Manager](i, func(i do.Injector) (cookies.Manager, error) {
		return cookies.NewCookieManager(
			do.MustInvoke[*cookies.Config](i),
			do.MustInvoke[tracing.TracerProvider](i),
		)
	})
}

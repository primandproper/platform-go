package routingcfg

import (
	"github.com/primandproper/platform-go/v4/routing"

	"github.com/samber/do/v2"
)

// RegisterRouteParamManager registers a routing.RouteParamManager with the injector.
func RegisterRouteParamManager(i do.Injector) {
	do.Provide[routing.RouteParamManager](i, func(i do.Injector) (routing.RouteParamManager, error) {
		return NewRouteParamManager(do.MustInvoke[*Config](i))
	})
}

package routingcfg

import (
	"testing"

	"github.com/primandproper/platform-go/routing"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterRouteParamManager(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, &Config{
			Provider: ProviderChi,
		})

		RegisterRouteParamManager(i)

		manager, err := do.Invoke[routing.RouteParamManager](i)
		must.NoError(t, err)
		test.NotNil(t, manager)
	})
}

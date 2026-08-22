package healthcheck

import (
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterRegistry registers both *CheckerRegistry and Registry with the injector.
func RegisterRegistry(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*CheckerRegistry, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewRegistry(WithPillars(pillars))
	})
	do.Provide(i, func(i do.Injector) (Registry, error) {
		return do.MustInvoke[*CheckerRegistry](i), nil
	})
}

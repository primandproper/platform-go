package analyticscfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/analytics"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterEventReporter registers an analytics.EventReporter with the injector.
func RegisterEventReporter(i do.Injector) {
	do.Provide(i, func(i do.Injector) (analytics.EventReporter, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewEventReporter(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			WithPillars(pillars),
		)
	})
}

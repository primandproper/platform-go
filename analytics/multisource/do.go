package multisource

import (
	"context"

	analyticscfg "github.com/primandproper/platform-go/v13/analytics/config"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterMultiSourceEventReporter registers a *MultiSourceEventReporter with the injector.
// Prerequisite: map[string]*analyticscfg.SourceConfig must be registered in the injector.
func RegisterMultiSourceEventReporter(i do.Injector) {
	do.Provide(i, func(i do.Injector) (*MultiSourceEventReporter, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewMultiSourceEventReporterFromConfig(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[map[string]*analyticscfg.SourceConfig](i),
			WithPillars(pillars),
		)
	})
}

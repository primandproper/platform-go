package multisource

import (
	"context"

	analyticscfg "github.com/primandproper/platform-go/analytics/config"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"

	"github.com/samber/do/v2"
)

// RegisterMultiSourceEventReporter registers a *MultiSourceEventReporter with the injector.
// Prerequisite: map[string]*analyticscfg.SourceConfig must be registered in the injector.
func RegisterMultiSourceEventReporter(i do.Injector) {
	do.Provide[*MultiSourceEventReporter](i, func(i do.Injector) (*MultiSourceEventReporter, error) {
		return ProvideMultiSourceEventReporter(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[map[string]*analyticscfg.SourceConfig](i),
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
			do.MustInvoke[metrics.Provider](i),
		)
	})
}

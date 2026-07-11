package analyticscfg

import (
	"context"

	"github.com/primandproper/platform-go/v4/analytics"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/metrics"
	"github.com/primandproper/platform-go/v4/observability/tracing"

	"github.com/samber/do/v2"
)

// RegisterEventReporter registers an analytics.EventReporter with the injector.
func RegisterEventReporter(i do.Injector) {
	do.Provide[analytics.EventReporter](i, func(i do.Injector) (analytics.EventReporter, error) {
		return NewEventReporter(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
			do.MustInvoke[metrics.Provider](i),
		)
	})
}

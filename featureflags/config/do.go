package featureflagscfg

import (
	"context"
	"net/http"

	"github.com/primandproper/platform-go/v5/featureflags"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	"github.com/primandproper/platform-go/v5/observability/tracing"

	"github.com/samber/do/v2"
)

// RegisterFeatureFlagManager registers a featureflags.FeatureFlagManager with the injector.
func RegisterFeatureFlagManager(i do.Injector) {
	do.Provide[featureflags.FeatureFlagManager](i, func(i do.Injector) (featureflags.FeatureFlagManager, error) {
		return NewFeatureFlagManager(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
			do.MustInvoke[metrics.Provider](i),
			do.MustInvoke[*http.Client](i),
		)
	})
}

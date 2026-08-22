package featureflagscfg

import (
	"context"
	"net/http"

	"github.com/primandproper/platform-go/v13/featureflags"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterFeatureFlagManager registers a featureflags.FeatureFlagManager with the injector.
func RegisterFeatureFlagManager(i do.Injector) {
	do.Provide(i, func(i do.Injector) (featureflags.FeatureFlagManager, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewFeatureFlagManager(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[*http.Client](i),
			WithPillars(pillars),
		)
	})
}

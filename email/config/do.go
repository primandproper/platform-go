package emailcfg

import (
	"context"
	"net/http"

	"github.com/primandproper/platform-go/v5/email"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	"github.com/primandproper/platform-go/v5/observability/tracing"

	"github.com/samber/do/v2"
)

// RegisterEmailer registers an email.Emailer with the injector.
func RegisterEmailer(i do.Injector) {
	do.Provide[email.Emailer](i, func(i do.Injector) (email.Emailer, error) {
		return NewEmailer(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
			do.MustInvoke[metrics.Provider](i),
			do.MustInvoke[*http.Client](i),
		)
	})
}

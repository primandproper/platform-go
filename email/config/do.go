package emailcfg

import (
	"context"
	"net/http"

	"github.com/primandproper/platform-go/v13/email"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterEmailer registers an email.Emailer with the injector.
func RegisterEmailer(i do.Injector) {
	do.Provide(i, func(i do.Injector) (email.Emailer, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewEmailer(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			do.MustInvoke[*http.Client](i),
			WithPillars(pillars),
		)
	})
}

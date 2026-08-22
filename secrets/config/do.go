package secretscfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/secrets"

	"github.com/samber/do/v2"
)

// RegisterSecretSource registers a secrets.SecretSource with the injector.
func RegisterSecretSource(i do.Injector) {
	do.Provide(i, func(i do.Injector) (secrets.SecretSource, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewSecretSource(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			WithPillars(pillars),
		)
	})
}

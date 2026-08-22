package tokenscfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/authentication/tokens"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// NewTokenIssuer provides a tokens.Issuer from a config.
func NewTokenIssuer(ctx context.Context, cfg *Config, opts ...Option) (tokens.Issuer, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	return cfg.NewTokenIssuer(ctx, opts...)
}

// RegisterTokenIssuer registers the token issuer with the injector.
func RegisterTokenIssuer(i do.Injector) {
	do.Provide(i, func(i do.Injector) (tokens.Issuer, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewTokenIssuer(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			WithPillars(pillars),
		)
	})
}

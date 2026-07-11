package tokenscfg

import (
	"github.com/primandproper/platform-go/v4/authentication/tokens"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/tracing"

	"github.com/samber/do/v2"
)

// NewTokenIssuer provides a tokens.Issuer from a config.
func NewTokenIssuer(cfg *Config, logger logging.Logger, tracerProvider tracing.TracerProvider) (tokens.Issuer, error) {
	return cfg.NewTokenIssuer(logger, tracerProvider)
}

// RegisterTokenIssuer registers the token issuer with the injector.
func RegisterTokenIssuer(i do.Injector) {
	do.Provide[tokens.Issuer](i, func(i do.Injector) (tokens.Issuer, error) {
		return NewTokenIssuer(
			do.MustInvoke[*Config](i),
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
		)
	})
}

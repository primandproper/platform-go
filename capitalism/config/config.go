package config

import (
	"context"
	"strings"

	"github.com/primandproper/platform-go/v2/capitalism"
	"github.com/primandproper/platform-go/v2/capitalism/stripe"
	"github.com/primandproper/platform-go/v2/errors"
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/tracing"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// StripeProvider is the key that indicates Stripe should be used for payments.
	StripeProvider = "stripe"
)

type (
	// Config allows for the configuration of this package and its subpackages.
	Config struct {
		Stripe   *stripe.Config `env:"init"     envPrefix:"STRIPE_" json:"stripe"`
		Provider string         `env:"PROVIDER" json:"provider"`
		Enabled  bool           `env:"ENABLED"  json:"enabled"`
	}
)

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a StripeConfig struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	if !cfg.Enabled {
		return nil
	}

	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Provider, validation.In(StripeProvider)),
		validation.Field(&cfg.Stripe, validation.When(cfg.Provider == StripeProvider, validation.Required)),
	)
}

// ProvideCapitalismImplementation provides a capitalism.PaymentManager implementation based on the config.
func ProvideCapitalismImplementation(logger logging.Logger, tracerProvider tracing.TracerProvider, cfg *Config) (capitalism.PaymentManager, error) {
	switch strings.TrimSpace(strings.ToLower(cfg.Provider)) {
	case StripeProvider:
		return stripe.ProvideStripePaymentManager(logger, tracerProvider, cfg.Stripe), nil
	default:
		return nil, errors.Newf("unknown provider: %q", cfg.Provider)
	}
}

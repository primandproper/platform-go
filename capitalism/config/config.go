package config

import (
	"context"
	"strings"

	"github.com/primandproper/platform-go/v4/capitalism"
	"github.com/primandproper/platform-go/v4/capitalism/noop"
	"github.com/primandproper/platform-go/v4/capitalism/stripe"
	"github.com/primandproper/platform-go/v4/errors"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/tracing"

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

// NewCapitalismImplementation provides a capitalism.PaymentManager implementation based on the
// config. stripeEventHandler is optional (may be nil) and, for the Stripe provider, is invoked with
// each verified webhook event.
func NewCapitalismImplementation(logger logging.Logger, tracerProvider tracing.TracerProvider, cfg *Config, stripeEventHandler stripe.EventHandler) (capitalism.PaymentManager, error) {
	if !cfg.Enabled {
		return noop.NewPaymentManager(), nil
	}

	switch strings.TrimSpace(strings.ToLower(cfg.Provider)) {
	case StripeProvider:
		return stripe.NewStripePaymentManager(logger, tracerProvider, cfg.Stripe, stripeEventHandler)
	default:
		return nil, errors.Newf("unknown provider: %q", cfg.Provider)
	}
}

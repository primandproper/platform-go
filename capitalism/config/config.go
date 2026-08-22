// Package capitalismcfg builds both halves of the payments seam from one
// configuration — a capitalism.PaymentManager and a capitalism.UsageReporter —
// over Stripe or the noop provider.
//
// The two constructors share a validation path, so a deployment cannot build a
// payment manager against a config the usage reporter would have rejected.
// Naming the noop provider is what makes "meter everything, bill nothing" a
// supported deployment; reaching it by leaving Provider unset is not, because a
// manager that accepts every charge and moves no money looks like a working one.
package capitalismcfg

import (
	"context"
	"slices"

	"github.com/primandproper/platform-go/v13/capitalism"
	"github.com/primandproper/platform-go/v13/capitalism/noop"
	"github.com/primandproper/platform-go/v13/capitalism/stripe"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/cfgnorm"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// StripeProvider is the key that indicates Stripe should be used for payments.
	StripeProvider = "stripe"
	// NoopProvider charges nothing and reports no usage. It must be selected
	// deliberately — an unset or typo'd provider is an error, because a payment
	// manager that silently accepts every call without charging anyone looks like
	// a working deployment right up until someone reconciles the books.
	//
	// It is also what makes "meter everything, bill nothing" a supported
	// deployment: metering keeps counting durably and enforcing quotas, and
	// nothing reaches a provider.
	NoopProvider = "noop"
)

type (
	// Config allows for the configuration of this package and its subpackages.
	Config struct {
		Stripe   *stripe.Config `env:",init"    envPrefix:"STRIPE_"       json:"stripe,omitempty"   yaml:"stripe,omitempty"`
		Provider string         `env:"PROVIDER" json:"provider,omitempty" yaml:"provider,omitempty"`
	}
)

// providers are every provider this package implements. Validation and both
// constructors read it.
var providers = []string{StripeProvider, NoopProvider}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
//
// The Stripe sub-config is skipped rather than merely unguarded when Stripe is
// not the provider: ozzo validates any non-nil pointer to a Validatable once a
// field's rules have run, and `env:",init"` leaves every sub-config non-nil. A
// validation.When guard alone stops the Required rule and nothing else, so a
// webhook secret was demanded of deployments that charge nobody.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	provider := cfgnorm.Provider(cfg.Provider)

	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Provider, validation.Required, validation.By(func(any) error {
			// Checked normalized, matching dispatch: validating the raw string
			// rejected "Stripe" and " stripe " while both constructors built them.
			if !slices.Contains(providers, provider) {
				return errors.Wrapf(errors.ErrUnknownProvider, "payments provider %q", cfg.Provider)
			}

			return nil
		})),
		validation.Field(&cfg.Stripe, validation.Skip.When(provider != StripeProvider), validation.Required),
	)
}

// prepare validates cfg, shared by both constructors so that building a payment
// manager cannot succeed against a config the usage reporter would reject.
//
// It returns the normalized provider, which is what the callers dispatch on.
func prepare(ctx context.Context, cfg *Config) (string, error) {
	if cfg == nil {
		return "", errors.ErrNilInputParameter
	}

	provider, err := cfgnorm.SelectProvider(cfg.Provider, providers, "payments provider")
	if err != nil {
		return "", err
	}

	if err = cfg.ValidateWithContext(ctx); err != nil {
		return "", errors.Wrap(err, "validating payments config")
	}

	return provider, nil
}

// NewPaymentManager provides a capitalism.PaymentManager implementation based on the
// config. stripeEventHandler is optional (may be nil) and, for the Stripe provider, is invoked with
// each verified webhook event.
//
// Each provider is built into a variable and returned only once its error is
// known to be nil. The provider constructors return their own concrete types,
// so returning one straight through would convert a nil *stripe.PaymentManager into a
// non-nil capitalism.PaymentManager on the error path, and a caller testing the result against
// nil would find a manager that panics on first use.
func NewPaymentManager(ctx context.Context, cfg *Config, stripeEventHandler stripe.EventHandler, opts ...Option) (capitalism.PaymentManager, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	provider, err := prepare(ctx, cfg)
	if err != nil {
		return nil, err
	}

	switch provider {
	case StripeProvider:
		m, managerErr := stripe.NewPaymentManager(cfg.Stripe, stripeEventHandler, stripe.WithLogger(logger), stripe.WithTracerProvider(tracerProvider), stripe.WithMetricsProvider(metricsProvider))
		if managerErr != nil {
			return nil, managerErr
		}

		return m, nil
	case NoopProvider:
		return noop.NewPaymentManager(), nil
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "payments provider %q", cfg.Provider)
	}
}

// NewUsageReporter provides a capitalism.UsageReporter based on the config.
//
// Each provider is built into a variable and returned only once its error is
// known to be nil. The provider constructors return their own concrete types,
// so returning one straight through would convert a nil *stripe.UsageReporter into a
// non-nil capitalism.UsageReporter on the error path, and a caller testing the result against
// nil would find a reporter that panics on first use.
func NewUsageReporter(ctx context.Context, cfg *Config, opts ...Option) (capitalism.UsageReporter, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	provider, err := prepare(ctx, cfg)
	if err != nil {
		return nil, err
	}

	switch provider {
	case StripeProvider:
		r, reporterErr := stripe.NewUsageReporter(cfg.Stripe, stripe.WithLogger(logger), stripe.WithTracerProvider(tracerProvider), stripe.WithMetricsProvider(metricsProvider))
		if reporterErr != nil {
			return nil, reporterErr
		}

		return r, nil
	case NoopProvider:
		return noop.NewUsageReporter(), nil
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "payments provider %q", cfg.Provider)
	}
}

// Package emailcfg selects and builds an email.Emailer from configuration over
// six vendors — SendGrid, Mailgun, Mailjet, Resend, Postmark, SES — or the noop
// emailer.
//
// It is one of the few config seams that takes an *http.Client as a dependency
// rather than an option: the vendor clients transport over HTTP, and the
// timeouts and transport a deployment wants there are not this package's to
// choose. The circuit breaker wrapped around whichever vendor is selected comes
// from configuration, not from the caller.
package emailcfg

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	circuitbreakingcfg "github.com/primandproper/platform-go/v13/circuitbreaking/config"
	"github.com/primandproper/platform-go/v13/email"
	"github.com/primandproper/platform-go/v13/email/mailgun"
	"github.com/primandproper/platform-go/v13/email/mailjet"
	"github.com/primandproper/platform-go/v13/email/noop"
	"github.com/primandproper/platform-go/v13/email/postmark"
	"github.com/primandproper/platform-go/v13/email/resend"
	"github.com/primandproper/platform-go/v13/email/sendgrid"
	"github.com/primandproper/platform-go/v13/email/ses"
	"github.com/primandproper/platform-go/v13/errors"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderSendgrid represents SendGrid.
	ProviderSendgrid = "sendgrid"
	// ProviderMailgun represents Mailgun.
	ProviderMailgun = "mailgun"
	// ProviderMailjet represents Mailjet.
	ProviderMailjet = "mailjet"
	// ProviderResend represents Resend.
	ProviderResend = "resend"
	// ProviderPostmark represents Postmark.
	ProviderPostmark = "postmark"
	// ProviderSES represents AWS SES.
	ProviderSES = "ses"
	// ProviderNoop discards every message. It must be selected deliberately —
	// an unset or typo'd provider is an error, because outbound mail that
	// silently goes nowhere is discovered by the people who never received it.
	ProviderNoop = "noop"
)

// providers are every provider this package implements. The dispatch switch and
// ValidateWithContext both read it, so they cannot drift apart.
var providers = []string{
	ProviderNoop,
	ProviderSendgrid,
	ProviderMailgun,
	ProviderMailjet,
	ProviderResend,
	ProviderPostmark,
	ProviderSES,
}

// knownProvider reports whether p names an implementation, ignoring case and
// surrounding space, exactly as the dispatch switch does.
func knownProvider(p string) bool {
	return slices.Contains(providers, strings.ToLower(strings.TrimSpace(p)))
}

type (
	// Config is the configuration structure.
	Config struct {
		Sendgrid       *sendgrid.Config          `env:",init"    envPrefix:"SENDGRID_"         json:"sendgrid,omitempty"            yaml:"sendgrid,omitempty"`
		Mailgun        *mailgun.Config           `env:",init"    envPrefix:"MAILGUN_"          json:"mailgun,omitempty"             yaml:"mailgun,omitempty"`
		Mailjet        *mailjet.Config           `env:",init"    envPrefix:"MAILJET_"          json:"mailjet,omitempty"             yaml:"mailjet,omitempty"`
		Resend         *resend.Config            `env:",init"    envPrefix:"RESEND_"           json:"resend,omitempty"              yaml:"resend,omitempty"`
		Postmark       *postmark.Config          `env:",init"    envPrefix:"POSTMARK_"         json:"postmark,omitempty"            yaml:"postmark,omitempty"`
		SES            *ses.Config               `env:",init"    envPrefix:"SES_"              json:"ses,omitempty"                 yaml:"ses,omitempty"`
		Provider       string                    `env:"PROVIDER" json:"provider,omitempty"     yaml:"provider,omitempty"`
		CircuitBreaker circuitbreakingcfg.Config `env:",init"    envPrefix:"CIRCUIT_BREAKING_" json:"circuitBreakerConfig,omitzero" yaml:"circuitBreakerConfig,omitempty"`
	}
)

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults sets sensible defaults for zero-valued fields.
func (cfg *Config) EnsureDefaults() {
	cfg.CircuitBreaker.EnsureDefaults()
}

// ValidateWithContext validates a Config.
//
// The sub-configs for providers that were not selected are skipped rather than
// merely unguarded: ozzo validates any non-nil pointer to a Validatable once a
// field's rules have run, and `env:",init"` leaves every sub-config non-nil. A
// validation.When guard alone stops the Required rule and nothing else, so all
// six providers' credentials were required at once and no config could load.
//
// The selection is read normalized, matching dispatch: a "SENDGRID" that
// knownProvider accepts and NewEmailer dispatches on would otherwise skip the
// very block it is about to use.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))

	return validation.ValidateStructWithContext(
		ctx,
		cfg,
		validation.Field(&cfg.Provider, validation.Required, validation.By(func(any) error {
			if !knownProvider(cfg.Provider) {
				return errors.Wrapf(errors.ErrUnknownProvider, "email provider %q", cfg.Provider)
			}

			return nil
		})),
		validation.Field(&cfg.Sendgrid, validation.Skip.When(provider != ProviderSendgrid), validation.Required),
		validation.Field(&cfg.Mailgun, validation.Skip.When(provider != ProviderMailgun), validation.Required),
		validation.Field(&cfg.Mailjet, validation.Skip.When(provider != ProviderMailjet), validation.Required),
		validation.Field(&cfg.Resend, validation.Skip.When(provider != ProviderResend), validation.Required),
		validation.Field(&cfg.Postmark, validation.Skip.When(provider != ProviderPostmark), validation.Required),
		validation.Field(&cfg.SES, validation.Skip.When(provider != ProviderSES), validation.Required),
	)
}

// NewEmailer provides an outbound_emailer.
func (cfg *Config) NewEmailer(ctx context.Context, client *http.Client, circuitBreaker circuitbreaking.CircuitBreaker, opts ...Option) (email.Emailer, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	cfg.EnsureDefaults()

	// The provider is checked before the rest of the config so an unrecognized
	// one reports ErrUnknownProvider rather than whichever sub-config happened
	// to be missing as a consequence.
	if !knownProvider(cfg.Provider) {
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "email provider %q", cfg.Provider)
	}

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating email config")
	}

	// Every branch builds into a variable and returns only once its error is
	// known to be nil. The provider packages return their own *Emailer, and
	// returning one straight through would convert a nil pointer into a non-nil
	// email.Emailer on the error path — a value that passes a caller's nil check
	// and panics on the first send.
	var (
		emailer email.Emailer
		err     error
	)

	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case ProviderSendgrid:
		emailer, err = sendgrid.NewSendGridEmailer(cfg.Sendgrid, client, circuitBreaker, sendgrid.WithLogger(logger), sendgrid.WithTracerProvider(tracerProvider), sendgrid.WithMetricsProvider(metricsProvider))
	case ProviderMailgun:
		emailer, err = mailgun.NewMailgunEmailer(cfg.Mailgun, client, circuitBreaker, mailgun.WithLogger(logger), mailgun.WithTracerProvider(tracerProvider), mailgun.WithMetricsProvider(metricsProvider))
	case ProviderMailjet:
		emailer, err = mailjet.NewMailjetEmailer(cfg.Mailjet, client, circuitBreaker, mailjet.WithLogger(logger), mailjet.WithTracerProvider(tracerProvider), mailjet.WithMetricsProvider(metricsProvider))
	case ProviderResend:
		emailer, err = resend.NewResendEmailer(cfg.Resend, client, circuitBreaker, resend.WithLogger(logger), resend.WithTracerProvider(tracerProvider), resend.WithMetricsProvider(metricsProvider))
	case ProviderPostmark:
		emailer, err = postmark.NewPostmarkEmailer(cfg.Postmark, client, circuitBreaker, postmark.WithLogger(logger), postmark.WithTracerProvider(tracerProvider), postmark.WithMetricsProvider(metricsProvider))
	case ProviderSES:
		emailer, err = ses.NewSESEmailer(ctx, cfg.SES, client, circuitBreaker, nil, ses.WithLogger(logger), ses.WithTracerProvider(tracerProvider), ses.WithMetricsProvider(metricsProvider))
	case ProviderNoop:
		emailer, err = noop.NewEmailer()
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "email provider %q", cfg.Provider)
	}

	if err != nil {
		return nil, err
	}

	return emailer, nil
}

package emailcfg

import (
	"context"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v6/circuitbreaking"
	circuitbreakingcfg "github.com/primandproper/platform-go/v6/circuitbreaking/config"
	"github.com/primandproper/platform-go/v6/email"
	"github.com/primandproper/platform-go/v6/email/mailgun"
	"github.com/primandproper/platform-go/v6/email/mailjet"
	"github.com/primandproper/platform-go/v6/email/noop"
	"github.com/primandproper/platform-go/v6/email/postmark"
	"github.com/primandproper/platform-go/v6/email/resend"
	"github.com/primandproper/platform-go/v6/email/sendgrid"
	"github.com/primandproper/platform-go/v6/email/ses"
	"github.com/primandproper/platform-go/v6/observability/logging"
	"github.com/primandproper/platform-go/v6/observability/metrics"
	"github.com/primandproper/platform-go/v6/observability/tracing"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"github.com/matcornic/hermes/v2"
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
)

type (
	// Config is the configuration structure.
	Config struct {
		Sendgrid                            *sendgrid.Config          `env:"init"                                    envPrefix:"SENDGRID_"                      json:"sendgrid"                            yaml:"sendgrid"`
		Mailgun                             *mailgun.Config           `env:"init"                                    envPrefix:"MAILGUN_"                       json:"mailgun"                             yaml:"mailgun"`
		Mailjet                             *mailjet.Config           `env:"init"                                    envPrefix:"MAILJET_"                       json:"mailjet"                             yaml:"mailjet"`
		Resend                              *resend.Config            `env:"init"                                    envPrefix:"RESEND_"                        json:"resend"                              yaml:"resend"`
		Postmark                            *postmark.Config          `env:"init"                                    envPrefix:"POSTMARK_"                      json:"postmark"                            yaml:"postmark"`
		SES                                 *ses.Config               `env:"init"                                    envPrefix:"SES_"                           json:"ses"                                 yaml:"ses"`
		Provider                            string                    `env:"PROVIDER"                                json:"provider"                            yaml:"provider"`
		BaseURL                             template.URL              `env:"BASE_URL"                                json:"baseURL"                             yaml:"baseURL"`
		OutboundInvitesEmailAddress         string                    `env:"OUTBOUND_INVITES_EMAIL_ADDRESS"          json:"outboundInvitesEmailAddress"         yaml:"outboundInvitesEmailAddress"`
		PasswordResetCreationEmailAddress   string                    `env:"PASSWORD_RESET_CREATION_EMAIL_ADDRESS"   json:"passwordResetCreationEmailAddress"   yaml:"passwordResetCreationEmailAddress"`
		PasswordResetRedemptionEmailAddress string                    `env:"PASSWORD_RESET_REDEMPTION_EMAIL_ADDRESS" json:"passwordResetRedemptionEmailAddress" yaml:"passwordResetRedemptionEmailAddress"`
		CircuitBreaker                      circuitbreakingcfg.Config `env:"init"                                    envPrefix:"CIRCUIT_BREAKING_"              json:"circuitBreakerConfig"                yaml:"circuitBreakerConfig"`
	}
)

// BuildHermes builds a Hermes instance for rendering email templates.
func (cfg *Config) BuildHermes(branding *email.EmailBranding) *hermes.Hermes {
	var name, logo, copyright string
	if branding != nil {
		name = branding.CompanyName
		logo = branding.LogoURL
		copyright = fmt.Sprintf("Copyright © %d %s. All rights reserved.", time.Now().Year(), branding.CompanyName)
	}
	return &hermes.Hermes{
		Product: hermes.Product{
			Name:      name,
			Link:      string(cfg.BaseURL),
			Logo:      logo,
			Copyright: copyright,
		},
	}
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults sets sensible defaults for zero-valued fields.
func (cfg *Config) EnsureDefaults() {
	cfg.CircuitBreaker.EnsureDefaults()
}

// ValidateWithContext validates a Config.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(
		ctx,
		cfg,
		validation.Field(&cfg.Provider, validation.In(
			ProviderSendgrid,
			ProviderMailgun,
			ProviderMailjet,
			ProviderResend,
			ProviderPostmark,
			ProviderSES,
		)),
		validation.Field(&cfg.Sendgrid, validation.When(cfg.Provider == ProviderSendgrid, validation.Required)),
		validation.Field(&cfg.Mailgun, validation.When(cfg.Provider == ProviderMailgun, validation.Required)),
		validation.Field(&cfg.Mailjet, validation.When(cfg.Provider == ProviderMailjet, validation.Required)),
		validation.Field(&cfg.Resend, validation.When(cfg.Provider == ProviderResend, validation.Required)),
		validation.Field(&cfg.Postmark, validation.When(cfg.Provider == ProviderPostmark, validation.Required)),
		validation.Field(&cfg.SES, validation.When(cfg.Provider == ProviderSES, validation.Required)),
	)
}

// NewEmailer provides an outbound_emailer.
func (cfg *Config) NewEmailer(ctx context.Context, logger logging.Logger, tracerProvider tracing.TracerProvider, client *http.Client, circuitBreaker circuitbreaking.CircuitBreaker, metricsProvider metrics.Provider) (email.Emailer, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case ProviderSendgrid:
		return sendgrid.NewSendGridEmailer(cfg.Sendgrid, logger, tracerProvider, client, circuitBreaker, metricsProvider)
	case ProviderMailgun:
		return mailgun.NewMailgunEmailer(cfg.Mailgun, logger, tracerProvider, client, circuitBreaker, metricsProvider)
	case ProviderMailjet:
		return mailjet.NewMailjetEmailer(cfg.Mailjet, logger, tracerProvider, client, circuitBreaker, metricsProvider)
	case ProviderResend:
		return resend.NewResendEmailer(cfg.Resend, logger, tracerProvider, client, circuitBreaker, metricsProvider)
	case ProviderPostmark:
		return postmark.NewPostmarkEmailer(cfg.Postmark, logger, tracerProvider, client, circuitBreaker, metricsProvider)
	case ProviderSES:
		return ses.NewSESEmailer(ctx, cfg.SES, logger, tracerProvider, client, circuitBreaker, metricsProvider, nil)
	default:
		logger.Debug("providing noop outbound_emailer")
		return noop.NewEmailer()
	}
}

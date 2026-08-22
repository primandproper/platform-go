package mailgun

import (
	"context"
	"net/url"

	"github.com/primandproper/platform-go/v13/errors"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"github.com/mailgun/mailgun-go/v4"
)

const (
	// BaseURLUS is the API base for a Mailgun account provisioned in the US
	// region. It is the SDK's default, so leaving Config.BaseURL empty means
	// this.
	BaseURLUS = mailgun.APIBaseUS
	// BaseURLEU is the API base for a Mailgun account provisioned in the EU
	// region. An EU account is unreachable through the US base — the domain
	// does not exist there — so a deployment on one has to name this.
	BaseURLEU = mailgun.APIBaseEU
)

type (
	// Config configures Mailgun to send email.
	Config struct {
		PrivateAPIKey string `env:"PRIVATE_API_KEY" json:"privateAPIKey,omitempty" yaml:"privateAPIKey,omitempty"`
		Domain        string `env:"DOMAIN"          json:"domain,omitempty"        yaml:"domain,omitempty"`
		// BaseURL is the Mailgun API base to send through. Empty means the
		// SDK's default, which is the US region: an EU-provisioned account must
		// set this to BaseURLEU or it will not be reached. Pointing it at a
		// test server is the other reason to set it.
		BaseURL string `env:"BASE_URL" json:"baseURL,omitempty" yaml:"baseURL,omitempty"`
	}
)

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (s *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, s,
		validation.Field(&s.PrivateAPIKey, validation.Required),
		validation.Field(&s.Domain, validation.Required),
		validation.Field(&s.BaseURL, validation.By(func(value any) error {
			raw, ok := value.(string)
			if !ok {
				return errors.New("base URL must be a string")
			}

			if raw == "" {
				return nil
			}

			parsed, parseErr := url.Parse(raw)
			if parseErr != nil {
				return parseErr
			}

			if parsed.Scheme == "" || parsed.Host == "" {
				return errors.New("base URL must be absolute")
			}

			return nil
		})),
	)
}

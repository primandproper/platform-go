package pusher

import (
	"context"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config holds Pusher async notifier configuration.
type Config struct {
	AppID   string `env:"APP_ID"  json:"appID"   yaml:"appID"`
	Key     string `env:"KEY"     json:"key"     yaml:"key"`
	Secret  string `env:"SECRET"  json:"secret"  yaml:"secret"`
	Cluster string `env:"CLUSTER" json:"cluster" yaml:"cluster"`
	Secure  bool   `env:"SECURE"  json:"secure"  yaml:"secure"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.AppID, validation.Required),
		validation.Field(&cfg.Key, validation.Required),
		validation.Field(&cfg.Secret, validation.Required),
		validation.Field(&cfg.Cluster, validation.Required),
	)
}

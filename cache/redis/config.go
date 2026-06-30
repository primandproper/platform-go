package redis

import (
	"context"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config configures a Redis-backed consumer.
type Config struct {
	Username       string   `env:"USERNAME"        json:"username"`
	Password       string   `env:"PASSWORD"        json:"password,omitempty"`
	QueueAddresses []string `env:"QUEUE_ADDRESSES" json:"queueAddresses"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.QueueAddresses, validation.Required, validation.Length(1, 0)),
	)
}

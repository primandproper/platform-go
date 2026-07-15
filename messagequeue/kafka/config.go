package kafka

import (
	"context"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config configures a Kafka-backed message queue.
type Config struct {
	GroupID string   `env:"GROUP_ID" json:"groupId" yaml:"groupId"`
	Brokers []string `env:"BROKERS"  json:"brokers" yaml:"brokers"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.GroupID, validation.Required),
		validation.Field(&cfg.Brokers, validation.Required, validation.Length(1, 0)),
	)
}

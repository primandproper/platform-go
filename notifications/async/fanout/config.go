package fanout

import (
	"context"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// DefaultTopic is the messagequeue topic the backplane publishes to and
// consumes from when a Config names none.
//
// One topic serves every notification channel; see the package documentation
// for why it is fixed rather than derived per channel.
const DefaultTopic = "async_notifications_fanout"

// Config configures the backplane.
type Config struct {
	// Topic is the messagequeue topic every replica publishes to and consumes
	// from. It defaults to DefaultTopic.
	//
	// Every replica of one service must name the same topic, and two services
	// sharing a broker should not: a replica delivers every event it consumes
	// to its local connections, so a shared topic means each service filtering
	// the other's channels for nothing.
	Topic string `env:"TOPIC" envDefault:"async_notifications_fanout" json:"topic,omitempty" yaml:"topic,omitempty"`

	// Enabled turns the backplane on. It is off by default, so a single-replica
	// deployment pays nothing.
	Enabled bool `env:"ENABLED" json:"enabled,omitempty" yaml:"enabled,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Topic, validation.Required),
	)
}

// EnsureDefaults fills unset knobs with the package defaults.
func (cfg *Config) EnsureDefaults() {
	if cfg.Topic == "" {
		cfg.Topic = DefaultTopic
	}
}

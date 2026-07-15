package redis

import (
	"context"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config configures a Redis-backed consumer.
type Config struct {
	Username       string   `env:"USERNAME"        json:"username"           yaml:"username"`
	Password       string   `env:"PASSWORD"        json:"password,omitempty" yaml:"password,omitempty"`
	QueueAddresses []string `env:"QUEUE_ADDRESSES" json:"queueAddresses"     yaml:"queueAddresses"`
	Cluster        bool     `env:"CLUSTER"         json:"cluster"            yaml:"cluster"`
}

// clusterMode reports whether the client should run in Redis Cluster mode. A
// cluster can be reached through a single seed address, so an explicit Cluster
// flag is honored in addition to the multi-address heuristic — otherwise a
// single-seed cluster is misclassified as single-node and multi-slot GetMany/
// SetMany fail with CROSSSLOT.
func (cfg *Config) clusterMode() bool {
	return cfg.Cluster || len(cfg.QueueAddresses) > 1
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.QueueAddresses, validation.Required, validation.Length(1, 0)),
	)
}

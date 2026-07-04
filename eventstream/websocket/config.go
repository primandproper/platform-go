package websocket

import (
	"context"
	"time"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config holds WebSocket-specific configuration.
type Config struct {
	// AllowedOrigins is the set of exact Origin header values permitted to upgrade.
	// When empty, upgrades are restricted to same-origin requests.
	AllowedOrigins    []string      `env:"ALLOWED_ORIGINS"    json:"allowedOrigins"`
	HeartbeatInterval time.Duration `env:"HEARTBEAT_INTERVAL" json:"heartbeatInterval"`
	ReadBufferSize    int           `env:"READ_BUFFER_SIZE"   json:"readBufferSize"`
	WriteBufferSize   int           `env:"WRITE_BUFFER_SIZE"  json:"writeBufferSize"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg)
}

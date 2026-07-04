package postgres

import (
	"context"
	"time"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// DefaultConnWaitTimeout bounds how long Acquire waits to reserve a write-pool
// connection before giving up. See Config.ConnWaitTimeout.
const DefaultConnWaitTimeout = 5 * time.Second

// Config configures a Postgres-backed distributed locker. Namespace is mixed into
// the lock-id hash so independent services that share a Postgres cluster do not
// collide on the same advisory-lock id space.
type Config struct {
	// Namespace is mixed into the lock-id hash so independent services sharing a
	// Postgres cluster do not collide on the same advisory-lock id space.
	Namespace int32 `env:"NAMESPACE" envDefault:"0" json:"namespace"`

	// ConnWaitTimeout bounds how long Acquire will wait to reserve a connection
	// from the write pool. Each held lock pins one write connection for its whole
	// lifetime, so a saturated pool would otherwise make Acquire block indefinitely
	// in database/sql's Conn(). When the wait is exceeded, Acquire returns
	// distributedlock.ErrLockNotAcquired instead of blocking. Zero uses
	// DefaultConnWaitTimeout; a negative value disables the bound (wait forever).
	ConnWaitTimeout time.Duration `env:"CONN_WAIT_TIMEOUT" envDefault:"5s" json:"connWaitTimeout"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct. Namespace has no upper bound;
// any int32 is acceptable.
func (cfg *Config) ValidateWithContext(_ context.Context) error {
	return validation.Validate(cfg)
}

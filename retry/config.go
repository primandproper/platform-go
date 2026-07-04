package retry

import (
	"context"
	"time"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	defaultMaxAttempts  = 3
	defaultInitialDelay = 100 * time.Millisecond
	defaultMaxDelay     = 5 * time.Second
	defaultMultiplier   = 2.0
)

// Config configures retry behavior.
type Config struct {
	MaxAttempts  uint          `env:"MAX_ATTEMPTS"  json:"maxAttempts"`
	InitialDelay time.Duration `env:"INITIAL_DELAY" json:"initialDelay"`
	MaxDelay     time.Duration `env:"MAX_DELAY"     json:"maxDelay"`
	Multiplier   float64       `env:"MULTIPLIER"    json:"multiplier"`
	UseJitter    bool          `env:"USE_JITTER"    json:"useJitter"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults sets default values for zero (and invalid) fields. It clamps
// rather than merely zero-checking so a nonsensical config — a negative delay or
// a Multiplier below 1 that would shrink the backoff — can't produce a
// pathological policy, since the constructor returns no error to reject it.
func (cfg *Config) EnsureDefaults() {
	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = defaultMaxAttempts
	}

	if cfg.InitialDelay <= 0 {
		cfg.InitialDelay = defaultInitialDelay
	}

	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = defaultMaxDelay
	}

	if cfg.Multiplier < 1 {
		cfg.Multiplier = defaultMultiplier
	}
}

// ValidateWithContext validates the config.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.MaxAttempts, validation.Required, validation.Min(uint(1))),
		validation.Field(&cfg.InitialDelay, validation.Required, validation.Min(time.Millisecond)),
		validation.Field(&cfg.MaxDelay, validation.Required, validation.Min(time.Millisecond)),
		validation.Field(&cfg.Multiplier, validation.Min(1.0)),
	)
}

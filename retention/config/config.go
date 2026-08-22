/*
Package retentioncfg assembles a retention Sweeper from environment
configuration.

The knobs are here; the policies are not. What data an application keeps, for
how long, and why is Go — a table name, a column, a duration, and a sentence
explaining the choice — and there is no useful way to express it in the
environment. A retention policy read from an env var is a DELETE statement
nobody reviewed, which is the opposite of what this exists to provide. Policies
are passed explicitly to NewSweeper.

What does belong in configuration is the shape of the sweep: how large a batch
is, how many of them one run may spend, how long to pause between them. Those
are operational, they differ between a staging database and a production one,
and none of them changes what gets deleted.
*/
package retentioncfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/retention"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config assembles a retention Sweeper.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// Sweeper carries the batching and pacing knobs.
	Sweeper retention.SweeperConfig `env:",init" envPrefix:"SWEEPER_" json:"sweeper,omitzero" yaml:"sweeper,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in zero fields.
func (cfg *Config) EnsureDefaults() {
	cfg.Sweeper.EnsureDefaults()
}

// ValidateWithContext validates a Config.
//
// The nested config is validated through a validation.By closure because ozzo
// dereferences a struct-value field before checking ValidatableWithContext, so
// it would otherwise be skipped.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Sweeper, validation.By(func(any) error {
			return cfg.Sweeper.ValidateWithContext(ctx)
		})),
	)
}

// prepare fills defaults and validates.
func (cfg *Config) prepare(ctx context.Context) error {
	if cfg == nil {
		return errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return errors.Wrap(err, "validating retention config")
	}

	return nil
}

// NewSweeper builds the Sweeper. client must be the database the policies'
// targets name tables in, and policies is what this deployment enforces.
//
// Explicit options run after the config-derived ones, so a caller can still
// override anything.
func NewSweeper(
	ctx context.Context,
	cfg *Config,
	client database.Client,
	policies []retention.Policy,
	opts ...Option,
) (*retention.Sweeper, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if err := cfg.prepare(ctx); err != nil {
		return nil, err
	}

	var base []retention.SweeperOption

	if logger != nil {
		base = append(base, retention.WithSweeperLogger(logger))
	}
	if tracerProvider != nil {
		base = append(base, retention.WithSweeperTracerProvider(tracerProvider))
	}
	if metricsProvider != nil {
		base = append(base, retention.WithSweeperMetricsProvider(metricsProvider))
	}

	return retention.NewSweeper(ctx, &cfg.Sweeper, client, policies, append(base, o.sweeper...)...)
}

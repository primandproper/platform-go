/*
Package timerscfg assembles a timer set, and optionally the worker that fires
it, from environment configuration.

There is one dependency neither can be built without: a database.Client speaking
Postgres. The dialect is not configured here at all — it comes off the client, so
the SQL cannot disagree with the database it runs against.

Both constructors are generic over the key type, which the caller names at the
call site. That is the only part of a timer set the environment cannot express: a
key is a Go type, so a config file has nothing to say about it. A handler is the
other, which is why NewWorker takes one positionally.

# Why there is a Config here when workqueuecfg has none

A work queue has exactly one thing to configure, so a wrapper there would hold a
single field and forward two method calls. A timer set has two: the set and the
worker that drains it, whose knobs are separately meaningful and separately
tuned — and which a consumer running a schedule-only process configures without
configuring at all.

The nesting is what a consumer's environment already looks like. TIMERS_NAME and
TIMERS_WORKER_BATCH belong to one component and read like it.
*/
package timerscfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/timers"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config configures a timer set and the worker that fires it.
type Config struct {
	// Timers configures the set itself: the table, the logical set name, and
	// the retention and claim policy over it.
	Timers timers.Config `env:",init" json:"timers,omitzero" yaml:"timers,omitempty"`

	// Worker configures the firing loop. It is inert for a process that only
	// schedules — an application server writing timers a separate worker fleet
	// fires leaves it entirely unset.
	Worker timers.WorkerConfig `env:",init" envPrefix:"WORKER_" json:"worker,omitzero" yaml:"worker,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills unset knobs on both halves with their package defaults.
func (cfg *Config) EnsureDefaults() {
	cfg.Timers.EnsureDefaults()
	cfg.Worker.EnsureDefaults()
}

// ValidateWithContext validates both halves.
//
// Each nested config is validated through a validation.By closure because ozzo
// dereferences a struct-value field before checking ValidatableWithContext, so
// it would otherwise be skipped.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Timers, validation.By(func(any) error {
			return cfg.Timers.ValidateWithContext(ctx)
		})),
		validation.Field(&cfg.Worker, validation.By(func(any) error {
			return cfg.Worker.ValidateWithContext(ctx)
		})),
	)
}

// NewTimers builds a timer set from configuration.
//
// client must speak Postgres: this package's SQL is written against it rather
// than reduced to a portable subset, and timers.New returns
// dialect.ErrUnsupported for anything else. See the timers package doc for which
// construct is the binding one.
//
// K is the key type the set schedules against, and is the caller's to name:
//
//	set, err := timerscfg.NewTimers[TrialID](ctx, cfg, client)
//
// Explicit options run after the config-derived ones, so a caller can still
// override anything — including the key codec and the clock, which configuration
// has no way to express.
//
// The config is defaulted and validated by timers.New rather than here, so there
// is one place that decides what a usable timer config is.
func NewTimers[K comparable](
	ctx context.Context,
	cfg *Config,
	client database.Client,
	opts ...Option,
) (*timers.Timers[K], error) {
	if cfg == nil {
		return nil, timers.ErrNilConfig
	}

	o := newOptions(opts)

	base := make([]timers.Option, 0, len(o.timers)+4) //nolint:mnd // the four options below

	if o.clock != nil {
		base = append(base, timers.WithClock(o.clock))
	}
	if o.logger != nil {
		base = append(base, timers.WithLogger(o.logger))
	}
	if o.tracerProvider != nil {
		base = append(base, timers.WithTracerProvider(o.tracerProvider))
	}
	if o.metricsProvider != nil {
		base = append(base, timers.WithMetricsProvider(o.metricsProvider))
	}

	return timers.New[K](ctx, &cfg.Timers, client, append(base, o.timers...)...)
}

// NewWorker builds a timer set and the worker that fires it, returning both.
//
// The set comes back alongside the worker because a process that fires timers
// almost always schedules them too — the service that expires a trial is the one
// that started it — and a second set over the same table would mean a second
// copy of everything it carries, including its metrics.
//
// handler is positional because it is the one dependency a worker cannot be
// given any other way: it is the work.
func NewWorker[K comparable](
	ctx context.Context,
	cfg *Config,
	client database.Client,
	handler timers.Handler[K],
	opts ...Option,
) (*timers.Worker[K], *timers.Timers[K], error) {
	if cfg == nil {
		return nil, nil, timers.ErrNilConfig
	}

	set, err := NewTimers[K](ctx, cfg, client, opts...)
	if err != nil {
		return nil, nil, err
	}

	worker, err := timers.NewWorker(ctx, &cfg.Worker, set, handler)
	if err != nil {
		return nil, nil, err
	}

	return worker, set, nil
}

/*
Package seriescfg assembles a series Store, and optionally the horizon worker
that writes it forward, from environment configuration.

There are two things to configure: the table prefix, which must match the prefix
the migrations were rendered with, and the worker's knobs. The dialect comes from
the database.Client, and the distributed lock the worker runs under is a
dependency rather than configuration — primitives-go's distributedlock/config
builds one from its own environment, and the deployment decides which backend
one pass at a time is enforced by.

The nesting is what a consumer's environment already looks like:
SERIES_TABLE_PREFIX and SERIES_WORKER_HORIZON belong to one component and read
like it. A process that only reads and writes occurrences leaves the worker's
half unset and never calls NewWorker.
*/
package seriescfg

import (
	"context"

	"github.com/primandproper/platform-go/v15/series"
	"github.com/primandproper/platform-go/v15/series/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/distributedlock"
	"github.com/primandproper/primitives-go/v2/errors"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config assembles a series Store and its horizon worker.
type Config struct {
	// TablePrefix names the two tables. It must match the prefix the
	// migrations were rendered with. Defaults to series.DefaultTablePrefix.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`

	// Worker configures the horizon worker. It is inert for a process that
	// never calls NewWorker.
	Worker series.WorkerConfig `env:",init" envPrefix:"WORKER_" json:"worker,omitzero" yaml:"worker,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in zero fields on both halves.
func (cfg *Config) EnsureDefaults() {
	if cfg.TablePrefix == "" {
		cfg.TablePrefix = series.DefaultTablePrefix
	}

	cfg.Worker.EnsureDefaults()
}

// ValidateWithContext validates a Config.
//
// The prefix is vetted against the identifiers it actually renders, so a prefix
// that is legal in isolation but produces an over-long index name fails here
// instead of at the first migration. The worker's half is validated through a
// validation.By closure because ozzo dereferences a struct-value field before
// checking ValidatableWithContext, so it would otherwise be skipped.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	if err := validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Worker, validation.By(func(any) error {
			return cfg.Worker.ValidateWithContext(ctx)
		})),
	); err != nil {
		return err
	}

	return migrations.ValidatePrefix(cfg.TablePrefix)
}

// NewStore builds the Store. client must be the database holding the two
// tables.
//
// The store is built into a variable and returned only once its error is known
// to be nil: series.NewSQLStore returns its own concrete type, and returning one
// straight through would turn a nil *series.SQLStore into a non-nil
// series.Store on the error path.
func NewStore(ctx context.Context, cfg *Config, client database.Client, opts ...Option) (series.Store, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating series store config")
	}

	options := newOptions(opts)

	base := []series.SQLStoreOption{
		series.WithTablePrefix(cfg.TablePrefix),
		series.WithLogger(options.logger),
		series.WithTracerProvider(options.tracerProvider),
		series.WithMetricsProvider(options.metricsProvider),
	}

	store, storeErr := series.NewSQLStore(client, append(base, options.store...)...)
	if storeErr != nil {
		return nil, storeErr
	}

	return store, nil
}

// NewWorker builds a Store and the horizon worker that writes it forward,
// returning both, since a process running the worker almost always serves the
// store too and a second store over the same tables would be a second copy of
// its instruments.
//
// locker is positional because it is the one dependency the worker cannot be
// given any other way: it is what makes one pass at a time a fleet-wide fact.
func NewWorker(
	ctx context.Context,
	cfg *Config,
	client database.Client,
	locker distributedlock.ScopedLocker,
	opts ...Option,
) (*series.Worker, series.Store, error) {
	store, err := NewStore(ctx, cfg, client, opts...)
	if err != nil {
		return nil, nil, err
	}

	options := newOptions(opts)

	base := []series.WorkerOption{
		series.WithWorkerLogger(options.logger),
		series.WithWorkerTracerProvider(options.tracerProvider),
		series.WithWorkerMetricsProvider(options.metricsProvider),
	}

	worker, err := series.NewWorker(ctx, &cfg.Worker, client, store, locker, append(base, options.worker...)...)
	if err != nil {
		return nil, nil, err
	}

	return worker, store, nil
}

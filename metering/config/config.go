/*
Package meteringcfg assembles the usage metering machinery from environment
configuration: the Store every part shares, the Recorder usage arrives through,
the Enforcer quotas are checked against, and the Flusher that posts to the
billing provider.

All four read one Config, so the table prefix the Recorder writes to is by
construction the one the Enforcer reads and the Flusher flushes. The dialect is
not configured here at all: it comes from the database.Client, so it cannot
disagree with the database the statements actually run against.

The registry is not configured here. Which meters an application counts, and what
their aggregations mean, is Go code — and there is no useful way to express an
aggregation in the environment. It is passed explicitly to NewRecorder and
NewEnforcer.
*/
package meteringcfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/metering"

	"github.com/primandproper/primitives-go/capitalism"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/errors"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config assembles a metering Store, Recorder, Enforcer, and Flusher.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// TablePrefix names the metering tables. It must match the prefix the
	// migrations were rendered with. Defaults to metering.DefaultTablePrefix.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`

	// Enforcer carries the read path's knobs.
	Enforcer metering.EnforcerConfig `env:",init" envPrefix:"ENFORCER_" json:"enforcer,omitzero" yaml:"enforcer,omitempty"`

	// Flusher carries the provider push's knobs.
	Flusher metering.FlusherConfig `env:",init" envPrefix:"FLUSHER_" json:"flusher,omitzero" yaml:"flusher,omitempty"`

	// Recorder carries the ingest path's knobs.
	Recorder metering.RecorderConfig `env:",init" envPrefix:"RECORDER_" json:"recorder,omitzero" yaml:"recorder,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in zero fields.
func (cfg *Config) EnsureDefaults() {
	if cfg.TablePrefix == "" {
		cfg.TablePrefix = metering.DefaultTablePrefix
	}

	cfg.Recorder.EnsureDefaults()
	cfg.Enforcer.EnsureDefaults()
	cfg.Flusher.EnsureDefaults()
}

// ValidateWithContext validates a Config.
//
// The nested configs are validated through validation.By closures because ozzo
// dereferences a struct-value field before checking ValidatableWithContext, so
// they would otherwise be skipped.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Recorder, validation.By(func(any) error {
			return cfg.Recorder.ValidateWithContext(ctx)
		})),
		validation.Field(&cfg.Enforcer, validation.By(func(any) error {
			return cfg.Enforcer.ValidateWithContext(ctx)
		})),
		validation.Field(&cfg.Flusher, validation.By(func(any) error {
			return cfg.Flusher.ValidateWithContext(ctx)
		})),
	)
}

// prepare fills defaults and validates, which every constructor below does first
// and identically.
func (cfg *Config) prepare(ctx context.Context) error {
	if cfg == nil {
		return errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return errors.Wrap(err, "validating metering config")
	}

	return nil
}

// NewStore builds the Store every part shares. client must be the database
// holding the metering tables.
//
// Explicit options run after the config-derived ones, so a caller can still
// override anything.
//
// Each provider is built into a variable and returned only once its error is
// known to be nil. The provider constructors return their own concrete types,
// so returning one straight through would convert a nil *metering.SQLStore into a
// non-nil metering.Store on the error path, and a caller testing the result against
// nil would find a store that panics on first use.
func NewStore(
	ctx context.Context,
	cfg *Config,
	client database.Client,
	opts ...Option,
) (metering.Store, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if err := cfg.prepare(ctx); err != nil {
		return nil, err
	}

	base := []metering.SQLStoreOption{metering.WithTablePrefix(cfg.TablePrefix)}

	if logger != nil {
		base = append(base, metering.WithStoreLogger(logger))
	}
	if tracerProvider != nil {
		base = append(base, metering.WithStoreTracerProvider(tracerProvider))
	}
	if metricsProvider != nil {
		base = append(base, metering.WithStoreMetricsProvider(metricsProvider))
	}

	store, storeErr := metering.NewSQLStore(client, append(base, o.store...)...)
	if storeErr != nil {
		return nil, storeErr
	}

	return store, nil
}

// NewRecorder builds the ingest path.
//
// The registry is a required argument rather than a config field: which meters an
// application counts is Go code. The analytics mirror is not an argument at all —
// see WithRecorderAnalytics, which is off by default and says why.
func NewRecorder(
	ctx context.Context,
	cfg *Config,
	store metering.Store,
	registry *metering.Registry,
	resolver metering.PeriodResolver,
	opts ...Option,
) (*metering.DurableRecorder, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if err := cfg.prepare(ctx); err != nil {
		return nil, err
	}

	var base []metering.RecorderOption
	if resolver != nil {
		base = append(base, metering.WithRecorderPeriodResolver(resolver))
	}
	if o.analytics != nil {
		base = append(base, metering.WithRecorderAnalytics(o.analytics))
	}
	if logger != nil {
		base = append(base, metering.WithRecorderLogger(logger))
	}
	if tracerProvider != nil {
		base = append(base, metering.WithRecorderTracerProvider(tracerProvider))
	}
	if metricsProvider != nil {
		base = append(base, metering.WithRecorderMetricsProvider(metricsProvider))
	}

	return metering.NewDurableRecorder(ctx, &cfg.Recorder, store, registry, append(base, o.recorder...)...)
}

// NewEnforcer builds the read path.
//
// client must be the database holding the metering tables — the same one
// NewStore was given. What the enforcer takes from it is one thing: the reader
// Check falls back to when the cache cannot answer. Its writes are the caller's
// to open, because metering.Enforcer.Consume records usage in the transaction
// that did the work it authorized.
//
// It is Reader() rather than Writer(), which preserves what this package did
// internally before the executor became the caller's to choose. A deployment
// whose replica lag is long enough for a subject to spend the same quota twice
// wants metering.NewQuotaEnforcer directly, with Writer().
//
// The totals cache and the quota source are options rather than arguments — see
// WithEnforcerCache and WithEnforcerQuotaSource, each of which says what an
// enforcer without it does.
//
// resolver must be the same one NewRecorder was given. Two resolvers that
// disagree about where a period begins would have the enforcer reading a total
// the recorder is not writing to, which presents as a quota that never fills;
// passing it through one config-level constructor per process is what keeps them
// the same object.
func NewEnforcer(
	ctx context.Context,
	cfg *Config,
	client database.Client,
	store metering.Store,
	registry *metering.Registry,
	resolver metering.PeriodResolver,
	opts ...Option,
) (*metering.QuotaEnforcer, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if err := cfg.prepare(ctx); err != nil {
		return nil, err
	}

	if client == nil {
		return nil, errors.ErrNilInputParameter
	}

	var base []metering.EnforcerOption
	if resolver != nil {
		base = append(base, metering.WithEnforcerPeriodResolver(resolver))
	}
	if o.quotas != nil {
		base = append(base, metering.WithEnforcerQuotaSource(o.quotas))
	}
	if o.totals != nil {
		base = append(base, metering.WithEnforcerCache(o.totals))
	}
	if logger != nil {
		base = append(base, metering.WithEnforcerLogger(logger))
	}
	if tracerProvider != nil {
		base = append(base, metering.WithEnforcerTracerProvider(tracerProvider))
	}
	if metricsProvider != nil {
		base = append(base, metering.WithEnforcerMetricsProvider(metricsProvider))
	}

	return metering.NewQuotaEnforcer(
		ctx, &cfg.Enforcer, store, registry, client.Reader(), append(base, o.enforcer...)...,
	)
}

// NewFlusher builds the provider push. Register its Job with a jobs.Scheduler;
// see metering.Flusher.Job.
//
// reporter has no default. A flusher that posted nowhere would still mark usage
// flushed and advance the sequence, discarding revenue through an omission in the
// wiring — capitalism/noop.NewUsageReporter exists for the deployment that means
// it, and says so at the call site.
func NewFlusher(
	ctx context.Context,
	cfg *Config,
	store metering.Store,
	mapper metering.ProviderMapper,
	reporter capitalism.UsageReporter,
	opts ...Option,
) (*metering.Flusher, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if err := cfg.prepare(ctx); err != nil {
		return nil, err
	}

	var base []metering.FlusherOption
	if logger != nil {
		base = append(base, metering.WithFlusherLogger(logger))
	}
	if tracerProvider != nil {
		base = append(base, metering.WithFlusherTracerProvider(tracerProvider))
	}
	if metricsProvider != nil {
		base = append(base, metering.WithFlusherMetricsProvider(metricsProvider))
	}

	return metering.NewFlusher(ctx, &cfg.Flusher, store, mapper, reporter, append(base, o.flusher...)...)
}

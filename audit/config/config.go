/*
Package auditcfg assembles the audit log from environment configuration: the
Recorder applications write through, the Reader they query and verify with, and
the retention.Policy that prunes it.

The dialect and table prefix are fields of this Config rather than of any one
component's, because all of them take the same two values and getting them out
of step is the one misconfiguration that stays invisible until somebody asks the
log a question and gets an empty answer.

# Retention is a policy, not a sweeper

This package does not build a sweeper. NewRetentionPolicy returns a
retention.Policy, which the application appends to the policy set it hands
retention.NewSweeper — so pruning the audit log is scheduled by the same
jobs.Scheduler, holds the same distributed lock, reports the same backlog, and
is accounted for by the same audit entry as every other retention policy the
deployment enforces.

	policy, err := auditcfg.NewRetentionPolicy(ctx, auditConfig)
	if err != nil {
		return err
	}

	sweeper, err := retentioncfg.NewSweeper(ctx, retentionConfig, client,
		append(applicationPolicies, policy),
		retentioncfg.WithPillars(pillars),
		retentioncfg.WithSweeperOptions(retention.WithSweeperAuditRecorder(recorder)),
	)

With the recorder attached, the entry accounting for that sweep is written to
the log the sweep just pruned. That is the intended reading and not an
accident: until now the one deletion this module performed against the audit log
was the one deletion nothing recorded.
*/
package auditcfg

import (
	"context"

	"github.com/primandproper/platform-go/v10/audit"
	"github.com/primandproper/platform-go/v10/database"
	"github.com/primandproper/platform-go/v10/database/dialect"
	"github.com/primandproper/platform-go/v10/errors"
	"github.com/primandproper/platform-go/v10/retention"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config assembles an audit Recorder and Reader from environment
// configuration, and the retention policy that prunes what they write.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// Redactions declares what happens to named fields on the way into the log,
	// keyed by resource type. The empty key applies to every resource type.
	//
	// It carries no env tag: a map of string slices has no reasonable flat
	// environment encoding, and this is a policy that belongs in a reviewed file
	// rather than in a deployment variable — "which fields must never be
	// recorded" is exactly the sort of decision that should show up in a diff.
	Redactions map[string]audit.Redaction `json:"redactions,omitempty" yaml:"redactions,omitempty"`

	// Dialect selects the SQL emitted; it must match the database.Client the
	// Reader and the retention sweeper run against.
	Dialect dialect.Dialect `env:"DIALECT" json:"dialect,omitempty" yaml:"dialect,omitempty"`

	// TablePrefix is the prefix the audit tables carry. Defaults to
	// audit.DefaultTablePrefix, and must match the prefix the migrations were
	// rendered with.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`

	// Retention carries the window entries are kept for and the bounds a sweep
	// of the log runs under.
	Retention audit.RetentionConfig `env:",init" json:"retention,omitzero" yaml:"retention,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in zero fields.
func (cfg *Config) EnsureDefaults() {
	if cfg.TablePrefix == "" {
		cfg.TablePrefix = audit.DefaultTablePrefix
	}

	cfg.Retention.EnsureDefaults()
}

// ValidateWithContext validates a Config struct.
//
// The nested config is validated through a validation.By closure because ozzo
// dereferences a struct-value field before checking ValidatableWithContext, so
// it would otherwise be skipped.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Dialect, validation.Required, validation.By(func(any) error {
			if !cfg.Dialect.Valid() {
				return errors.Wrapf(dialect.ErrUnsupported, "audit dialect %q", cfg.Dialect)
			}

			return nil
		})),
		validation.Field(&cfg.TablePrefix, validation.By(func(any) error {
			return audit.ValidateTablePrefix(cfg.TablePrefix)
		})),
		validation.Field(&cfg.Retention, validation.By(func(any) error {
			return cfg.Retention.ValidateWithContext(ctx)
		})),
	)
}

// NewRecorder builds a Recorder from configuration.
//
// Explicit options run after the config-derived ones, so a caller can still
// override anything, and can register redactions beyond those in the file — see
// WithRecorderOptions.
func NewRecorder(
	ctx context.Context,
	cfg *Config,
	opts ...Option,
) (audit.Recorder, error) {
	if err := prepare(ctx, cfg); err != nil {
		return nil, err
	}

	o := newOptions(opts)

	base := []audit.RecorderOption{audit.WithRecorderTablePrefix(cfg.TablePrefix)}
	if o.logger != nil {
		base = append(base, audit.WithRecorderLogger(o.logger))
	}
	if o.tracerProvider != nil {
		base = append(base, audit.WithRecorderTracerProvider(o.tracerProvider))
	}
	if o.metricsProvider != nil {
		base = append(base, audit.WithRecorderMetricsProvider(o.metricsProvider))
	}

	for resourceType := range cfg.Redactions {
		base = append(base, audit.WithRedaction(resourceType, cfg.Redactions[resourceType]))
	}

	return audit.NewRecorder(cfg.Dialect, append(base, o.recorder...)...)
}

// NewReader builds a Reader from configuration. client must be the database
// holding the audit tables — the same one the Recorder's transactions run
// against.
//
// Explicit options run after the config-derived ones, so a caller can still
// override anything — see WithReaderOptions.
func NewReader(
	ctx context.Context,
	cfg *Config,
	client database.Client,
	opts ...Option,
) (audit.Reader, error) {
	if err := prepare(ctx, cfg); err != nil {
		return nil, err
	}

	o := newOptions(opts)

	base := []audit.ReaderOption{audit.WithReaderTablePrefix(cfg.TablePrefix)}
	if o.logger != nil {
		base = append(base, audit.WithReaderLogger(o.logger))
	}
	if o.tracerProvider != nil {
		base = append(base, audit.WithReaderTracerProvider(o.tracerProvider))
	}
	if o.metricsProvider != nil {
		base = append(base, audit.WithReaderMetricsProvider(o.metricsProvider))
	}

	return audit.NewReader(client, append(base, o.reader...)...)
}

// NewPruneTarget builds the retention target that removes aged entries, for a
// caller assembling a policy of its own — a different name, a different scope
// for the accounting entry, or an age that is not the configured window.
//
// Most callers want NewRetentionPolicy, which wraps this in the policy the
// configuration describes.
func NewPruneTarget(ctx context.Context, cfg *Config) (audit.PruneTarget, error) {
	if err := prepare(ctx, cfg); err != nil {
		return audit.PruneTarget{}, err
	}

	return audit.PruneTarget{
		TablePrefix:   cfg.TablePrefix,
		ScopePageSize: cfg.Retention.ScopePageSize,
	}, nil
}

// NewRetentionPolicy builds the retention.Policy that prunes the audit log,
// for the application to append to the policy set it hands a retention.Sweeper.
//
// The policy's Age is the configured retention window, which is where the
// seven-year default and the one-hour floor live — retention.Policy permits a
// zero age, because against an expires_at column that is a legal grace period,
// and against this log it would mean emptying the table.
//
// Scope is left empty: a fleet-wide sweep belongs to no tenant, and the empty
// scope is the chain platform-level events are recorded in.
func NewRetentionPolicy(ctx context.Context, cfg *Config) (retention.Policy, error) {
	target, err := NewPruneTarget(ctx, cfg)
	if err != nil {
		return retention.Policy{}, err
	}

	return retention.Policy{
		Name:      audit.DefaultRetentionPolicyName,
		Target:    target,
		Age:       cfg.Retention.Retention,
		Basis:     cfg.Retention.Basis,
		BatchSize: cfg.Retention.BatchSize,
	}, nil
}

// prepare defaults and validates a config, shared by every constructor so that
// building one component cannot succeed against a config the next one would
// reject.
func prepare(ctx context.Context, cfg *Config) error {
	if cfg == nil {
		return errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return errors.Wrap(err, "validating audit config")
	}

	return nil
}

/*
Package dataprivacycfg assembles the data privacy machinery from environment
configuration: the Store every part shares, the Service applications submit
through, the Fulfiller that does the work, and the Sweeper that expires.

All four read one Config, so the dialect and table prefix the Service writes to
are by construction the ones the Fulfiller reads and the Sweeper expires.

There is no worker here, and that is the point of the port onto operations. The
loop that used to claim, lease, retry, and back off privacy requests is an
operations.Worker now, configured in operations/config and shared with every
other long-running thing in the application. What this package builds is the two
runners that worker calls, and the Service that starts operations for it.

The registry is not configured here either. Which domains hold data about a
person is Go code — a set of interface implementations — and there is no useful
way to express it in the environment. It is passed explicitly to NewFulfiller.
*/
package dataprivacycfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/audit"
	"github.com/primandproper/platform-go/v13/compression"
	"github.com/primandproper/platform-go/v13/cryptography/encryption"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/dataprivacy"
	"github.com/primandproper/platform-go/v13/dataprivacy/auditerasure"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/operations"
	"github.com/primandproper/platform-go/v13/uploads"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config assembles a dataprivacy Store, Service, Fulfiller, and Sweeper.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// Dialect selects the SQL emitted; it must match the database.Client.
	Dialect dialect.Dialect `env:"DIALECT" json:"dialect,omitempty" yaml:"dialect,omitempty"`

	// TablePrefix names the request table. It must match the prefix the
	// migrations were rendered with. Defaults to
	// dataprivacy.DefaultTablePrefix.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`

	// AuditErasure configures the audit log's own eraser.
	AuditErasure AuditErasureConfig `env:",init" envPrefix:"AUDIT_ERASURE_" json:"auditErasure,omitzero" yaml:"auditErasure,omitempty"`

	// Fulfiller carries the two runners' knobs.
	Fulfiller dataprivacy.FulfillerConfig `env:",init" envPrefix:"FULFILLER_" json:"fulfiller,omitzero" yaml:"fulfiller,omitempty"`

	// Service carries the request state machine's timings.
	Service dataprivacy.ServiceConfig `env:",init" envPrefix:"SERVICE_" json:"service,omitzero" yaml:"service,omitempty"`

	// Sweeper carries the expiry and retention knobs.
	Sweeper dataprivacy.SweeperConfig `env:",init" envPrefix:"SWEEPER_" json:"sweeper,omitzero" yaml:"sweeper,omitempty"`
}

// AuditErasureConfig configures whether, and how, an erasure touches the audit
// log.
//
// It is a config section rather than a plain registration because "do we erase
// our own audit records about this person" is a policy question with a
// different answer per jurisdiction and per deployment, and it should be
// answerable by an operator without a code change.
type AuditErasureConfig struct {
	// TablePrefix is the prefix the audit tables carry. Defaults to
	// audit.DefaultTablePrefix, and must match the audit Recorder's.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`

	// RetentionBasis is the wording recorded against audit entries that cannot
	// be removed. Defaults to auditerasure.DefaultRetentionBasis.
	RetentionBasis string `env:"RETENTION_BASIS" json:"retentionBasis,omitempty" yaml:"retentionBasis,omitempty"`

	// Disabled stops the audit eraser being registered, leaving the audit log
	// entirely untouched by an erasure.
	//
	// The polarity is deliberate: an erasure that silently skipped a store of
	// personal data would be the more surprising default, so the eraser is on
	// unless an operator turns it off. Turning it off is the right call where
	// retention of audit records is mandatory, and the whole reason this is a
	// configuration flag rather than a code change.
	//
	// Note what "on" does and does not mean. The eraser deletes whole audit
	// scopes belonging to the subject and reports everything else as retained —
	// it never deletes entries from the middle of a chain, because that would
	// make audit.Reader.Verify report tampering for the rest of that scope's
	// history. See the dataprivacy/auditerasure package documentation.
	Disabled bool `env:"DISABLED" json:"disabled,omitempty" yaml:"disabled,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in zero fields.
func (cfg *Config) EnsureDefaults() {
	if cfg.TablePrefix == "" {
		cfg.TablePrefix = dataprivacy.DefaultTablePrefix
	}

	if cfg.AuditErasure.TablePrefix == "" {
		cfg.AuditErasure.TablePrefix = audit.DefaultTablePrefix
	}

	if cfg.AuditErasure.RetentionBasis == "" {
		cfg.AuditErasure.RetentionBasis = auditerasure.DefaultRetentionBasis
	}

	cfg.Service.EnsureDefaults()
	cfg.Fulfiller.EnsureDefaults()
	cfg.Sweeper.EnsureDefaults()
}

// ValidateWithContext validates a Config.
//
// The nested configs are validated through validation.By closures because ozzo
// dereferences a struct-value field before checking ValidatableWithContext, so
// they would otherwise be skipped.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Dialect, validation.Required, validation.By(func(any) error {
			if !cfg.Dialect.Valid() {
				return errors.Wrapf(dialect.ErrUnsupported, "dataprivacy dialect %q", cfg.Dialect)
			}

			return nil
		})),
		validation.Field(&cfg.Service, validation.By(func(any) error {
			return cfg.Service.ValidateWithContext(ctx)
		})),
		validation.Field(&cfg.Fulfiller, validation.By(func(any) error {
			return cfg.Fulfiller.ValidateWithContext(ctx)
		})),
		validation.Field(&cfg.Sweeper, validation.By(func(any) error {
			return cfg.Sweeper.ValidateWithContext(ctx)
		})),
	)
}

// prepare fills defaults and validates, which every constructor below does
// first and identically.
func (cfg *Config) prepare(ctx context.Context) error {
	if cfg == nil {
		return errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return errors.Wrap(err, "validating dataprivacy config")
	}

	return nil
}

// NewStore builds the Store every part shares. client must be the database
// holding the request table.
//
// Explicit options run after the config-derived ones, so a caller can still
// override anything.
func NewStore(
	ctx context.Context,
	cfg *Config,
	client database.Client,
	opts ...Option,
) (dataprivacy.Store, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if err := cfg.prepare(ctx); err != nil {
		return nil, err
	}

	base := []dataprivacy.SQLStoreOption{dataprivacy.WithTablePrefix(cfg.TablePrefix)}

	if logger != nil {
		base = append(base, dataprivacy.WithStoreLogger(logger))
	}
	if tracerProvider != nil {
		base = append(base, dataprivacy.WithStoreTracerProvider(tracerProvider))
	}
	if metricsProvider != nil {
		base = append(base, dataprivacy.WithStoreMetricsProvider(metricsProvider))
	}

	store, err := dataprivacy.NewSQLStore(client, append(base, o.store...)...)
	if err != nil {
		return nil, err
	}

	return store, nil
}

// NewService builds the Service applications submit through.
//
// ops must be an operations Service over a registry this package's kinds are
// registered in — see NewFulfiller and dataprivacy.Fulfiller.Register — because
// starting an operation resolves its kind at submission.
//
// Explicit options run after the config-derived ones, so a caller can still
// override anything.
func NewService(
	ctx context.Context,
	cfg *Config,
	store dataprivacy.Store,
	ops operations.Service,
	opts ...Option,
) (dataprivacy.Service, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if err := cfg.prepare(ctx); err != nil {
		return nil, err
	}

	var base []dataprivacy.ServiceOption
	if logger != nil {
		base = append(base, dataprivacy.WithServiceLogger(logger))
	}
	if tracerProvider != nil {
		base = append(base, dataprivacy.WithServiceTracerProvider(tracerProvider))
	}
	if metricsProvider != nil {
		base = append(base, dataprivacy.WithServiceMetricsProvider(metricsProvider))
	}

	svc, err := dataprivacy.NewService(ctx, &cfg.Service, store, ops, append(base, o.service...)...)
	if err != nil {
		return nil, err
	}

	return svc, nil
}

// NewFulfiller builds the Fulfiller behind this package's operation kinds, and
// registers them into registry so an operations.Worker over the same registry
// can run them.
//
// Registering here rather than leaving it to the caller is what keeps the two
// halves of the wiring from drifting: a Fulfiller nobody registered is a set of
// runners nothing calls, and the symptom is a queue of operations failing with
// operations.CodeUnknownKind.
//
// domains is a required argument rather than a config field: which domains hold
// data about a person is Go code. uploader may be nil for an erasure-only
// deployment. encrypted must say whether artifacts are written encrypted — it
// decides whether a notification can carry a download link at all, and it is a
// bool rather than the encryptor itself because this constructor never encrypts
// anything, it only needs to know. Pass the encryptor through EnsurePackaging.
func NewFulfiller(
	ctx context.Context,
	cfg *Config,
	store dataprivacy.Store,
	domains *dataprivacy.Registry,
	registry *operations.Registry,
	uploader uploads.UploadManager,
	encrypted bool,
	opts ...Option,
) (*dataprivacy.Fulfiller, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if err := cfg.prepare(ctx); err != nil {
		return nil, err
	}

	var base []dataprivacy.FulfillerOption
	if uploader != nil {
		// Wired here so a completion notification carries a working link
		// without the caller assembling the signer by hand — and so its TTL is
		// by construction the one Service.Download would have used.
		base = append(base,
			dataprivacy.WithFulfillerUploadManager(uploader),
			dataprivacy.WithFulfillerURLSigner(dataprivacy.NewArtifactURLSigner(
				uploader, cfg.Service.SignedURLTTL, encrypted, o.urlSigner...,
			)),
		)
	}

	if logger != nil {
		base = append(base, dataprivacy.WithFulfillerLogger(logger))
	}
	if tracerProvider != nil {
		base = append(base, dataprivacy.WithFulfillerTracerProvider(tracerProvider))
	}
	if metricsProvider != nil {
		base = append(base, dataprivacy.WithFulfillerMetricsProvider(metricsProvider))
	}

	fulfiller, err := dataprivacy.NewFulfiller(ctx, &cfg.Fulfiller, store, domains, append(base, o.fulfiller...)...)
	if err != nil {
		return nil, err
	}

	if err = fulfiller.Register(registry); err != nil {
		return nil, err
	}

	return fulfiller, nil
}

// NewSweeper builds the Sweeper. Register its Job with a jobs.Scheduler; see
// dataprivacy.Sweeper.Job.
func NewSweeper(
	ctx context.Context,
	cfg *Config,
	store dataprivacy.Store,
	uploader uploads.UploadManager,
	opts ...Option,
) (*dataprivacy.Sweeper, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if err := cfg.prepare(ctx); err != nil {
		return nil, err
	}

	base := []dataprivacy.SweeperOption{dataprivacy.WithSweeperUploadManager(uploader)}
	if logger != nil {
		base = append(base, dataprivacy.WithSweeperLogger(logger))
	}
	if tracerProvider != nil {
		base = append(base, dataprivacy.WithSweeperTracerProvider(tracerProvider))
	}
	if metricsProvider != nil {
		base = append(base, dataprivacy.WithSweeperMetricsProvider(metricsProvider))
	}

	return dataprivacy.NewSweeper(ctx, &cfg.Sweeper, store, append(base, o.sweeper...)...)
}

// RegisterAuditEraser registers the audit log's eraser into registry unless
// AuditErasure.Disabled is set.
//
// It returns whether it registered, so a caller can log which way the policy
// went. "Did this deployment erase audit records" is a question that gets asked
// long after the deployment, and a boolean in a config file somewhere is a poor
// place to have recorded the answer.
func RegisterAuditEraser(
	ctx context.Context,
	cfg *Config,
	registry *dataprivacy.Registry,
	opts ...auditerasure.Option,
) (bool, error) {
	if err := cfg.prepare(ctx); err != nil {
		return false, err
	}

	if cfg.AuditErasure.Disabled {
		return false, nil
	}

	if registry == nil {
		return false, errors.Wrap(errors.ErrNilInputParameter, "nil dataprivacy registry")
	}

	base := []auditerasure.Option{auditerasure.WithRetentionBasis(cfg.AuditErasure.RetentionBasis)}

	eraser, err := auditerasure.New(cfg.Dialect, cfg.AuditErasure.TablePrefix, append(base, opts...)...)
	if err != nil {
		return false, errors.Wrap(err, "building dataprivacy audit eraser")
	}

	if err = registry.RegisterEraser(auditerasure.DefaultKey, eraser); err != nil {
		return false, errors.Wrap(err, "registering dataprivacy audit eraser")
	}

	return true, nil
}

// EnsurePackaging returns the compressor and encryptor pair the Fulfiller
// writes artifacts with and the Service reads them with.
//
// It exists so the two cannot be configured apart. An artifact written with one
// compressor and read with another is unreadable, and the failure surfaces at
// the subject rather than at startup.
func EnsurePackaging(
	compressor compression.Compressor,
	encryptorDecryptor encryption.EncryptorDecryptor,
) (fulfillerOpts []dataprivacy.FulfillerOption, serviceOpts []dataprivacy.ServiceOption) {
	if compressor != nil {
		fulfillerOpts = append(fulfillerOpts, dataprivacy.WithFulfillerCompressor(compressor))
		serviceOpts = append(serviceOpts, dataprivacy.WithServiceCompressor(compressor))
	}

	if encryptorDecryptor != nil {
		fulfillerOpts = append(fulfillerOpts, dataprivacy.WithFulfillerEncryptor(encryptorDecryptor))
		serviceOpts = append(serviceOpts, dataprivacy.WithServiceDecryptor(encryptorDecryptor))
	}

	return fulfillerOpts, serviceOpts
}

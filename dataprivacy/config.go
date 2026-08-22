package dataprivacy

import (
	"context"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// DefaultResponseWindow is how long a request may take before it is
	// overdue.
	//
	// Thirty days is GDPR's window; CCPA allows forty-five. The stricter of the
	// two is the default because a deadline that is too early produces a gauge
	// somebody looks at, and one that is too late produces a fine.
	DefaultResponseWindow = 30 * 24 * time.Hour

	// DefaultArtifactTTL is how long an export artifact survives before the
	// sweeper deletes it.
	//
	// Seven days. The artifact contains everything an application knows about a
	// person, and the single worst outcome available to this package is leaving
	// one in a bucket indefinitely. Long enough that somebody on holiday can
	// still fetch it; short enough that it is not a permanent object.
	DefaultArtifactTTL = 7 * 24 * time.Hour

	// DefaultSignedURLTTL is how long a download URL is valid. Minutes, not
	// days: the link is mailed to the subject and mail is not a confidential
	// channel, so the window in which an intercepted link is useful should be
	// the window in which somebody clicks it.
	DefaultSignedURLTTL = 15 * time.Minute

	// DefaultArtifactPathPrefix is the storage prefix artifacts are written
	// under.
	DefaultArtifactPathPrefix = "dataprivacy/exports"

	// DefaultCollectorConcurrency is how many of one request's collectors run at
	// once.
	DefaultCollectorConcurrency = 4

	// DefaultCollectorTimeout bounds one collector. It exists so that one slow
	// domain costs its own section rather than the whole export — which is the
	// entire reason collection is per-key.
	DefaultCollectorTimeout = 30 * time.Second

	// DefaultFulfillmentTimeout bounds one whole attempt at one request.
	DefaultFulfillmentTimeout = 10 * time.Minute

	// DefaultMaxAttempts is how many times an operation fulfilling a privacy
	// request may be claimed before it is failed.
	//
	// Three, which is lower than the operations worker's own default of five,
	// and lower on purpose. One attempt here is a fan-out over every registered
	// domain against the application's own database, so the attempts are
	// expensive; and a request that is going to fail is worth failing early,
	// while there is still time inside the statutory window for somebody to fix
	// the cause and for the subject to be served.
	DefaultMaxAttempts = 3

	// DefaultMaxDocumentBytes caps the assembled export before it is written.
	//
	// A collector that answers a bad subject ID with its entire table is a bug
	// that presents as an out-of-memory kill in the worker, taking every other
	// in-flight operation with it. Failing the one request loudly is better.
	DefaultMaxDocumentBytes int64 = 512 << 20 // 512 MiB

	// DefaultSweepInterval is the recommended cadence for running the Sweeper.
	//
	// It is a suggestion for the caller's scheduler, not something this package
	// acts on: the Sweeper has no ticker of its own and does one pass per call.
	// It was previously also a config field, which read as though setting it made
	// the Sweeper run on that interval — nothing ever did.
	DefaultSweepInterval = time.Hour

	// DefaultSweepBatchSize caps how much one sweep tick does.
	DefaultSweepBatchSize = 100

	// DefaultRequestRetention is how long a terminal request record is kept
	// before the sweeper reaps it.
	//
	// A record of a privacy request is itself personal data — it says that a
	// named person asked, and when — so keeping it forever is the mistake this
	// package would otherwise make on every consumer's behalf. Three years
	// outlasts any plausible dispute about whether a request was honored while
	// not being indefinite.
	DefaultRequestRetention = 3 * 365 * 24 * time.Hour
)

// ServiceConfig configures the request state machine's timings.
type ServiceConfig struct {
	// ExportResponseWindow is how long an export may take before it counts as
	// overdue. Defaults to DefaultResponseWindow.
	ExportResponseWindow time.Duration `env:"EXPORT_RESPONSE_WINDOW" json:"exportResponseWindow,omitempty" yaml:"exportResponseWindow,omitempty"`

	// ErasureResponseWindow is the same for erasures. Separate from the export
	// window because the jurisdictions that distinguish them give erasure the
	// longer one, and a single knob would force the stricter deadline onto both.
	ErasureResponseWindow time.Duration `env:"ERASURE_RESPONSE_WINDOW" json:"erasureResponseWindow,omitempty" yaml:"erasureResponseWindow,omitempty"`

	// ConfirmationWindow is how long an erasure waits for confirmation before
	// it is cancelled. Zero — the default — means erasures are queued on
	// submission and Confirm is never needed.
	//
	// Turning it on is the difference between an accidental erasure being a
	// support ticket and being unrecoverable. Regulation generally permits a
	// verification step, and the failure mode it prevents is the only one in
	// this package that cannot be undone.
	ConfirmationWindow time.Duration `env:"CONFIRMATION_WINDOW" json:"confirmationWindow,omitempty" yaml:"confirmationWindow,omitempty"`

	// SignedURLTTL is how long a download URL is valid. Defaults to
	// DefaultSignedURLTTL.
	SignedURLTTL time.Duration `env:"SIGNED_URL_TTL" json:"signedURLTTL,omitempty" yaml:"signedURLTTL,omitempty"`
}

var _ validation.ValidatableWithContext = (*ServiceConfig)(nil)

// EnsureDefaults fills unset knobs with the package defaults.
func (cfg *ServiceConfig) EnsureDefaults() {
	if cfg.ExportResponseWindow <= 0 {
		cfg.ExportResponseWindow = DefaultResponseWindow
	}
	if cfg.ErasureResponseWindow <= 0 {
		cfg.ErasureResponseWindow = DefaultResponseWindow
	}
	if cfg.SignedURLTTL <= 0 {
		cfg.SignedURLTTL = DefaultSignedURLTTL
	}
}

// ValidateWithContext validates a ServiceConfig.
func (cfg *ServiceConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.ExportResponseWindow, validation.Required),
		validation.Field(&cfg.ErasureResponseWindow, validation.Required),
		validation.Field(&cfg.SignedURLTTL, validation.Required),
	)
}

// responseWindow returns the deadline window for a request type.
func (cfg *ServiceConfig) responseWindow(t RequestType) time.Duration {
	if t == RequestErasure {
		return cfg.ErasureResponseWindow
	}

	return cfg.ExportResponseWindow
}

// FulfillerConfig configures the two operation runners.
//
// It is most of what it used to be minus a whole category of knob. The poll
// interval, the batch size, the request concurrency, the lease, and the backoff
// schedule are the operations worker's now — one worker, one set of numbers,
// shared with every other long-running thing in the application — and this is
// left with the settings that are genuinely about privacy requests.
type FulfillerConfig struct {
	// ArtifactPathPrefix is the storage prefix artifacts are written under.
	// Defaults to DefaultArtifactPathPrefix.
	ArtifactPathPrefix string `env:"ARTIFACT_PATH_PREFIX" json:"artifactPathPrefix,omitempty" yaml:"artifactPathPrefix,omitempty"`

	// FulfillmentTimeout bounds one whole attempt at one request.
	//
	// It matters more than it looks now that the operation's lease is extended
	// by every progress flush: the lease no longer bounds anything, so this is
	// what stands between a wedged domain and an operation that never reaches a
	// terminal state.
	FulfillmentTimeout time.Duration `env:"FULFILLMENT_TIMEOUT" json:"fulfillmentTimeout,omitempty" yaml:"fulfillmentTimeout,omitempty"`

	// CollectorTimeout bounds one collector, so one slow domain costs its own
	// section rather than the export.
	CollectorTimeout time.Duration `env:"COLLECTOR_TIMEOUT" json:"collectorTimeout,omitempty" yaml:"collectorTimeout,omitempty"`

	// ArtifactTTL is how long an export artifact survives after completion,
	// stamped onto the request as ExpiresAt when the export is recorded.
	// Defaults to DefaultArtifactTTL.
	ArtifactTTL time.Duration `env:"ARTIFACT_TTL" json:"artifactTTL,omitempty" yaml:"artifactTTL,omitempty"`

	// MaxDocumentBytes caps the assembled export. Defaults to
	// DefaultMaxDocumentBytes.
	MaxDocumentBytes int64 `env:"MAX_DOCUMENT_BYTES" json:"maxDocumentBytes,omitempty" yaml:"maxDocumentBytes,omitempty"`

	// MaxAttempts is how many times an operation of either kind may be claimed
	// before it is failed. It becomes operations.Definition.MaxAttempts, and
	// zero means the operations worker's own ceiling.
	//
	// It is set here rather than left to that ceiling because a privacy request
	// is not a webhook replay: one attempt is a fan-out over every registered
	// domain, and the default is deliberately low so that a request which is
	// going to fail says so within the statutory window rather than at the end
	// of it. See DefaultMaxAttempts.
	MaxAttempts int `env:"MAX_ATTEMPTS" json:"maxAttempts,omitempty" yaml:"maxAttempts,omitempty"`

	// CollectorConcurrency is how many of one request's collectors run at once.
	//
	// It is bounded rather than unlimited because every collector queries the
	// application's own database, and a subject present in forty domains would
	// otherwise open forty concurrent queries on behalf of one background job.
	CollectorConcurrency int `env:"COLLECTOR_CONCURRENCY" json:"collectorConcurrency,omitempty" yaml:"collectorConcurrency,omitempty"`
}

var _ validation.ValidatableWithContext = (*FulfillerConfig)(nil)

// EnsureDefaults fills unset knobs with the package defaults.
func (cfg *FulfillerConfig) EnsureDefaults() {
	if cfg.FulfillmentTimeout <= 0 {
		cfg.FulfillmentTimeout = DefaultFulfillmentTimeout
	}
	if cfg.CollectorTimeout <= 0 {
		cfg.CollectorTimeout = DefaultCollectorTimeout
	}
	if cfg.ArtifactTTL <= 0 {
		cfg.ArtifactTTL = DefaultArtifactTTL
	}
	if cfg.ArtifactPathPrefix == "" {
		cfg.ArtifactPathPrefix = DefaultArtifactPathPrefix
	}
	if cfg.MaxDocumentBytes <= 0 {
		cfg.MaxDocumentBytes = DefaultMaxDocumentBytes
	}
	if cfg.MaxAttempts <= 0 {
		cfg.MaxAttempts = DefaultMaxAttempts
	}
	if cfg.CollectorConcurrency <= 0 {
		cfg.CollectorConcurrency = DefaultCollectorConcurrency
	}
}

// ValidateWithContext validates a FulfillerConfig.
func (cfg *FulfillerConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.CollectorConcurrency, validation.Required, validation.Min(1)),
		validation.Field(&cfg.CollectorTimeout, validation.Required),
		validation.Field(&cfg.FulfillmentTimeout, validation.Required, validation.By(func(any) error {
			// One collector must be able to finish inside the bound on the whole
			// request. The reverse ordering produces an export that times out
			// while its first domain is still within its own allowance, which
			// reads as a broken collector rather than a mis-sized config.
			if cfg.FulfillmentTimeout <= cfg.CollectorTimeout {
				return platformerrors.Newf(
					"dataprivacy fulfillment timeout %s must exceed collector timeout %s",
					cfg.FulfillmentTimeout, cfg.CollectorTimeout,
				)
			}

			return nil
		})),
		validation.Field(&cfg.ArtifactTTL, validation.Required),
		validation.Field(&cfg.ArtifactPathPrefix, validation.Required),
		validation.Field(&cfg.MaxAttempts, validation.Required, validation.Min(1)),
	)
}

// SweeperConfig configures the expiry, lapse, and retention sweeps.
type SweeperConfig struct {
	// RequestRetention is how long a terminal request record is kept. Defaults
	// to DefaultRequestRetention.
	RequestRetention time.Duration `env:"REQUEST_RETENTION" json:"requestRetention,omitempty" yaml:"requestRetention,omitempty"`

	// BatchSize caps how much one sweep tick does, so a long-neglected table is
	// trimmed over several passes instead of one statement that holds locks for
	// minutes.
	BatchSize int `env:"BATCH_SIZE" json:"batchSize,omitempty" yaml:"batchSize,omitempty"`

	// DisableReap stops the sweeper deleting terminal request records.
	//
	// It exists because "how long do we keep the record that somebody asked" is
	// a jurisdiction's answer and not a library's, and an operator whose answer
	// is "forever, and we will argue about it later" should be able to say so
	// without setting a retention of a hundred years.
	DisableReap bool `env:"DISABLE_REAP" json:"disableReap,omitempty" yaml:"disableReap,omitempty"`
}

var _ validation.ValidatableWithContext = (*SweeperConfig)(nil)

// EnsureDefaults fills unset knobs with the package defaults.
func (cfg *SweeperConfig) EnsureDefaults() {
	if cfg.RequestRetention <= 0 {
		cfg.RequestRetention = DefaultRequestRetention
	}
	if cfg.BatchSize <= 0 {
		cfg.BatchSize = DefaultSweepBatchSize
	}
}

// ValidateWithContext validates a SweeperConfig.
func (cfg *SweeperConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.RequestRetention, validation.Required),
		validation.Field(&cfg.BatchSize, validation.Required, validation.Min(1)),
	)
}

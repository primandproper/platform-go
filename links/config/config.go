/*
Package linkscfg assembles a links.Minter from environment configuration.

The action registry is the one part that does not come from the environment.
Where a magic-login link points and how long it lives is a security policy, not
a deployment knob, and it belongs in a file somebody reviews — see Config.Actions.
*/
package linkscfg

import (
	"context"
	"time"

	cachecfg "github.com/primandproper/platform-go/v13/cache/config"
	"github.com/primandproper/platform-go/v13/database"
	distributedlockcfg "github.com/primandproper/platform-go/v13/distributedlock/config"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/links"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config assembles a links.Minter from environment configuration.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// Actions declares the links this deployment can mint, keyed by action.
	//
	// It carries no env tag, and that is deliberate rather than a limitation of
	// the encoding. A URL and a lifetime per action have no reasonable flat
	// environment spelling, and more to the point this is the file where
	// "password reset links live for one hour and point at this host" is
	// written down — a decision that should appear in a diff and be reviewed,
	// not one that should be adjustable by whoever can edit a deployment
	// variable.
	//
	// A caller assembling actions in code should use WithMinterOptions and
	// links.WithAction instead; the two compose, and the explicit options win.
	Actions map[links.Action]links.ActionPolicy `json:"actions,omitempty" yaml:"actions,omitempty"`

	// KeyPrefix namespaces store and lock keys.
	KeyPrefix string `env:"KEY_PREFIX" json:"keyPrefix,omitempty" yaml:"keyPrefix,omitempty"`

	// Lock configures the locker that makes redemption single-use. It has no
	// safe default: the noop provider acquires unconditionally, which leaves
	// every sequential test passing and lets two concurrent redemptions of one
	// token both succeed.
	Lock distributedlockcfg.Config `env:",init" envPrefix:"LOCK_" json:"lock,omitzero" yaml:"lock,omitempty"`

	// Cache configures the record store. Use the redis provider in production:
	// the memory provider is per-process, so a link minted by one replica does
	// not exist for the next.
	Cache cachecfg.Config `env:",init" envPrefix:"CACHE_" json:"cache,omitzero" yaml:"cache,omitempty"`

	// Retention is how long a resolved link stays in the store after it stops
	// working, and so how long redemption can still say why it failed.
	Retention time.Duration `env:"RETENTION" json:"retention,omitempty" yaml:"retention,omitempty"`

	// TokenBytes is how many random bytes a token carries before encoding.
	TokenBytes int `env:"TOKEN_BYTES" json:"tokenBytes,omitempty" yaml:"tokenBytes,omitempty"`

	// MaxTokenLength bounds what a redemption will hash.
	MaxTokenLength int `env:"MAX_TOKEN_LENGTH" json:"maxTokenLength,omitempty" yaml:"maxTokenLength,omitempty"`

	// AllowInsecureURLs permits http action URLs against hosts that are not
	// loopback, which hands the token to every hop between the mail client and
	// the application. Loopback http already works without it — see
	// links.WithInsecureURLs.
	AllowInsecureURLs bool `env:"ALLOW_INSECURE_URLS" json:"allowInsecureURLs,omitempty" yaml:"allowInsecureURLs,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in zero fields.
//
// Nothing here defaults an action's TTL. links.ActionPolicy has no default
// lifetime on purpose, and inventing one at the configuration layer would put
// it back exactly where it was rejected.
func (cfg *Config) EnsureDefaults() {
	if cfg.Retention == 0 {
		cfg.Retention = links.DefaultRetention
	}
	if cfg.TokenBytes == 0 {
		cfg.TokenBytes = links.DefaultTokenBytes
	}
	if cfg.MaxTokenLength == 0 {
		cfg.MaxTokenLength = links.DefaultMaxTokenLength
	}
	if cfg.KeyPrefix == "" {
		cfg.KeyPrefix = links.DefaultKeyPrefix
	}
}

// ValidateWithContext validates a Config struct.
//
// The action policies are not validated here. NewMinter validates them against
// the insecure-URL setting, which is where the whole registry is visible at
// once — including the actions a caller added through WithMinterOptions, which
// this Config has never seen.
//
// The nested configs are validated through validation.By closures because ozzo
// dereferences a struct-value field before checking ValidatableWithContext, so
// it would otherwise skip them.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Retention, validation.Min(time.Duration(0))),
		validation.Field(&cfg.TokenBytes, validation.Min(0)),
		validation.Field(&cfg.MaxTokenLength, validation.Min(0)),
		validation.Field(&cfg.Cache, validation.By(func(any) error {
			return cfg.Cache.ValidateWithContext(ctx)
		})),
		validation.Field(&cfg.Lock, validation.By(func(any) error {
			return cfg.Lock.ValidateWithContext(ctx)
		})),
	)
}

// NewMinter builds a links.Minter from configuration.
//
// db is required only when the lock provider is postgres; pass nil otherwise.
func NewMinter(
	ctx context.Context,
	cfg *Config,
	db database.Client,
	opts ...Option,
) (*links.Minter, error) {
	o := newOptions(opts)

	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating links config")
	}

	store, err := cachecfg.NewCache[links.Record](ctx, &cfg.Cache,
		cachecfg.WithLogger(o.logger),
		cachecfg.WithTracerProvider(o.tracerProvider),
		cachecfg.WithMetricsProvider(o.metricsProvider))
	if err != nil {
		return nil, errors.Wrap(err, "building action link store")
	}

	locker, err := distributedlockcfg.NewScopedLocker(ctx, &cfg.Lock, db,
		distributedlockcfg.WithLogger(o.logger),
		distributedlockcfg.WithTracerProvider(o.tracerProvider),
		distributedlockcfg.WithMetricsProvider(o.metricsProvider))
	if err != nil {
		return nil, errors.Wrap(err, "building action link locker")
	}

	minterOpts := []links.Option{
		links.WithActions(cfg.Actions),
		links.WithKeyPrefix(cfg.KeyPrefix),
		links.WithRetention(cfg.Retention),
		links.WithTokenBytes(cfg.TokenBytes),
		links.WithMaxTokenLength(cfg.MaxTokenLength),
		links.WithLogger(o.logger),
		links.WithTracerProvider(o.tracerProvider),
		links.WithMetricsProvider(o.metricsProvider),
	}

	// Conditional rather than always applied: links.WithInsecureURLs takes no
	// argument, so there is no value of it that means "keep requiring https".
	if cfg.AllowInsecureURLs {
		minterOpts = append(minterOpts, links.WithInsecureURLs())
	}

	// Caller options are appended last so they win over anything configured.
	return links.NewMinter(store, locker, append(minterOpts, o.minter...)...)
}

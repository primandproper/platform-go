/*
Package linkscfg assembles a links.Minter from environment configuration.
Records live in a SQL table, and there is nothing to select between.

There was a cache provider and it was withdrawn — links/doc.go carries the
argument, which is that a cache reads by key, the key has to be the digest of
the token, and a credential store that cannot enumerate a person's outstanding
links by subject is missing the write a password reset, a locked account and an
erasure each need. What survives here is the consequence: this package needs a
database.Client and nothing else. No cache, no lock service, and no PROVIDER to
get wrong.

The action registry is the one part that does not come from the environment.
Where a magic-login link points and how long it lives is a security policy, not
a deployment knob, and it belongs in a file somebody reviews — see Config.Actions.
*/
package linkscfg

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v14/links"
	linksdatabase "github.com/primandproper/platform-go/v14/links/database"

	"github.com/primandproper/primitives-go/v2/config/cfgnorm"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/pointer"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// DefaultSweepInterval is how often the store removes rows past their purge
// deadline, when nothing says otherwise.
const DefaultSweepInterval = 5 * time.Minute

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

	// SweepInterval is how often the store removes rows past their purge
	// deadline. Unset takes DefaultSweepInterval; zero starts no sweeper.
	//
	// Starting no sweeper is right when a scheduler calls Sweep instead — one
	// sweep for the fleet rather than one per replica — and wrong when nothing
	// else does, since the table then grows by a row for every link ever
	// minted. That asymmetry is why the sweeper is what an unconfigured Config
	// gets, and why the pointer is here: unset and zero are different answers,
	// and a time.Duration has only one way to say both.
	//
	// In the environment that is an absent SWEEP_INTERVAL against
	// SWEEP_INTERVAL=0.
	SweepInterval *time.Duration `env:"SWEEP_INTERVAL" json:"sweepInterval,omitempty" yaml:"sweepInterval,omitempty"`

	// Database configures the store. The dialect comes from the
	// database.Client rather than from here.
	Database linksdatabase.Config `env:",init" envPrefix:"DATABASE_" json:"database,omitzero" yaml:"database,omitempty"`

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
//
// SweepInterval is the one field where defaulting requires a pointer: only a
// nil is unset, so a zero reaching this method is a deployment asking for no
// sweeper and is left alone.
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

	cfg.SweepInterval = cfgnorm.EnsureSweepInterval(cfg.SweepInterval, DefaultSweepInterval)
}

// ValidateWithContext validates a Config struct.
//
// The action policies are not validated here. NewMinter validates them against
// the insecure-URL setting, which is where the whole registry is visible at
// once — including the actions a caller added through WithMinterOptions, which
// this Config has never seen.
//
// The store's config is validated unconditionally, where it used to be skipped
// under a provider that did not select it. There is one store now, so its rules
// always apply and a bad table prefix is a startup failure rather than a field
// nothing reads.
//
// It is validated through a validation.By closure because ozzo dereferences a
// struct-value field before checking ValidatableWithContext, so it would
// otherwise be skipped.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Retention, validation.Min(time.Duration(0))),
		validation.Field(&cfg.TokenBytes, validation.Min(0)),
		validation.Field(&cfg.MaxTokenLength, validation.Min(0)),
		validation.Field(&cfg.Database,
			validation.By(func(any) error { return cfg.Database.ValidateWithContext(ctx) })),
		validation.Field(&cfg.SweepInterval, cfgnorm.SweepIntervalRule),
	)
}

// NewMinter builds a links.Minter from configuration.
//
// db is required. It was optional while a cache provider existed and a
// deployment could store links without a database at all; nothing reaches a
// working Minter without one now, so a nil client is refused here rather than
// at the first mint.
func NewMinter(
	ctx context.Context,
	cfg *Config,
	db database.Client,
	opts ...Option,
) (*links.Minter, error) {
	o := newOptions(opts)

	if cfg == nil || db == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating links config")
	}

	// The store is held as the concrete *linksdatabase.Store its constructor
	// returns, and reaches links.NewMinter only past this error check. A
	// constructor that returned it into a links.Store variable would convert a
	// nil pointer into a non-nil interface on the failure path — the hazard the
	// old provider switch spent a comment on, and one this shape cannot have.
	store, err := linksdatabase.New(&cfg.Database, db, append([]linksdatabase.Option{
		linksdatabase.WithLogger(o.logger),
		linksdatabase.WithTracerProvider(o.tracerProvider),
		linksdatabase.WithMetricsProvider(o.metricsProvider),
		// Bound to the caller's context: the sweep stops when whatever scope
		// owns this Minter does.
		linksdatabase.WithSweeper(ctx, pointer.Dereference(cfg.SweepInterval)),
	}, o.databaseStore...)...)
	if err != nil {
		return nil, err
	}

	minterOpts := []links.Option{
		links.WithActions(cfg.Actions),
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
	return links.NewMinter(store, append(minterOpts, o.minter...)...)
}

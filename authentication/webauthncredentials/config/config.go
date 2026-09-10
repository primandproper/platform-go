package webauthncredentialscfg

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/webauthncredentials"

	"github.com/primandproper/primitives-go/authentication/webauthn"
	webauthncfg "github.com/primandproper/primitives-go/authentication/webauthn/config"
	"github.com/primandproper/primitives-go/config/cfgnorm"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/pointer"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderDatabase stores ceremony state in a SQL table. The default.
	ProviderDatabase = "database"
	// ProviderCache stores ceremony state in a cache — redis for a fleet,
	// memory for tests and single-process services.
	ProviderCache = "cache"
)

// providers are every provider this package implements. Validation and
// NewSessionStore both read it.
var providers = []string{ProviderDatabase, ProviderCache}

// DefaultSweepInterval is how often the database provider removes rows whose
// deadlines have passed, when nothing says otherwise. It is ignored by the
// cache provider, which reclaims its own entries.
const DefaultSweepInterval = 5 * time.Minute

// Config assembles a webauthn.RelyingParty and its ceremony store from
// environment configuration, over either provider.
//
// The embedded webauthncfg.Config carries no env tag, so its fields are promoted
// into this one at this struct's own prefix — the relying party is read from RP_
// and the cache from CACHE_ exactly as they were before the two halves were
// separate structs. JSON and TOML promote an embedded struct on their own; YAML
// does not, which is what the `yaml:",inline"` is for.
type Config struct {
	// SweepInterval is how often the database provider removes rows whose
	// deadlines have passed. It is ignored by the cache provider. Unset takes
	// DefaultSweepInterval; zero starts no sweeper.
	//
	// Starting no sweeper is right when a scheduler calls Sweep instead — one
	// sweep for the fleet rather than one per replica — and wrong when nothing
	// else does, since the table then grows by a row for every ceremony ever
	// begun. That asymmetry is why the sweeper is what an unconfigured database
	// Config gets, and why the pointer is here: unset and zero are different
	// answers, and a time.Duration has only one way to say both.
	//
	// In the environment that is an absent SWEEP_INTERVAL against
	// SWEEP_INTERVAL=0.
	SweepInterval *time.Duration `env:"SWEEP_INTERVAL" json:"sweepInterval,omitempty" yaml:"sweepInterval,omitempty"`

	// Provider selects where ceremony state lives: database or cache.
	Provider string `env:"PROVIDER" envDefault:"database" json:"provider,omitempty" yaml:"provider,omitempty"`

	// Database configures the store when Provider is database. The dialect
	// comes from the database.Client rather than from here.
	Database           webauthncredentials.Config `env:",init"    envPrefix:"DATABASE_" json:"database,omitzero" yaml:"database,omitempty"`
	webauthncfg.Config `yaml:",inline"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in zero fields, including the embedded config's own.
//
// SweepInterval is defaulted here so that a Config reads as what it will
// actually do, and needs a pointer to do it: only a nil is unset, so a zero
// reaching this method is a deployment asking for no sweeper and is left alone.
// It is left alone under the cache provider too, which has nothing to sweep.
func (cfg *Config) EnsureDefaults() {
	if cfg.Provider == "" {
		cfg.Provider = ProviderDatabase
	}

	cfg.Config.EnsureDefaults()

	if cfg.provider() == ProviderDatabase {
		cfg.SweepInterval = cfgnorm.EnsureSweepInterval(cfg.SweepInterval, DefaultSweepInterval)
	}
}

// ValidateWithContext validates a Config struct.
//
// The sub-config for a provider that was not selected is skipped rather than
// merely unguarded: ozzo validates any non-nil pointer to a Validatable once a
// field's rules have run, and `env:",init"` leaves every sub-config populated.
// Validating the cache's rules under the database provider would make a
// perfectly good database configuration unloadable — which is why the embedded
// config's own method is not called wholesale here: it validates the cache
// unconditionally, being the half for which the cache is the only store.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Provider, validation.Required, validation.By(func(any) error {
			// Checked normalized, matching dispatch: validating the raw string
			// would reject "Database" and " cache " while NewSessionStore built
			// them.
			_, err := cfgnorm.SelectProvider(cfg.Provider, providers, "webauthn session store provider")

			return err
		})),
		validation.Field(&cfg.RelyingParty,
			validation.By(func(any) error { return cfg.RelyingParty.ValidateWithContext(ctx) })),
		validation.Field(&cfg.Database,
			validation.Skip.When(cfg.provider() != ProviderDatabase),
			validation.By(func(any) error { return cfg.Database.ValidateWithContext(ctx) })),
		validation.Field(&cfg.Cache,
			validation.Skip.When(cfg.provider() != ProviderCache),
			validation.By(func(any) error { return cfg.Cache.ValidateWithContext(ctx) })),
		validation.Field(&cfg.SweepInterval, cfgnorm.SweepIntervalRule),
	)
}

// provider normalizes the configured provider name, so that trailing whitespace
// out of an environment file is not a different provider.
func (cfg *Config) provider() string {
	return cfgnorm.Provider(cfg.Provider)
}

// NewSessionStore builds the ceremony store the configured provider names.
//
// db is required only when the provider is database; pass nil otherwise.
func NewSessionStore(
	ctx context.Context,
	cfg *Config,
	db database.Client,
	opts ...Option,
) (webauthn.SessionStore, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	o := newOptions(opts)

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating webauthn config")
	}

	return newSessionStore(ctx, cfg, db, o)
}

// newSessionStore selects and builds the storage the configured provider names.
//
// An unrecognized provider is an error rather than a working-looking default:
// see the package documentation on why the fallback would be the worst of the
// two providers rather than either of them.
func newSessionStore(
	ctx context.Context,
	cfg *Config,
	db database.Client,
	o *options,
) (webauthn.SessionStore, error) {
	switch cfg.provider() {
	case ProviderDatabase:
		// Built into a variable and returned only once err is known to be nil:
		// the constructor hands back its own concrete type, and returning it
		// straight through would convert a nil pointer into a non-nil
		// webauthn.SessionStore on the error path — a value that passes a
		// caller's nil check and panics on the first Save.
		store, err := webauthncredentials.NewSessionStore(&cfg.Database, db, append([]webauthncredentials.Option{
			webauthncredentials.WithLogger(o.logger),
			webauthncredentials.WithTracerProvider(o.tracerProvider),
			webauthncredentials.WithMetricsProvider(o.metricsProvider),
			// Bound to the caller's context: the sweep stops when whatever
			// scope owns this store does.
			webauthncredentials.WithSweeper(ctx, pointer.Dereference(cfg.SweepInterval)),
		}, o.databaseStore...)...)
		if err != nil {
			return nil, err
		}

		return store, nil
	case ProviderCache:
		// The primitive half owns the cache-backed store, so this branch is a
		// delegation rather than a second assembly.
		return webauthncfg.NewSessionStore(ctx, &cfg.Config, o.relyingPartyOptions()...)
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "webauthn session store provider %q", cfg.Provider)
	}
}

// NewRelyingParty builds the relying party and the ceremony store under it.
//
// db is required only when the provider is database; pass nil otherwise. The
// relying party itself is webauthncfg's to build — this function's contribution
// is choosing the store to hand it.
func NewRelyingParty(
	ctx context.Context,
	cfg *Config,
	db database.Client,
	opts ...Option,
) (*webauthn.RelyingParty, error) {
	store, err := NewSessionStore(ctx, cfg, db, opts...)
	if err != nil {
		return nil, err
	}

	o := newOptions(opts)

	return webauthncfg.NewRelyingParty(ctx, &cfg.Config, store, o.relyingPartyOptions()...)
}

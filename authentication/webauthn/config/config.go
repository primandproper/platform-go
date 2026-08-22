// Package webauthncfg assembles a WebAuthn relying party, and the ceremony
// store under it, from environment configuration. Ceremony state lives either
// in a SQL table — the default — or in a cache.
//
// The default is the table, which is the opposite of sessionscfg's. The reason
// is what the alternative defaults to: an unconfigured cache is a memory cache,
// and a memory cache holds a challenge on the replica that issued it, so the
// login that answers on another replica fails. A database provider with no
// client refuses to start; a cache provider with the wrong cache starts, works
// on one replica, and fails a fraction of logins on two. Between a loud failure
// and a quiet one, the loud one is the default.
package webauthncfg

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/webauthn"
	webauthncache "github.com/primandproper/platform-go/v13/authentication/webauthn/cache"
	webauthndatabase "github.com/primandproper/platform-go/v13/authentication/webauthn/database"
	cachecfg "github.com/primandproper/platform-go/v13/cache/config"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/cfgnorm"

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
// environment configuration.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// Provider selects where ceremony state lives: database or cache.
	Provider string `env:"PROVIDER" envDefault:"database" json:"provider,omitempty" yaml:"provider,omitempty"`

	// RelyingParty is the WebAuthn relying party itself — the domain, the
	// display name, the permitted origins, and the ceremony deadline.
	RelyingParty webauthn.Config `env:",init" envPrefix:"RP_" json:"relyingParty,omitzero" yaml:"relyingParty,omitempty"`

	// Database configures the store when Provider is database. The dialect
	// comes from the database.Client rather than from here.
	Database webauthndatabase.Config `env:",init" envPrefix:"DATABASE_" json:"database,omitzero" yaml:"database,omitempty"`

	// Cache configures the store when Provider is cache. Use the redis
	// provider: the memory provider is per-process, so a challenge issued by
	// one replica cannot be answered on another.
	Cache cachecfg.Config `env:",init" envPrefix:"CACHE_" json:"cache,omitzero" yaml:"cache,omitempty"`

	// SweepInterval is how often the database provider removes rows whose
	// deadlines have passed. It is ignored by the cache provider.
	//
	// A non-positive value starts no sweeper, which is right when a scheduler
	// calls Sweep instead — one sweep for the fleet rather than one per replica
	// — and wrong when nothing else does, since the table then grows by a row
	// for every ceremony ever begun.
	SweepInterval time.Duration `env:"SWEEP_INTERVAL" json:"sweepInterval,omitempty" yaml:"sweepInterval,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in zero fields, including the relying party's own.
func (cfg *Config) EnsureDefaults() {
	if cfg.Provider == "" {
		cfg.Provider = ProviderDatabase
	}

	cfg.RelyingParty.EnsureDefaults()

	if cfg.SweepInterval == 0 && cfg.provider() == ProviderDatabase {
		cfg.SweepInterval = DefaultSweepInterval
	}
}

// ValidateWithContext validates a Config struct.
//
// The sub-config for a provider that was not selected is skipped rather than
// merely unguarded: ozzo validates any non-nil pointer to a Validatable once a
// field's rules have run, and `env:",init"` leaves every sub-config populated.
// Validating the cache's rules under the database provider would make a
// perfectly good database configuration unloadable.
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
	// Every branch builds into store and returns it only once err is known to
	// be nil. Both constructors hand back their own concrete type, and
	// returning one straight through would convert a nil pointer into a
	// non-nil webauthn.SessionStore on the error path — a value that passes a
	// caller's nil check and panics on the first Save.
	var (
		store webauthn.SessionStore
		err   error
	)

	switch cfg.provider() {
	case ProviderDatabase:
		store, err = webauthndatabase.NewSessionStore(&cfg.Database, db, append([]webauthndatabase.Option{
			webauthndatabase.WithLogger(o.logger),
			webauthndatabase.WithTracerProvider(o.tracerProvider),
			webauthndatabase.WithMetricsProvider(o.metricsProvider),
			// Bound to the caller's context: the sweep stops when whatever
			// scope owns this store does.
			webauthndatabase.WithSweeper(ctx, cfg.SweepInterval),
		}, o.databaseStore...)...)
	case ProviderCache:
		// cacheErr rather than the err above: this one is returned on the spot,
		// and := here would shadow err for the rest of the case, leaving the
		// store assignment below writing to a variable nothing reads.
		c, cacheErr := cachecfg.NewCache[webauthn.SessionData](ctx, &cfg.Cache,
			cachecfg.WithLogger(o.logger),
			cachecfg.WithTracerProvider(o.tracerProvider),
			cachecfg.WithMetricsProvider(o.metricsProvider))
		if cacheErr != nil {
			return nil, errors.Wrap(cacheErr, "building webauthn ceremony session cache")
		}

		store, err = webauthncache.NewSessionStore(c, append([]webauthncache.Option{
			webauthncache.WithLogger(o.logger),
			webauthncache.WithTracerProvider(o.tracerProvider),
		}, o.cacheStore...)...)
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "webauthn session store provider %q", cfg.Provider)
	}

	if err != nil {
		return nil, err
	}

	return store, nil
}

// NewRelyingParty builds the relying party and the ceremony store under it.
//
// db is required only when the provider is database; pass nil otherwise.
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

	return webauthn.NewRelyingParty(ctx, &cfg.RelyingParty, store, append([]webauthn.Option{
		webauthn.WithLogger(o.logger),
		webauthn.WithTracerProvider(o.tracerProvider),
		webauthn.WithMetricsProvider(o.metricsProvider),
	}, o.relyingParty...)...)
}

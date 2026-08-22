// Package sessionscfg assembles a session store, and optionally a cookie-bound
// manager, from environment configuration. Sessions live either in a cache — the
// default — or in a SQL table.
//
// The choice is not a performance one. The database provider is for deployments
// where losing the cache must not sign everybody out, and where a sign-out has
// to be enforceable rather than very nearly enforceable; it is also the only
// provider with rows to sweep, which is what SweepInterval is for.
//
// NewStore builds the store alone and never reads CookieName. Only NewManager
// binds sessions to a cookie, which is why a caller that carries session
// identifiers some other way never configures one.
package sessionscfg

import (
	"context"
	"strings"
	"time"

	cachecfg "github.com/primandproper/platform-go/v13/cache/config"
	"github.com/primandproper/platform-go/v13/cookies"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/sessions"
	sessionscache "github.com/primandproper/platform-go/v13/sessions/cache"
	sessionsdatabase "github.com/primandproper/platform-go/v13/sessions/database"
	sessionshttp "github.com/primandproper/platform-go/v13/sessions/http"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderCache stores sessions in a cache — redis for a fleet, memory for
	// tests. The default answer.
	ProviderCache = "cache"
	// ProviderDatabase stores sessions in a SQL table, for deployments where
	// losing the cache must not sign everybody out and a sign-out has to be
	// enforceable rather than very nearly enforceable.
	ProviderDatabase = "database"
)

// DefaultSweepInterval is how often the database provider removes rows whose
// deadlines have passed, when nothing says otherwise. It is ignored by the
// cache provider, which reclaims its own entries.
const DefaultSweepInterval = 5 * time.Minute

// Config assembles a session store, and optionally a cookie-bound manager, from
// environment configuration.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// Provider selects where sessions live: cache or database.
	Provider string `env:"PROVIDER" envDefault:"cache" json:"provider,omitempty" yaml:"provider,omitempty"`

	// Database configures the store when Provider is database. The dialect
	// comes from the database.Client rather than from here.
	Database sessionsdatabase.Config `env:",init" envPrefix:"DATABASE_" json:"database,omitzero" yaml:"database,omitempty"`

	// CookieName is the cookie a session identifier travels in. It is read only
	// by NewManager; a caller building a bare store never sees a cookie.
	CookieName string `env:"COOKIE_NAME" json:"cookieName,omitempty" yaml:"cookieName,omitempty"`

	// Cache configures the store when Provider is cache. Use the redis
	// provider in production: the memory provider is per-process, so two
	// replicas do not share sessions and a user is signed in to whichever one
	// their request lands on.
	Cache cachecfg.Config `env:",init" envPrefix:"CACHE_" json:"cache,omitzero" yaml:"cache,omitempty"`

	// AbsoluteTimeout bounds a session's total lifetime from the moment it was
	// established. Nothing extends it — not activity, not renewal — which is
	// what makes it the only bound on a cookie somebody stole.
	AbsoluteTimeout time.Duration `env:"ABSOLUTE_TIMEOUT" json:"absoluteTimeout,omitempty" yaml:"absoluteTimeout,omitempty"`

	// IdleTimeout bounds how long a session may go unread.
	IdleTimeout time.Duration `env:"IDLE_TIMEOUT" json:"idleTimeout,omitempty" yaml:"idleTimeout,omitempty"`

	// TouchInterval is how much of the idle window must elapse before a read
	// refreshes the idle deadline. Zero refreshes on every read, which at any
	// real request rate is a write per request; see sessions.Policy.
	TouchInterval time.Duration `env:"TOUCH_INTERVAL" json:"touchInterval,omitempty" yaml:"touchInterval,omitempty"`

	// SweepInterval is how often the database provider removes rows whose
	// deadlines have passed. It is ignored by the cache provider.
	//
	// A non-positive value starts no sweeper, which is right when a scheduler
	// calls Sweep instead — one sweep for the fleet rather than one per replica
	// — and wrong when nothing else does, since the table then grows with every
	// session ever created.
	SweepInterval time.Duration `env:"SWEEP_INTERVAL" json:"sweepInterval,omitempty" yaml:"sweepInterval,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in zero fields.
//
// The timeouts are defaulted here rather than left to the store so that a
// Config reads as what it will actually do. SweepInterval is deliberately not
// defaulted for the cache provider, which has nothing to sweep.
func (cfg *Config) EnsureDefaults() {
	if cfg.Provider == "" {
		cfg.Provider = ProviderCache
	}
	if cfg.AbsoluteTimeout == 0 {
		cfg.AbsoluteTimeout = sessions.DefaultAbsoluteTimeout
	}
	if cfg.IdleTimeout == 0 {
		cfg.IdleTimeout = sessions.DefaultIdleTimeout
	}
	if cfg.CookieName == "" {
		cfg.CookieName = sessionshttp.DefaultCookieName
	}
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
		validation.Field(&cfg.Provider, validation.In(ProviderCache, ProviderDatabase)),
		validation.Field(&cfg.Cache,
			validation.Skip.When(cfg.provider() != ProviderCache),
			validation.By(func(any) error { return cfg.Cache.ValidateWithContext(ctx) })),
		validation.Field(&cfg.Database,
			validation.Skip.When(cfg.provider() != ProviderDatabase),
			validation.By(func(any) error { return cfg.Database.ValidateWithContext(ctx) })),
		validation.Field(&cfg.AbsoluteTimeout, validation.Min(time.Duration(0))),
		validation.Field(&cfg.IdleTimeout, validation.Min(time.Duration(0))),
		validation.Field(&cfg.TouchInterval, validation.Min(time.Duration(0))),
	)
}

// provider normalizes the configured provider name, so that trailing whitespace
// out of an environment file is not a different provider.
func (cfg *Config) provider() string {
	return strings.TrimSpace(strings.ToLower(cfg.Provider))
}

// storeOptions renders the Config's expiry settings as store options, ahead of
// whatever the caller passed.
func (cfg *Config) storeOptions(o *options) []sessions.Option {
	opts := []sessions.Option{
		sessions.WithAbsoluteTimeout(cfg.AbsoluteTimeout),
		sessions.WithIdleTimeout(cfg.IdleTimeout),
		sessions.WithLogger(o.logger),
		sessions.WithTracerProvider(o.tracerProvider),
		sessions.WithMetricsProvider(o.metricsProvider),
	}

	// Applied only when configured, because zero is a meaningful touch interval
	// — refresh on every read — and cannot double as "unset". Leaving the
	// option off is what lets the store apply its own default.
	if cfg.TouchInterval > 0 {
		opts = append(opts, sessions.WithTouchInterval(cfg.TouchInterval))
	}

	return append(opts, o.store...)
}

// NewStore builds a session Store for T from configuration.
//
// T must be supplied explicitly — NewStore[Principal](...) — because this
// constructor builds the backend itself, so nothing in the argument list
// mentions T. That single annotation is the whole cost: sessions.Option carries
// no type parameter, so the options passed here need none.
//
// db is required only when the provider is database; pass nil otherwise.
func NewStore[T any](ctx context.Context, cfg *Config, db database.Client, opts ...Option) (sessions.Store[T], error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	o := newOptions(opts)

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating sessions config")
	}

	backend, err := newBackend[T](ctx, cfg, db, o)
	if err != nil {
		return nil, err
	}

	store, err := sessions.NewStore(backend, cfg.storeOptions(o)...)
	if err != nil {
		return nil, err
	}

	return store, nil
}

// newBackend selects and builds the storage the configured provider names.
//
// An unrecognized provider is an error rather than a working-looking default.
// Falling back to the memory cache would produce a service that signs users in,
// signs them out again on the next request that lands on another replica, and
// looks configured the whole time.
func newBackend[T any](
	ctx context.Context,
	cfg *Config,
	db database.Client,
	o *options,
) (sessions.Backend[T], error) {
	// Every branch builds into backend and returns it only once err is known to
	// be nil. sessions/database.NewBackend hands back a *database.Backend[T],
	// and returning it straight through would convert a nil pointer into a
	// non-nil sessions.Backend[T] on the error path — a value that passes a
	// caller's nil check and panics on the first Get. The cache branch is
	// written the same way even though its constructor still returns the
	// interface, so that narrowing it to a concrete type does not quietly
	// reintroduce the trap here.
	var (
		backend sessions.Backend[T]
		err     error
	)

	switch cfg.provider() {
	case ProviderCache:
		// cacheErr rather than the err above: this one is returned on the spot,
		// and := here would shadow err for the rest of the case, leaving the
		// backend assignment below writing to a variable nothing reads.
		c, cacheErr := cachecfg.NewCache[sessions.Record[T]](ctx, &cfg.Cache,
			cachecfg.WithLogger(o.logger),
			cachecfg.WithTracerProvider(o.tracerProvider),
			cachecfg.WithMetricsProvider(o.metricsProvider))
		if cacheErr != nil {
			return nil, errors.Wrap(cacheErr, "building session cache")
		}

		backend, err = sessionscache.NewBackend(c, append([]sessionscache.Option{
			sessionscache.WithLogger(o.logger),
			sessionscache.WithTracerProvider(o.tracerProvider),
		}, o.cacheBackend...)...)
	case ProviderDatabase:
		backend, err = sessionsdatabase.NewBackend[T](&cfg.Database, db, append([]sessionsdatabase.Option{
			sessionsdatabase.WithLogger(o.logger),
			sessionsdatabase.WithTracerProvider(o.tracerProvider),
			sessionsdatabase.WithMetricsProvider(o.metricsProvider),
			// Bound to the caller's context: the sweep stops when whatever
			// scope owns this store does.
			sessionsdatabase.WithSweeper(ctx, cfg.SweepInterval),
		}, o.databaseBackend...)...)
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "session provider %q", cfg.Provider)
	}

	if err != nil {
		return nil, err
	}

	return backend, nil
}

// NewManager builds a cookie-bound session manager for T from configuration.
//
// The cookie manager is a dependency rather than something built from this
// Config: cookie signing keys, domain, and SameSite policy belong to the
// application and are shared with every other cookie it sets, so configuring
// them a second time here would be a second place for them to be wrong.
//
// db is required only when the provider is database; pass nil otherwise.
func NewManager[T any](
	ctx context.Context,
	cfg *Config,
	db database.Client,
	cookieManager cookies.Manager,
	opts ...Option,
) (*sessionshttp.Manager[T], error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	o := newOptions(opts)

	store, err := NewStore[T](ctx, cfg, db, opts...)
	if err != nil {
		return nil, err
	}

	return sessionshttp.NewManager(store, cookieManager, append([]sessionshttp.Option{
		sessionshttp.WithCookieName(cfg.CookieName),
		sessionshttp.WithLogger(o.logger),
		sessionshttp.WithTracerProvider(o.tracerProvider),
	}, o.manager...)...)
}

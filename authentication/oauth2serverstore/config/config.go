package oauth2serverstorecfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/authentication/oauth2serverstore"

	"github.com/primandproper/primitives-go/v2/authentication/oauth2server"
	oauth2servercfg "github.com/primandproper/primitives-go/v2/authentication/oauth2server/config"
	"github.com/primandproper/primitives-go/v2/config/cfgnorm"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/pointer"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderMemory keeps every record in maps. For tests and single-process
	// development; see the memory package for what it cannot do.
	ProviderMemory = "memory"

	// ProviderDatabase keeps every record in SQL tables. The answer for any
	// deployment with more than one replica, which under this protocol is any
	// deployment where logins have to work.
	ProviderDatabase = "database"
)

// providers are every provider this package implements. Validation and NewStore
// both read it.
var providers = []string{ProviderMemory, ProviderDatabase}

// Config assembles an authorization server from environment configuration, over
// either store.
//
// The embedded oauth2servercfg.Config carries no env tag, so its fields are
// promoted into this one at this struct's own prefix — the issuer, the lifetimes
// and the scopes are read at the names they always had, from before the two
// halves were separate structs. JSON and TOML promote an embedded struct on
// their own; YAML does not, which is what the `yaml:",inline"` is for.
type Config struct {
	// Provider selects where the records live: memory or database.
	Provider string `env:"PROVIDER" envDefault:"database" json:"provider,omitempty" yaml:"provider,omitempty"`

	// Database configures the store when Provider is database. The dialect
	// comes from the database.Client rather than from here.
	Database               oauth2serverstore.Config `env:",init"    envPrefix:"DATABASE_" json:"database,omitzero" yaml:"database,omitempty"`
	oauth2servercfg.Config `yaml:",inline"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in zero fields, including the embedded config's own.
func (cfg *Config) EnsureDefaults() {
	if cfg.Provider == "" {
		cfg.Provider = ProviderDatabase
	}

	cfg.Config.EnsureDefaults()
}

// ValidateWithContext validates a Config struct.
//
// The database sub-config is skipped under the memory provider rather than
// merely unguarded: ozzo validates any non-nil pointer to a Validatable once a
// field's rules have run, and `env:",init"` leaves it populated either way.
//
// The embedded config's own rules are run by calling its method rather than by
// restating its fields here: a second copy of a rule is a second place for it to
// be wrong.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	if err := cfg.Config.ValidateWithContext(ctx); err != nil {
		return err
	}

	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Provider, validation.By(func(any) error {
			// Checked normalized, matching dispatch. The raw string used to be
			// validated while dispatch read the normalized one, so " Memory "
			// was a config that failed to load and a store that would have
			// built.
			_, err := cfgnorm.SelectProvider(cfg.Provider, providers, "oauth2 store provider")

			return err
		})),
		validation.Field(&cfg.Database,
			validation.Skip.When(cfg.provider() != ProviderDatabase),
			validation.By(func(any) error { return cfg.Database.ValidateWithContext(ctx) })),
	)
}

// provider normalizes the configured provider name, so that trailing whitespace
// out of an environment file is not a different provider.
func (cfg *Config) provider() string {
	return cfgnorm.Provider(cfg.Provider)
}

// NewStore builds the authorization server's Store from configuration.
//
// db is required only when the provider is database; pass nil otherwise.
func NewStore(ctx context.Context, cfg *Config, db database.Client, opts ...Option) (oauth2server.Store, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	o := newOptions(opts)

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating oauth2 server config")
	}

	switch cfg.provider() {
	case ProviderMemory:
		// The primitive half owns the memory store, so this branch is a
		// delegation rather than a second assembly.
		return oauth2servercfg.NewStore(ctx, &cfg.Config, o.serverOptions()...)
	case ProviderDatabase:
		// Built into a variable and returned only once err is known to be nil:
		// the constructor hands back its own concrete type, and returning it
		// straight through would convert a nil *database.Store into a non-nil
		// oauth2server.Store on the error path — a value that passes a caller's
		// nil check and panics on first use.
		store, err := oauth2serverstore.NewStore(&cfg.Database, db, append([]oauth2serverstore.Option{
			oauth2serverstore.WithLogger(o.logger),
			oauth2serverstore.WithTracerProvider(o.tracerProvider),
			oauth2serverstore.WithMetricsProvider(o.metricsProvider),
			// Bound to the caller's context: the sweep stops when whatever
			// scope owns this store does.
			oauth2serverstore.WithSweeper(ctx, pointer.Dereference(cfg.SweepInterval)),
		}, o.databaseStore...)...)
		if err != nil {
			return nil, err
		}

		return store, nil
	default:
		// An unrecognized provider is an error rather than a working-looking
		// default. Falling back to memory would produce an authorization server
		// that signs users in, fails their next login on another replica, and
		// looks configured the whole time.
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "oauth2 store provider %q", cfg.Provider)
	}
}

// NewServer builds an authorization server and the Store behind it.
//
// db is required only when the provider is database; pass nil otherwise. The
// server itself is oauth2servercfg's to build — this function's contribution is
// choosing the store to hand it.
//
// authenticator is a parameter rather than an option because it is the one
// thing no configuration can supply: it is how this deployment identifies a
// human, and a default would be a server that issues authorization codes to
// whoever asks. The login form does have a default, so it is an option —
// oauth2server.WithLoginRenderer, through WithServerOptions.
func NewServer(
	ctx context.Context,
	cfg *Config,
	db database.Client,
	authenticator oauth2server.SubjectAuthenticator,
	opts ...Option,
) (*oauth2server.Server, error) {
	store, err := NewStore(ctx, cfg, db, opts...)
	if err != nil {
		return nil, err
	}

	o := newOptions(opts)

	return oauth2servercfg.NewServer(ctx, &cfg.Config, store, authenticator, o.serverOptions()...)
}

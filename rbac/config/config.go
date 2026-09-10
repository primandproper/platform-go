package rbaccfg

import (
	"context"
	"slices"

	"github.com/primandproper/platform-go/v14/rbac"

	"github.com/primandproper/primitives-go/authorization"
	authorizationcfg "github.com/primandproper/primitives-go/authorization/config"
	"github.com/primandproper/primitives-go/cache"
	"github.com/primandproper/primitives-go/config/cfgnorm"
	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/errors"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderStatic resolves policy declared at build time or loaded from
	// config. It is the default, and an empty Provider selects it.
	ProviderStatic = "static"
	// ProviderDatabase resolves policy from SQL tables, for deployments where
	// roles must be editable without a release.
	ProviderDatabase = "database"
)

// providers are every provider this package implements, plus the empty string,
// which selects the static resolver. Validation and dispatch both read it.
var providers = []string{"", ProviderStatic, ProviderDatabase}

// Config configures a policy resolver, over either backend.
//
// The embedded authorizationcfg.Config carries no env tag, so its fields are
// promoted into this one at this struct's own prefix — CacheTTL is read from
// CACHE_TTL exactly as it was before the two halves were separate structs. JSON
// and TOML promote an embedded struct on their own; YAML does not, which is what
// the `yaml:",inline"` is for.
//
// The zero value is valid and yields a working static resolver that grants
// nothing. That is deliberate on both counts: the most accessible
// implementation is the default so the package runs with no infrastructure, and
// an unconfigured authorization layer denies rather than admits.
type Config struct {
	// Database configures the database provider. Required when Provider is
	// "database", and must be absent otherwise.
	Database *rbac.Config `env:",init" envPrefix:"DATABASE_" json:"database,omitempty" yaml:"database,omitempty"`
	// Provider selects the implementation. Empty means ProviderStatic.
	Provider                string `env:"PROVIDER" json:"provider,omitempty" yaml:"provider,omitempty"`
	authorizationcfg.Config `yaml:",inline"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config.
//
// The embedded config's own rules are run first, by calling its method rather
// than by restating its fields here: a second copy of a rule is a second place
// for it to be wrong.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	if err := cfg.Config.ValidateWithContext(ctx); err != nil {
		return err
	}

	provider := cfgnorm.Provider(cfg.Provider)

	// Release the sub-configs env parsing's ",init" allocated and nothing filled
	// in, so the Nil rules below read "the operator configured this" rather than
	// "env parsing ran".
	cfgnorm.ZeroToNil(&cfg.Database)

	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Provider, validation.By(func(any) error {
			// Checked normalized, matching dispatch: validating the raw string
			// rejected "Static" and " static " while the factory accepted both.
			if !slices.Contains(providers, provider) {
				return errors.Wrapf(errors.ErrUnknownProvider, "authorization provider %q", cfg.Provider)
			}

			return nil
		})),
		validation.Field(&cfg.Database,
			validation.When(provider == ProviderDatabase, validation.Required),
			validation.When(provider != ProviderDatabase, validation.Nil),
		),
	)
}

// NewPolicyResolver builds the configured resolver.
//
// db is used only by the database provider and may be nil otherwise. c is
// optional: when non-nil the resolver is wrapped in authorization/cached, which
// is what turns the database provider from a query per session build into a
// query per policy change.
//
// The result is an interface, so a caller that needs to drop cached policy
// after an edit type-asserts authorization.PolicyInvalidator rather than a
// concrete type — whether a cache is in the chain is this function's decision,
// not the caller's.
func NewPolicyResolver(
	ctx context.Context,
	cfg *Config,
	db database.SQLQueryExecutor,
	c cache.Cache[authorization.PermissionSet],
	opts ...Option,
) (authorization.PolicyResolver, error) {
	// A nil config is the zero config, which this package documents as valid and
	// as selecting the static resolver. It is still put through validation
	// below, so the two spellings of "unconfigured" cannot diverge.
	if cfg == nil {
		cfg = &Config{}
	}

	o := newOptions(opts)

	provider, err := cfgnorm.SelectProvider(cfg.Provider, providers, "authorization provider")
	if err != nil {
		return nil, err
	}

	if err = cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating authorization config")
	}

	inner := o.resolverOptions()

	switch provider {
	case ProviderDatabase:
		if cfg.Database == nil {
			return nil, errors.New("database authorization provider selected with no database config")
		}

		// Built into a variable and passed on only once err is known to be nil:
		// rbac.NewResolver returns a *rbac.Resolver, and handing it
		// straight to a parameter of interface type would make a non-nil
		// PolicyResolver wrapping a nil pointer whenever it failed.
		resolver, resolverErr := rbac.NewResolver(cfg.Database, db, append([]rbac.Option{
			rbac.WithLogger(o.logger),
			rbac.WithTracerProvider(o.tracerProvider),
			rbac.WithMetricsProvider(o.metricsProvider),
		}, o.database...)...)
		if resolverErr != nil {
			return nil, resolverErr
		}

		return authorizationcfg.NewCachedResolver(&cfg.Config, resolver, c, inner...)
	case ProviderStatic, "":
		// The primitive half owns the static resolver and the caching decorator
		// alike, so this branch is a delegation rather than a second assembly.
		return authorizationcfg.NewPolicyResolver(ctx, &cfg.Config, c, inner...)
	default:
		// Unreachable: SelectProvider above has already refused anything that is
		// not in providers. Kept because the switch is what dispatch reads, and
		// a provider added to the list and not to the switch is a silent static
		// resolver rather than a compile error.
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "authorization provider %q", cfg.Provider)
	}
}

/*
Package registrycfg assembles an upload registry Store from environment
configuration.

There is one thing to configure and one thing to build, which is why this package
is smaller than most of its siblings: the dialect comes from the database.Client
so it cannot disagree with the database the statements run against, and
everything else about the store is either the schema's or an option.

The table prefix is the exception, and it has to be here because it must match
the prefix the migrations were rendered with — a deployment sharing one database
between applications sets both from the same value.

It configures no object storage. Which bucket the bytes go to is uploadscfg's
question, and keeping the two apart is the same separation the registry itself
rests on: a consumer registering objects somebody else stored has no storage
config to give.
*/
package registrycfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/uploads/registry"
	"github.com/primandproper/platform-go/v14/uploads/registry/migrations"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/errors"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config assembles an upload registry Store.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// TablePrefix names the registry table. It must match the prefix the
	// migrations were rendered with. Defaults to registry.DefaultTablePrefix.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in zero fields.
func (cfg *Config) EnsureDefaults() {
	if cfg.TablePrefix == "" {
		cfg.TablePrefix = registry.DefaultTablePrefix
	}
}

// ValidateWithContext validates a Config.
//
// The prefix is vetted against the identifiers it actually renders rather than
// against a pattern, so a prefix that is legal in isolation but produces an
// over-long index name fails here instead of at the first migration.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	if err := validation.ValidateStructWithContext(ctx, cfg); err != nil {
		return err
	}

	return migrations.ValidatePrefix(cfg.TablePrefix)
}

// NewStore builds the Store. client must be the database holding the registry
// table.
//
// The store is built into a variable and returned only once its error is known
// to be nil. registry.NewSQLStore returns its own concrete type, so returning
// one straight through would convert a nil *registry.SQLStore into a non-nil
// registry.Store on the error path, and a caller testing the result against nil
// would find a store that panics on first use.
func NewStore(ctx context.Context, cfg *Config, client database.Client, opts ...Option) (registry.Store, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating uploads registry config")
	}

	options := newOptions(opts)

	base := []registry.SQLStoreOption{
		registry.WithTablePrefix(cfg.TablePrefix),
		registry.WithStoreLogger(options.logger),
		registry.WithStoreTracerProvider(options.tracerProvider),
		registry.WithStoreMetricsProvider(options.metricsProvider),
	}

	store, storeErr := registry.NewSQLStore(client, append(base, options.store...)...)
	if storeErr != nil {
		return nil, storeErr
	}

	return store, nil
}

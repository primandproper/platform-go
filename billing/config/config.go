/*
Package billingcfg assembles a billing Store from environment configuration.

There is one thing to configure and one thing to build, which is why this
package is smaller than most of its siblings: the dialect comes from the
database.Client so it cannot disagree with the database the statements run
against, and everything else about the store is either the schema's or an
option.

The table prefix is the exception, and it has to be here because it must match
the prefix the migrations were rendered with — a deployment sharing one database
between applications sets both from the same value.

What is deliberately not configurable is the catalog. What a deployment sells,
at what price, on what recurrence are rows rather than environment: they are
administered, they change without a redeployment, and every one of them is a fact
this package's schema already stores. A price in an environment variable is a
price that differs between two instances mid-rollout.

Neither is the reading of a status. Which of capitalism's subscription statuses
leaves an account entitled is policy, and this module deliberately holds none of
it — see billing/plans, which is where a deployment writes its answer down as
code it can test rather than as a list of words in a config file.
*/
package billingcfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/billing"
	"github.com/primandproper/platform-go/v14/billing/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/errors"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config assembles a billing Store.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// TablePrefix names the four billing tables. It must match the prefix the
	// migrations were rendered with. Defaults to billing.DefaultTablePrefix.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in zero fields.
func (cfg *Config) EnsureDefaults() {
	if cfg.TablePrefix == "" {
		cfg.TablePrefix = billing.DefaultTablePrefix
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

// NewStore builds the Store. client must be the database holding the billing
// tables.
//
// The store is built into a variable and returned only once its error is known
// to be nil. billing.NewSQLStore returns its own concrete type, so returning
// it straight through would convert a nil *billing.SQLStore into a non-nil
// billing.Store on the error path, and a caller testing the result against nil
// would find a store that panics on first use.
func NewStore(ctx context.Context, cfg *Config, client database.Client, opts ...Option) (billing.Store, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating billing config")
	}

	options := newOptions(opts)

	base := []billing.SQLStoreOption{
		billing.WithTablePrefix(cfg.TablePrefix),
		billing.WithStoreLogger(options.logger),
		billing.WithStoreTracerProvider(options.tracerProvider),
		billing.WithStoreMetricsProvider(options.metricsProvider),
	}

	store, storeErr := billing.NewSQLStore(client, append(base, options.store...)...)
	if storeErr != nil {
		return nil, storeErr
	}

	return store, nil
}

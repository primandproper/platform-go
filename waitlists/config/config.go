/*
Package waitlistscfg assembles a waitlists Store from environment configuration.

There is one thing to configure and one thing to build, which is why this
package is smaller than most of its siblings: the dialect comes from the
database.Client so it cannot disagree with the database the statements run
against, and everything else about the store is either the schema's or an
option.

The table prefix is the exception, and it has to be here because it must match
the prefix the migrations were rendered with — a deployment sharing one database
between applications sets both from the same value.

What is deliberately not configurable is the catalog. Which waitlists a
deployment runs, what they are called and when each closes are rows rather than
environment: they are administered, they change without a redeployment, and every
one of them is a fact this package's schema already stores.

Neither is the hasher. Changing what the contact_digest column holds orphans
every digest already written — existing signups stop being found by their
contacts, and a withdrawn contact stops being suppressed — so it is a
waitlists.WithHasher passed through WithStoreOptions by a deployment that has
also rewritten the column, rather than an environment variable a restart can
change. See waitlists.WithHasher.

The privacy seam is not here either, and that absence is the same reading the
composition root takes of every registry: waitlists/privacy needs a
ScopeResolver, which is a mapping from a person to the tenants they belong to,
and no environment variable can express one. A service that wants its signups in
its subject access requests registers the collector and the eraser itself, with
the store this package built.
*/
package waitlistscfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/waitlists"
	"github.com/primandproper/platform-go/v14/waitlists/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/errors"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config assembles a waitlists Store.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// TablePrefix names the two waitlist tables. It must match the prefix the
	// migrations were rendered with. Defaults to waitlists.DefaultTablePrefix.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in zero fields.
func (cfg *Config) EnsureDefaults() {
	if cfg.TablePrefix == "" {
		cfg.TablePrefix = waitlists.DefaultTablePrefix
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

// NewStore builds the Store. client must be the database holding the waitlist
// tables.
//
// The store is built into a variable and returned only once its error is known
// to be nil. waitlists.NewSQLStore returns its own concrete type, so returning
// it straight through would convert a nil *waitlists.SQLStore into a non-nil
// waitlists.Store on the error path, and a caller testing the result against nil
// would find a store that panics on first use.
func NewStore(ctx context.Context, cfg *Config, client database.Client, opts ...Option) (waitlists.Store, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating waitlists config")
	}

	options := newOptions(opts)

	base := []waitlists.SQLStoreOption{
		waitlists.WithTablePrefix(cfg.TablePrefix),
		waitlists.WithStoreLogger(options.logger),
		waitlists.WithStoreTracerProvider(options.tracerProvider),
		waitlists.WithStoreMetricsProvider(options.metricsProvider),
	}

	store, storeErr := waitlists.NewSQLStore(client, append(base, options.store...)...)
	if storeErr != nil {
		return nil, storeErr
	}

	return store, nil
}

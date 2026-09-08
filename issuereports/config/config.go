/*
Package issuereportscfg assembles an issuereports Store from environment
configuration.

There is one thing to configure and one thing to build, which is why this package
is smaller than most of its siblings: the dialect comes from the database.Client
so it cannot disagree with the database the statements run against, and
everything else about the store is either the schema's or an option.

The table prefix is the exception, and it has to be here because it must match
the prefix the migrations were rendered with — a deployment sharing one database
between applications sets both from the same value.

What is deliberately not configurable is the lifecycle. Which statuses a report
can be in, and which moves between them are allowed, are the package's rather
than a deployment's: an operator who could add a status would be adding one no
client renders and no queue lists, and one who could remove a transition would be
stranding every report already in the status it led out of.

The privacy seam is not here either, and that absence is the same reading the
composition root takes of every registry: issuereports/privacy needs a
ScopeResolver, which is a mapping from a person to the tenants they belong to,
and no environment variable can express one. A service that wants its issue
reports in its subject access requests registers the collector and the eraser
itself, with the store this package built.
*/
package issuereportscfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/issuereports"
	"github.com/primandproper/platform-go/v14/issuereports/migrations"

	"github.com/primandproper/primitives-go/database"
	"github.com/primandproper/primitives-go/errors"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config assembles an issuereports Store.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// TablePrefix names the issue reports table. It must match the prefix the
	// migrations were rendered with. Defaults to
	// issuereports.DefaultTablePrefix.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in zero fields.
func (cfg *Config) EnsureDefaults() {
	if cfg.TablePrefix == "" {
		cfg.TablePrefix = issuereports.DefaultTablePrefix
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

// NewStore builds the Store. client must be the database holding the issue
// reports table.
//
// The store is built into a variable and returned only once its error is known
// to be nil. issuereports.NewSQLStore returns its own concrete type, so returning
// it straight through would convert a nil *issuereports.SQLStore into a non-nil
// issuereports.Store on the error path, and a caller testing the result against
// nil would find a store that panics on first use.
func NewStore(
	ctx context.Context,
	cfg *Config,
	client database.Client,
	opts ...Option,
) (issuereports.Store, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating issuereports config")
	}

	options := newOptions(opts)

	base := []issuereports.SQLStoreOption{
		issuereports.WithTablePrefix(cfg.TablePrefix),
		issuereports.WithStoreLogger(options.logger),
		issuereports.WithStoreTracerProvider(options.tracerProvider),
		issuereports.WithStoreMetricsProvider(options.metricsProvider),
	}

	store, storeErr := issuereports.NewSQLStore(client, append(base, options.store...)...)
	if storeErr != nil {
		return nil, storeErr
	}

	return store, nil
}

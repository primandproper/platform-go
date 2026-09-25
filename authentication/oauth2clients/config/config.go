/*
Package oauth2clientscfg assembles an oauth2clients Store and Service from
environment configuration.

There is one thing to configure and two things to build. The dialect comes from
the database.Client so it cannot disagree with the database the statements run
against, and everything else about the store and the service is either the
schema's or an option.

The table prefix is the one setting, and it has to be here because it must match
the prefix the migrations were rendered with — a deployment sharing one database
between applications sets both from the same value. Before this package existed
every consumer wrote the two constructor calls out by hand, and a hand-written
copy of the prefix is a copy that can drift from the one the migrations were
rendered with.

What is deliberately not configurable is the credential generator. How long a
client secret is and what digests it is decided once, in oauth2clients, and a
deployment that genuinely needs another passes oauth2clients.WithCredentialGenerator
through WithServiceOptions rather than setting a variable a restart can change.

The privacy seam is not here either, for the reason the composition root gives
of every registry: oauth2clients/privacy needs a mapping from a person to the
tenants they belong to, and no environment variable can express one. A service
that wants its personal credentials in its subject access requests registers the
collector and the eraser itself, with the store this package built.
*/
package oauth2clientscfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/errors"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config assembles an oauth2clients Store and Service.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// TablePrefix names the registered clients table. It must match the prefix
	// the migrations were rendered with. Defaults to
	// oauth2clients.DefaultTablePrefix.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in zero fields.
func (cfg *Config) EnsureDefaults() {
	if cfg.TablePrefix == "" {
		cfg.TablePrefix = oauth2clients.DefaultTablePrefix
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

// NewStore builds the Store. client must be the database holding the registered
// clients table.
//
// The store is built into a variable and returned only once its error is known
// to be nil. oauth2clients.NewSQLStore returns its own concrete type, so
// returning it straight through would convert a nil *oauth2clients.SQLStore into
// a non-nil oauth2clients.Store on the error path, and a caller testing the
// result against nil would find a store that panics on first use.
//
// The metrics provider an Option supplies reaches the Service and not the
// store, which ships no instruments; see oauth2clients.NewSQLStore.
func NewStore(ctx context.Context, cfg *Config, client database.Client, opts ...Option) (oauth2clients.Store, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating oauth2clients config")
	}

	options := newOptions(opts)

	base := []oauth2clients.SQLStoreOption{
		oauth2clients.WithTablePrefix(cfg.TablePrefix),
		oauth2clients.WithStoreLogger(options.logger),
		oauth2clients.WithStoreTracerProvider(options.tracerProvider),
	}

	store, storeErr := oauth2clients.NewSQLStore(client, append(base, options.store...)...)
	if storeErr != nil {
		return nil, storeErr
	}

	return store, nil
}

// NewService builds the orchestration layer over a Store.
//
// The config carries nothing the service reads, and is taken anyway so that a
// Service is built from a validated block exactly as its Store is: a deployment
// whose prefix cannot render hears about it from whichever of the two it
// resolves first.
func NewService(
	ctx context.Context,
	cfg *Config,
	client database.Client,
	store oauth2clients.Store,
	opts ...Option,
) (*oauth2clients.Service, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating oauth2clients config")
	}

	options := newOptions(opts)

	base := []oauth2clients.ServiceOption{
		oauth2clients.WithServiceLogger(options.logger),
		oauth2clients.WithServiceTracerProvider(options.tracerProvider),
		oauth2clients.WithServiceMetricsProvider(options.metricsProvider),
	}

	if options.hooks != nil {
		base = append(base, oauth2clients.WithHooks(options.hooks))
	}

	return oauth2clients.NewService(client, store, append(base, options.service...)...)
}

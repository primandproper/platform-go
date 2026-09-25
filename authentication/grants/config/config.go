/*
Package grantscfg assembles a third-party grant Store from environment
configuration.

There is one thing to configure: the table prefix, which must match the prefix
the migrations were rendered with. The dialect comes from the database.Client,
and the encryptor is a dependency rather than configuration — which keys seal a
refresh token is a keyring's business, built from key material by
cryptography/encryption's own config, and no environment variable here should be
the place a deployment decides it.

The privacy seam is not here either: authentication/grants/privacy needs a
ScopeResolver, a mapping from a person to the tenants they belong to that no
environment variable can express. A service registers it through
privacyadapters with the store this package built.
*/
package grantscfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/authentication/grants"
	"github.com/primandproper/platform-go/v14/authentication/grants/migrations"

	"github.com/primandproper/primitives-go/v2/cryptography/encryption"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/errors"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config assembles a grant Store.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// TablePrefix names the grant table. It must match the prefix the
	// migrations were rendered with. Defaults to grants.DefaultTablePrefix.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in zero fields.
func (cfg *Config) EnsureDefaults() {
	if cfg.TablePrefix == "" {
		cfg.TablePrefix = grants.DefaultTablePrefix
	}
}

// ValidateWithContext validates a Config.
//
// The prefix is vetted against the identifiers it actually renders, so a prefix
// that is legal in isolation but produces an over-long index name fails here
// instead of at the first migration.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	if err := validation.ValidateStructWithContext(ctx, cfg); err != nil {
		return err
	}

	return migrations.ValidatePrefix(cfg.TablePrefix)
}

// NewStore builds the Store. client must be the database holding the grant
// table, and encryptor is what seals every token in it.
//
// The store is built into a variable and returned only once its error is known
// to be nil: grants.NewSQLStore returns its own concrete type, and returning
// one straight through would turn a nil *grants.SQLStore into a non-nil
// grants.Store on the error path.
func NewStore(
	ctx context.Context,
	cfg *Config,
	client database.Client,
	encryptor encryption.EncryptorDecryptor,
	opts ...Option,
) (grants.Store, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating grant store config")
	}

	options := newOptions(opts)

	base := []grants.SQLStoreOption{
		grants.WithTablePrefix(cfg.TablePrefix),
		grants.WithLogger(options.logger),
		grants.WithTracerProvider(options.tracerProvider),
		grants.WithMetricsProvider(options.metricsProvider),
	}

	store, storeErr := grants.NewSQLStore(client, encryptor, append(base, options.store...)...)
	if storeErr != nil {
		return nil, storeErr
	}

	return store, nil
}

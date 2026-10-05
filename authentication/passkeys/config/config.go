package passkeyscfg

import (
	"context"

	"github.com/primandproper/platform-go/v15/authentication/passkeys"
	"github.com/primandproper/platform-go/v15/authentication/passkeys/migrations"

	"github.com/primandproper/primitives-go/v2/authentication/webauthn"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/errors"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// Config assembles a passkeys Store and Service.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// TablePrefix is the namespace prepended to the credential table's name. It
	// must match the prefix the migrations were rendered with. Unset renders the
	// schema's own name; see passkeys.DefaultTablePrefix.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in unset fields.
func (cfg *Config) EnsureDefaults() {
	if cfg.TablePrefix == "" {
		cfg.TablePrefix = passkeys.DefaultTablePrefix
	}
}

// ValidateWithContext validates a Config. The prefix is vetted against the
// identifiers it renders, so one that produces an over-long index name fails
// here rather than at the first migration.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.TablePrefix, validation.By(func(any) error {
			return migrations.ValidatePrefix(cfg.TablePrefix)
		})),
	)
}

// NewStore builds the Store. client must be the database holding the
// credential table.
//
// The store is built into a variable and returned only once its error is
// known to be nil, so a failed construction is a nil passkeys.Store rather
// than a non-nil one holding a nil *passkeys.SQLStore.
func NewStore(ctx context.Context, cfg *Config, client database.Client, opts ...Option) (passkeys.Store, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating passkeys config")
	}

	options := newOptions(opts)

	base := []passkeys.SQLStoreOption{
		passkeys.WithTablePrefix(cfg.TablePrefix),
		passkeys.WithLogger(options.logger),
		passkeys.WithTracerProvider(options.tracerProvider),
		passkeys.WithMetricsProvider(options.metricsProvider),
	}

	store, err := passkeys.NewSQLStore(client, append(base, options.store...)...)
	if err != nil {
		return nil, err
	}

	return store, nil
}

// NewService builds the ceremony service over a Store.
//
// The relying party, the user resolver and the enrollment gate are parameters
// for the reasons the package documentation gives; RegisterService resolves
// all three from the injector. Options passed through WithServiceOptions apply
// after the gate, so a caller can replace it.
func NewService(
	ctx context.Context,
	cfg *Config,
	client database.Client,
	store passkeys.Store,
	rp *webauthn.RelyingParty,
	resolve passkeys.UserResolver,
	gate passkeys.EnrollmentGate,
	opts ...Option,
) (*passkeys.Service, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating passkeys config")
	}

	users, err := passkeys.NewUserSource(store, resolve)
	if err != nil {
		return nil, err
	}

	options := newOptions(opts)

	base := []passkeys.ServiceOption{
		passkeys.WithEnrollmentGate(gate),
		passkeys.WithServiceLogger(options.logger),
		passkeys.WithServiceTracerProvider(options.tracerProvider),
		passkeys.WithServiceMetricsProvider(options.metricsProvider),
	}

	return passkeys.NewService(client, store, rp, users, append(base, options.service...)...)
}

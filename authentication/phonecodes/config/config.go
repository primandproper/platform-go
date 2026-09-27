/*
Package phonecodescfg assembles a texted-code Store from environment
configuration.

Six settings, and each is a number a deployment decides rather than a policy
this module does: the table prefix, which has to match the one the migrations
were rendered with; how many digits a code has; how long it stays redeemable;
how many wrong guesses it survives; how long a dead row is kept; and how often
dead rows are swept. The dialect comes from the database.Client, so it cannot
disagree with the database the statements run against.

The privacy seam is not here: authentication/phonecodes/privacy needs a
ScopeResolver, a mapping from a person to the tenants they belong to that no
environment variable can express. A service registers it through
privacyadapters with the store this package built.
*/
package phonecodescfg

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/phonecodes"
	"github.com/primandproper/platform-go/v14/authentication/phonecodes/migrations"

	"github.com/primandproper/primitives-go/v2/config/cfgnorm"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/pointer"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// DefaultSweepInterval is how often the store removes rows past their purge
// deadlines, when nothing says otherwise.
const DefaultSweepInterval = 5 * time.Minute

// Config assembles a texted-code Store.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// SweepInterval is how often the store removes rows past their purge
	// deadlines. Unset takes DefaultSweepInterval; zero starts no sweeper,
	// which is right when a scheduler calls Sweep for the fleet instead and
	// wrong when nothing does, since the table then keeps every number anybody
	// was ever texted a code at.
	SweepInterval *time.Duration `env:"SWEEP_INTERVAL" json:"sweepInterval,omitempty" yaml:"sweepInterval,omitempty"`

	// TablePrefix is the namespace prepended to the code table's name. It must
	// match the prefix the migrations were rendered with. Unset renders the
	// schema's own name; see phonecodes.DefaultTablePrefix.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`

	// Lifetime is how long a code stays redeemable. Unset takes
	// phonecodes.DefaultLifetime.
	Lifetime time.Duration `env:"LIFETIME" json:"lifetime,omitempty" yaml:"lifetime,omitempty"`

	// Retention is how long past its deadline a row is kept before the sweeper
	// may collect it. Unset takes phonecodes.DefaultRetention.
	Retention time.Duration `env:"RETENTION" json:"retention,omitempty" yaml:"retention,omitempty"`

	// CodeLength is how many digits a code has, between phonecodes.MinCodeLength
	// and phonecodes.MaxCodeLength. Unset takes phonecodes.DefaultCodeLength.
	CodeLength int `env:"CODE_LENGTH" json:"codeLength,omitempty" yaml:"codeLength,omitempty"`

	// MaxAttempts is how many wrong codes a code survives when its request
	// names no limit of its own. Unset takes phonecodes.DefaultMaxAttempts.
	MaxAttempts int `env:"MAX_ATTEMPTS" json:"maxAttempts,omitempty" yaml:"maxAttempts,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in unset fields. SweepInterval is unset only when nil,
// so a zero reaching this method is a deployment's answer and is left alone.
func (cfg *Config) EnsureDefaults() {
	if cfg.TablePrefix == "" {
		cfg.TablePrefix = phonecodes.DefaultTablePrefix
	}

	if cfg.CodeLength == 0 {
		cfg.CodeLength = phonecodes.DefaultCodeLength
	}

	if cfg.Lifetime == 0 {
		cfg.Lifetime = phonecodes.DefaultLifetime
	}

	if cfg.MaxAttempts == 0 {
		cfg.MaxAttempts = phonecodes.DefaultMaxAttempts
	}

	if cfg.Retention == 0 {
		cfg.Retention = phonecodes.DefaultRetention
	}

	cfg.SweepInterval = cfgnorm.EnsureSweepInterval(cfg.SweepInterval, DefaultSweepInterval)
}

// ValidateWithContext validates a Config.
//
// A zero in any numeric field passes, because it is unset and EnsureDefaults
// replaces it; a value outside what the store accepts does not, since
// phonecodes.NewSQLStore would refuse it one step later with less to say about
// where it came from. The prefix is vetted against the identifiers it actually
// renders, so one that produces an over-long index name fails here instead of
// at the first migration.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.TablePrefix, validation.By(func(any) error {
			return migrations.ValidatePrefix(cfg.TablePrefix)
		})),
		validation.Field(&cfg.CodeLength, validation.When(cfg.CodeLength != 0,
			validation.Min(phonecodes.MinCodeLength), validation.Max(phonecodes.MaxCodeLength))),
		validation.Field(&cfg.Lifetime, validation.Min(time.Duration(0))),
		validation.Field(&cfg.Retention, validation.Min(time.Duration(0))),
		validation.Field(&cfg.MaxAttempts, validation.Min(0)),
		validation.Field(&cfg.SweepInterval, cfgnorm.SweepIntervalRule),
	)
}

// NewStore builds the Store. client must be the database holding the code
// table.
//
// The sweeper, when the config starts one, is bound to ctx: it stops when
// whatever scope owns this store does.
//
// The store is built into a variable and returned only once its error is known
// to be nil. phonecodes.NewSQLStore returns its own concrete type, so returning
// it straight through would convert a nil *phonecodes.SQLStore into a non-nil
// phonecodes.Store on the error path.
func NewStore(ctx context.Context, cfg *Config, client database.Client, opts ...Option) (phonecodes.Store, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating phone code store config")
	}

	options := newOptions(opts)

	base := []phonecodes.Option{
		phonecodes.WithTablePrefix(cfg.TablePrefix),
		phonecodes.WithCodeLength(cfg.CodeLength),
		phonecodes.WithLifetime(cfg.Lifetime),
		phonecodes.WithMaxAttempts(cfg.MaxAttempts),
		phonecodes.WithRetention(cfg.Retention),
		phonecodes.WithSweeper(ctx, pointer.Dereference(cfg.SweepInterval)),
		phonecodes.WithLogger(options.logger),
		phonecodes.WithTracerProvider(options.tracerProvider),
		phonecodes.WithMetricsProvider(options.metricsProvider),
	}

	store, storeErr := phonecodes.NewSQLStore(client, append(base, options.store...)...)
	if storeErr != nil {
		return nil, storeErr
	}

	return store, nil
}

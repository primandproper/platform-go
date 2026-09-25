/*
Package passwordresetcfg assembles a passwordreset Store and Service from
environment configuration.

Four settings, and each is a number a deployment decides rather than a policy
this module does: the table prefix, which has to match the one the migrations
were rendered with; how long a link stays spendable; how long a request takes
at a minimum; and how often expired rows are swept. The dialect comes from the
database.Client, so it cannot disagree with the database the statements run
against.

Three of the Service's dependencies are not configuration, and the absence of a
field for them is the reason this package resolves them from the injector
rather than the reason it could not exist:

  - The passwordreset.Mailer is the application's by definition. The library
    does not know the URL a link points at, the language it is written in, or
    whose branding it carries, so there is nothing an environment variable
    could name. It is required: a container that registers none fails at boot,
    naming it, the way one that registers no comments.Targets does.
  - The directory is the identity.Store registered from the same injector, so
    the users a reset is issued for are read from the same prefix sign-in reads
    them from. A consumer whose directory is not identity's schema implements
    passwordreset.Directory and builds the Service with NewService.
  - The authentication.Authenticator is resolved from the injector as well, and
    that is a ruling rather than a convenience. The engine that hashes the
    password a reset writes has to be the engine sign-in verifies it with; a
    reset that hashes differently is a login that fails immediately after a
    successful reset. A default here — argon2, overridable by a registered one —
    would make that divergence the easy path: an application that configured
    sign-in with its own policy and forgot this one would get two. So there is
    no default, a container that registers none fails at boot naming it, and
    the one registration serves every component that hashes a password.

The privacy seam is not here either, for the reason the composition root gives
of every registry: passwordreset/privacy needs a mapping from a person to the
tenants they belong to, and no environment variable can express one.
*/
package passwordresetcfg

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset"
	"github.com/primandproper/platform-go/v14/authentication/passwordreset/migrations"

	"github.com/primandproper/primitives-go/v2/authentication"
	"github.com/primandproper/primitives-go/v2/config/cfgnorm"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/pointer"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// DefaultSweepInterval is how often the store removes rows past their
// deadlines, when nothing says otherwise.
const DefaultSweepInterval = 5 * time.Minute

// Config assembles a passwordreset Store and Service.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// RequestFloor is how long Service.Request takes at a minimum. Unset takes
	// passwordreset.DefaultRequestFloor; zero turns the padding off.
	//
	// It is a pointer for the reason SweepInterval is: unset and zero are
	// different answers. Zero is a deployment deciding that the timing
	// difference between a known and an unknown address is somebody else's
	// problem — a proxy that normalizes response times — and that has to be
	// said rather than reached by leaving a field out. See
	// passwordreset.WithRequestFloor, which says to set it from a measurement.
	RequestFloor *time.Duration `env:"REQUEST_FLOOR" json:"requestFloor,omitempty" yaml:"requestFloor,omitempty"`

	// SweepInterval is how often the store removes rows past their deadlines.
	// Unset takes DefaultSweepInterval; zero starts no sweeper, which is right
	// when a scheduler calls Sweep for the fleet instead and wrong when nothing
	// does, since the table then grows by a row for every password anybody
	// ever forgot.
	SweepInterval *time.Duration `env:"SWEEP_INTERVAL" json:"sweepInterval,omitempty" yaml:"sweepInterval,omitempty"`

	// TablePrefix is the namespace prepended to the token table's name. It must
	// match the prefix the migrations were rendered with. Unset renders the
	// schema's own name; see passwordreset.DefaultTablePrefix.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`

	// TokenLifetime is how long a link Service.Request mints stays spendable.
	// Unset takes passwordreset.DefaultTokenLifetime.
	TokenLifetime time.Duration `env:"TOKEN_LIFETIME" json:"tokenLifetime,omitempty" yaml:"tokenLifetime,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// EnsureDefaults fills in unset fields. The two pointers are unset only when
// nil, so a zero reaching this method is a deployment's answer and is left
// alone.
func (cfg *Config) EnsureDefaults() {
	if cfg.TablePrefix == "" {
		cfg.TablePrefix = passwordreset.DefaultTablePrefix
	}

	if cfg.TokenLifetime == 0 {
		cfg.TokenLifetime = passwordreset.DefaultTokenLifetime
	}

	if cfg.RequestFloor == nil {
		cfg.RequestFloor = pointer.To(passwordreset.DefaultRequestFloor)
	}

	cfg.SweepInterval = cfgnorm.EnsureSweepInterval(cfg.SweepInterval, DefaultSweepInterval)
}

// requestFloorRule permits a nil or non-negative floor. A negative one would
// reach the Service as "no padding", which zero already says without the
// ambiguity, so a deployment that wrote one is describing a floor it will not
// get.
var requestFloorRule = validation.By(func(value any) error {
	if floor, ok := value.(*time.Duration); ok && floor != nil && *floor < 0 {
		return errors.New("must be a non-negative duration; zero turns the padding off")
	}

	return nil
})

// ValidateWithContext validates a Config.
//
// The prefix is vetted against the identifiers it actually renders rather than
// against a pattern, so a prefix that is legal in isolation but produces an
// over-long index name fails here instead of at the first migration.
//
// A zero TokenLifetime passes, because it is unset and EnsureDefaults replaces
// it; a negative one does not, since passwordreset.NewService would refuse it
// one step later with less to say about where it came from.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.TablePrefix, validation.By(func(any) error {
			return migrations.ValidatePrefix(cfg.TablePrefix)
		})),
		validation.Field(&cfg.TokenLifetime, validation.Min(time.Duration(0))),
		validation.Field(&cfg.RequestFloor, requestFloorRule),
		validation.Field(&cfg.SweepInterval, cfgnorm.SweepIntervalRule),
	)
}

// NewStore builds the Store. client must be the database holding the token
// table.
//
// The sweeper, when the config starts one, is bound to ctx: it stops when
// whatever scope owns this store does.
//
// The store is built into a variable and returned only once its error is known
// to be nil. passwordreset.NewSQLStore returns its own concrete type, so
// returning it straight through would convert a nil *passwordreset.SQLStore
// into a non-nil passwordreset.Store on the error path, and a caller testing the
// result against nil would find a store that panics on first use.
func NewStore(ctx context.Context, cfg *Config, client database.Client, opts ...Option) (passwordreset.Store, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating password reset config")
	}

	options := newOptions(opts)

	base := []passwordreset.Option{
		passwordreset.WithLogger(options.logger),
		passwordreset.WithTracerProvider(options.tracerProvider),
		passwordreset.WithMetricsProvider(options.metricsProvider),
		passwordreset.WithSweeper(ctx, pointer.Dereference(cfg.SweepInterval)),
	}

	store, storeErr := passwordreset.NewSQLStore(
		&passwordreset.Config{TablePrefix: cfg.TablePrefix},
		client,
		append(base, options.store...)...,
	)
	if storeErr != nil {
		return nil, storeErr
	}

	return store, nil
}

// NewService builds the reset flow over a Store.
//
// directory, authenticator and mailer are parameters rather than fields for
// the reasons the package documentation gives, and RegisterService resolves
// all three from the injector. The config supplies the token lifetime and the
// request floor; options passed through WithServiceOptions apply after those,
// so a caller can override either.
func NewService(
	ctx context.Context,
	cfg *Config,
	client database.Client,
	store passwordreset.Store,
	directory passwordreset.Directory,
	authenticator authentication.Authenticator,
	mailer passwordreset.Mailer,
	opts ...Option,
) (*passwordreset.Service, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating password reset config")
	}

	options := newOptions(opts)

	base := []passwordreset.ServiceOption{
		passwordreset.WithTokenLifetime(cfg.TokenLifetime),
		passwordreset.WithRequestFloor(pointer.Dereference(cfg.RequestFloor)),
		passwordreset.WithServiceLogger(options.logger),
		passwordreset.WithServiceTracerProvider(options.tracerProvider),
		passwordreset.WithServiceMetricsProvider(options.metricsProvider),
	}

	return passwordreset.NewService(client, store, directory, authenticator, mailer,
		append(base, options.service...)...)
}

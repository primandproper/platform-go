/*
Package signincfg assembles a signin Service from environment configuration.

A service built here is a whole sign-in server, so it turns on what nearly
every deployment wants and leaves opt-in only what has a reason to be:

  - RefreshTokens is refresh-token rotation, and it is always on. A sign-in
    mints a rotating pair over the refreshtokens table, so a person stays
    signed in past the access token's hour without typing a password again.
    The block holds the store's settings, not a switch.
  - RecoveryCodes is the way back in for somebody who has lost their
    authenticator, and it is always on. A second-factor code that is not the
    TOTP code is tried as a recovery code. Without it a lost phone is a support
    ticket, which is not a choice a deployment should make by leaving a block
    out.
  - Registration is the door that creates people. It is on unless its Disabled
    field is set, and while it is on the service registers through identity's
    Service. Disabled is a field rather than an absent block, because the
    decision it records is to keep strangers from creating accounts, and that
    decision should be written down. Its Closed field is the narrower one: it
    closes the sign-up door on the wire and leaves registration in process
    alone, for a deployment whose own code provisions people. Either one is
    read by ServerOptions, so a closed door answers REGISTRATION_CLOSED rather
    than a refusal a client cannot tell from a broken server.
  - MagicLinks is the passwordless door, and it is the one opt-in block. It is
    on when the block is present, and then the service mints and redeems
    sign-in links over the magiclinks table. It cannot work until the
    application supplies what delivers the mail, so a deployment switches it on
    by supplying that and naming the block.

The handle reminder door has no block. It mints nothing and has no table, so the
one thing it needs is what delivers the mail, and supplying that is the switch:
a service handed a signin.HandleReminderMailer reminds people of their handle,
and one handed none refuses that door with
signin.ErrHandleRemindersNotConfigured. HandleReminderFloor is the door's one
setting, and it is read whether or not the mailer is there.

MagicLinks' presence means what service.Config means by it, because the rule
is the same one applied one level further down. Environment parsing allocates
the block, and a block that holds nothing beyond what an empty environment
parses to is released before it is read. So SIGN_IN_MAGIC_LINKS_TABLE_PREFIX
switches the door on and an unset environment leaves it off. A block that
spells out nothing but defaults configures nothing, which is the edge
service.Config documents.

A deployment that wants neither rotation nor recovery codes, such as one that
checks a password here and keeps people signed in through its own sessions,
builds the service with signin.NewService, which attaches only the stores it is
handed.

What this package does not hold, and why:

  - The authentication.Authenticator. RegisterService resolves it from the
    injector as required, with no default, for the reason passwordresetcfg
    gives. The engine that hashes a password at registration or after a reset
    must be the engine sign-in verifies it with, and one registration serves
    every component that hashes one.
  - The token issuer, which is the Tokens block's, resolved as tokens.Issuer.
  - The directory. It is the identity.Store registered from the same injector,
    and it serves as the Verifications as well. That is not a separate switch,
    because the directory already holds the verification columns. Leaving
    signin.WithVerifications off a hand-built service compiles and yields a
    service with no email verification, and a block cannot make that mistake.
  - The registrar and the magic-link mailer. They are the application's, and
    each is required exactly when its door is on. A container that leaves
    registration open and holds no *identity.Service, or that names a
    MagicLinks block and holds no signin.MagicLinkMailer, fails at boot naming
    what it wanted, rather than mounting a door that refuses every request.
  - Hooks, a password policy and a claims builder. These are code rather than
    configuration, so RegisterService uses whichever of them the application
    registered and leaves the service's default in place otherwise, which is
    the reading identitycfg takes of identity.Hooks.
*/
package signincfg

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	signingrpc "github.com/primandproper/platform-go/v14/authentication/signin/grpc"
	magiclinkmigrations "github.com/primandproper/platform-go/v14/authentication/signin/magiclinks/migrations"
	recoverycodemigrations "github.com/primandproper/platform-go/v14/authentication/signin/recoverycodes/migrations"
	refreshtokenmigrations "github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens/migrations"
	"github.com/primandproper/platform-go/v14/internal/scheduledjob"

	"github.com/primandproper/primitives-go/v2/config/cfgnorm"
	"github.com/primandproper/primitives-go/v2/errors"
	jobscfg "github.com/primandproper/primitives-go/v2/jobs/config"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// DefaultSweepInterval is how often the refresh-token and sign-in link stores
// remove rows past their purge deadlines, when their block says nothing.
const DefaultSweepInterval = 5 * time.Minute

// The two spellings SecondFactor accepts, which are the policies' own String
// renderings so a log line and a config file say the same thing.
const (
	SecondFactorWhenEnrolled = "when_enrolled"
	SecondFactorRequired     = "required"
)

// secondFactors maps each accepted spelling to its policy. The empty string is
// the service's own default.
var secondFactors = map[string]signin.SecondFactorPolicy{
	"":                       signin.SecondFactorWhenEnrolled,
	SecondFactorWhenEnrolled: signin.SecondFactorWhenEnrolled,
	SecondFactorRequired:     signin.SecondFactorRequired,
}

// Config assembles a signin Service.
type Config struct {
	_ struct{} `json:"-" yaml:"-"`

	// MagicLinks switches the passwordless door on. It is the one block whose
	// presence is the switch. See the package documentation for what presence
	// means.
	MagicLinks *MagicLinksConfig `env:",init" envPrefix:"MAGIC_LINKS_" json:"magicLinks,omitempty" yaml:"magicLinks,omitempty"`

	// SecondFactor is what happens to a user who holds no proven second factor:
	// SecondFactorWhenEnrolled, which is the default, or SecondFactorRequired.
	// See signin.SecondFactorRequired before switching to it.
	SecondFactor string `env:"SECOND_FACTOR" json:"secondFactor,omitempty" yaml:"secondFactor,omitempty"`

	// TOTPIssuer is the name an authenticator app labels an enrollment with.
	// There is no default, for the reason signin.WithTOTPIssuer gives, and a
	// deployment that enrolls nobody needs none.
	TOTPIssuer string `env:"TOTP_ISSUER" json:"totpIssuer,omitempty" yaml:"totpIssuer,omitempty"`

	// RecoveryCodes is the recovery code store's settings. Recovery codes are
	// always on.
	RecoveryCodes RecoveryCodesConfig `envPrefix:"RECOVERY_CODES_" json:"recoveryCodes" yaml:"recoveryCodes"`

	// DefaultOwnerRoles are the roles a registrant holds in the account their
	// registration mints, unless the service's RegistrationPolicy replaces
	// them. They are the deployment's own role names and are required: the
	// library never picks one, and a deployment that names none fails at
	// startup rather than on its first sign-up. See signin.NewService.
	DefaultOwnerRoles []string `env:"DEFAULT_OWNER_ROLES" json:"defaultOwnerRoles,omitempty" yaml:"defaultOwnerRoles,omitempty"`

	// AdminServiceRoles names the identity service roles that admit an
	// administrative sign-in. Empty means the service has no administrative
	// door, as signin.WithAdminServiceRoles documents.
	AdminServiceRoles []string `env:"ADMIN_SERVICE_ROLES" json:"adminServiceRoles,omitempty" yaml:"adminServiceRoles,omitempty"`

	// RefreshTokens is the refresh token store's settings. Rotation is always
	// on.
	RefreshTokens RefreshTokensConfig `envPrefix:"REFRESH_TOKENS_" json:"refreshTokens" yaml:"refreshTokens"`

	// Registration is the registration door's settings. The door is on unless
	// Registration.Disabled is set.
	Registration RegistrationConfig `envPrefix:"REGISTRATION_" json:"registration" yaml:"registration"`

	// TokenTTL is how long an ordinary sign-in's token lives. Unset takes
	// signin.DefaultTokenTTL.
	TokenTTL time.Duration `env:"TOKEN_TTL" json:"tokenTTL,omitempty" yaml:"tokenTTL,omitempty"`

	// AdminTokenTTL is how long an administrative sign-in's token lives. Unset
	// takes signin.DefaultAdminTokenTTL.
	AdminTokenTTL time.Duration `env:"ADMIN_TOKEN_TTL" json:"adminTokenTTL,omitempty" yaml:"adminTokenTTL,omitempty"`

	// ImpersonationTokenTTL is how long an impersonation token lives. Unset
	// takes signin.DefaultImpersonationTokenTTL. It does nothing without a
	// registered signin.ImpersonationPolicy, which is what opens the door.
	ImpersonationTokenTTL time.Duration `env:"IMPERSONATION_TOKEN_TTL" json:"impersonationTokenTTL,omitempty" yaml:"impersonationTokenTTL,omitempty"`

	// HandleReminderFloor is the minimum time Service.RequestHandleReminder
	// takes. Unset takes signin.DefaultHandleReminderFloor. Set it above the
	// slowest mail send the deployment makes, as signin.WithHandleReminderFloor
	// explains.
	HandleReminderFloor time.Duration `env:"HANDLE_REMINDER_FLOOR" json:"handleReminderFloor,omitempty" yaml:"handleReminderFloor,omitempty"`
}

// RefreshTokensConfig is refresh-token rotation's block.
type RefreshTokensConfig struct {
	_ struct{} `json:"-" yaml:"-"`

	// SweepInterval is how often expired rows are removed. Unset takes
	// DefaultSweepInterval. Zero starts no sweeper, which is correct only when a
	// scheduler calls Sweep for the fleet instead.
	SweepInterval *time.Duration `env:"SWEEP_INTERVAL" json:"sweepInterval,omitempty" yaml:"sweepInterval,omitempty"`

	// TablePrefix is the namespace prepended to the refresh token table's name.
	// It must match the prefix the migrations were rendered with.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`

	// SweepJob is the scheduled sweep NewJobs renders: the refresh token
	// store's Sweep, run once across a fleet under the scheduler's lock rather
	// than once per replica. It runs unless Disabled, every
	// DefaultSweepInterval unless it names its own schedule. It is independent
	// of SweepInterval's in-process loop, which is the sweep a deployment with
	// no scheduler still gets; a fleet that schedules this job can set
	// SWEEP_INTERVAL=0 and leave the one.
	SweepJob jobscfg.JobConfig `env:",init" envPrefix:"SWEEP_JOB_" json:"sweepJob,omitzero" yaml:"sweepJob,omitempty"`

	// TTL is how long an ordinary sign-in lasts. Unset takes
	// signin.DefaultRefreshTokenTTL.
	TTL time.Duration `env:"TTL" json:"ttl,omitempty" yaml:"ttl,omitempty"`

	// AdminTTL is how long an administrative sign-in lasts. Unset takes
	// signin.DefaultAdminRefreshTokenTTL.
	AdminTTL time.Duration `env:"ADMIN_TTL" json:"adminTTL,omitempty" yaml:"adminTTL,omitempty"`

	// RefuseSupersededTokens makes signin.Service.CheckSignIn refuse an access
	// token its login has since replaced, as signin.WithSupersededTokenRefusal
	// documents. Unset is false, which is the family model's own reading: an
	// access token stands until its login ends or it expires. It is a yes rather
	// than a no to opt out of, because it changes what a client that refreshes
	// mid-request sees, and that is a deployment's decision to have made.
	RefuseSupersededTokens bool `env:"REFUSE_SUPERSEDED_TOKENS" json:"refuseSupersededTokens,omitempty" yaml:"refuseSupersededTokens,omitempty"`
}

// MagicLinksConfig is the passwordless door's block.
type MagicLinksConfig struct {
	_ struct{} `json:"-" yaml:"-"`

	// SweepInterval is how often expired rows are removed. Unset takes
	// DefaultSweepInterval. Zero starts no sweeper.
	SweepInterval *time.Duration `env:"SWEEP_INTERVAL" json:"sweepInterval,omitempty" yaml:"sweepInterval,omitempty"`

	// TablePrefix is the namespace prepended to the sign-in link table's name.
	// It must match the prefix the migrations were rendered with.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`

	// TTL is how long a sign-in link stays redeemable. Unset takes
	// signin.DefaultMagicLinkTTL.
	TTL time.Duration `env:"TTL" json:"ttl,omitempty" yaml:"ttl,omitempty"`

	// RequestFloor is the minimum time Service.RequestMagicLink takes. Unset
	// takes signin.DefaultMagicLinkRequestFloor. Set it above the slowest mail
	// send the deployment makes. signin.WithMagicLinkRequestFloor explains why.
	RequestFloor time.Duration `env:"REQUEST_FLOOR" json:"requestFloor,omitempty" yaml:"requestFloor,omitempty"`
}

// RecoveryCodesConfig is the recovery codes block.
type RecoveryCodesConfig struct {
	_ struct{} `json:"-" yaml:"-"`

	// TablePrefix is the namespace prepended to the recovery code table's name.
	// It must match the prefix the migrations were rendered with.
	TablePrefix string `env:"TABLE_PREFIX" json:"tablePrefix,omitempty" yaml:"tablePrefix,omitempty"`

	// Count is how many codes a set holds. Unset takes
	// signin.DefaultRecoveryCodeCount.
	Count int `env:"COUNT" json:"count,omitempty" yaml:"count,omitempty"`
}

// RegistrationConfig is the registration door's block.
type RegistrationConfig struct {
	_ struct{} `json:"-" yaml:"-"`

	// Disabled closes the registration door, so Service.Register refuses and
	// no *identity.Service is needed. Unset leaves it open. A disabled door is
	// closed on the wire as well, as Closed closes it.
	Disabled bool `env:"DISABLED" json:"disabled,omitempty" yaml:"disabled,omitempty"`

	// Closed closes the sign-up door on the wire and nowhere else: the server
	// ServerOptions configures refuses every Register with
	// signin.ErrRegistrationClosed, carrying REGISTRATION_CLOSED, while the
	// service still registers whoever the application's own code hands it. It
	// is for a deployment that provisions people itself, from an operator tool
	// or an import, and wants no stranger signing up. Unset leaves the door
	// open, which is signin/grpc's default. A deployment that wants sign-up for
	// some people and not others keeps it open and says who in its
	// signin.RegistrationPolicy.
	Closed bool `env:"CLOSED" json:"closed,omitempty" yaml:"closed,omitempty"`

	// VerificationLinkTTL is how long the link minted at registration stays
	// answerable. Unset takes signin.DefaultVerificationLinkTTL.
	VerificationLinkTTL time.Duration `env:"VERIFICATION_LINK_TTL" json:"verificationLinkTTL,omitempty" yaml:"verificationLinkTTL,omitempty"`
}

var (
	_ validation.ValidatableWithContext = (*Config)(nil)
	_ validation.ValidatableWithContext = (*RefreshTokensConfig)(nil)
	_ validation.ValidatableWithContext = (*MagicLinksConfig)(nil)
	_ validation.ValidatableWithContext = (*RecoveryCodesConfig)(nil)
	_ validation.ValidatableWithContext = (*RegistrationConfig)(nil)
)

// nonNegative refuses a negative duration or count. Zero is unset and is
// defaulted, either here or by the option it becomes, so a negative value is a
// deployment describing a setting it will not get.
var nonNegative = validation.Min(0)

// EnsureDefaults fills in the refresh token store's unset sweep interval. The
// pointer is unset only when nil, so a zero is the deployment's answer.
func (cfg *RefreshTokensConfig) EnsureDefaults() {
	cfg.SweepInterval = cfgnorm.EnsureSweepInterval(cfg.SweepInterval, DefaultSweepInterval)

	scheduledjob.EnsureDefaults(&cfg.SweepJob, DefaultSweepInterval, DefaultSweepJobLeaseTTL)
}

// ValidateWithContext validates a RefreshTokensConfig.
func (cfg *RefreshTokensConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.TablePrefix, validation.By(func(any) error {
			return refreshtokenmigrations.ValidatePrefix(cfg.TablePrefix)
		})),
		validation.Field(&cfg.TTL, validation.Min(time.Duration(0))),
		validation.Field(&cfg.AdminTTL, validation.Min(time.Duration(0))),
		validation.Field(&cfg.SweepInterval, cfgnorm.SweepIntervalRule),
		validation.Field(&cfg.SweepJob, validation.By(func(any) error {
			return scheduledjob.Validate(ctx, &cfg.SweepJob)
		})),
	)
}

// EnsureDefaults fills in the sign-in link store's unset sweep interval.
func (cfg *MagicLinksConfig) EnsureDefaults() {
	cfg.SweepInterval = cfgnorm.EnsureSweepInterval(cfg.SweepInterval, DefaultSweepInterval)
}

// ValidateWithContext validates a MagicLinksConfig.
func (cfg *MagicLinksConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.TablePrefix, validation.By(func(any) error {
			return magiclinkmigrations.ValidatePrefix(cfg.TablePrefix)
		})),
		validation.Field(&cfg.TTL, validation.Min(time.Duration(0))),
		validation.Field(&cfg.RequestFloor, validation.Min(time.Duration(0))),
		validation.Field(&cfg.SweepInterval, cfgnorm.SweepIntervalRule),
	)
}

// ValidateWithContext validates a RecoveryCodesConfig.
func (cfg *RecoveryCodesConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.TablePrefix, validation.By(func(any) error {
			return recoverycodemigrations.ValidatePrefix(cfg.TablePrefix)
		})),
		validation.Field(&cfg.Count, nonNegative),
	)
}

// ValidateWithContext validates a RegistrationConfig.
func (cfg *RegistrationConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.VerificationLinkTTL, validation.Min(time.Duration(0))),
	)
}

// ValidateWithContext releases a MagicLinks block that configures nothing,
// applies every block's defaults, and then validates what is left.
//
// It follows service.Config's order for service.Config's reason. Until the
// MagicLinks block that `env:",init"` allocated has been released, the door
// looks switched on. Defaulting before the release would fill the block in,
// and it would never look unconfigured again. So the release is part of
// validation and not a separate step, and a caller holding a validated Config
// holds one whose nil MagicLinks means the door is off.
//
// The three blocks held by value are defaulted here directly, because
// cfgnorm.EnsureSubDefaults reaches only the pointer blocks.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	if err := cfgnorm.UnconfiguredToNil(cfg); err != nil {
		return err
	}

	if err := cfgnorm.EnsureSubDefaults(cfg); err != nil {
		return err
	}

	cfg.RefreshTokens.EnsureDefaults()

	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.SecondFactor, validation.By(func(any) error {
			if _, ok := secondFactors[cfg.SecondFactor]; !ok {
				return errors.Newf("must be %q or %q", SecondFactorWhenEnrolled, SecondFactorRequired)
			}

			return nil
		})),
		validation.Field(&cfg.DefaultOwnerRoles, validation.Required, validation.Each(validation.Required)),
		validation.Field(&cfg.TokenTTL, validation.Min(time.Duration(0))),
		validation.Field(&cfg.AdminTokenTTL, validation.Min(time.Duration(0))),
		validation.Field(&cfg.ImpersonationTokenTTL, validation.Min(time.Duration(0))),
		validation.Field(&cfg.HandleReminderFloor, validation.Min(time.Duration(0))),
		validation.Field(&cfg.RefreshTokens, byValue(&cfg.RefreshTokens)),
		validation.Field(&cfg.MagicLinks),
		validation.Field(&cfg.RecoveryCodes, byValue(&cfg.RecoveryCodes)),
		validation.Field(&cfg.Registration, byValue(&cfg.Registration)),
	)
}

// ServerOptions is the server half of the config, as the signingrpc options
// that carry it: whether the sign-up door is closed on the wire, which it is
// when Registration names either Closed or Disabled. A disabled door is
// closed here too because the service behind it has no registrar, and a
// Register that reached it would be refused as a wiring failure — a 500 a
// client cannot tell from a broken server — where a closed door answers
// REGISTRATION_CLOSED before the service sees anything.
//
// It is the one place that mapping is written, as identitycfg's ServerOptions
// is for that surface: service's mount reads it, and so does any composition
// root that builds signingrpc.NewServer itself from a Config it was handed.
func (cfg *Config) ServerOptions() []signingrpc.Option {
	var opts []signingrpc.Option

	if cfg.Registration.Closed || cfg.Registration.Disabled {
		opts = append(opts, signingrpc.WithoutOpenRegistration())
	}

	return opts
}

// byValue validates a block the Config holds by value.
//
// ozzo validates a field through the field's value, and a value does not carry
// the pointer-receiver ValidateWithContext these blocks declare. Without this
// rule the three blocks held by value would pass validation unexamined.
func byValue(block validation.ValidatableWithContext) validation.Rule {
	return validation.WithContext(func(ctx context.Context, _ any) error {
		return block.ValidateWithContext(ctx)
	})
}

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
    decision should be written down.
  - MagicLinks is the passwordless door, and it is the one opt-in block. It is
    on when the block is present, and then the service mints and redeems
    sign-in links over the magiclinks table. It cannot work until the
    application supplies what delivers the mail, so a deployment switches it on
    by supplying that and naming the block.

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
	magiclinkmigrations "github.com/primandproper/platform-go/v14/authentication/signin/magiclinks/migrations"
	recoverycodemigrations "github.com/primandproper/platform-go/v14/authentication/signin/recoverycodes/migrations"
	refreshtokenmigrations "github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens/migrations"

	"github.com/primandproper/primitives-go/v2/config/cfgnorm"
	"github.com/primandproper/primitives-go/v2/errors"

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

	// TTL is how long an ordinary sign-in lasts. Unset takes
	// signin.DefaultRefreshTokenTTL.
	TTL time.Duration `env:"TTL" json:"ttl,omitempty" yaml:"ttl,omitempty"`

	// AdminTTL is how long an administrative sign-in lasts. Unset takes
	// signin.DefaultAdminRefreshTokenTTL.
	AdminTTL time.Duration `env:"ADMIN_TTL" json:"adminTTL,omitempty" yaml:"adminTTL,omitempty"`
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
	// no *identity.Service is needed. Unset leaves it open.
	Disabled bool `env:"DISABLED" json:"disabled,omitempty" yaml:"disabled,omitempty"`

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
		validation.Field(&cfg.TokenTTL, validation.Min(time.Duration(0))),
		validation.Field(&cfg.AdminTokenTTL, validation.Min(time.Duration(0))),
		validation.Field(&cfg.RefreshTokens, byValue(&cfg.RefreshTokens)),
		validation.Field(&cfg.MagicLinks),
		validation.Field(&cfg.RecoveryCodes, byValue(&cfg.RecoveryCodes)),
		validation.Field(&cfg.Registration, byValue(&cfg.Registration)),
	)
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

/*
Package signincfg assembles a signin Service from environment configuration.

Sign-in is where "a present block switches a feature on" fits most closely,
because most of what signin.NewService accepts is an optional store, and each
store is a feature a deployment turns on. Each one is a nested block here:

  - RefreshTokens is refresh-token rotation. When it is present, a sign-in mints
    a rotating pair over the refreshtokens table. When it is absent, the service
    issues one token per sign-in and the three refresh doors refuse, as
    signin.WithRefreshTokenStore documents.
  - MagicLinks is the passwordless door. When it is present, the service mints
    and redeems sign-in links over the magiclinks table. When it is absent, both
    of those doors refuse.
  - RecoveryCodes is the way back in for somebody who has lost their
    authenticator. When it is present, a second-factor code that is not the
    TOTP code is tried as a recovery code. When it is absent, a second factor is
    a TOTP code and nothing else.
  - Registration is the door that creates people. When it is present, the
    service registers through identity's Service. When it is absent,
    Service.Register refuses. It has no table of its own, but it is still a
    block and not a flag, because whether a public service creates accounts is
    a decision a deployment makes deliberately, and the verification link's
    lifetime only means something when the door is open.

Presence means what service.Config means by it, because the rule is the same
one applied one level further down. Environment parsing allocates every
block, so a block that holds nothing beyond what an empty environment parses
to is released before it is read. As a result, SIGN_IN_REFRESH_TOKENS_TABLE_PREFIX
switches rotation on and an unset environment switches nothing on. That
includes the edge service.Config documents: a block that spells out nothing
but defaults configures nothing. A block assembled in code or in a file turns
its feature on by naming anything in it, which is almost always its table
prefix.

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
    each is required exactly when its block is present. A Registration block
    whose container holds no *identity.Service, or a MagicLinks block whose
    container holds no signin.MagicLinkMailer, fails at boot naming what it
    wanted, rather than mounting a door that refuses every request.
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

	// MagicLinks switches the passwordless door on. See the package
	// documentation for what presence means.
	MagicLinks *MagicLinksConfig `env:",init" envPrefix:"MAGIC_LINKS_" json:"magicLinks,omitempty" yaml:"magicLinks,omitempty"`

	// RecoveryCodes switches recovery codes on.
	RecoveryCodes *RecoveryCodesConfig `env:",init" envPrefix:"RECOVERY_CODES_" json:"recoveryCodes,omitempty" yaml:"recoveryCodes,omitempty"`

	// RefreshTokens switches refresh-token rotation on.
	RefreshTokens *RefreshTokensConfig `env:",init" envPrefix:"REFRESH_TOKENS_" json:"refreshTokens,omitempty" yaml:"refreshTokens,omitempty"`

	// Registration switches the registration door on.
	Registration *RegistrationConfig `env:",init" envPrefix:"REGISTRATION_" json:"registration,omitempty" yaml:"registration,omitempty"`

	// SecondFactor is what happens to a user who holds no proven second factor:
	// SecondFactorWhenEnrolled, which is the default, or SecondFactorRequired.
	// See signin.SecondFactorRequired before switching to it.
	SecondFactor string `env:"SECOND_FACTOR" json:"secondFactor,omitempty" yaml:"secondFactor,omitempty"`

	// TOTPIssuer is the name an authenticator app labels an enrollment with.
	// There is no default, for the reason signin.WithTOTPIssuer gives, and a
	// deployment that enrolls nobody needs none.
	TOTPIssuer string `env:"TOTP_ISSUER" json:"totpIssuer,omitempty" yaml:"totpIssuer,omitempty"`

	// AdminServiceRoles names the identity service roles that admit an
	// administrative sign-in. Empty means the service has no administrative
	// door, as signin.WithAdminServiceRoles documents.
	AdminServiceRoles []string `env:"ADMIN_SERVICE_ROLES" json:"adminServiceRoles,omitempty" yaml:"adminServiceRoles,omitempty"`

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

// ValidateWithContext releases the blocks that configure nothing, applies the
// survivors' defaults, and then validates what is left.
//
// It follows service.Config's order for service.Config's reason. Until the
// blocks that `env:",init"` allocated have been released, every feature looks
// switched on. Defaulting before the release would fill every allocated block
// in, and none would ever look unconfigured again. So the release is part of
// validation and not a separate step, and a caller holding a validated Config
// holds one whose nil blocks are the features that are off.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	if err := cfgnorm.UnconfiguredToNil(cfg); err != nil {
		return err
	}

	if err := cfgnorm.EnsureSubDefaults(cfg); err != nil {
		return err
	}

	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.SecondFactor, validation.By(func(any) error {
			if _, ok := secondFactors[cfg.SecondFactor]; !ok {
				return errors.Newf("must be %q or %q", SecondFactorWhenEnrolled, SecondFactorRequired)
			}

			return nil
		})),
		validation.Field(&cfg.TokenTTL, validation.Min(time.Duration(0))),
		validation.Field(&cfg.AdminTokenTTL, validation.Min(time.Duration(0))),
		validation.Field(&cfg.RefreshTokens),
		validation.Field(&cfg.MagicLinks),
		validation.Field(&cfg.RecoveryCodes),
		validation.Field(&cfg.Registration),
	)
}

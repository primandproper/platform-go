package signincfg

import (
	"context"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/magiclinks"
	"github.com/primandproper/platform-go/v14/authentication/signin/recoverycodes"
	"github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens"

	"github.com/primandproper/primitives-go/v2/authentication"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/pointer"
)

// Directory is the service's directory and its verifications in one value,
// which identity's Store is.
//
// The two are one parameter so that a service built here always has email
// verification. A consumer whose directory is not identity's schema, or who
// wants the two apart, builds the service with signin.NewService.
type Directory interface {
	signin.Directory
	signin.Verifications
}

// NewService builds the sign-in service, the refresh token and recovery code
// stores, and the magic link store when its block is present.
//
// client, directory, authenticator and issuer are parameters, not fields. The
// package documentation explains why, and RegisterService resolves all four
// from the injector. Registration left open requires WithRegistrar, and a
// present MagicLinks block requires WithMagicLinkMailer. Either one missing is
// refused here, so it is never discovered by a caller of a door that refuses
// every request.
//
// The refresh token and recovery code stores are always built, so their tables
// must be migrated before the service is used.
//
// The sweepers, when their blocks start them, are bound to ctx.
func NewService(
	ctx context.Context,
	cfg *Config,
	client database.Client,
	directory Directory,
	authenticator authentication.Authenticator,
	issuer signin.TokenIssuer,
	opts ...Option,
) (*signin.Service, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating sign-in config")
	}

	options := newOptions(opts)

	// Both checked before any store is built, so a refusal here has started no
	// sweeper.
	if !cfg.Registration.Disabled && options.registrar == nil {
		return nil, errors.New("registration is not disabled but no registrar was supplied")
	}

	if cfg.MagicLinks != nil && options.magicLinkMailer == nil {
		return nil, errors.New("the magic links block is present but no magic link mailer was supplied")
	}

	serviceOpts := []signin.ServiceOption{
		signin.WithLogger(options.logger),
		signin.WithTracerProvider(options.tracerProvider),
		signin.WithMetricsProvider(options.metricsProvider),
		signin.WithVerifications(directory),
		signin.WithTOTPIssuer(cfg.TOTPIssuer),
		signin.WithAdminServiceRoles(cfg.AdminServiceRoles...),
		signin.WithSecondFactorPolicy(secondFactors[cfg.SecondFactor]),
		signin.WithTokenTTL(cfg.TokenTTL),
		signin.WithAdminTokenTTL(cfg.AdminTokenTTL),
	}

	if block := cfg.Registration; !block.Disabled {
		serviceOpts = append(serviceOpts,
			signin.WithRegistrar(options.registrar),
			signin.WithVerificationLinkTTL(block.VerificationLinkTTL),
		)
	}

	refreshTokenStore, err := refreshtokens.NewSQLStore(&refreshtokens.Config{TablePrefix: cfg.RefreshTokens.TablePrefix}, client,
		append([]refreshtokens.Option{
			refreshtokens.WithLogger(options.logger),
			refreshtokens.WithTracerProvider(options.tracerProvider),
			refreshtokens.WithMetricsProvider(options.metricsProvider),
			refreshtokens.WithSweeper(ctx, pointer.Dereference(cfg.RefreshTokens.SweepInterval)),
		}, options.refreshTokens...)...)
	if err != nil {
		return nil, errors.Wrap(err, "building the refresh token store")
	}

	serviceOpts = append(serviceOpts,
		signin.WithRefreshTokenStore(refreshTokenStore),
		signin.WithRefreshTokenTTL(cfg.RefreshTokens.TTL),
		signin.WithAdminRefreshTokenTTL(cfg.RefreshTokens.AdminTTL),
	)

	if block := cfg.MagicLinks; block != nil {
		store, storeErr := magiclinks.NewSQLStore(&magiclinks.Config{TablePrefix: block.TablePrefix}, client,
			append([]magiclinks.Option{
				magiclinks.WithLogger(options.logger),
				magiclinks.WithTracerProvider(options.tracerProvider),
				magiclinks.WithMetricsProvider(options.metricsProvider),
				magiclinks.WithSweeper(ctx, pointer.Dereference(block.SweepInterval)),
			}, options.magicLinks...)...)
		if storeErr != nil {
			return nil, errors.Wrap(storeErr, "building the sign-in link store")
		}

		serviceOpts = append(serviceOpts,
			signin.WithMagicLinkStore(store),
			signin.WithMagicLinkMailer(options.magicLinkMailer),
			signin.WithMagicLinkTTL(block.TTL),
			signin.WithMagicLinkRequestFloor(block.RequestFloor),
		)
	}

	recoveryCodeStore, err := recoverycodes.NewSQLStore(&recoverycodes.Config{TablePrefix: cfg.RecoveryCodes.TablePrefix}, client,
		append([]recoverycodes.Option{
			recoverycodes.WithLogger(options.logger),
			recoverycodes.WithTracerProvider(options.tracerProvider),
		}, options.recoveryCodes...)...)
	if err != nil {
		return nil, errors.Wrap(err, "building the recovery code store")
	}

	serviceOpts = append(serviceOpts,
		signin.WithRecoveryCodeStore(recoveryCodeStore),
		signin.WithRecoveryCodeCount(cfg.RecoveryCodes.Count),
	)

	return signin.NewService(client, directory, authenticator, issuer, append(serviceOpts, options.service...)...)
}

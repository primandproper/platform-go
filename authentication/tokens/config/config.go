// Package tokenscfg selects and builds a tokens.Issuer from configuration:
// either the JWT signer or the PASETO one.
//
// The signing key is supplied base64-encoded and checked by its decoded length
// rather than merely for being present, because 32 bytes is what both signers
// need. A key that satisfied a Required rule here and was then refused by the
// signer is the failure that buys.
package tokenscfg

import (
	"context"
	"encoding/base64"
	"slices"

	"github.com/primandproper/platform-go/v13/authentication/tokens"
	"github.com/primandproper/platform-go/v13/authentication/tokens/jwt"
	"github.com/primandproper/platform-go/v13/authentication/tokens/paseto"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/cfgnorm"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderJWT represents JWT.
	ProviderJWT = "jwt"
	// ProviderPASETO represents PASETO.
	ProviderPASETO = "paseto"
)

type (
	// Config is the configuration structure.
	Config struct {
		Provider                string `env:"PROVIDER"    json:"provider,omitempty"                yaml:"provider,omitempty"`
		Issuer                  string `env:"ISSUER"      json:"issuer,omitempty"                  yaml:"issuer,omitempty"`
		Audience                string `env:"AUDIENCE"    json:"audience,omitempty"                yaml:"audience,omitempty"`
		Base64EncodedSigningKey string `env:"SIGNING_KEY" json:"base64EncodedSigningKey,omitempty" yaml:"base64EncodedSigningKey,omitempty"`
	}
)

// providers are every provider this package implements. Validation and
// NewTokenIssuer both read it.
var providers = []string{ProviderJWT, ProviderPASETO}

// signingKeyLength is how many bytes both signers need, and is fixed by the
// algorithms rather than chosen here.
const signingKeyLength = 32

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(
		ctx,
		cfg,
		validation.Field(&cfg.Provider, validation.Required, validation.By(func(any) error {
			// Checked normalized, matching dispatch: validating the raw string
			// rejected "JWT" and " paseto " while NewTokenIssuer built them.
			if !slices.Contains(providers, cfgnorm.Provider(cfg.Provider)) {
				return errors.Wrapf(errors.ErrUnknownProvider, "token issuer provider %q", cfg.Provider)
			}

			return nil
		})),
		validation.Field(&cfg.Issuer, validation.Required),
		validation.Field(&cfg.Audience, validation.Required),
		// Checked as a decoded length rather than as a non-empty string,
		// because that is what both signers need and what NewTokenIssuer
		// refuses without. A key that was Required here and the wrong size
		// there passed config validation and failed to build.
		validation.Field(&cfg.Base64EncodedSigningKey, validation.Required, validation.By(func(any) error {
			key, err := base64.URLEncoding.DecodeString(cfg.Base64EncodedSigningKey)
			if err != nil {
				return errors.Wrap(err, "decoding the signing key")
			}

			if len(key) != signingKeyLength {
				return errors.Newf("signing key must decode to %d bytes, got %d", signingKeyLength, len(key))
			}

			return nil
		})),
	)
}

// NewTokenIssuer provides a token issuer.
//
// It takes a context so that the config it is handed goes through the same
// ValidateWithContext a composition root would run — the issuer, audience and
// signing key are what every token carries, and none of those rules were
// reachable from here before.
//
// Each provider is built into a variable and returned only once its error is
// known to be nil. The provider constructors return their own concrete types,
// so returning one straight through would convert a nil *jwt.Signer into a
// non-nil tokens.Issuer on the error path, and a caller testing the result against
// nil would find an issuer that panics on first use.
func (cfg *Config) NewTokenIssuer(ctx context.Context, opts ...Option) (tokens.Issuer, error) {
	o := newOptions(opts)
	logger, tracerProvider := o.logger, o.tracerProvider

	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	provider, err := cfgnorm.SelectProvider(cfg.Provider, providers, "token issuer provider")
	if err != nil {
		return nil, err
	}

	if err = cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating token issuer config")
	}

	// Cannot fail: validation decoded the same string and checked its length.
	decryptedSigningKey, err := base64.URLEncoding.DecodeString(cfg.Base64EncodedSigningKey)
	if err != nil {
		return nil, errors.Wrap(err, "decoding the token signing key")
	}

	switch provider {
	case ProviderJWT:
		signer, signerErr := jwt.NewSigner(cfg.Issuer, cfg.Audience, decryptedSigningKey, jwt.WithLogger(logger), jwt.WithTracerProvider(tracerProvider))
		if signerErr != nil {
			return nil, signerErr
		}

		return signer, nil
	case ProviderPASETO:
		signer, signerErr := paseto.NewSigner(cfg.Issuer, cfg.Audience, decryptedSigningKey, paseto.WithLogger(logger), paseto.WithTracerProvider(tracerProvider))
		if signerErr != nil {
			return nil, signerErr
		}

		return signer, nil
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "token issuer provider %q", cfg.Provider)
	}
}

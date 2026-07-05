package tokenscfg

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v4/authentication/tokens"
	"github.com/primandproper/platform-go/v4/authentication/tokens/jwt"
	"github.com/primandproper/platform-go/v4/authentication/tokens/paseto"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/tracing"

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
		Provider                string        `env:"PROVIDER"                   json:"provider"`
		Issuer                  string        `env:"ISSUER"                     json:"issuer"`
		Audience                string        `env:"AUDIENCE"                   json:"audience"`
		Base64EncodedSigningKey string        `env:"SIGNING_KEY"                json:"base64EncodedSigningKey"`
		MaxAccessTokenLifetime  time.Duration `env:"MAX_ACCESS_TOKEN_LIFETIME"  json:"maxAccessTokenLifetime"`
		MaxRefreshTokenLifetime time.Duration `env:"MAX_REFRESH_TOKEN_LIFETIME" json:"maxRefreshTokenLifetime"`
	}
)

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(
		ctx,
		cfg,
		validation.Field(&cfg.Provider, validation.Required, validation.In(ProviderJWT, ProviderPASETO)),
		validation.Field(&cfg.Issuer, validation.Required),
		validation.Field(&cfg.Audience, validation.Required),
		validation.Field(&cfg.Base64EncodedSigningKey, validation.Required),
	)
}

// ProvideTokenIssuer provides a token issuer.
func (cfg *Config) ProvideTokenIssuer(logger logging.Logger, tracerProvider tracing.TracerProvider) (tokens.Issuer, error) {
	decryptedSigningKey, err := base64.URLEncoding.DecodeString(cfg.Base64EncodedSigningKey)
	if err != nil {
		return nil, fmt.Errorf("decoding json web token signing key: %w", err)
	}

	if len(decryptedSigningKey) != 32 {
		return nil, fmt.Errorf("decoding json web token signing key must be 32 bytes")
	}

	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case ProviderJWT:
		return jwt.NewJWTSigner(logger, tracerProvider, cfg.Issuer, cfg.Audience, decryptedSigningKey)
	case ProviderPASETO:
		return paseto.NewPASETOSigner(logger, tracerProvider, cfg.Issuer, cfg.Audience, decryptedSigningKey)
	default:
		return nil, fmt.Errorf("unknown token issuer provider: %q", cfg.Provider)
	}
}

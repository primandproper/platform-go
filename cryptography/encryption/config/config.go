package config

import (
	"context"
	"strings"

	"github.com/primandproper/platform-go/v2/cryptography/encryption"
	"github.com/primandproper/platform-go/v2/cryptography/encryption/aes"
	"github.com/primandproper/platform-go/v2/cryptography/encryption/salsa20"
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/tracing"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderAES is the AES-GCM encryption provider.
	ProviderAES = "aes"
	// ProviderSalsa20 is the Salsa20 encryption provider.
	ProviderSalsa20 = "salsa20"
)

type (
	// Config is the configuration for the encryption provider.
	Config struct {
		Provider string `env:"PROVIDER" json:"provider"`
	}
)

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Provider, validation.When(cfg.Provider != "", validation.In(ProviderAES, ProviderSalsa20))),
	)
}

// ProvideEncryptorDecryptor provides an EncryptorDecryptor based on the configured provider.
func ProvideEncryptorDecryptor(
	cfg *Config,
	tracerProvider tracing.TracerProvider,
	logger logging.Logger,
	key []byte,
) (encryption.EncryptorDecryptor, error) {
	switch strings.TrimSpace(strings.ToLower(cfg.Provider)) {
	case ProviderAES:
		return aes.NewEncryptorDecryptor(tracerProvider, logger, key)
	default:
		return salsa20.NewEncryptorDecryptor(tracerProvider, logger, key)
	}
}

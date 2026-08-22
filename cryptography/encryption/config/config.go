// Package encryptioncfg builds an encryption keyring over a caller-supplied
// encryption.Keyset, with one cipher provider — AES-256-GCM today — governing
// every key in the ring.
//
// A mixed ring is expressible in principle, since a ciphertext names its key and
// the key determines its cipher, but nothing has wanted one and offering it
// would mean every deployment configuring an algorithm per key forever.
//
// CurrentKeyID names the key new ciphertexts are written under and is required:
// rotation works by changing it, so inferring a default would make the choice
// invisible at the moment it matters most.
package encryptioncfg

import (
	"context"
	"slices"
	"strings"

	"github.com/primandproper/platform-go/v13/cryptography/encryption"
	"github.com/primandproper/platform-go/v13/cryptography/encryption/aes"
	perrors "github.com/primandproper/platform-go/v13/errors"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderAES is the AES-256-GCM encryption provider.
	ProviderAES = "aes"
)

type (
	// Config is the configuration for the encryption keyring.
	Config struct {
		// Provider names the cipher every key in the ring uses.
		Provider string `env:"PROVIDER" json:"provider,omitempty" yaml:"provider,omitempty"`
		// CurrentKeyID names the key new ciphertexts are written under. It has
		// to be one of the keys supplied to NewKeyring, and it is required:
		// rotation works by changing this value, so there is no sensible
		// default and inferring one would make the choice invisible.
		CurrentKeyID string `env:"CURRENT_KEY_ID" json:"currentKeyID,omitempty" yaml:"currentKeyID,omitempty"`
	}
)

var _ validation.ValidatableWithContext = (*Config)(nil)

// providers are every provider this package implements. Validation and the
// dispatch switch both read it, so they cannot drift apart.
var providers = []string{ProviderAES}

// normalize canonicalizes a provider name the way the dispatch switch does.
func normalize(provider string) string {
	return strings.TrimSpace(strings.ToLower(provider))
}

// ValidateWithContext validates a Config struct.
//
// It checks the normalized provider, not the raw string: dispatch lowercases
// and trims, so validating the raw value rejected "AES" and " aes " while the
// factory accepted both.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Provider, validation.Required, validation.By(func(any) error {
			if !slices.Contains(providers, normalize(cfg.Provider)) {
				return perrors.Wrapf(perrors.ErrUnknownProvider, "encryption provider %q", cfg.Provider)
			}

			return nil
		})),
		validation.Field(&cfg.CurrentKeyID, validation.Required),
	)
}

// NewKeyring builds an encryption.Keyring over keys, using the configured
// provider for every one of them.
//
// One provider governs the whole ring rather than one per key. A ciphertext
// names its key, and the key determines its cipher, so a mixed ring is
// expressible — but nothing has ever wanted one, and offering it would mean
// every deployment configuring an algorithm per key forever.
func NewKeyring(
	ctx context.Context,
	cfg *Config,
	keys encryption.Keyset,
	opts ...Option,
) (encryption.EncryptorDecryptor, error) {
	if cfg == nil {
		return nil, perrors.ErrNilInputParameter
	}

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, perrors.Wrap(err, "encryption keyring")
	}

	if len(keys) == 0 {
		return nil, encryption.ErrEmptyKeyring
	}

	o := newOptions(opts)

	ringKeys := make([]encryption.RingKey, 0, len(keys))

	for id, material := range keys {
		cipher, err := newCipher(cfg, material, o)
		if err != nil {
			return nil, perrors.Wrapf(err, "building cipher for key %q", id)
		}

		ringKeys = append(ringKeys, encryption.RingKey{ID: id, Cipher: cipher})
	}

	// Built into a variable and returned only once err is known to be nil:
	// encryption.NewKeyring hands back a *Keyring, and returning it straight
	// through would make a nil one a non-nil EncryptorDecryptor on the error
	// path — a value a caller's != nil check accepts and the first Encrypt
	// panics on.
	keyring, err := encryption.NewKeyring(
		encryption.KeyID(cfg.CurrentKeyID),
		ringKeys,
		encryption.WithLogger(o.logger),
		encryption.WithTracerProvider(o.tracerProvider),
		encryption.WithMetricsProvider(o.metricsProvider),
	)
	if err != nil {
		return nil, err
	}

	return keyring, nil
}

// newCipher dispatches on the configured provider.
//
// Each provider is built into a variable and returned only once its error is
// known to be nil. The provider constructors return their own concrete types,
// so `return aes.NewCipher(...)` would convert a nil *aes.Cipher into a non-nil
// encryption.Cipher on the error path.
func newCipher(cfg *Config, material encryption.MasterKey, o *options) (encryption.Cipher, error) {
	switch normalize(cfg.Provider) {
	case ProviderAES:
		c, err := aes.NewCipher(material, aes.WithLogger(o.logger), aes.WithTracerProvider(o.tracerProvider))
		if err != nil {
			return nil, err
		}

		return c, nil
	default:
		return nil, perrors.Wrapf(perrors.ErrUnknownProvider, "encryption provider %q", cfg.Provider)
	}
}

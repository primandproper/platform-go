package cookies

import (
	"context"
	"fmt"
	"strings"
	"time"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// SameSite policy values accepted by Config.SameSite (case-insensitive). An
// empty value defaults to Lax.
const (
	SameSiteLax    = "lax"
	SameSiteStrict = "strict"
	SameSiteNone   = "none"
)

type Config struct {
	Domain                string        `env:"DOMAIN"      json:"domain"`
	CookieName            string        `env:"COOKIE_NAME" json:"cookieName"`
	Base64EncodedHashKey  string        `env:"HASH_KEY"    json:"base64EncodedHashKey"`
	Base64EncodedBlockKey string        `env:"BLOCK_KEY"   json:"base64EncodedBlockKey"`
	SameSite              string        `env:"SAME_SITE"   json:"sameSite"`
	Lifetime              time.Duration `env:"LIFETIME"    json:"lifetime"`
	SecureOnly            bool          `env:"SECURE_ONLY" json:"secureOnly"`
}

const minCookieLifetime = 5 * time.Minute

func (c *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, c,
		validation.Field(&c.CookieName, validation.Required),
		validation.Field(&c.Base64EncodedHashKey, validation.Required),
		validation.Field(&c.Base64EncodedBlockKey, validation.Required),
		validation.Field(&c.Lifetime, validation.Min(minCookieLifetime)),
		validation.Field(&c.SameSite, validation.By(c.validateSameSite)),
	)
}

// validateSameSite accepts an empty value (defaults to Lax) or any of the
// SameSite constants case-insensitively, and rejects SameSite=None unless
// SecureOnly is set.
func (c *Config) validateSameSite(any) error {
	switch strings.ToLower(c.SameSite) {
	case "", SameSiteLax, SameSiteStrict:
		return nil
	case SameSiteNone:
		if !c.SecureOnly {
			// Browsers silently drop a SameSite=None cookie that is not Secure.
			return fmt.Errorf("cookie SameSite=%s requires SecureOnly", SameSiteNone)
		}
		return nil
	default:
		return fmt.Errorf("unsupported cookie SameSite value %q", c.SameSite)
	}
}

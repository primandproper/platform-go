package cookies

import (
	"testing"
	"time"

	"github.com/shoenig/test"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			CookieName:            t.Name(),
			Base64EncodedHashKey:  t.Name(),
			Base64EncodedBlockKey: t.Name(),
			Lifetime:              24 * time.Hour,
		}

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("with lifetime below minimum", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			CookieName:            t.Name(),
			Base64EncodedHashKey:  t.Name(),
			Base64EncodedBlockKey: t.Name(),
			Lifetime:              1 * time.Minute,
		}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("with missing name", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Base64EncodedHashKey:  t.Name(),
			Base64EncodedBlockKey: t.Name(),
		}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("with missing hash key", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Base64EncodedBlockKey: t.Name(),
		}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("with missing block key", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Base64EncodedHashKey: t.Name(),
		}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("with explicit SameSite policy", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			CookieName:            t.Name(),
			Base64EncodedHashKey:  t.Name(),
			Base64EncodedBlockKey: t.Name(),
			Lifetime:              24 * time.Hour,
			SameSite:              SameSiteStrict,
		}

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("with unsupported SameSite value", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			CookieName:            t.Name(),
			Base64EncodedHashKey:  t.Name(),
			Base64EncodedBlockKey: t.Name(),
			Lifetime:              24 * time.Hour,
			SameSite:              "sideways",
		}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("with SameSite=None but not SecureOnly", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			CookieName:            t.Name(),
			Base64EncodedHashKey:  t.Name(),
			Base64EncodedBlockKey: t.Name(),
			Lifetime:              24 * time.Hour,
			SameSite:              SameSiteNone,
		}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("with SameSite=None and SecureOnly", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			CookieName:            t.Name(),
			Base64EncodedHashKey:  t.Name(),
			Base64EncodedBlockKey: t.Name(),
			Lifetime:              24 * time.Hour,
			SameSite:              SameSiteNone,
			SecureOnly:            true,
		}

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})
}

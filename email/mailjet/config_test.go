package mailjet

import (
	"testing"

	"github.com/shoenig/test"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("with populated config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			APIKey:    t.Name(),
			SecretKey: t.Name(),
		}

		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("with empty config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}

		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

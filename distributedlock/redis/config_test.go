package redis

import (
	"testing"

	platformerrors "github.com/primandproper/platform-go/v4/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()
		var cfg *Config
		must.ErrorIs(t, cfg.ValidateWithContext(t.Context()), platformerrors.ErrNilInputParameter)
	})

	T.Run("missing addresses", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{}
		err := cfg.ValidateWithContext(t.Context())
		must.Error(t, err)
		test.StrContains(t, err.Error(), "addresses")
	})

	T.Run("happy path", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{Addresses: []string{"localhost:6379"}}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})
}

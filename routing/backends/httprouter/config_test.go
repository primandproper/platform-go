package httprouter

import (
	"testing"

	"github.com/shoenig/test"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{ServiceName: t.Name()}
		test.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("missing service name", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})
}

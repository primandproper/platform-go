package loggingcfg

import (
	"testing"

	"github.com/primandproper/platform-go/v5/observability/logging"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterLogger(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, &Config{
			Provider: ProviderZerolog,
		})

		RegisterLogger(i)

		l, err := do.Invoke[logging.Logger](i)
		must.NoError(t, err)
		test.NotNil(t, l)
	})
}

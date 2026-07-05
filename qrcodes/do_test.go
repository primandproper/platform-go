package qrcodes

import (
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v4/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v4/observability/tracing/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterBuilder(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, Issuer(t.Name()))
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue(i, loggingnoop.NewLogger())

		RegisterBuilder(i)

		b, err := do.Invoke[Builder](i)
		must.NoError(t, err)
		test.NotNil(t, b)
	})
}

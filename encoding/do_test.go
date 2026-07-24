package encoding

import (
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v6/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v6/observability/tracing/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterServerEncoderDecoder(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, Config{ContentType: "application/json"})
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())

		RegisterServerEncoderDecoder(i)

		ct, err := do.Invoke[ContentType](i)
		must.NoError(t, err)
		test.NotNil(t, ct)

		sed, err := do.Invoke[ServerEncoderDecoder](i)
		must.NoError(t, err)
		test.NotNil(t, sed)
	})
}

package images

import (
	"testing"

	loggingnoop "github.com/primandproper/platform/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform/observability/tracing/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterMediaUploadProcessor(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())

		test.NotPanic(t, func() {
			RegisterMediaUploadProcessor(i)
		})

		p, err := do.Invoke[MediaUploadProcessor](i)
		must.NoError(t, err)
		test.NotNil(t, p)
	})
}

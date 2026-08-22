package tracing

import (
	"errors"
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"

	"github.com/shoenig/test"
)

func TestErrorHandler_Handle(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		NewErrorHandler(loggingnoop.NewLogger()).Handle(errors.New("blah"))
	})

	T.Run("nil error", func(t *testing.T) {
		t.Parallel()

		NewErrorHandler(loggingnoop.NewLogger()).Handle(nil)
	})

	T.Run("absent logger resolves to the noop", func(t *testing.T) {
		t.Parallel()

		NewErrorHandler(nil).Handle(errors.New("blah"))
	})
}

//nolint:paralleltest // assigns OTel's process-global error handler; running it beside other tests would race them.
func TestSetGlobalErrorHandler(T *testing.T) {
	T.Run("reports whether it installed one", func(t *testing.T) {
		test.False(t, SetGlobalErrorHandler(nil))
		test.True(t, SetGlobalErrorHandler(loggingnoop.NewLogger()))
	})
}

func Test_noopProvider_ForceFlush(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		tp := EnsureTracerProvider(nil)

		test.NoError(t, tp.ForceFlush(t.Context()))
	})
}

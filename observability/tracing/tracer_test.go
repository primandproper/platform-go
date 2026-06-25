package tracing

import (
	"errors"
	"testing"

	loggingnoop "github.com/primandproper/platform/observability/logging/noop"

	"github.com/shoenig/test"
)

func Test_tracingErrorHandler_Handle(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		errorHandler{logger: loggingnoop.NewLogger()}.Handle(errors.New("blah"))
	})
}

func Test_noopTracerProvider_ForceFlush(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		tp := EnsureTracerProvider(nil)

		test.NoError(t, tp.ForceFlush(t.Context()))
	})
}

package cloudtrace

import (
	"errors"
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v2/observability/logging/noop"
)

func Test_tracingErrorHandler_Handle(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		errorHandler{logger: loggingnoop.NewLogger()}.Handle(errors.New("blah"))
	})
}

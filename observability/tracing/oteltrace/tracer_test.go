package oteltrace

import (
	"errors"
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v5/observability/logging/noop"

	"github.com/shoenig/test"
)

func Test_tracingErrorHandler_Handle(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		errorHandler{logger: loggingnoop.NewLogger()}.Handle(errors.New("blah"))
	})
}

func TestConfig_SetupOtelHTTP(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			CollectorEndpoint: "blah blah blah",
		}

		actual, err := SetupOtelGRPC(ctx, t.Name(), 0, cfg)
		test.NoError(t, err)
		test.NotNil(t, actual)
	})
}

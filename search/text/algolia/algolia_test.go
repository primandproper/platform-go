package algolia

import (
	"testing"
	"time"

	cbnoop "github.com/primandproper/platform-go/v4/circuitbreaking/noop"
	loggingnoop "github.com/primandproper/platform-go/v4/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v4/observability/tracing/noop"

	"github.com/shoenig/test"
)

type example struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func TestProvideIndexManager(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()

		im, err := ProvideIndexManager[example](logger, tracerProvider, &Config{}, "test", cbnoop.NewCircuitBreaker())
		test.NoError(t, err)
		test.NotNil(t, im)
	})

	T.Run("with timeout configured", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()

		im, err := ProvideIndexManager[example](logger, tracerProvider, &Config{Timeout: 5 * time.Second}, "test", cbnoop.NewCircuitBreaker())
		test.NoError(t, err)
		test.NotNil(t, im)
	})

	T.Run("with nil config", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()

		im, err := ProvideIndexManager[example](logger, tracerProvider, nil, "test", cbnoop.NewCircuitBreaker())
		test.Error(t, err)
		test.Nil(t, im)
	})
}

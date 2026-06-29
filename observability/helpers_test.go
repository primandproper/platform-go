package observability

import (
	"testing"

	loggingnoop "github.com/primandproper/platform-go/v2/observability/logging/noop"
	"github.com/primandproper/platform-go/v2/observability/tracing"

	"github.com/shoenig/test"
)

func TestObserveValues(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		_, span := tracing.StartSpan(ctx)

		result := ObserveValues(map[string]any{"key": "value", "other": 123}, span, logger)
		test.NotNil(t, result)
	})

	T.Run("with nil span", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		result := ObserveValues(map[string]any{"key": "value"}, nil, logger)
		test.NotNil(t, result)
	})

	T.Run("with nil logger", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		_, span := tracing.StartSpan(ctx)

		result := ObserveValues(map[string]any{"key": "value"}, span, nil)
		test.Nil(t, result)
	})

	T.Run("with nil span and nil logger", func(t *testing.T) {
		t.Parallel()

		result := ObserveValues(map[string]any{"key": "value"}, nil, nil)
		test.Nil(t, result)
	})

	T.Run("with empty values", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		logger := loggingnoop.NewLogger()
		_, span := tracing.StartSpan(ctx)

		result := ObserveValues(map[string]any{}, span, logger)
		test.NotNil(t, result)
	})
}

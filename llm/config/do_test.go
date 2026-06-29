package llmcfg

import (
	"testing"

	"github.com/primandproper/platform-go/v2/llm"
	loggingnoop "github.com/primandproper/platform-go/v2/observability/logging/noop"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	tracingnoop "github.com/primandproper/platform-go/v2/observability/tracing/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterLLMProvider(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue[metrics.Provider](i, nil)
		do.ProvideValue(i, &Config{})

		RegisterLLMProvider(i)

		provider, err := do.Invoke[llm.Provider](i)
		must.NoError(t, err)
		test.NotNil(t, provider)
	})
}

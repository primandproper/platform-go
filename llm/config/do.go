package llmcfg

import (
	"github.com/primandproper/platform-go/v2/llm"
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	"github.com/primandproper/platform-go/v2/observability/tracing"

	"github.com/samber/do/v2"
)

// RegisterLLMProvider registers an llm.Provider with the injector.
func RegisterLLMProvider(i do.Injector) {
	do.Provide[llm.Provider](i, func(i do.Injector) (llm.Provider, error) {
		return ProvideLLMProvider(
			do.MustInvoke[*Config](i),
			do.MustInvoke[logging.Logger](i),
			do.MustInvoke[tracing.TracerProvider](i),
			do.MustInvoke[metrics.Provider](i),
		)
	})
}

package llmcfg

import (
	"context"

	"github.com/primandproper/platform-go/v13/llm"
	"github.com/primandproper/platform-go/v13/observability"

	"github.com/samber/do/v2"
)

// RegisterLLMProvider registers an llm.Provider with the injector.
func RegisterLLMProvider(i do.Injector) {
	do.Provide(i, func(i do.Injector) (llm.Provider, error) {
		pillars, err := observability.InvokePillars(i)
		if err != nil {
			return nil, err
		}

		return NewLLMProvider(
			do.MustInvoke[context.Context](i),
			do.MustInvoke[*Config](i),
			WithPillars(pillars),
		)
	})
}

package embeddingscfg

import (
	"context"

	"github.com/primandproper/platform-go/v5/embeddings"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/tracing"

	"github.com/samber/do/v2"
)

// RegisterEmbedder registers an embeddings.Embedder with the injector.
func RegisterEmbedder(i do.Injector) {
	do.Provide[embeddings.Embedder](i, func(i do.Injector) (embeddings.Embedder, error) {
		ctx := do.MustInvoke[context.Context](i)
		cfg := do.MustInvoke[*Config](i)
		logger := do.MustInvoke[logging.Logger](i)
		tracer := do.MustInvoke[tracing.Tracer](i)
		return NewEmbedder(ctx, cfg, logger, tracer)
	})
}

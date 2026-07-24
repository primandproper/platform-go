package embeddingscfg

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v6/embeddings"
	loggingnoop "github.com/primandproper/platform-go/v6/observability/logging/noop"
	"github.com/primandproper/platform-go/v6/observability/tracing"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterEmbedder(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracing.NewTracerForTest("test"))
		do.ProvideValue(i, &Config{})

		RegisterEmbedder(i)

		embedder, err := do.Invoke[embeddings.Embedder](i)
		must.NoError(t, err)
		test.NotNil(t, embedder)
	})
}

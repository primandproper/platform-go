package embeddings_test

import (
	"testing"

	"github.com/primandproper/platform-go/v2/embeddings"
	embeddingsnoop "github.com/primandproper/platform-go/v2/embeddings/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNoopEmbedder_GenerateEmbedding(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		embedder := embeddingsnoop.NewEmbedder()

		result, err := embedder.GenerateEmbedding(ctx, &embeddings.Input{
			Content: "hello world",
		})

		must.NoError(t, err)
		must.NotNil(t, result)
		test.EqOp(t, "hello world", result.SourceText)
		test.EqOp(t, "noop", result.Model)
		test.EqOp(t, "noop", result.Provider)
		test.EqOp(t, 0, result.Dimensions)
		test.SliceEmpty(t, result.Vector)
		test.False(t, result.GeneratedAt.IsZero())
	})
}

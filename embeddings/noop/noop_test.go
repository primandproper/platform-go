package noop

import (
	"testing"

	"github.com/primandproper/platform-go/v4/embeddings"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewEmbedder(T *testing.T) {
	T.Parallel()

	T.Run("returns non-nil embedder", func(t *testing.T) {
		t.Parallel()

		e := NewEmbedder()
		must.NotNil(t, e)
	})
}

func TestEmbedder_GenerateEmbedding(T *testing.T) {
	T.Parallel()

	T.Run("returns empty vector and no error", func(t *testing.T) {
		t.Parallel()

		e := NewEmbedder()
		result, err := e.GenerateEmbedding(t.Context(), &embeddings.Input{
			Content: "hello world",
		})

		must.NoError(t, err)
		must.NotNil(t, result)
		test.SliceEmpty(t, result.Vector)
		test.EqOp(t, "hello world", result.SourceText)
		test.EqOp(t, "noop", result.Model)
		test.EqOp(t, "noop", result.Provider)
		test.EqOp(t, 0, result.Dimensions)
	})

	T.Run("returns an error on nil input instead of panicking", func(t *testing.T) {
		t.Parallel()

		e := NewEmbedder()
		result, err := e.GenerateEmbedding(t.Context(), nil)

		test.ErrorIs(t, err, embeddings.ErrNilInput)
		test.Nil(t, result)
	})
}

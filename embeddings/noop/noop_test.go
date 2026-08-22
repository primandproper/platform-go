package noop

import (
	"testing"

	"github.com/primandproper/platform-go/v13/embeddings"

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

func TestEmbedder_GenerateEmbeddings(T *testing.T) {
	T.Parallel()

	T.Run("returns one embedding per input", func(t *testing.T) {
		t.Parallel()

		e := NewEmbedder()
		results, err := e.GenerateEmbeddings(t.Context(), []*embeddings.Input{
			{Content: "first"},
			{Content: "second"},
		})

		must.NoError(t, err)
		must.SliceLen(t, 2, results)
		test.EqOp(t, "first", results[0].SourceText)
		test.EqOp(t, "second", results[1].SourceText)
		test.SliceEmpty(t, results[0].Vector)
		test.EqOp(t, "noop", results[1].Provider)
	})

	T.Run("returns an empty slice for no inputs", func(t *testing.T) {
		t.Parallel()

		e := NewEmbedder()
		results, err := e.GenerateEmbeddings(t.Context(), nil)

		must.NoError(t, err)
		test.SliceEmpty(t, results)
	})

	T.Run("propagates the nil-input error rather than a partial batch", func(t *testing.T) {
		t.Parallel()

		e := NewEmbedder()
		results, err := e.GenerateEmbeddings(t.Context(), []*embeddings.Input{
			{Content: "fine"},
			nil,
		})

		test.ErrorIs(t, err, embeddings.ErrNilInput)
		test.Nil(t, results)
	})
}

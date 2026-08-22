package embeddings_test

import (
	"testing"

	"github.com/primandproper/platform-go/v13/embeddings"
	embeddingsnoop "github.com/primandproper/platform-go/v13/embeddings/noop"

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

func TestEmbedder_batchContract(T *testing.T) {
	T.Parallel()

	// The contract every provider owes GenerateEmbeddings, asserted against the
	// noop embedder so it stays true of the shape rather than of one backend.
	e := embeddingsnoop.NewEmbedder()

	T.Run("returns one embedding per input, in order", func(t *testing.T) {
		t.Parallel()

		out, err := e.GenerateEmbeddings(t.Context(), []*embeddings.Input{
			{Content: "first"},
			{Content: "second"},
			{Content: "third"},
		})
		must.NoError(t, err)
		must.SliceLen(t, 3, out)

		test.EqOp(t, "first", out[0].SourceText)
		test.EqOp(t, "second", out[1].SourceText)
		test.EqOp(t, "third", out[2].SourceText)
	})

	T.Run("an empty batch is not an error", func(t *testing.T) {
		t.Parallel()

		out, err := e.GenerateEmbeddings(t.Context(), nil)
		must.NoError(t, err)
		test.SliceEmpty(t, out)
	})

	// The whole call fails rather than returning a slice with a hole in it.
	T.Run("a nil input fails the whole batch", func(t *testing.T) {
		t.Parallel()

		out, err := e.GenerateEmbeddings(t.Context(), []*embeddings.Input{{Content: "ok"}, nil})
		test.ErrorIs(t, err, embeddings.ErrNilInput)
		test.Nil(t, out)
	})
}

func TestPurpose_String(T *testing.T) {
	T.Parallel()

	T.Run("names the two defined sides", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "document", embeddings.PurposeDocument.String())
		test.EqOp(t, "query", embeddings.PurposeQuery.String())
	})

	// An undefined value must not impersonate a side it isn't, since the whole
	// point of the type is that the two sides are not interchangeable.
	T.Run("an undefined purpose renders its number", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "Purpose(9)", embeddings.Purpose(9).String())
	})

	// Callers that predate the field set nothing, and must keep being treated
	// as documents.
	T.Run("the zero value is the document side", func(t *testing.T) {
		t.Parallel()

		var input embeddings.Input

		test.EqOp(t, embeddings.PurposeDocument, input.Purpose)
	})
}

func TestToFloat32(T *testing.T) {
	T.Parallel()

	T.Run("narrows every element", func(t *testing.T) {
		t.Parallel()

		test.Eq(t, []float32{0.5, -1.25, 0}, embeddings.ToFloat32([]float64{0.5, -1.25, 0}))
	})

	// A caller ranging over the result never has to distinguish "no vector"
	// from "a vector of nothing".
	T.Run("an absent vector is an empty one", func(t *testing.T) {
		t.Parallel()

		got := embeddings.ToFloat32(nil)

		must.NotNil(t, got)
		test.SliceEmpty(t, got)
	})
}

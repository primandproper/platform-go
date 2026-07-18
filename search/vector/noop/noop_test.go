package noop

import (
	"testing"

	vectorsearch "github.com/primandproper/platform-go/v5/search/vector"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

type example struct {
	Name string
}

func TestNewIndex(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		idx := NewIndex[example]()
		test.NotNil(t, idx)
	})
}

func TestIndexManager_Upsert(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		idx := NewIndex[example]()
		must.NoError(t, idx.Upsert(t.Context(), vectorsearch.Vector[example]{
			ID:        "abc",
			Embedding: []float32{0.1, 0.2, 0.3},
			Metadata:  &example{Name: "doc"},
		}))
	})
}

func TestIndexManager_Delete(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		idx := NewIndex[example]()
		must.NoError(t, idx.Delete(t.Context(), "abc", "def"))
	})
}

func TestIndexManager_Wipe(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		idx := NewIndex[example]()
		must.NoError(t, idx.Wipe(t.Context()))
	})
}

func TestIndexManager_Query(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		idx := NewIndex[example]()
		results, err := idx.Query(t.Context(), vectorsearch.QueryRequest{
			Embedding: []float32{0.1, 0.2, 0.3},
			TopK:      10,
		})
		must.NoError(t, err)
		test.SliceEmpty(t, results)
	})
}

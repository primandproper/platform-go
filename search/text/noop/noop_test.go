package noop

import (
	"context"
	"testing"

	textsearch "github.com/primandproper/platform-go/v13/search/text"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestIndexManager_Search(T *testing.T) {
	T.Parallel()

	T.Run("returns empty slice and no error", func(t *testing.T) {
		t.Parallel()

		m := NewIndexManager[string]()
		results, err := m.Search(context.Background(), textsearch.SearchRequest{Query: "query"})

		must.NoError(t, err)
		test.SliceEmpty(t, results.Hits)
		test.NotNil(t, results)
	})
}

func TestIndexManager_Index(T *testing.T) {
	T.Parallel()

	T.Run("returns no error", func(t *testing.T) {
		t.Parallel()

		m := NewIndexManager[string]()
		err := m.Index(context.Background(), "id", "value")

		test.NoError(t, err)
	})
}

func TestIndexManager_Delete(T *testing.T) {
	T.Parallel()

	T.Run("returns no error", func(t *testing.T) {
		t.Parallel()

		m := NewIndexManager[string]()
		err := m.Delete(context.Background(), "id")

		test.NoError(t, err)
	})
}

func TestIndexManager_Wipe(T *testing.T) {
	T.Parallel()

	T.Run("returns no error", func(t *testing.T) {
		t.Parallel()

		m := NewIndexManager[string]()
		err := m.Wipe(context.Background())

		test.NoError(t, err)
	})
}

package searchsync

import (
	"context"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	textsearchmock "github.com/primandproper/platform-go/v13/search/text/mock"
	vectorsearch "github.com/primandproper/platform-go/v13/search/vector"
	vectorsearchmock "github.com/primandproper/platform-go/v13/search/vector/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestTextTarget(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil index", func(t *testing.T) {
		t.Parallel()

		_, err := TextTarget[testDoc](nil)
		test.ErrorIs(t, err, ErrNilIndex)
	})

	T.Run("indexes each document by ID", func(t *testing.T) {
		t.Parallel()

		index := &textsearchmock.IndexMock[testDoc]{
			IndexFunc: func(context.Context, string, any) error { return nil },
		}

		target, err := TextTarget[testDoc](index)
		must.NoError(t, err)

		body := &testDoc{Name: "widget"}
		must.NoError(t, target.Upsert(context.Background(),
			Document[testDoc]{ID: "doc-1", Body: body},
			Document[testDoc]{ID: "doc-2", Body: &testDoc{Name: "sprocket"}}))

		calls := index.IndexCalls()
		must.SliceLen(t, 2, calls)
		test.EqOp(t, "doc-1", calls[0].ID)
		indexed, ok := calls[0].Value.(*testDoc)
		must.True(t, ok)
		test.EqOp(t, body, indexed)
		test.EqOp(t, "doc-2", calls[1].ID)
	})

	T.Run("ignores the embedding", func(t *testing.T) {
		t.Parallel()

		index := &textsearchmock.IndexMock[testDoc]{
			IndexFunc: func(context.Context, string, any) error { return nil },
		}

		target, err := TextTarget[testDoc](index)
		must.NoError(t, err)

		must.NoError(t, target.Upsert(context.Background(),
			Document[testDoc]{ID: "doc-1", Body: &testDoc{}, Embedding: []float32{1, 2, 3}}))
		must.SliceLen(t, 1, index.IndexCalls())
	})

	T.Run("deletes each ID", func(t *testing.T) {
		t.Parallel()

		index := &textsearchmock.IndexMock[testDoc]{
			DeleteFunc: func(context.Context, string) error { return nil },
		}

		target, err := TextTarget[testDoc](index)
		must.NoError(t, err)

		must.NoError(t, target.Delete(context.Background(), "doc-1", "doc-2"))
		must.SliceLen(t, 2, index.DeleteCalls())
	})

	T.Run("refuses a document with no ID", func(t *testing.T) {
		t.Parallel()

		index := &textsearchmock.IndexMock[testDoc]{
			IndexFunc: func(context.Context, string, any) error { return nil },
		}

		target, err := TextTarget[testDoc](index)
		must.NoError(t, err)

		test.ErrorIs(t, target.Upsert(context.Background(), Document[testDoc]{Body: &testDoc{}}), ErrEmptyDocumentID)
		test.ErrorIs(t, target.Delete(context.Background(), ""), ErrEmptyDocumentID)
		test.SliceEmpty(t, index.IndexCalls())
	})

	T.Run("names the document an index failure was for", func(t *testing.T) {
		t.Parallel()

		expected := platformerrors.New("index is down")
		index := &textsearchmock.IndexMock[testDoc]{
			IndexFunc: func(context.Context, string, any) error { return expected },
		}

		target, err := TextTarget[testDoc](index)
		must.NoError(t, err)

		err = target.Upsert(context.Background(), Document[testDoc]{ID: "doc-1", Body: &testDoc{}})
		test.ErrorIs(t, err, expected)
		test.StrContains(t, err.Error(), "doc-1")
	})
}

func TestVectorTarget(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil index", func(t *testing.T) {
		t.Parallel()

		_, err := VectorTarget[testDoc](nil)
		test.ErrorIs(t, err, ErrNilIndex)
	})

	T.Run("upserts the whole batch in one call", func(t *testing.T) {
		t.Parallel()

		index := &vectorsearchmock.IndexMock[testDoc]{
			UpsertFunc: func(context.Context, ...vectorsearch.Vector[testDoc]) error { return nil },
		}

		target, err := VectorTarget[testDoc](index)
		must.NoError(t, err)

		body := &testDoc{Name: "widget"}
		must.NoError(t, target.Upsert(context.Background(),
			Document[testDoc]{ID: "doc-1", Body: body, Embedding: []float32{1, 2}},
			Document[testDoc]{ID: "doc-2", Body: &testDoc{}, Embedding: []float32{3, 4}}))

		calls := index.UpsertCalls()
		must.SliceLen(t, 1, calls)
		must.SliceLen(t, 2, calls[0].Vectors)
		test.EqOp(t, "doc-1", calls[0].Vectors[0].ID)
		test.EqOp(t, body, calls[0].Vectors[0].Metadata)
		test.Eq(t, []float32{1, 2}, calls[0].Vectors[0].Embedding)
	})

	T.Run("names the document that has no embedding", func(t *testing.T) {
		t.Parallel()

		// The backends would reject the batch anyway; rejecting it here says
		// which document is missing one, which a rejected batch does not.
		index := &vectorsearchmock.IndexMock[testDoc]{
			UpsertFunc: func(context.Context, ...vectorsearch.Vector[testDoc]) error { return nil },
		}

		target, err := VectorTarget[testDoc](index)
		must.NoError(t, err)

		err = target.Upsert(context.Background(), Document[testDoc]{ID: "doc-1", Body: &testDoc{}})
		test.ErrorIs(t, err, vectorsearch.ErrEmptyEmbedding)
		test.StrContains(t, err.Error(), "doc-1")
		test.SliceEmpty(t, index.UpsertCalls())
	})

	T.Run("deletes the whole batch in one call", func(t *testing.T) {
		t.Parallel()

		index := &vectorsearchmock.IndexMock[testDoc]{
			DeleteFunc: func(context.Context, ...string) error { return nil },
		}

		target, err := VectorTarget[testDoc](index)
		must.NoError(t, err)

		must.NoError(t, target.Delete(context.Background(), "doc-1", "doc-2"))

		calls := index.DeleteCalls()
		must.SliceLen(t, 1, calls)
		test.Eq(t, []string{"doc-1", "doc-2"}, calls[0].Ids)
	})

	T.Run("does nothing with an empty batch", func(t *testing.T) {
		t.Parallel()

		index := &vectorsearchmock.IndexMock[testDoc]{}

		target, err := VectorTarget[testDoc](index)
		must.NoError(t, err)

		must.NoError(t, target.Upsert(context.Background()))
		must.NoError(t, target.Delete(context.Background()))
		test.SliceEmpty(t, index.UpsertCalls())
		test.SliceEmpty(t, index.DeleteCalls())
	})

	T.Run("refuses a document with no ID", func(t *testing.T) {
		t.Parallel()

		index := &vectorsearchmock.IndexMock[testDoc]{}

		target, err := VectorTarget[testDoc](index)
		must.NoError(t, err)

		test.ErrorIs(t, target.Upsert(context.Background(),
			Document[testDoc]{Body: &testDoc{}, Embedding: []float32{1}}), ErrEmptyDocumentID)
		test.ErrorIs(t, target.Delete(context.Background(), "doc-1", ""), ErrEmptyDocumentID)
		test.SliceEmpty(t, index.DeleteCalls())
	})
}

// TestTargets_syncerRoundTrip exercises the two adapters through the Syncer
// they exist for, rather than only in isolation.
func TestTargets_syncerRoundTrip(T *testing.T) {
	T.Parallel()

	T.Run("a text target sees an upsert and a delete", func(t *testing.T) {
		t.Parallel()

		index := &textsearchmock.IndexMock[testDoc]{
			IndexFunc:  func(context.Context, string, any) error { return nil },
			DeleteFunc: func(context.Context, string) error { return nil },
		}

		target, err := TextTarget[testDoc](index)
		must.NoError(t, err)

		source := &stubSource{fetchFunc: func(ids ...string) ([]Document[testDoc], error) {
			return []Document[testDoc]{{ID: ids[0], Body: &testDoc{Name: "widget"}}}, nil
		}}

		syncer := newTestSyncer(t, source, target)
		must.NoError(t, syncer.Apply(context.Background(), NewEvent(OpUpsert, "doc-1")))
		must.NoError(t, syncer.Apply(context.Background(), NewEvent(OpDelete, "doc-1")))

		must.SliceLen(t, 1, index.IndexCalls())
		must.SliceLen(t, 1, index.DeleteCalls())
	})
}

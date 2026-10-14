package searchsync

import (
	"context"
	"slices"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	textsearch "github.com/primandproper/primitives-go/v2/search/text"
	vectorsearch "github.com/primandproper/primitives-go/v2/search/vector"
)

// Wiper is a Target whose index can be emptied, documents only, leaving the
// index itself and its mapping in place.
//
// It is a separate interface rather than a third method on Target because a
// Target is what a Syncer writes through, and a Syncer has no business emptying
// anything — two methods is the whole contract an application implementing one
// by hand owes. Registry.Wipe is what asks for this, and it refuses a Target
// that does not admit it with ErrTargetCannotWipe rather than passing it over.
// TextTarget and VectorTarget both implement it, because both backends can.
type Wiper interface {
	Wipe(ctx context.Context) error
}

// TextTarget adapts a textsearch.IndexManager — Algolia, Elasticsearch, or the
// noop — to a Target.
//
// It takes the IndexManager half rather than a whole textsearch.Index[T]
// because syncing never reads: the write half is the entire surface this needs,
// and asking for the read half too would make the document type a Syncer holds
// and the hit type a search returns the same type, which they routinely are not.
// That is also why the type parameter is on TextTarget rather than inferred —
// it is the Syncer's document type, spelled once at the wiring site.
func TextTarget[T any](index textsearch.IndexManager) (Target[T], error) {
	if index == nil {
		return nil, ErrNilIndex
	}

	return &textTarget[T]{index: index}, nil
}

type textTarget[T any] struct {
	index textsearch.IndexManager
}

var _ Wiper = (*textTarget[any])(nil)

// Upsert indexes each document in turn. textsearch.IndexManager takes one
// document per call, so a batch is a loop: the backends here index one document
// per request and batching is theirs to do underneath, not this adapter's to
// simulate.
//
// A failure partway through leaves the documents before it indexed. That is
// safe because every write here is idempotent — the retry re-indexes them, and
// re-indexing a document that is already current is the same as not doing it.
func (t *textTarget[T]) Upsert(ctx context.Context, docs ...Document[T]) error {
	for _, doc := range docs {
		if doc.ID == "" {
			return ErrEmptyDocumentID
		}

		if err := t.index.Index(ctx, doc.ID, doc.Body); err != nil {
			return platformerrors.Wrapf(err, "indexing document %q", doc.ID)
		}
	}

	return nil
}

// Delete removes each ID in turn, with the same partial-failure behavior, and
// for the same reason: deleting a document that is already gone is not an error
// for any backend here.
func (t *textTarget[T]) Delete(ctx context.Context, ids ...string) error {
	for _, id := range ids {
		if id == "" {
			return ErrEmptyDocumentID
		}

		if err := t.index.Delete(ctx, id); err != nil {
			return platformerrors.Wrapf(err, "deleting document %q", id)
		}
	}

	return nil
}

// Wipe empties the index through the IndexManager's own Wipe.
func (t *textTarget[T]) Wipe(ctx context.Context) error {
	return t.index.Wipe(ctx)
}

// VectorTarget adapts a vectorsearch.IndexWriter — pgvector, Qdrant, or the
// noop — to a Target.
//
// T is the vector index's metadata type, so a Document's Body is what comes
// back as a QueryResult's Metadata. The embedding comes from the Source along
// with the body: computing it here would mean this package holding an
// embeddings client and deciding when to spend on it, which is the
// application's call and its cost.
func VectorTarget[T any](index vectorsearch.IndexWriter[T]) (Target[T], error) {
	if index == nil {
		return nil, ErrNilIndex
	}

	return &vectorTarget[T]{index: index}, nil
}

type vectorTarget[T any] struct {
	index vectorsearch.IndexWriter[T]
}

var _ Wiper = (*vectorTarget[any])(nil)

// Upsert writes the whole batch in one call, which is what vectorsearch.Upsert
// is shaped for.
//
// A document with no embedding is refused rather than written with an empty
// one. Both backends here would reject it anyway — vectorsearch.ErrEmptyEmbedding
// is theirs — and refusing it in the adapter names the document that is missing
// it, which a backend rejecting a batch does not.
func (t *vectorTarget[T]) Upsert(ctx context.Context, docs ...Document[T]) error {
	if len(docs) == 0 {
		return nil
	}

	vectors := make([]vectorsearch.Vector[T], 0, len(docs))
	for _, doc := range docs {
		if doc.ID == "" {
			return ErrEmptyDocumentID
		}

		if len(doc.Embedding) == 0 {
			return platformerrors.Wrapf(vectorsearch.ErrEmptyEmbedding, "document %q", doc.ID)
		}

		vectors = append(vectors, vectorsearch.Vector[T]{
			Metadata:  doc.Body,
			ID:        doc.ID,
			Embedding: doc.Embedding,
		})
	}

	return t.index.Upsert(ctx, vectors...)
}

// Delete removes the whole batch in one call.
func (t *vectorTarget[T]) Delete(ctx context.Context, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}

	if slices.Contains(ids, "") {
		return ErrEmptyDocumentID
	}

	return t.index.Delete(ctx, ids...)
}

// Wipe empties the index through the IndexWriter's own Wipe.
func (t *vectorTarget[T]) Wipe(ctx context.Context) error {
	return t.index.Wipe(ctx)
}

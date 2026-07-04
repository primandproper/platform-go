package vectorsearch

import (
	"context"

	platformerrors "github.com/primandproper/platform-go/v3/errors"
)

// DistanceMetric selects the nearest-neighbor scoring function used by an index.
type DistanceMetric string

const (
	// DistanceCosine ranks results by cosine similarity.
	DistanceCosine DistanceMetric = "cosine"
	// DistanceDotProduct ranks results by dot product.
	DistanceDotProduct DistanceMetric = "dot"
	// DistanceEuclidean ranks results by Euclidean (L2) distance.
	DistanceEuclidean DistanceMetric = "euclidean"
)

var (
	// ErrEmptyEmbedding indicates a query or upsert was attempted with a zero-length vector.
	ErrEmptyEmbedding = platformerrors.New("empty embedding vector provided")
	// ErrNotFound indicates a vector with the given ID does not exist in the index.
	ErrNotFound = platformerrors.New("vector not found")
	// ErrNilConfig indicates a nil provider config was passed to a constructor.
	ErrNilConfig = platformerrors.New("nil vector search config")
	// ErrDimensionMismatch indicates an embedding's dimension does not match the index dimension.
	ErrDimensionMismatch = platformerrors.New("embedding dimension does not match index dimension")
	// ErrNilDatabaseClient indicates a nil database.Client was passed to a postgres-backed provider.
	ErrNilDatabaseClient = platformerrors.New("nil database client")
	// ErrInvalidMetric indicates an unsupported DistanceMetric was specified.
	ErrInvalidMetric = platformerrors.New("invalid distance metric")
	// ErrInvalidDimension indicates a non-positive dimension was specified.
	ErrInvalidDimension = platformerrors.New("invalid index dimension")
)

type (
	// Vector is a single indexable point. T is the metadata payload type, generic in
	// the same way textsearch.Index[T] is generic over the document payload type.
	Vector[T any] struct {
		Metadata  *T
		ID        string
		Embedding []float32
	}

	// QueryRequest describes a top-K nearest-neighbor search.
	QueryRequest struct {
		// Filter is an OPAQUE per-provider DSL. The pgvector provider interprets it as
		// a SQL fragment appended to the WHERE clause; the qdrant provider interprets
		// it as a *qdrantfilter.Filter (or compatible JSON map). nil means no filter.
		// Cross-provider filter portability is intentionally not modeled — see doc.go.
		Filter any
		// Embedding is the query vector. Must match the index dimension.
		Embedding []float32
		// TopK is the number of results to return. Provider implementations may cap
		// this at a backend-specific maximum.
		TopK int
	}

	// QueryResult is a single hit returned from Query. The interpretation of Distance
	// depends on the index's configured DistanceMetric: cosine produces a value in
	// [0, 2] where lower is more similar; dot product is unbounded; euclidean is the
	// L2 distance.
	QueryResult[T any] struct {
		Metadata *T
		ID       string
		Distance float32
	}

	// IndexWriter is the write half of an Index.
	IndexWriter[T any] interface {
		// Upsert inserts or replaces vectors keyed by ID.
		Upsert(ctx context.Context, vectors ...Vector[T]) error
		// Delete removes vectors by ID. Missing IDs are ignored.
		Delete(ctx context.Context, ids ...string) error
		// Wipe removes all vectors from the index, leaving the index itself in place.
		Wipe(ctx context.Context) error
	}

	// IndexSearcher is the read half of an Index.
	IndexSearcher[T any] interface {
		// Query returns the top-K nearest neighbors for the supplied embedding.
		Query(ctx context.Context, req QueryRequest) ([]QueryResult[T], error)
	}

	// Index is a generic vector index, parameterized over the metadata payload type T.
	// It mirrors textsearch.Index[T] in shape — provider implementations live in
	// subpackages and are selected via search/vector/config.
	Index[T any] interface {
		IndexWriter[T]
		IndexSearcher[T]
	}
)

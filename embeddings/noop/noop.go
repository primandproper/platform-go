// Package noop is the embeddings.Embedder for a deployment that runs no model,
// and the thing to know is that it does not return nothing. It returns a
// populated embeddings.Embedding whose Vector is empty and whose Dimensions is
// zero, carrying the caller's SourceText and tagged with Model and Provider
// "noop".
//
// So the consequence lands downstream rather than here. A zero-length vector
// upserted into a vector index is a row with no coordinates, and a similarity
// search against one has no distance to compute. Nothing in this package
// refuses that: the only error it returns is embeddings.ErrNilInput, for a nil
// input. A retrieval pipeline wired to this embedder therefore indexes
// successfully and retrieves nothing, at every stage reporting success.
//
// Having no model, it has no side of a retrieval comparison to be on, so
// embeddings.Input.Purpose is ignored here as it is by the symmetric providers.
//
// embeddings/config selects it for the "noop" provider name or the empty
// string, embeddings being an optional capability that a service may legitimately
// not have.
package noop

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v13/embeddings"
)

var _ embeddings.Embedder = (*Embedder)(nil)

// Embedder is a no-op Embedder.
type Embedder struct{}

// NewEmbedder returns a no-op Embedder.
func NewEmbedder() *Embedder {
	return &Embedder{}
}

// GenerateEmbedding is a no-op that returns an empty vector.
func (*Embedder) GenerateEmbedding(_ context.Context, input *embeddings.Input) (*embeddings.Embedding, error) {
	if input == nil {
		return nil, embeddings.ErrNilInput
	}

	return &embeddings.Embedding{
		Vector:      []float32{},
		SourceText:  input.Content,
		Model:       "noop",
		Provider:    "noop",
		Dimensions:  0,
		GeneratedAt: time.Now(),
	}, nil
}

// GenerateEmbeddings returns one empty embedding per input.
func (e *Embedder) GenerateEmbeddings(ctx context.Context, inputs []*embeddings.Input) ([]*embeddings.Embedding, error) {
	out := make([]*embeddings.Embedding, len(inputs))
	for i, input := range inputs {
		emb, err := e.GenerateEmbedding(ctx, input)
		if err != nil {
			return nil, err
		}

		out[i] = emb
	}

	return out, nil
}

package noop

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v4/embeddings"
)

var _ embeddings.Embedder = (*Embedder)(nil)

// Embedder is a no-op Embedder.
type Embedder struct{}

// NewEmbedder returns a no-op Embedder.
func NewEmbedder() embeddings.Embedder {
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

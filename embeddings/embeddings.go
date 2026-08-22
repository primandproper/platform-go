package embeddings

import (
	"context"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/errors"
)

// ErrNilInput indicates a nil *Input was passed to GenerateEmbedding.
var ErrNilInput = errors.New("nil embedding input provided")

// ErrUnknownPurpose indicates an Input carried a Purpose outside the constants
// this package defines. Providers that act on Purpose return it rather than
// guessing a side, since guessing wrong is the failure that has no symptom.
var ErrUnknownPurpose = errors.New("unknown embedding purpose")

// Purpose says which side of a retrieval comparison an Input is on: the corpus
// being indexed, or the query being searched with.
//
// It exists because some models embed the two sides differently — Cohere's v3
// models take an input_type and place a query and a passage in deliberately
// different regions of the space, so that a query vector lands near the
// passages that answer it rather than near the passages that resemble it. Get
// the side wrong and both halves of the comparison are still vectors, the
// search still returns results, and only the ranking is quietly worse. There is
// no error to catch, which is why the caller has to be able to say.
//
// Providers whose models are symmetric — openai and ollama here — embed the
// same text identically either way and ignore this field; each says so in its
// package documentation. Setting it against them is harmless, and portable: the
// same Input can be handed to a symmetric provider today and an asymmetric one
// after a config change without the caller re-examining its call sites.
type Purpose uint8

const (
	// PurposeDocument embeds content as a passage to be stored and later
	// searched over. It is the zero value, so an Input that says nothing is
	// embedded as a document — which is what every provider here did before
	// this field existed, and what a corpus-indexing pipeline wants anyway.
	PurposeDocument Purpose = iota

	// PurposeQuery embeds content as a search query against a corpus that was
	// embedded as documents. Retrieval paths that embed the user's text at
	// request time want this one.
	PurposeQuery
)

// String returns a stable, lowercase name for a Purpose, suitable for a span
// attribute or a log field. An undefined value renders its number rather than
// impersonating one of the two defined sides.
func (p Purpose) String() string {
	switch p {
	case PurposeDocument:
		return "document"
	case PurposeQuery:
		return "query"
	default:
		return fmt.Sprintf("Purpose(%d)", uint8(p))
	}
}

// DefaultRequestTimeout bounds a single embedding HTTP request when a provider's
// Config leaves Timeout unset, so a hung provider can't block a caller forever.
const DefaultRequestTimeout = 2 * time.Minute

// Input is the content to be embedded.
type Input struct {
	// Content is the text to embed.
	Content string

	// Model optionally overrides the provider's configured DefaultModel.
	// Leave empty to use the default from the provider's Config.
	Model string

	// Purpose says whether this content is a document being indexed or a query
	// being searched with. The zero value is PurposeDocument. Providers with
	// symmetric models ignore it; see Purpose.
	Purpose Purpose
}

// Embedding is the result of embedding a single piece of content.
// It carries provenance alongside the vector so that re-embedding
// and ETL pipelines can be driven from the stored result alone.
type Embedding struct {
	GeneratedAt time.Time
	SourceText  string
	Model       string
	Provider    string
	Vector      []float32
	Dimensions  int
}

// Embedder generates vector embeddings for text.
//
// GenerateEmbeddings is on the interface rather than a helper over
// GenerateEmbedding because every backing API accepts an array: embedding a
// thousand documents one HTTP request at a time is a thousand round trips and a
// thousand chances to be rate limited, for work the provider would have done in
// one call. Adding the method after v9 is tagged would break every implementor,
// so it is here from the start.
type Embedder interface {
	GenerateEmbedding(ctx context.Context, input *Input) (*Embedding, error)
	// GenerateEmbeddings embeds several inputs in as few requests as the
	// provider allows. Results are returned in the order of inputs, one per
	// input. An empty inputs slice returns an empty result and no error; a nil
	// element is ErrNilInput, and the whole call fails rather than returning a
	// partially populated slice the caller has to inspect positionally.
	GenerateEmbeddings(ctx context.Context, inputs []*Input) ([]*Embedding, error)
}

// ToFloat32 narrows a float64 vector to the float32 one an Embedding carries.
//
// Every provider here needs it, because every provider's JSON decodes to
// float64 and every vector store this module talks to stores float32 — so the
// narrowing happens once per response on the way out of each provider. Three
// byte-identical copies of a four-line loop is not a crisis, but it is three
// places for the precision decision to be made independently, and the decision
// is the interesting part: float32 is what pgvector and Qdrant store, so
// keeping float64 through this layer would double every payload for precision
// that is discarded at the next hop.
//
// A nil or empty input yields an empty, non-nil slice, so a caller ranging over
// the result never has to distinguish "no vector" from "a vector of nothing".
func ToFloat32(f64 []float64) []float32 {
	out := make([]float32, len(f64))
	for i, v := range f64 {
		out[i] = float32(v)
	}

	return out
}

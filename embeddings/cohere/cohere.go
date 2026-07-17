package cohere

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/primandproper/platform-go/v5/embeddings"
	"github.com/primandproper/platform-go/v5/errors"
	"github.com/primandproper/platform-go/v5/observability"
	"github.com/primandproper/platform-go/v5/observability/keys"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/tracing"
)

const (
	defaultBaseURL = "https://api.cohere.com"
	defaultModel   = "embed-english-v3.0"
	providerName   = "cohere"
)

type embedder struct {
	o11y   observability.Observer
	client *http.Client
	cfg    *Config
}

// NewEmbedder creates a new Cohere-backed embeddings provider.
func NewEmbedder(ctx context.Context, cfg *Config, logger logging.Logger, tracer tracing.Tracer) (embeddings.Embedder, error) {
	if cfg == nil {
		return nil, errors.New("cohere embeddings config is required")
	}

	logger = logging.EnsureLogger(logger)

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating cohere embeddings config")
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = embeddings.DefaultRequestTimeout
	}
	client := &http.Client{Timeout: timeout}

	return &embedder{
		o11y:   observability.NewObserverWithTracer(providerName, logger, tracer),
		client: client,
		cfg:    cfg,
	}, nil
}

type embeddingRequest struct {
	Texts          []string `json:"texts"`
	Model          string   `json:"model"`
	InputType      string   `json:"input_type"`
	EmbeddingTypes []string `json:"embedding_types"`
}

type embeddingResponse struct {
	Embeddings struct {
		Float [][]float64 `json:"float"`
	} `json:"embeddings"`
}

// GenerateEmbedding implements embeddings.Embedder.
//
// Rate limiting: this method does not retry. A non-200 response (including 429 Too Many
// Requests) is surfaced to the caller as an error carrying the status code; it is not
// retried or backed off. Callers that want retry/backoff should wrap this call themselves
// (e.g. with the platform's retry package).
func (e *embedder) GenerateEmbedding(ctx context.Context, input *embeddings.Input) (*embeddings.Embedding, error) {
	ctx, op := e.o11y.Begin(ctx)
	defer op.End()

	if input == nil {
		return nil, embeddings.ErrNilInput
	}

	model := input.Model
	if model == "" {
		model = e.cfg.DefaultModel
	}
	if model == "" {
		model = defaultModel
	}

	baseURL := e.cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}

	op.Set("embedding.model", model).Set(keys.LengthKey, len(input.Content))

	reqBody := embeddingRequest{
		Texts:          []string{input.Content},
		Model:          model,
		InputType:      "search_document",
		EmbeddingTypes: []string{"float"},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, op.Error(err, "marshaling cohere embedding request")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/v2/embed", baseURL), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, op.Error(err, "building cohere embedding request")
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", e.cfg.APIKey))

	resp, err := e.client.Do(req) //nolint:gosec // G704: URL is constructed from trusted config
	if err != nil {
		return nil, op.Error(err, "executing cohere embedding request")
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			op.Acknowledge(closeErr, "closing response body")
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, op.Error(readErr, "reading cohere error response body")
		}
		err = errors.Errorf("cohere embedding API returned status %d: %s", resp.StatusCode, string(body))
		return nil, op.Error(err, "unexpected status code")
	}

	var embResp embeddingResponse
	if err = json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		return nil, op.Error(err, "decoding cohere embedding response")
	}

	if len(embResp.Embeddings.Float) == 0 {
		err = errors.New("cohere embedding response contained no data")
		return nil, op.Error(err, "empty response")
	}

	vector := toFloat32(embResp.Embeddings.Float[0])

	op.Set("embedding.dimensions", len(vector))

	return &embeddings.Embedding{
		Vector:      vector,
		SourceText:  input.Content,
		Model:       model,
		Provider:    providerName,
		Dimensions:  len(vector),
		GeneratedAt: time.Now(),
	}, nil
}

func toFloat32(f64 []float64) []float32 {
	out := make([]float32, len(f64))
	for i, v := range f64 {
		out[i] = float32(v)
	}
	return out
}

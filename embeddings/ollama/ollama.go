package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/primandproper/platform-go/v6/embeddings"
	"github.com/primandproper/platform-go/v6/errors"
	"github.com/primandproper/platform-go/v6/observability"
	"github.com/primandproper/platform-go/v6/observability/keys"
	"github.com/primandproper/platform-go/v6/observability/logging"
	"github.com/primandproper/platform-go/v6/observability/tracing"
)

const (
	defaultBaseURL = "http://localhost:11434"
	defaultModel   = "nomic-embed-text"
	providerName   = "ollama"
)

type embedder struct {
	o11y   observability.Observer
	client *http.Client
	cfg    *Config
}

// NewEmbedder creates a new Ollama-backed embeddings provider.
func NewEmbedder(ctx context.Context, cfg *Config, logger logging.Logger, tracer tracing.Tracer) (embeddings.Embedder, error) {
	if cfg == nil {
		return nil, errors.New("ollama embeddings config is required")
	}

	logger = logging.EnsureLogger(logger)

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating ollama embeddings config")
	}

	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
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
	Model string `json:"model"`
	Input string `json:"input"`
}

type embeddingResponse struct {
	Embeddings [][]float64 `json:"embeddings"`
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

	op.Set("embedding.model", model).Set(keys.LengthKey, len(input.Content))

	reqBody := embeddingRequest{
		Model: model,
		Input: input.Content,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, op.Error(err, "marshaling ollama embedding request")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/api/embed", e.cfg.BaseURL), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, op.Error(err, "building ollama embedding request")
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req) //nolint:gosec // G704: URL is constructed from trusted config
	if err != nil {
		return nil, op.Error(err, "executing ollama embedding request")
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			op.Acknowledge(closeErr, "closing response body")
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, op.Error(readErr, "reading ollama error response body")
		}
		err = errors.Errorf("ollama embedding API returned status %d: %s", resp.StatusCode, string(body))
		return nil, op.Error(err, "unexpected status code")
	}

	var embResp embeddingResponse
	if err = json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		return nil, op.Error(err, "decoding ollama embedding response")
	}

	if len(embResp.Embeddings) == 0 {
		err = errors.New("ollama embedding response contained no data")
		return nil, op.Error(err, "empty response")
	}

	vector := toFloat32(embResp.Embeddings[0])

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

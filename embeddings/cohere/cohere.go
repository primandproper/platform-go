package cohere

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/primandproper/platform-go/embeddings"
	"github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/tracing"
)

const (
	defaultBaseURL = "https://api.cohere.com"
	defaultModel   = "embed-english-v3.0"
	providerName   = "cohere"
)

type embedder struct {
	logger logging.Logger
	tracer tracing.Tracer
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

	client := &http.Client{}
	if cfg.Timeout > 0 {
		client.Timeout = cfg.Timeout
	}

	return &embedder{
		logger: logger,
		tracer: tracer,
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
func (e *embedder) GenerateEmbedding(ctx context.Context, input *embeddings.Input) (*embeddings.Embedding, error) {
	ctx, span := e.tracer.StartSpan(ctx)
	defer span.End()

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

	reqBody := embeddingRequest{
		Texts:          []string{input.Content},
		Model:          model,
		InputType:      "search_document",
		EmbeddingTypes: []string{"float"},
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		tracing.AttachErrorToSpan(span, "marshaling request", err)
		e.logger.Error("marshaling request", err)
		return nil, errors.Wrap(err, "marshaling cohere embedding request")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/v2/embed", baseURL), bytes.NewReader(bodyBytes))
	if err != nil {
		tracing.AttachErrorToSpan(span, "building request", err)
		e.logger.Error("building request", err)
		return nil, errors.Wrap(err, "building cohere embedding request")
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", e.cfg.APIKey))

	resp, err := e.client.Do(req) //nolint:gosec // G704: URL is constructed from trusted config
	if err != nil {
		tracing.AttachErrorToSpan(span, "executing request", err)
		e.logger.Error("executing request", err)
		return nil, errors.Wrap(err, "executing cohere embedding request")
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			e.logger.Error("closing response body", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, errors.Wrap(readErr, "reading cohere error response body")
		}
		err = errors.Errorf("cohere embedding API returned status %d: %s", resp.StatusCode, string(body))
		tracing.AttachErrorToSpan(span, "unexpected status code", err)
		e.logger.Error("unexpected status code", err)
		return nil, err
	}

	var embResp embeddingResponse
	if err = json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		tracing.AttachErrorToSpan(span, "decoding response", err)
		e.logger.Error("decoding response", err)
		return nil, errors.Wrap(err, "decoding cohere embedding response")
	}

	if len(embResp.Embeddings.Float) == 0 {
		err = errors.New("cohere embedding response contained no data")
		tracing.AttachErrorToSpan(span, "empty response", err)
		e.logger.Error("empty response", err)
		return nil, err
	}

	vector := toFloat32(embResp.Embeddings.Float[0])

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

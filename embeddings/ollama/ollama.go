package ollama

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
	defaultBaseURL = "http://localhost:11434"
	defaultModel   = "nomic-embed-text"
	providerName   = "ollama"
)

type embedder struct {
	logger logging.Logger
	tracer tracing.Tracer
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
	Model string `json:"model"`
	Input string `json:"input"`
}

type embeddingResponse struct {
	Embeddings [][]float64 `json:"embeddings"`
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

	reqBody := embeddingRequest{
		Model: model,
		Input: input.Content,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		tracing.AttachErrorToSpan(span, "marshaling request", err)
		e.logger.Error("marshaling request", err)
		return nil, errors.Wrap(err, "marshaling ollama embedding request")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/api/embed", e.cfg.BaseURL), bytes.NewReader(bodyBytes))
	if err != nil {
		tracing.AttachErrorToSpan(span, "building request", err)
		e.logger.Error("building request", err)
		return nil, errors.Wrap(err, "building ollama embedding request")
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req) //nolint:gosec // G704: URL is constructed from trusted config
	if err != nil {
		tracing.AttachErrorToSpan(span, "executing request", err)
		e.logger.Error("executing request", err)
		return nil, errors.Wrap(err, "executing ollama embedding request")
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			e.logger.Error("closing response body", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, errors.Wrap(readErr, "reading ollama error response body")
		}
		err = errors.Errorf("ollama embedding API returned status %d: %s", resp.StatusCode, string(body))
		tracing.AttachErrorToSpan(span, "unexpected status code", err)
		e.logger.Error("unexpected status code", err)
		return nil, err
	}

	var embResp embeddingResponse
	if err = json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		tracing.AttachErrorToSpan(span, "decoding response", err)
		e.logger.Error("decoding response", err)
		return nil, errors.Wrap(err, "decoding ollama embedding response")
	}

	if len(embResp.Embeddings) == 0 {
		err = errors.New("ollama embedding response contained no data")
		tracing.AttachErrorToSpan(span, "empty response", err)
		e.logger.Error("empty response", err)
		return nil, err
	}

	vector := toFloat32(embResp.Embeddings[0])

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

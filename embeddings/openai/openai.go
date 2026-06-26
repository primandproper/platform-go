package openai

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
	defaultBaseURL = "https://api.openai.com"
	defaultModel   = "text-embedding-3-small"
	providerName   = "openai"
)

type embedder struct {
	logger logging.Logger
	tracer tracing.Tracer
	client *http.Client
	cfg    *Config
}

// NewEmbedder creates a new OpenAI-backed embeddings provider.
func NewEmbedder(ctx context.Context, cfg *Config, logger logging.Logger, tracer tracing.Tracer) (embeddings.Embedder, error) {
	if cfg == nil {
		return nil, errors.New("openai embeddings config is required")
	}

	logger = logging.EnsureLogger(logger)

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating openai embeddings config")
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
	Input          string `json:"input"`
	Model          string `json:"model"`
	EncodingFormat string `json:"encoding_format"`
}

type embeddingResponse struct {
	Data []struct {
		Embedding []float64 `json:"embedding"`
	} `json:"data"`
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
		Input:          input.Content,
		Model:          model,
		EncodingFormat: "float",
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		tracing.AttachErrorToSpan(span, "marshaling request", err)
		e.logger.Error("marshaling request", err)
		return nil, errors.Wrap(err, "marshaling openai embedding request")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/v1/embeddings", baseURL), bytes.NewReader(bodyBytes))
	if err != nil {
		tracing.AttachErrorToSpan(span, "building request", err)
		e.logger.Error("building request", err)
		return nil, errors.Wrap(err, "building openai embedding request")
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", e.cfg.APIKey))

	resp, err := e.client.Do(req) //nolint:gosec // G704: URL is constructed from trusted config
	if err != nil {
		tracing.AttachErrorToSpan(span, "executing request", err)
		e.logger.Error("executing request", err)
		return nil, errors.Wrap(err, "executing openai embedding request")
	}
	defer func() {
		if closeErr := resp.Body.Close(); closeErr != nil {
			e.logger.Error("closing response body", closeErr)
		}
	}()

	if resp.StatusCode != http.StatusOK {
		body, readErr := io.ReadAll(resp.Body)
		if readErr != nil {
			return nil, errors.Wrap(readErr, "reading openai error response body")
		}
		err = errors.Errorf("openai embedding API returned status %d: %s", resp.StatusCode, string(body))
		tracing.AttachErrorToSpan(span, "unexpected status code", err)
		e.logger.Error("unexpected status code", err)
		return nil, err
	}

	var embResp embeddingResponse
	if err = json.NewDecoder(resp.Body).Decode(&embResp); err != nil {
		tracing.AttachErrorToSpan(span, "decoding response", err)
		e.logger.Error("decoding response", err)
		return nil, errors.Wrap(err, "decoding openai embedding response")
	}

	if len(embResp.Data) == 0 {
		err = errors.New("openai embedding response contained no data")
		tracing.AttachErrorToSpan(span, "empty response", err)
		e.logger.Error("empty response", err)
		return nil, err
	}

	vector := toFloat32(embResp.Data[0].Embedding)

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

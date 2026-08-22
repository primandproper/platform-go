package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/primandproper/platform-go/v13/embeddings"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
)

const (
	defaultBaseURL = "http://localhost:11434"
	defaultModel   = "nomic-embed-text"
	// name scopes this package's spans, logs, and metrics. It is qualified with
	// the component so that a vendor instrumented by more than one platform
	// package does not end up sharing an instrumentation scope.
	name = "ollama_embeddings"
	// providerName is what the Provider field of a returned Embedding reports:
	// the vendor rather than the component, since it is stored alongside the
	// vector and read back to reason about the model.
	providerName = "ollama"
)

var _ embeddings.Embedder = (*Embedder)(nil)

// Embedder is the Ollama embeddings.Embedder implementation. It is exported, and
// returned by NewEmbedder, so a caller who has chosen Ollama can depend on that
// choice rather than on the interface every embedding provider shares.
type Embedder struct {
	o11y        observability.Observer
	client      *http.Client
	cfg         *Config
	instruments *metrics.OperationSet
}

// NewEmbedder creates a new Ollama-backed embeddings provider.
func NewEmbedder(ctx context.Context, cfg *Config, opts ...Option) (*Embedder, error) {
	if cfg == nil {
		return nil, errors.New("ollama embeddings config is required")
	}

	o := newOptions(opts)
	logger := logging.EnsureLogger(o.logger)

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

	instruments, err := metrics.NewOperationSet(o.metricsProvider, name)
	if err != nil {
		return nil, err
	}

	return &Embedder{
		o11y:        observability.NewObserver(name, logger, o.tracerProvider),
		client:      client,
		cfg:         cfg,
		instruments: instruments,
	}, nil
}

type embeddingRequest struct {
	Model string   `json:"model"`
	Input []string `json:"input"`
}

type embeddingResponse struct {
	Embeddings [][]float64 `json:"embeddings"`
}

// GenerateEmbeddings implements embeddings.Embedder.
//
// Every input in a call must resolve to the same model, because Ollama embeds
// one batch against one model; a batch spanning two models is rejected rather
// than silently split, which would make the round-trip count depend on the
// caller's ordering.
//
// Rate limiting: this method does not retry. A non-200 response (including 429 Too Many
// Requests) is surfaced to the caller as an error carrying the status code; it is not
// retried or backed off. Callers that want retry/backoff should wrap this call themselves
// (e.g. with the platform's retry package).
func (e *Embedder) GenerateEmbeddings(ctx context.Context, inputs []*embeddings.Input) (_ []*embeddings.Embedding, err error) {
	ctx, op := e.o11y.Begin(ctx)
	defer op.End()

	if len(inputs) == 0 {
		return []*embeddings.Embedding{}, nil
	}

	// Instrumented here rather than in GenerateEmbedding, which delegates to
	// this method — counting both would double every single-input call.
	e.instruments.Attempt(ctx)
	defer op.Time(ctx, nil, e.instruments.Latency)()
	defer func() {
		if err != nil {
			e.instruments.Failed(ctx)
		}
	}()

	texts := make([]string, len(inputs))
	var model string
	for i, input := range inputs {
		if input == nil {
			return nil, embeddings.ErrNilInput
		}

		texts[i] = input.Content

		m := input.Model
		if m == "" {
			m = e.cfg.DefaultModel
		}
		if m == "" {
			m = defaultModel
		}

		if i == 0 {
			model = m
		} else if m != model {
			return nil, op.Error(errors.Newf("batch spans models %q and %q", model, m), "mixed models in one batch")
		}
	}

	op.Set(keys.EmbeddingModelKey, model).Set(keys.LengthKey, len(inputs))

	reqBody := embeddingRequest{
		Model: model,
		Input: texts,
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

	if len(embResp.Embeddings) != len(inputs) {
		return nil, op.Error(
			errors.Newf("ollama returned %d embeddings for %d inputs", len(embResp.Embeddings), len(inputs)),
			"mismatched response length",
		)
	}

	now := time.Now()
	out := make([]*embeddings.Embedding, len(inputs))
	for i, raw := range embResp.Embeddings {
		vector := embeddings.ToFloat32(raw)
		out[i] = &embeddings.Embedding{
			Vector:      vector,
			SourceText:  texts[i],
			Model:       model,
			Provider:    providerName,
			Dimensions:  len(vector),
			GeneratedAt: now,
		}
	}

	op.Set("embedding.dimensions", out[0].Dimensions)

	return out, nil
}

// GenerateEmbedding implements embeddings.Embedder by embedding one input.
//
// It is a thin wrapper over GenerateEmbeddings: Ollama's API takes an array,
// so one input is simply a batch of one.
func (e *Embedder) GenerateEmbedding(ctx context.Context, input *embeddings.Input) (*embeddings.Embedding, error) {
	out, err := e.GenerateEmbeddings(ctx, []*embeddings.Input{input})
	if err != nil {
		return nil, err
	}

	return out[0], nil
}

package openai

import (
	"context"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v5/errors"
	"github.com/primandproper/platform-go/v5/llm"
	"github.com/primandproper/platform-go/v5/observability"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	"github.com/primandproper/platform-go/v5/observability/tracing"
	"github.com/primandproper/platform-go/v5/pointer"

	anyllm "github.com/mozilla-ai/any-llm-go"
	anyllmopenai "github.com/mozilla-ai/any-llm-go/providers/openai"
)

const name = "openai_llm"

// NewProvider creates a new OpenAI-backed LLM provider.
func NewProvider(cfg *Config, logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider) (llm.Provider, error) {
	if cfg == nil {
		return nil, errors.New("openai config is required")
	}

	opts := []anyllm.Option{
		anyllm.WithAPIKey(cfg.APIKey),
	}
	if cfg.BaseURL != "" {
		opts = append(opts, anyllm.WithBaseURL(cfg.BaseURL))
	}
	if cfg.Timeout > 0 {
		opts = append(opts, anyllm.WithTimeout(cfg.Timeout))
	}

	provider, err := anyllmopenai.New(opts...)
	if err != nil {
		return nil, errors.Wrap(err, "create openai provider")
	}

	mp := metrics.EnsureMetricsProvider(metricsProvider)

	requestCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_requests", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating request counter")
	}

	errorCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_errors", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating error counter")
	}

	latencyHist, err := mp.NewFloat64Histogram(fmt.Sprintf("%s_latency_ms", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating latency histogram")
	}

	return &openaiProvider{
		o11y:           observability.NewObserver(name, logger, tracerProvider),
		requestCounter: requestCounter,
		errorCounter:   errorCounter,
		latencyHist:    latencyHist,
		provider:       provider,
		defaultModel:   cfg.DefaultModel,
	}, nil
}

type openaiProvider struct {
	o11y           observability.Observer
	requestCounter metrics.Int64Counter
	errorCounter   metrics.Int64Counter
	latencyHist    metrics.Float64Histogram
	provider       *anyllmopenai.Provider
	defaultModel   string
}

// Completion implements llm.Provider.
//
// Rate limiting: this method does not retry. The underlying any-llm library classifies a
// 429 as *anyllm.RateLimitError (which carries a RetryAfter) but never retries on its own,
// so the error is surfaced to the caller unchanged. Callers that want backoff should wrap
// this call themselves (e.g. with the platform's retry package), inspecting the error via
// errors.As for *anyllm.RateLimitError / errors.Is for anyllm.ErrRateLimit.
func (p *openaiProvider) Completion(ctx context.Context, params llm.CompletionParams) (*llm.CompletionResult, error) {
	ctx, op := p.o11y.Begin(ctx)
	defer op.End()

	startTime := time.Now()
	defer func() {
		p.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	model := params.Model
	if model == "" {
		model = p.defaultModel
	}
	if model == "" {
		model = "gpt-4o-mini"
	}

	op.Set("llm.model", model).Set("llm.message_count", len(params.Messages))

	anyllmParams := anyllm.CompletionParams{
		Model:    model,
		Messages: toAnyLLMMessages(pointer.ToSlice(params.Messages)),
	}

	resp, err := p.provider.Completion(ctx, anyllmParams)
	if err != nil {
		p.errorCounter.Add(ctx, 1)
		return nil, op.Error(err, "completing request")
	}

	p.requestCounter.Add(ctx, 1)

	if resp.Usage != nil {
		op.Set("llm.tokens.total", resp.Usage.TotalTokens)
	}
	if len(resp.Choices) > 0 {
		op.Set("llm.finish_reason", resp.Choices[0].FinishReason)
	}

	return toCompletionResult(resp), nil
}

func toAnyLLMMessages(msgs []*llm.Message) []anyllm.Message {
	out := make([]anyllm.Message, len(msgs))
	for i, m := range msgs {
		out[i] = anyllm.Message{
			Role:    m.Role,
			Content: m.Content,
		}
	}
	return out
}

func toCompletionResult(resp *anyllm.ChatCompletion) *llm.CompletionResult {
	content := ""
	if len(resp.Choices) > 0 {
		content = resp.Choices[0].Message.ContentString()
	}
	return &llm.CompletionResult{Content: content}
}

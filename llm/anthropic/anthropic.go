package anthropic

import (
	"context"
	"fmt"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/llm"
	"github.com/primandproper/platform-go/v13/llm/internal/bridge"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	anyllm "github.com/mozilla-ai/any-llm-go"
	anyllmanthropic "github.com/mozilla-ai/any-llm-go/providers/anthropic"
)

const (
	// name scopes this package's spans, logs, and metrics.
	name = "anthropic_llm"
	// providerName is what llm.Provider.Name reports. It is the vendor rather
	// than the component, since callers use it to reason about the model, and
	// it is stable enough to persist alongside stored completions.
	providerName = "anthropic"
	// fallbackModel is used when neither the request nor the config names one.
	//
	// It is deliberately a current, non-dated model alias rather than a pinned
	// snapshot: a dated ID retires on a published schedule and then starts
	// 404ing with no code change on this side, which is a failure that shows up
	// in production on a date nobody wrote down. The alias tracks the model.
	//
	// Sonnet tier is the default because this is what a caller who named no
	// model at all gets — a library should not silently select the most
	// expensive option on their behalf.
	fallbackModel = "claude-sonnet-5"
)

var _ llm.Provider = (*Provider)(nil)

// NewProvider creates a new Anthropic-backed LLM provider.
func NewProvider(cfg *Config, opts ...Option) (*Provider, error) {
	if cfg == nil {
		return nil, errors.New("anthropic config is required")
	}

	providerOpts := []anyllm.Option{
		anyllm.WithAPIKey(cfg.APIKey),
	}
	if cfg.BaseURL != "" {
		providerOpts = append(providerOpts, anyllm.WithBaseURL(cfg.BaseURL))
	}
	if cfg.Timeout > 0 {
		providerOpts = append(providerOpts, anyllm.WithTimeout(cfg.Timeout))
	}

	provider, err := anyllmanthropic.New(providerOpts...)
	if err != nil {
		return nil, errors.Wrap(err, "create anthropic provider")
	}

	o := newOptions(opts)
	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

	instruments, err := metrics.NewOperationSet(o.metricsProvider, name)
	if err != nil {
		return nil, err
	}

	tokenCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_tokens", name))
	if err != nil {
		return nil, errors.Wrap(err, "creating token counter")
	}

	return &Provider{
		o11y:         observability.NewObserver(name, o.logger, o.tracerProvider),
		instruments:  instruments,
		tokenCounter: tokenCounter,
		provider:     provider,
		defaultModel: cfg.DefaultModel,
	}, nil
}

// Provider is the Anthropic llm.Provider implementation. It is exported, and
// returned by NewProvider, so a caller who has chosen Anthropic can depend on that
// choice rather than on the interface every model provider shares.
type Provider struct {
	o11y         observability.Observer
	instruments  *metrics.OperationSet
	tokenCounter metrics.Int64Counter
	// provider is the interface rather than the concrete Anthropic provider, so
	// that the observability and translation seams around it can be exercised
	// without an HTTP round trip.
	provider     anyllm.Provider
	defaultModel string
}

// Name implements llm.Provider.
func (*Provider) Name() string {
	return providerName
}

// Capabilities implements llm.Provider.
//
// the capability describes the provider rather than what a caller can currently
// ask for.
func (*Provider) Capabilities() llm.Capabilities {
	return llm.Capabilities{
		Streaming:        true,
		Tools:            true,
		Images:           true,
		Reasoning:        true,
		StructuredOutput: true,
	}
}

// Completion implements llm.Provider.
//
// It does not retry. A rate limit comes back as an error matching
// llm.ErrRateLimited — usually a *llm.RateLimitError carrying the provider's
// advice about how long to wait — and choosing a backoff against that advice is
// the caller's job, since only the caller knows its own deadline.
func (p *Provider) Completion(ctx context.Context, req *llm.CompletionRequest) (*llm.CompletionResponse, error) {
	ctx, op := p.o11y.Begin(ctx)
	defer op.End()

	// Counted here, before anything can fail, so _requests means the same thing
	// in Completion as it does in Stream. It used to be incremented only after a
	// successful call here and before the call in Stream, so one counter name
	// meant "successes" on one method and "attempts" on the other — and
	// _requests minus _errors was a number with no interpretation at all.
	p.instruments.Attempt(ctx)

	defer op.Time(ctx, nil, p.instruments.Latency)()

	params, err := p.params(req, op)
	if err != nil {
		p.instruments.Failed(ctx)

		return nil, op.Error(err, "building request")
	}

	resp, err := p.provider.Completion(ctx, params)
	if err != nil {
		p.instruments.Failed(ctx)

		return nil, op.Error(bridge.NormalizeError(err), "completing request")
	}

	out := bridge.Response(resp)
	p.recordUsage(ctx, op, out.Usage)
	op.Set("llm.stop_reason", string(out.StopReason))

	return out, nil
}

// Stream implements llm.Provider.
//
// The span and the latency measurement cover the whole stream rather than the
// call that starts it, which is why they are ended by the returned stream's
// finish hook instead of a defer here. A consumer that abandons the stream
// without closing it leaves both open; llm.Stream documents Close as mandatory
// for exactly this reason.
func (p *Provider) Stream(ctx context.Context, req *llm.CompletionRequest) (llm.Stream, error) {
	ctx, op := p.o11y.Begin(ctx)

	params, err := p.params(req, op)
	if err != nil {
		defer op.End()
		p.instruments.Failed(ctx)

		return nil, op.Error(err, "building request")
	}

	recordLatency := op.Time(ctx, nil, p.instruments.Latency)
	p.instruments.Attempt(ctx)

	streamCtx, cancel := context.WithCancel(ctx)
	chunks, errs := p.provider.CompletionStream(streamCtx, params)

	return bridge.Stream(chunks, errs, func(streamErr error, streamUsage *llm.Usage) {
		cancel()
		recordLatency()
		p.recordUsage(ctx, op, streamUsage)

		if streamErr != nil {
			p.instruments.Failed(ctx)
			op.Acknowledge(streamErr, "streaming completion")
		}

		op.End()
	}), nil
}

// recordUsage puts a request's token accounting on the span and the token
// counter.
//
// Streaming reached neither until now. The usage is only known from the final
// chunk, so it was not available when Stream returned, and nothing carried it
// back afterwards — which meant a service doing all its work through Stream had
// no token numbers at all, despite this provider asking the API for them
// specifically.
func (p *Provider) recordUsage(ctx context.Context, op observability.Operation, u *llm.Usage) {
	if u == nil {
		return
	}

	op.Set("llm.tokens.total", u.TotalTokens)
	p.tokenCounter.Add(ctx, int64(u.TotalTokens))
}

// params resolves the model and translates the request, recording what was
// asked for on the operation either way.
func (p *Provider) params(req *llm.CompletionRequest, op observability.Operation) (anyllm.CompletionParams, error) {
	model := ""
	messageCount := 0
	if req != nil {
		model = req.Model
		messageCount = len(req.Messages)
	}

	if model == "" {
		model = p.defaultModel
	}
	if model == "" {
		model = fallbackModel
	}

	op.Set("llm.model", model).Set("llm.message_count", messageCount)

	return bridge.Params(req, model)
}

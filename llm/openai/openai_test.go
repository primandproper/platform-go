package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/llm"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/metrics/metricstest"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"

	anyllm "github.com/mozilla-ai/any-llm-go"
	anyllmerrors "github.com/mozilla-ai/any-llm-go/errors"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// newRecordingProvider builds a provider with a RecordingObserver swapped in, so a
// test can both drive Completion and assert which fields it observed.
func newRecordingProvider(t *testing.T, cfg *Config) (*Provider, *observability.RecordingObserver) {
	t.Helper()

	prov, err := NewProvider(cfg)
	must.NoError(t, err)
	must.NotNil(t, prov)

	obs := observability.NewRecordingObserver()
	prov.o11y = obs

	return prov, obs
}

// newFakeProvider builds a provider whose upstream is a fake rather than an
// HTTP client, for the paths — streaming above all — where standing up an SSE
// server would test net/http more than it tests this package.
func newFakeProvider(t *testing.T, upstream *fakeUpstream) (*Provider, *observability.RecordingObserver) {
	t.Helper()

	prov, obs := newRecordingProvider(t, &Config{APIKey: "test-key"})
	prov.provider = upstream

	return prov, obs
}

// fakeUpstream is a stand-in for any-llm-go's provider.
type fakeUpstream struct {
	completionErr error
	streamErr     error
	response      *anyllm.ChatCompletion
	params        anyllm.CompletionParams
	chunks        []anyllm.ChatCompletionChunk
	streamed      bool
}

func (*fakeUpstream) Name() string {
	return "fake"
}

//nolint:gocritic // The any-llm-go interface passes params by value; matching it is not optional.
func (f *fakeUpstream) Completion(_ context.Context, params anyllm.CompletionParams) (*anyllm.ChatCompletion, error) {
	f.params = params
	if f.completionErr != nil {
		return nil, f.completionErr
	}

	return f.response, nil
}

//nolint:gocritic // The any-llm-go interface passes params by value; matching it is not optional.
func (f *fakeUpstream) CompletionStream(ctx context.Context, params anyllm.CompletionParams) (<-chan anyllm.ChatCompletionChunk, <-chan error) {
	// Captured before the goroutine starts, so a test can read it without
	// racing the send.
	f.params = params
	f.streamed = true

	chunks := make(chan anyllm.ChatCompletionChunk)
	errs := make(chan error, 1)

	go func() {
		defer close(chunks)
		defer close(errs)

		for i := range f.chunks {
			select {
			case chunks <- f.chunks[i]:
			case <-ctx.Done():
				return
			}
		}

		if f.streamErr != nil {
			errs <- f.streamErr
		}
	}()

	return chunks, errs
}

func TestNewProvider(T *testing.T) {
	T.Parallel()

	T.Run("with nil config", func(t *testing.T) {
		t.Parallel()

		provider, err := NewProvider(nil)
		must.Error(t, err)
		must.Nil(t, provider)
	})

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		provider, err := NewProvider(&Config{APIKey: "test-key"})
		must.NoError(t, err)
		must.NotNil(t, provider)
	})

	T.Run("with base URL and timeout", func(t *testing.T) {
		t.Parallel()

		provider, err := NewProvider(
			&Config{
				APIKey:       "test-key",
				BaseURL:      "https://custom.example.com/v1",
				DefaultModel: "gpt-4o",
			},
		)
		must.NoError(t, err)
		must.NotNil(t, provider)
	})

	T.Run("with timeout", func(t *testing.T) {
		t.Parallel()

		provider, err := NewProvider(&Config{
			APIKey:  "test-key",
			Timeout: 5 * time.Second,
		})
		must.NoError(t, err)
		must.NotNil(t, provider)
	})

	T.Run("with a base URL the client rejects", func(t *testing.T) {
		t.Parallel()

		// A scheme-less base URL is refused by the underlying client, and the
		// failure has to come back from the constructor rather than surfacing
		// later as a confusing per-request error.
		provider, err := NewProvider(&Config{APIKey: "test-key", BaseURL: "example.com/no-scheme"})
		must.Error(t, err)
		must.Nil(t, provider)
	})

	T.Run("with error creating request counter", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, name+"_requests", counterName)
				return metricstest.Int64Counter(t, "x"), errors.New("arbitrary")
			},
		}

		provider, err := NewProvider(&Config{APIKey: "test-key"}, WithMetricsProvider(mp))
		must.Error(t, err)
		must.Nil(t, provider)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating error counter", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				switch counterName {
				case name + "_requests":
					return metricstest.Int64Counter(t, "x"), nil
				case name + "_errors":
					return metricstest.Int64Counter(t, "x"), errors.New("arbitrary")
				}
				t.Fatalf("unexpected NewInt64Counter call: %q", counterName)
				return nil, nil
			},
		}

		provider, err := NewProvider(&Config{APIKey: "test-key"}, WithMetricsProvider(mp))
		must.Error(t, err)
		must.Nil(t, provider)

		// requests, then the error counter that failed.
		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating latency histogram", func(t *testing.T) {
		t.Parallel()

		noopMP := metricsnoop.NewMetricsProvider()
		h, histErr := noopMP.NewFloat64Histogram("test")
		must.NoError(t, histErr)

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metricstest.Int64Counter(t, "x"), nil
			},
			NewFloat64HistogramFunc: func(histName string, _ ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
				test.EqOp(t, name+"_latency_ms", histName)
				return h, errors.New("arbitrary")
			},
		}

		provider, err := NewProvider(&Config{APIKey: "test-key"}, WithMetricsProvider(mp))
		must.Error(t, err)
		must.Nil(t, provider)

		// The set builds requests and errors before its histogram, and the
		// token counter is this provider's own, built after the set — so a
		// histogram that fails is reached with two counters made, not three.
		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
		test.SliceLen(t, 1, mp.NewFloat64HistogramCalls())
	})
}

func TestOpenAIProvider_Name(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		provider, err := NewProvider(&Config{APIKey: "test-key"})
		must.NoError(t, err)

		// The vendor, not the component: callers persist this alongside stored
		// completions, so it must not drift with the metric name.
		test.EqOp(t, "openai", provider.Name())
	})
}

func TestOpenAIProvider_Capabilities(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		provider, err := NewProvider(&Config{APIKey: "test-key"})
		must.NoError(t, err)

		test.Eq(t, llm.Capabilities{
			Streaming:        true,
			Tools:            true,
			Images:           true,
			Reasoning:        true,
			StructuredOutput: true,
		}, provider.Capabilities())
	})
}

func TestOpenAIProvider_Completion(T *testing.T) {
	T.Parallel()

	openAIChatCompletion := map[string]any{
		"id":      "chatcmpl-test",
		"object":  "chat.completion",
		"created": 1234567890,
		"model":   "gpt-4o-mini",
		"choices": []map[string]any{
			{
				"index": 0,
				"message": map[string]any{
					"role":    "assistant",
					"content": "Hello from mock!",
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]any{
			"prompt_tokens":     10,
			"completion_tokens": 5,
			"total_tokens":      15,
		},
	}

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			must.EqOp(t, "/v1/chat/completions", r.URL.Path)
			must.EqOp(t, http.MethodPost, r.Method)
			w.Header().Set("Content-Type", "application/json")
			must.NoError(t, json.NewEncoder(w).Encode(openAIChatCompletion))
		}))
		t.Cleanup(ts.Close)

		provider, obs := newRecordingProvider(t, &Config{
			APIKey:  "test-key",
			BaseURL: ts.URL + "/v1",
		})

		result, err := provider.Completion(t.Context(), &llm.CompletionRequest{
			Model:    "gpt-4o-mini",
			Messages: []llm.Message{llm.UserText("Hello")},
		})
		must.NoError(t, err)
		must.NotNil(t, result)
		must.EqOp(t, "Hello from mock!", result.Text())
		test.EqOp(t, llm.StopReasonEndTurn, result.StopReason)

		must.NotNil(t, result.Usage)
		test.EqOp(t, 15, result.Usage.TotalTokens)

		obs.ObservedOperationWithData(t, map[string]any{
			"llm.model":         "gpt-4o-mini",
			"llm.message_count": 1,
			"llm.tokens.total":  15,
			"llm.stop_reason":   "end_turn",
		})
	})

	T.Run("uses the configured default model when the request names none", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			must.NoError(t, json.NewEncoder(w).Encode(openAIChatCompletion))
		}))
		t.Cleanup(ts.Close)

		provider, err := NewProvider(&Config{
			APIKey:       "test-key",
			BaseURL:      ts.URL + "/v1",
			DefaultModel: "gpt-4o",
		})
		must.NoError(t, err)

		result, err := provider.Completion(t.Context(), &llm.CompletionRequest{
			Messages: []llm.Message{llm.UserText("Hi")},
		})
		must.NoError(t, err)
		must.EqOp(t, "Hello from mock!", result.Text())
	})

	T.Run("falls back to a built-in model when nothing names one", func(t *testing.T) {
		t.Parallel()

		upstream := &fakeUpstream{response: &anyllm.ChatCompletion{}}
		provider, obs := newFakeProvider(t, upstream)

		_, err := provider.Completion(t.Context(), &llm.CompletionRequest{
			Messages: []llm.Message{llm.UserText("Hi")},
		})
		must.NoError(t, err)

		test.EqOp(t, fallbackModel, upstream.params.Model)
		obs.ObservedOperationWithData(t, map[string]any{"llm.model": fallbackModel})
	})

	T.Run("translates the whole request", func(t *testing.T) {
		t.Parallel()

		upstream := &fakeUpstream{response: &anyllm.ChatCompletion{}}
		provider, _ := newFakeProvider(t, upstream)

		_, err := provider.Completion(t.Context(), &llm.CompletionRequest{
			Model:    "gpt-4o-mini",
			System:   "be terse",
			Messages: []llm.Message{llm.UserText("Hi")},
			Tools: []llm.Tool{{
				Name:        "lookup",
				Description: "looks things up",
				Schema:      map[string]any{"type": "object"},
			}},
			ResponseFormat: &llm.ResponseFormat{Name: "answer", Schema: map[string]any{"type": "object"}},
		})
		must.NoError(t, err)

		must.SliceLen(t, 2, upstream.params.Messages)
		test.EqOp(t, anyllm.RoleSystem, upstream.params.Messages[0].Role)
		must.SliceLen(t, 1, upstream.params.Tools)
		must.NotNil(t, upstream.params.ResponseFormat)
		test.EqOp(t, "json_schema", upstream.params.ResponseFormat.Type)
	})

	T.Run("returns the model's tool calls", func(t *testing.T) {
		t.Parallel()

		upstream := &fakeUpstream{response: &anyllm.ChatCompletion{
			Choices: []anyllm.Choice{{
				FinishReason: anyllm.FinishReasonToolCalls,
				Message: anyllm.Message{
					ToolCalls: []anyllm.ToolCall{{
						ID:       "call_1",
						Function: anyllm.FunctionCall{Name: "lookup", Arguments: `{"q":"x"}`},
					}},
				},
			}},
		}}
		provider, _ := newFakeProvider(t, upstream)

		result, err := provider.Completion(t.Context(), &llm.CompletionRequest{
			Messages: []llm.Message{llm.UserText("Hi")},
		})
		must.NoError(t, err)

		test.EqOp(t, llm.StopReasonToolUse, result.StopReason)
		uses := result.ToolUses()
		must.SliceLen(t, 1, uses)
		test.EqOp(t, "call_1", uses[0].ID)
	})

	T.Run("with an unbuildable request", func(t *testing.T) {
		t.Parallel()

		upstream := &fakeUpstream{}
		provider, obs := newFakeProvider(t, upstream)

		result, err := provider.Completion(t.Context(), &llm.CompletionRequest{})
		must.Error(t, err)
		must.Nil(t, result)
		must.ErrorIs(t, err, llm.ErrInvalidRequest)

		// The request is rejected here rather than over the network, so the
		// upstream is never reached.
		test.EqOp(t, "", upstream.params.Model)

		op := obs.ObservedOperationWithData(t, map[string]any{"llm.message_count": 0})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("with a nil request", func(t *testing.T) {
		t.Parallel()

		provider, _ := newFakeProvider(t, &fakeUpstream{})

		result, err := provider.Completion(t.Context(), nil)
		must.Error(t, err)
		must.Nil(t, result)
		must.ErrorIs(t, err, llm.ErrInvalidRequest)
	})

	T.Run("with API error", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"server error"}}`))
		}))
		t.Cleanup(ts.Close)

		provider, obs := newRecordingProvider(t, &Config{
			APIKey:  "test-key",
			BaseURL: ts.URL + "/v1",
		})

		result, err := provider.Completion(t.Context(), &llm.CompletionRequest{
			Model:    "gpt-4o-mini",
			Messages: []llm.Message{llm.UserText("Hi")},
		})
		must.Error(t, err)
		must.Nil(t, result)

		// Even though the request failed, the values must still have been observed,
		// and the failure itself recorded on the operation.
		op := obs.ObservedOperationWithData(t, map[string]any{
			"llm.model": "gpt-4o-mini",
		})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("normalizes the upstream error", func(t *testing.T) {
		t.Parallel()

		provider, _ := newFakeProvider(t, &fakeUpstream{
			completionErr: anyllmerrors.NewModelNotFoundError("openai", errors.New("no such model")),
		})

		_, err := provider.Completion(t.Context(), &llm.CompletionRequest{
			Messages: []llm.Message{llm.UserText("Hi")},
		})
		must.ErrorIs(t, err, llm.ErrModelNotFound)
	})
}

func TestOpenAIProvider_Stream(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		upstream := &fakeUpstream{chunks: []anyllm.ChatCompletionChunk{
			{Choices: []anyllm.ChunkChoice{{Delta: anyllm.ChunkDelta{Content: "Hel"}}}},
			{Choices: []anyllm.ChunkChoice{{Delta: anyllm.ChunkDelta{Content: "lo"}}}},
			{
				Choices: []anyllm.ChunkChoice{{FinishReason: anyllm.FinishReasonStop}},
				Usage:   &anyllm.Usage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 5},
			},
		}}
		provider, obs := newFakeProvider(t, upstream)

		stream, err := provider.Stream(t.Context(), &llm.CompletionRequest{
			Model:    "gpt-4o-mini",
			Messages: []llm.Message{llm.UserText("Hi")},
		})
		must.NoError(t, err)
		t.Cleanup(func() { must.NoError(t, stream.Close()) })

		var text strings.Builder
		var events int
		for stream.Next() {
			event := stream.Current()
			events++
			if event.Type == llm.EventTextDelta {
				text.WriteString(event.Text)
			}
		}
		must.NoError(t, stream.Err())

		test.EqOp(t, "Hello", text.String())
		test.EqOp(t, 3, events)

		op := obs.ObservedOperationWithData(t, map[string]any{
			"llm.model": "gpt-4o-mini",
		})
		test.True(t, op.Ended)
		test.SliceEmpty(t, op.Errors)
	})

	T.Run("asks for usage, which OpenAI otherwise omits from a stream", func(t *testing.T) {
		t.Parallel()

		upstream := &fakeUpstream{}
		provider, _ := newFakeProvider(t, upstream)

		stream, err := provider.Stream(t.Context(), &llm.CompletionRequest{
			Messages: []llm.Message{llm.UserText("Hi")},
		})
		must.NoError(t, err)
		t.Cleanup(func() { must.NoError(t, stream.Close()) })

		// Without this, a streamed llm.EventDone would carry no token
		// accounting at all.
		must.NotNil(t, upstream.params.StreamOptions)
		test.True(t, upstream.params.StreamOptions.IncludeUsage)
	})

	T.Run("leaves the operation open until the stream ends", func(t *testing.T) {
		t.Parallel()

		upstream := &fakeUpstream{chunks: []anyllm.ChatCompletionChunk{
			{Choices: []anyllm.ChunkChoice{{Delta: anyllm.ChunkDelta{Content: "one"}}}},
			{Choices: []anyllm.ChunkChoice{{FinishReason: anyllm.FinishReasonStop}}},
		}}
		provider, obs := newFakeProvider(t, upstream)

		stream, err := provider.Stream(t.Context(), &llm.CompletionRequest{
			Messages: []llm.Message{llm.UserText("Hi")},
		})
		must.NoError(t, err)

		must.True(t, stream.Next())

		// Read straight off the observer: the ObservedOperation helpers treat an
		// unended operation as a leaked span and fail, which is exactly the
		// state a stream in flight is supposed to be in.
		must.SliceLen(t, 1, obs.Operations)
		op := obs.Operations[0]
		test.False(t, op.Ended)

		must.NoError(t, stream.Close())
		test.True(t, op.Ended)
	})

	T.Run("accumulates streamed tool calls", func(t *testing.T) {
		t.Parallel()

		// OpenAI's framing: the ID and name once, then bare fragments.
		upstream := &fakeUpstream{chunks: []anyllm.ChatCompletionChunk{
			{Choices: []anyllm.ChunkChoice{{Delta: anyllm.ChunkDelta{ToolCalls: []anyllm.ToolCall{{
				ID:       "call_1",
				Function: anyllm.FunctionCall{Name: "lookup", Arguments: `{"q"`},
			}}}}}},
			{Choices: []anyllm.ChunkChoice{{Delta: anyllm.ChunkDelta{ToolCalls: []anyllm.ToolCall{{
				Function: anyllm.FunctionCall{Arguments: `:"x"}`},
			}}}}}},
			{Choices: []anyllm.ChunkChoice{{FinishReason: anyllm.FinishReasonToolCalls}}},
		}}
		provider, _ := newFakeProvider(t, upstream)

		stream, err := provider.Stream(t.Context(), &llm.CompletionRequest{
			Messages: []llm.Message{llm.UserText("Hi")},
		})
		must.NoError(t, err)
		t.Cleanup(func() { must.NoError(t, stream.Close()) })

		var uses []llm.ToolUse
		for stream.Next() {
			if event := stream.Current(); event.Type == llm.EventToolUse {
				uses = append(uses, *event.ToolUse)
			}
		}
		must.NoError(t, stream.Err())

		must.SliceLen(t, 1, uses)
		test.EqOp(t, "lookup", uses[0].Name)
		test.EqOp(t, `{"q":"x"}`, string(uses[0].Input))
	})

	T.Run("with an upstream failure", func(t *testing.T) {
		t.Parallel()

		upstream := &fakeUpstream{
			streamErr: anyllmerrors.NewContentFilterError("openai", errors.New("blocked")),
		}
		provider, obs := newFakeProvider(t, upstream)

		stream, err := provider.Stream(t.Context(), &llm.CompletionRequest{
			Messages: []llm.Message{llm.UserText("Hi")},
		})

		// A stream that fails after it started reports through Err, not here.
		must.NoError(t, err)
		t.Cleanup(func() { must.NoError(t, stream.Close()) })

		test.False(t, stream.Next())
		must.ErrorIs(t, stream.Err(), llm.ErrContentFiltered)

		op := obs.ObservedOperationWithData(t, map[string]any{"llm.message_count": 1})
		test.True(t, op.Ended)
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("with an unbuildable request", func(t *testing.T) {
		t.Parallel()

		upstream := &fakeUpstream{}
		provider, obs := newFakeProvider(t, upstream)

		stream, err := provider.Stream(t.Context(), &llm.CompletionRequest{})
		must.Error(t, err)
		must.Nil(t, stream)
		must.ErrorIs(t, err, llm.ErrInvalidRequest)

		test.False(t, upstream.streamed)

		op := obs.ObservedOperationWithData(t, map[string]any{"llm.message_count": 0})
		test.True(t, op.Ended)
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("with a nil request", func(t *testing.T) {
		t.Parallel()

		provider, _ := newFakeProvider(t, &fakeUpstream{})

		stream, err := provider.Stream(t.Context(), nil)
		must.Error(t, err)
		must.Nil(t, stream)
	})
}

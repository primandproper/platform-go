package anthropic

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/primandproper/platform-go/llm"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/observability/metrics/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// newRecordingProvider builds a provider with a RecordingObserver swapped in, so a
// test can both drive Completion and assert which fields it observed.
func newRecordingProvider(t *testing.T, cfg *Config) (*anthropicProvider, *observability.RecordingObserver) {
	t.Helper()

	p, err := NewProvider(cfg, nil, nil, nil)
	must.NoError(t, err)
	must.NotNil(t, p)

	prov, ok := p.(*anthropicProvider)
	must.True(t, ok)

	obs := observability.NewRecordingObserver()
	prov.o11y = obs

	return prov, obs
}

// anthropicMessageResponse mimics the Anthropic Messages API response format.
func anthropicMessageResponse(content string) map[string]any {
	return map[string]any{
		"id":          "msg-test",
		"type":        "message",
		"role":        "assistant",
		"model":       "claude-sonnet-4-20250514",
		"content":     []map[string]any{{"type": "text", "text": content}},
		"stop_reason": "end_turn",
		"usage": map[string]any{
			"input_tokens":  10,
			"output_tokens": 5,
		},
	}
}

func TestNewProvider(T *testing.T) {
	T.Parallel()

	T.Run("with nil config", func(t *testing.T) {
		t.Parallel()

		provider, err := NewProvider(nil, nil, nil, nil)
		must.Error(t, err)
		must.Nil(t, provider)
	})

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		provider, err := NewProvider(&Config{APIKey: "test-key"}, nil, nil, nil)
		must.NoError(t, err)
		must.NotNil(t, provider)
	})

	T.Run("with base URL", func(t *testing.T) {
		t.Parallel()

		provider, err := NewProvider(&Config{
			APIKey:       "test-key",
			BaseURL:      "https://custom.example.com",
			DefaultModel: "claude-sonnet-4",
		}, nil, nil, nil)
		must.NoError(t, err)
		must.NotNil(t, provider)
	})

	T.Run("with timeout", func(t *testing.T) {
		t.Parallel()

		provider, err := NewProvider(&Config{
			APIKey:  "test-key",
			Timeout: 5 * time.Second,
		}, nil, nil, nil)
		must.NoError(t, err)
		must.NotNil(t, provider)
	})

	T.Run("with error creating request counter", func(t *testing.T) {
		t.Parallel()

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, name+"_requests", counterName)
				return metrics.Int64CounterForTest(t, "x"), errors.New("arbitrary")
			},
		}

		provider, err := NewProvider(&Config{APIKey: "test-key"}, nil, nil, mp)
		must.Error(t, err)
		must.Nil(t, provider)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating error counter", func(t *testing.T) {
		t.Parallel()

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				switch counterName {
				case name + "_requests":
					return metrics.Int64CounterForTest(t, "x"), nil
				case name + "_errors":
					return metrics.Int64CounterForTest(t, "x"), errors.New("arbitrary")
				}
				t.Fatalf("unexpected NewInt64Counter call: %q", counterName)
				return nil, nil
			},
		}

		provider, err := NewProvider(&Config{APIKey: "test-key"}, nil, nil, mp)
		must.Error(t, err)
		must.Nil(t, provider)

		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating latency histogram", func(t *testing.T) {
		t.Parallel()

		noopMP := metricsnoop.NewMetricsProvider()
		h, histErr := noopMP.NewFloat64Histogram("test")
		must.NoError(t, histErr)

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metrics.Int64CounterForTest(t, "x"), nil
			},
			NewFloat64HistogramFunc: func(histName string, _ ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
				test.EqOp(t, name+"_latency_ms", histName)
				return h, errors.New("arbitrary")
			},
		}

		provider, err := NewProvider(&Config{APIKey: "test-key"}, nil, nil, mp)
		must.Error(t, err)
		must.Nil(t, provider)

		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
		test.SliceLen(t, 1, mp.NewFloat64HistogramCalls())
	})
}

func TestAnthropicProvider_Completion(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			must.EqOp(t, "/v1/messages", r.URL.Path)
			must.EqOp(t, http.MethodPost, r.Method)
			w.Header().Set("Content-Type", "application/json")
			must.NoError(t, json.NewEncoder(w).Encode(anthropicMessageResponse("Hello from Claude mock!")))
		}))
		t.Cleanup(ts.Close)

		provider, obs := newRecordingProvider(t, &Config{
			APIKey:  "test-key",
			BaseURL: ts.URL,
		})

		ctx := t.Context()
		result, err := provider.Completion(ctx, llm.CompletionParams{
			Model: "claude-sonnet-4-20250514",
			Messages: []llm.Message{
				{Role: "user", Content: "Hello"},
			},
		})
		must.NoError(t, err)
		must.NotNil(t, result)
		must.EqOp(t, "Hello from Claude mock!", result.Content)

		obs.ObservedOperationWithData(t, map[string]any{
			"llm.model": "claude-sonnet-4-20250514",
		})
	})

	T.Run("uses default model when not specified", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			must.NoError(t, json.NewEncoder(w).Encode(anthropicMessageResponse("Hi there!")))
		}))
		t.Cleanup(ts.Close)

		provider, err := NewProvider(&Config{
			APIKey:       "test-key",
			BaseURL:      ts.URL,
			DefaultModel: "claude-sonnet-4",
		}, nil, nil, nil)
		must.NoError(t, err)

		ctx := t.Context()
		result, err := provider.Completion(ctx, llm.CompletionParams{
			Messages: []llm.Message{{Role: "user", Content: "Hi"}},
		})
		must.NoError(t, err)
		must.EqOp(t, "Hi there!", result.Content)
	})

	T.Run("with API error", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"server error"}}`))
		}))
		t.Cleanup(ts.Close)

		provider, obs := newRecordingProvider(t, &Config{
			APIKey:  "test-key",
			BaseURL: ts.URL,
		})

		ctx := t.Context()
		result, err := provider.Completion(ctx, llm.CompletionParams{
			Model:    "claude-sonnet-4-20250514",
			Messages: []llm.Message{{Role: "user", Content: "Hi"}},
		})
		must.Error(t, err)
		must.Nil(t, result)

		// Even though the request failed, the values must still have been observed,
		// and the failure itself recorded on the operation.
		op := obs.ObservedOperationWithData(t, map[string]any{
			"llm.model": "claude-sonnet-4-20250514",
		})
		must.SliceLen(t, 1, op.Errors)
	})
}

package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v4/embeddings"
	"github.com/primandproper/platform-go/v4/observability"
	loggingnoop "github.com/primandproper/platform-go/v4/observability/logging/noop"
	"github.com/primandproper/platform-go/v4/observability/tracing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newRecordingEmbedder builds an embedder with a RecordingObserver swapped in, so
// a test can both drive GenerateEmbedding and assert which fields it observed.
func newRecordingEmbedder(t *testing.T, cfg *Config) (*embedder, *observability.RecordingObserver) {
	t.Helper()

	emb, err := NewEmbedder(t.Context(), cfg, loggingnoop.NewLogger(), tracing.NewTracerForTest("test"))
	must.NoError(t, err)
	must.NotNil(t, emb)

	e, ok := emb.(*embedder)
	must.True(t, ok)

	obs := observability.NewRecordingObserver()
	e.o11y = obs

	return e, obs
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

type errReader struct{}

func (*errReader) Read([]byte) (int, error) { return 0, fmt.Errorf("read error") }
func (*errReader) Close() error             { return nil }

type errCloser struct{ io.Reader }

func (*errCloser) Close() error { return fmt.Errorf("close error") }

func TestNewEmbedder(T *testing.T) {
	T.Parallel()

	T.Run("with nil config", func(t *testing.T) {
		t.Parallel()

		emb, err := NewEmbedder(t.Context(), nil, loggingnoop.NewLogger(), tracing.NewTracerForTest("test"))
		must.Error(t, err)
		must.Nil(t, emb)
	})

	T.Run("with missing API key", func(t *testing.T) {
		t.Parallel()

		emb, err := NewEmbedder(t.Context(), &Config{}, loggingnoop.NewLogger(), tracing.NewTracerForTest("test"))
		must.Error(t, err)
		must.Nil(t, emb)
	})

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		emb, err := NewEmbedder(t.Context(), &Config{APIKey: "test-key"}, loggingnoop.NewLogger(), tracing.NewTracerForTest("test"))
		must.NoError(t, err)
		must.NotNil(t, emb)
	})

	T.Run("with timeout", func(t *testing.T) {
		t.Parallel()

		emb, err := NewEmbedder(t.Context(), &Config{
			APIKey:  "test-key",
			Timeout: 5 * time.Second,
		}, loggingnoop.NewLogger(), tracing.NewTracerForTest("test"))
		must.NoError(t, err)
		must.NotNil(t, emb)
	})
}

func TestEmbedder_GenerateEmbedding(T *testing.T) {
	T.Parallel()

	openAIEmbeddingResponse := map[string]any{
		"object": "list",
		"data": []map[string]any{
			{
				"object":    "embedding",
				"index":     0,
				"embedding": []float64{0.1, 0.2, 0.3},
			},
		},
		"model": "text-embedding-3-small",
		"usage": map[string]any{
			"prompt_tokens": 5,
			"total_tokens":  5,
		},
	}

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			must.EqOp(t, "/v1/embeddings", r.URL.Path)
			must.EqOp(t, http.MethodPost, r.Method)
			must.EqOp(t, "Bearer test-key", r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "application/json")
			must.NoError(t, json.NewEncoder(w).Encode(openAIEmbeddingResponse))
		}))
		t.Cleanup(ts.Close)

		emb, obs := newRecordingEmbedder(t, &Config{
			APIKey:  "test-key",
			BaseURL: ts.URL,
		})

		ctx := t.Context()
		result, err := emb.GenerateEmbedding(ctx, &embeddings.Input{
			Content: "hello world",
		})

		must.NoError(t, err)
		must.NotNil(t, result)
		test.EqOp(t, "hello world", result.SourceText)
		test.EqOp(t, "text-embedding-3-small", result.Model)
		test.EqOp(t, "openai", result.Provider)
		test.EqOp(t, 3, result.Dimensions)
		test.SliceLen(t, 3, result.Vector)
		test.False(t, result.GeneratedAt.IsZero())

		obs.ObservedOperationWithData(t, map[string]any{
			"embeddings.model": "text-embedding-3-small",
		})
	})

	T.Run("uses input model override", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var reqBody embeddingRequest
			must.NoError(t, json.NewDecoder(r.Body).Decode(&reqBody))
			must.EqOp(t, "text-embedding-3-large", reqBody.Model)
			w.Header().Set("Content-Type", "application/json")
			must.NoError(t, json.NewEncoder(w).Encode(openAIEmbeddingResponse))
		}))
		t.Cleanup(ts.Close)

		emb, err := NewEmbedder(t.Context(), &Config{
			APIKey:       "test-key",
			BaseURL:      ts.URL,
			DefaultModel: "text-embedding-3-small",
		}, loggingnoop.NewLogger(), tracing.NewTracerForTest("test"))
		must.NoError(t, err)

		ctx := t.Context()
		result, err := emb.GenerateEmbedding(ctx, &embeddings.Input{
			Content: "hello",
			Model:   "text-embedding-3-large",
		})

		must.NoError(t, err)
		must.NotNil(t, result)
	})

	T.Run("with non-200 response", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":{"message":"server error"}}`))
		}))
		t.Cleanup(ts.Close)

		emb, obs := newRecordingEmbedder(t, &Config{
			APIKey:  "test-key",
			BaseURL: ts.URL,
		})

		ctx := t.Context()
		result, err := emb.GenerateEmbedding(ctx, &embeddings.Input{
			Content: "hello",
		})

		must.Error(t, err)
		must.Nil(t, result)

		// Even though the request failed, the model must still have been observed,
		// and the failure recorded on the operation.
		op := obs.ObservedOperationWithData(t, map[string]any{
			"embeddings.model": "text-embedding-3-small",
		})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("with malformed JSON response", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{not json`))
		}))
		t.Cleanup(ts.Close)

		emb, err := NewEmbedder(t.Context(), &Config{
			APIKey:  "test-key",
			BaseURL: ts.URL,
		}, loggingnoop.NewLogger(), tracing.NewTracerForTest("test"))
		must.NoError(t, err)

		ctx := t.Context()
		result, err := emb.GenerateEmbedding(ctx, &embeddings.Input{
			Content: "hello",
		})

		must.Error(t, err)
		must.Nil(t, result)
	})

	T.Run("with empty data response", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			must.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]any{},
			}))
		}))
		t.Cleanup(ts.Close)

		emb, err := NewEmbedder(t.Context(), &Config{
			APIKey:  "test-key",
			BaseURL: ts.URL,
		}, loggingnoop.NewLogger(), tracing.NewTracerForTest("test"))
		must.NoError(t, err)

		ctx := t.Context()
		result, err := emb.GenerateEmbedding(ctx, &embeddings.Input{
			Content: "hello",
		})

		must.Error(t, err)
		must.Nil(t, result)
	})

	T.Run("with connection error", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		ts.Close()

		emb, err := NewEmbedder(t.Context(), &Config{
			APIKey:  "test-key",
			BaseURL: ts.URL,
		}, loggingnoop.NewLogger(), tracing.NewTracerForTest("test"))
		must.NoError(t, err)

		ctx := t.Context()
		result, err := emb.GenerateEmbedding(ctx, &embeddings.Input{
			Content: "hello",
		})

		must.Error(t, err)
		must.Nil(t, result)
	})

	T.Run("uses config default model", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var reqBody embeddingRequest
			must.NoError(t, json.NewDecoder(r.Body).Decode(&reqBody))
			must.EqOp(t, "text-embedding-3-large", reqBody.Model)
			w.Header().Set("Content-Type", "application/json")
			must.NoError(t, json.NewEncoder(w).Encode(openAIEmbeddingResponse))
		}))
		t.Cleanup(ts.Close)

		emb, err := NewEmbedder(t.Context(), &Config{
			APIKey:       "test-key",
			BaseURL:      ts.URL,
			DefaultModel: "text-embedding-3-large",
		}, loggingnoop.NewLogger(), tracing.NewTracerForTest("test"))
		must.NoError(t, err)

		ctx := t.Context()
		result, err := emb.GenerateEmbedding(ctx, &embeddings.Input{
			Content: "hello",
		})

		must.NoError(t, err)
		must.NotNil(t, result)
		test.EqOp(t, "text-embedding-3-large", result.Model)
	})

	T.Run("with default base URL", func(t *testing.T) {
		t.Parallel()

		e := &embedder{
			cfg:  &Config{APIKey: "test-key"},
			o11y: observability.NewObserverWithTracer(providerName, loggingnoop.NewLogger(), tracing.NewTracerForTest("test")),
			client: &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					test.StrContains(t, r.URL.String(), defaultBaseURL)
					body := `{"data":[{"embedding":[0.1,0.2]}]}`
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(body)),
					}, nil
				}),
			},
		}

		result, err := e.GenerateEmbedding(t.Context(), &embeddings.Input{Content: "hello"})

		must.NoError(t, err)
		must.NotNil(t, result)
	})

	T.Run("with request building error", func(t *testing.T) {
		t.Parallel()

		e := &embedder{
			cfg:    &Config{APIKey: "test-key", BaseURL: string([]byte{0x7f})},
			o11y:   observability.NewObserverWithTracer(providerName, loggingnoop.NewLogger(), tracing.NewTracerForTest("test")),
			client: &http.Client{},
		}

		result, err := e.GenerateEmbedding(t.Context(), &embeddings.Input{Content: "hello"})

		must.Error(t, err)
		must.Nil(t, result)
	})

	T.Run("with response body close error", func(t *testing.T) {
		t.Parallel()

		body := `{"data":[{"embedding":[0.1,0.2]}]}`
		e := &embedder{
			cfg:  &Config{APIKey: "test-key", BaseURL: "http://localhost"},
			o11y: observability.NewObserverWithTracer(providerName, loggingnoop.NewLogger(), tracing.NewTracerForTest("test")),
			client: &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       &errCloser{Reader: strings.NewReader(body)},
					}, nil
				}),
			},
		}

		result, err := e.GenerateEmbedding(t.Context(), &embeddings.Input{Content: "hello"})

		must.NoError(t, err)
		must.NotNil(t, result)
	})

	T.Run("with error reading error response body", func(t *testing.T) {
		t.Parallel()

		e := &embedder{
			cfg:  &Config{APIKey: "test-key", BaseURL: "http://localhost"},
			o11y: observability.NewObserverWithTracer(providerName, loggingnoop.NewLogger(), tracing.NewTracerForTest("test")),
			client: &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusInternalServerError,
						Body:       &errReader{},
					}, nil
				}),
			},
		}

		result, err := e.GenerateEmbedding(t.Context(), &embeddings.Input{Content: "hello"})

		must.Error(t, err)
		must.Nil(t, result)
	})

	T.Run("returns an error on nil input", func(t *testing.T) {
		t.Parallel()

		e, _ := newRecordingEmbedder(t, &Config{APIKey: "test-key"})

		result, err := e.GenerateEmbedding(t.Context(), nil)

		test.ErrorIs(t, err, embeddings.ErrNilInput)
		test.Nil(t, result)
	})
}

package cohere

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/embeddings"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	"github.com/primandproper/platform-go/observability/tracing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

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

	cohereEmbeddingResponse := map[string]any{
		"id": "emb-test",
		"embeddings": map[string]any{
			"float": [][]float64{
				{0.1, 0.2, 0.3, 0.4, 0.5},
			},
		},
		"texts": []string{"hello world"},
	}

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			must.EqOp(t, "/v2/embed", r.URL.Path)
			must.EqOp(t, http.MethodPost, r.Method)
			must.EqOp(t, "Bearer test-key", r.Header.Get("Authorization"))
			w.Header().Set("Content-Type", "application/json")
			must.NoError(t, json.NewEncoder(w).Encode(cohereEmbeddingResponse))
		}))
		t.Cleanup(ts.Close)

		emb, err := NewEmbedder(t.Context(), &Config{
			APIKey:  "test-key",
			BaseURL: ts.URL,
		}, loggingnoop.NewLogger(), tracing.NewTracerForTest("test"))
		must.NoError(t, err)

		ctx := t.Context()
		result, err := emb.GenerateEmbedding(ctx, &embeddings.Input{
			Content: "hello world",
		})

		must.NoError(t, err)
		must.NotNil(t, result)
		test.EqOp(t, "hello world", result.SourceText)
		test.EqOp(t, "embed-english-v3.0", result.Model)
		test.EqOp(t, "cohere", result.Provider)
		test.EqOp(t, 5, result.Dimensions)
		test.SliceLen(t, 5, result.Vector)
		test.False(t, result.GeneratedAt.IsZero())
	})

	T.Run("uses input model override", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var reqBody embeddingRequest
			must.NoError(t, json.NewDecoder(r.Body).Decode(&reqBody))
			must.EqOp(t, "embed-multilingual-v3.0", reqBody.Model)
			w.Header().Set("Content-Type", "application/json")
			must.NoError(t, json.NewEncoder(w).Encode(cohereEmbeddingResponse))
		}))
		t.Cleanup(ts.Close)

		emb, err := NewEmbedder(t.Context(), &Config{
			APIKey:       "test-key",
			BaseURL:      ts.URL,
			DefaultModel: "embed-english-v3.0",
		}, loggingnoop.NewLogger(), tracing.NewTracerForTest("test"))
		must.NoError(t, err)

		ctx := t.Context()
		result, err := emb.GenerateEmbedding(ctx, &embeddings.Input{
			Content: "hello",
			Model:   "embed-multilingual-v3.0",
		})

		must.NoError(t, err)
		must.NotNil(t, result)
	})

	T.Run("with non-200 response", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"message":"invalid api token"}`))
		}))
		t.Cleanup(ts.Close)

		emb, err := NewEmbedder(t.Context(), &Config{
			APIKey:  "bad-key",
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

	T.Run("with empty embeddings response", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			must.NoError(t, json.NewEncoder(w).Encode(map[string]any{
				"embeddings": map[string]any{
					"float": [][]float64{},
				},
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
			must.EqOp(t, "embed-multilingual-v3.0", reqBody.Model)
			w.Header().Set("Content-Type", "application/json")
			must.NoError(t, json.NewEncoder(w).Encode(cohereEmbeddingResponse))
		}))
		t.Cleanup(ts.Close)

		emb, err := NewEmbedder(t.Context(), &Config{
			APIKey:       "test-key",
			BaseURL:      ts.URL,
			DefaultModel: "embed-multilingual-v3.0",
		}, loggingnoop.NewLogger(), tracing.NewTracerForTest("test"))
		must.NoError(t, err)

		ctx := t.Context()
		result, err := emb.GenerateEmbedding(ctx, &embeddings.Input{
			Content: "hello",
		})

		must.NoError(t, err)
		must.NotNil(t, result)
		test.EqOp(t, "embed-multilingual-v3.0", result.Model)
	})

	T.Run("with default base URL", func(t *testing.T) {
		t.Parallel()

		e := &embedder{
			cfg:    &Config{APIKey: "test-key"},
			logger: loggingnoop.NewLogger(),
			tracer: tracing.NewTracerForTest("test"),
			client: &http.Client{
				Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
					test.StrContains(t, r.URL.String(), defaultBaseURL)
					body := `{"embeddings":{"float":[[0.1,0.2]]}}`
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
			logger: loggingnoop.NewLogger(),
			tracer: tracing.NewTracerForTest("test"),
			client: &http.Client{},
		}

		result, err := e.GenerateEmbedding(t.Context(), &embeddings.Input{Content: "hello"})

		must.Error(t, err)
		must.Nil(t, result)
	})

	T.Run("with response body close error", func(t *testing.T) {
		t.Parallel()

		body := `{"embeddings":{"float":[[0.1,0.2]]}}`
		e := &embedder{
			cfg:    &Config{APIKey: "test-key", BaseURL: "http://localhost"},
			logger: loggingnoop.NewLogger(),
			tracer: tracing.NewTracerForTest("test"),
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
			cfg:    &Config{APIKey: "test-key", BaseURL: "http://localhost"},
			logger: loggingnoop.NewLogger(),
			tracer: tracing.NewTracerForTest("test"),
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
}

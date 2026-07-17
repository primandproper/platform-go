package ollama

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v5/embeddings"
	"github.com/primandproper/platform-go/v5/observability"
	loggingnoop "github.com/primandproper/platform-go/v5/observability/logging/noop"
	"github.com/primandproper/platform-go/v5/observability/tracing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

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

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		emb, err := NewEmbedder(t.Context(), &Config{}, loggingnoop.NewLogger(), tracing.NewTracerForTest("test"))
		must.NoError(t, err)
		must.NotNil(t, emb)
	})

	T.Run("with custom base URL", func(t *testing.T) {
		t.Parallel()

		emb, err := NewEmbedder(t.Context(), &Config{
			BaseURL: "http://custom:11434",
		}, loggingnoop.NewLogger(), tracing.NewTracerForTest("test"))
		must.NoError(t, err)
		must.NotNil(t, emb)
	})

	T.Run("with timeout", func(t *testing.T) {
		t.Parallel()

		emb, err := NewEmbedder(t.Context(), &Config{
			Timeout: 5 * time.Second,
		}, loggingnoop.NewLogger(), tracing.NewTracerForTest("test"))
		must.NoError(t, err)
		must.NotNil(t, emb)
	})
}

func TestEmbedder_GenerateEmbedding(T *testing.T) {
	T.Parallel()

	ollamaEmbeddingResponse := map[string]any{
		"embeddings": [][]float64{
			{0.1, 0.2, 0.3, 0.4},
		},
	}

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			must.EqOp(t, "/api/embed", r.URL.Path)
			must.EqOp(t, http.MethodPost, r.Method)
			w.Header().Set("Content-Type", "application/json")
			must.NoError(t, json.NewEncoder(w).Encode(ollamaEmbeddingResponse))
		}))
		t.Cleanup(ts.Close)

		emb, obs := newRecordingEmbedder(t, &Config{BaseURL: ts.URL})

		ctx := t.Context()
		result, err := emb.GenerateEmbedding(ctx, &embeddings.Input{
			Content: "hello world",
		})

		must.NoError(t, err)
		must.NotNil(t, result)
		test.EqOp(t, "hello world", result.SourceText)
		test.EqOp(t, "nomic-embed-text", result.Model)
		test.EqOp(t, "ollama", result.Provider)
		test.EqOp(t, 4, result.Dimensions)
		test.SliceLen(t, 4, result.Vector)
		test.False(t, result.GeneratedAt.IsZero())

		obs.ObservedOperationWithData(t, map[string]any{
			"embedding.model": "nomic-embed-text",
		})
	})

	T.Run("uses input model override", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var reqBody embeddingRequest
			must.NoError(t, json.NewDecoder(r.Body).Decode(&reqBody))
			must.EqOp(t, "mxbai-embed-large", reqBody.Model)
			w.Header().Set("Content-Type", "application/json")
			must.NoError(t, json.NewEncoder(w).Encode(ollamaEmbeddingResponse))
		}))
		t.Cleanup(ts.Close)

		emb, err := NewEmbedder(t.Context(), &Config{
			BaseURL:      ts.URL,
			DefaultModel: "nomic-embed-text",
		}, loggingnoop.NewLogger(), tracing.NewTracerForTest("test"))
		must.NoError(t, err)

		ctx := t.Context()
		result, err := emb.GenerateEmbedding(ctx, &embeddings.Input{
			Content: "hello",
			Model:   "mxbai-embed-large",
		})

		must.NoError(t, err)
		must.NotNil(t, result)
	})

	T.Run("with non-200 response", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error":"server error"}`))
		}))
		t.Cleanup(ts.Close)

		emb, obs := newRecordingEmbedder(t, &Config{BaseURL: ts.URL})

		ctx := t.Context()
		result, err := emb.GenerateEmbedding(ctx, &embeddings.Input{
			Content: "hello",
		})

		must.Error(t, err)
		must.Nil(t, result)

		// Even though the call failed, the model must still have been observed,
		// and the failure itself recorded on the operation.
		op := obs.ObservedOperationWithData(t, map[string]any{
			"embedding.model": "nomic-embed-text",
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
				"embeddings": [][]float64{},
			}))
		}))
		t.Cleanup(ts.Close)

		emb, err := NewEmbedder(t.Context(), &Config{
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
			must.EqOp(t, "mxbai-embed-large", reqBody.Model)
			w.Header().Set("Content-Type", "application/json")
			must.NoError(t, json.NewEncoder(w).Encode(ollamaEmbeddingResponse))
		}))
		t.Cleanup(ts.Close)

		emb, err := NewEmbedder(t.Context(), &Config{
			BaseURL:      ts.URL,
			DefaultModel: "mxbai-embed-large",
		}, loggingnoop.NewLogger(), tracing.NewTracerForTest("test"))
		must.NoError(t, err)

		ctx := t.Context()
		result, err := emb.GenerateEmbedding(ctx, &embeddings.Input{
			Content: "hello",
		})

		must.NoError(t, err)
		must.NotNil(t, result)
		test.EqOp(t, "mxbai-embed-large", result.Model)
	})

	T.Run("with request building error", func(t *testing.T) {
		t.Parallel()

		e := &embedder{
			cfg:    &Config{BaseURL: string([]byte{0x7f})},
			o11y:   observability.NewObserverForTest("test"),
			client: &http.Client{},
		}

		result, err := e.GenerateEmbedding(t.Context(), &embeddings.Input{Content: "hello"})

		must.Error(t, err)
		must.Nil(t, result)
	})

	T.Run("with response body close error", func(t *testing.T) {
		t.Parallel()

		body := `{"embeddings":[[0.1,0.2]]}`
		e := &embedder{
			cfg:  &Config{BaseURL: "http://localhost"},
			o11y: observability.NewObserverForTest("test"),
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
			cfg:  &Config{BaseURL: "http://localhost"},
			o11y: observability.NewObserverForTest("test"),
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

		e, _ := newRecordingEmbedder(t, &Config{})

		result, err := e.GenerateEmbedding(t.Context(), nil)

		test.ErrorIs(t, err, embeddings.ErrNilInput)
		test.Nil(t, result)
	})
}

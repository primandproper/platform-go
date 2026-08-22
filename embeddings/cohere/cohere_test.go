package cohere

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/embeddings"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/metrics/metricstest"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

// newRecordingEmbedder builds an embedder via the public constructor, then swaps
// in a RecordingObserver so a test can both drive GenerateEmbedding and assert
// which fields it observed.
func newRecordingEmbedder(t *testing.T, cfg *Config) (*Embedder, *observability.RecordingObserver) {
	t.Helper()

	e, err := NewEmbedder(t.Context(), cfg, WithTracerProvider(tracingnoop.NewTracerProvider()))
	must.NoError(t, err)
	must.NotNil(t, e)

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

		e, err := NewEmbedder(t.Context(), nil, WithTracerProvider(tracingnoop.NewTracerProvider()))
		must.Error(t, err)
		must.Nil(t, e)
	})

	T.Run("with missing API key", func(t *testing.T) {
		t.Parallel()

		e, err := NewEmbedder(t.Context(), &Config{}, WithTracerProvider(tracingnoop.NewTracerProvider()))
		must.Error(t, err)
		must.Nil(t, e)
	})

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		e, err := NewEmbedder(t.Context(), &Config{APIKey: "test-key"}, WithTracerProvider(tracingnoop.NewTracerProvider()))
		must.NoError(t, err)
		must.NotNil(t, e)
	})

	T.Run("with timeout", func(t *testing.T) {
		t.Parallel()

		e, err := NewEmbedder(
			t.Context(),
			&Config{
				APIKey:  "test-key",
				Timeout: 5 * time.Second,
			},
			WithTracerProvider(tracingnoop.NewTracerProvider()),
		)
		must.NoError(t, err)
		must.NotNil(t, e)
	})
}

func TestEmbedder_GenerateEmbedding(T *testing.T) {
	T.Parallel()

	cohereEmbeddingResponse := map[string]any{
		"id": "e-test",
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

		e, obs := newRecordingEmbedder(t, &Config{
			APIKey:  "test-key",
			BaseURL: ts.URL,
		})

		ctx := t.Context()
		result, err := e.GenerateEmbedding(ctx, &embeddings.Input{
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

		obs.ObservedOperationWithData(t, map[string]any{
			keys.EmbeddingModelKey: "embed-english-v3.0",
		})
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

		e, err := NewEmbedder(
			t.Context(),
			&Config{
				APIKey:       "test-key",
				BaseURL:      ts.URL,
				DefaultModel: "embed-english-v3.0",
			},
			WithTracerProvider(tracingnoop.NewTracerProvider()),
		)
		must.NoError(t, err)

		ctx := t.Context()
		result, err := e.GenerateEmbedding(ctx, &embeddings.Input{
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

		e, obs := newRecordingEmbedder(t, &Config{
			APIKey:  "bad-key",
			BaseURL: ts.URL,
		})

		ctx := t.Context()
		result, err := e.GenerateEmbedding(ctx, &embeddings.Input{
			Content: "hello",
		})

		must.Error(t, err)
		must.Nil(t, result)

		// Even though the request failed, the values must still have been observed,
		// and the failure itself recorded on the operation.
		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.EmbeddingModelKey: "embed-english-v3.0",
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

		e, err := NewEmbedder(
			t.Context(),
			&Config{
				APIKey:  "test-key",
				BaseURL: ts.URL,
			},
			WithTracerProvider(tracingnoop.NewTracerProvider()),
		)
		must.NoError(t, err)

		ctx := t.Context()
		result, err := e.GenerateEmbedding(ctx, &embeddings.Input{
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

		e, err := NewEmbedder(
			t.Context(),
			&Config{
				APIKey:  "test-key",
				BaseURL: ts.URL,
			},
			WithTracerProvider(tracingnoop.NewTracerProvider()),
		)
		must.NoError(t, err)

		ctx := t.Context()
		result, err := e.GenerateEmbedding(ctx, &embeddings.Input{
			Content: "hello",
		})

		must.Error(t, err)
		must.Nil(t, result)
	})

	T.Run("with connection error", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		ts.Close()

		e, err := NewEmbedder(
			t.Context(),
			&Config{
				APIKey:  "test-key",
				BaseURL: ts.URL,
			},
			WithTracerProvider(tracingnoop.NewTracerProvider()),
		)
		must.NoError(t, err)

		ctx := t.Context()
		result, err := e.GenerateEmbedding(ctx, &embeddings.Input{
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

		e, err := NewEmbedder(
			t.Context(),
			&Config{
				APIKey:       "test-key",
				BaseURL:      ts.URL,
				DefaultModel: "embed-multilingual-v3.0",
			},
			WithTracerProvider(tracingnoop.NewTracerProvider()),
		)
		must.NoError(t, err)

		ctx := t.Context()
		result, err := e.GenerateEmbedding(ctx, &embeddings.Input{
			Content: "hello",
		})

		must.NoError(t, err)
		must.NotNil(t, result)
		test.EqOp(t, "embed-multilingual-v3.0", result.Model)
	})

	T.Run("with default base URL", func(t *testing.T) {
		t.Parallel()

		e := &Embedder{
			instruments: metricstest.OperationSet(t, "test"),
			cfg:         &Config{APIKey: "test-key"},
			o11y:        observability.NewObserverForTest("test"),
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

		e := &Embedder{
			instruments: metricstest.OperationSet(t, "test"),
			cfg:         &Config{APIKey: "test-key", BaseURL: string([]byte{0x7f})},
			o11y:        observability.NewObserverForTest("test"),
			client:      &http.Client{},
		}

		result, err := e.GenerateEmbedding(t.Context(), &embeddings.Input{Content: "hello"})

		must.Error(t, err)
		must.Nil(t, result)
	})

	T.Run("with response body close error", func(t *testing.T) {
		t.Parallel()

		body := `{"embeddings":{"float":[[0.1,0.2]]}}`
		e := &Embedder{
			instruments: metricstest.OperationSet(t, "test"),
			cfg:         &Config{APIKey: "test-key", BaseURL: "http://localhost"},
			o11y:        observability.NewObserverForTest("test"),
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

		e := &Embedder{
			instruments: metricstest.OperationSet(t, "test"),
			cfg:         &Config{APIKey: "test-key", BaseURL: "http://localhost"},
			o11y:        observability.NewObserverForTest("test"),
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

func TestNewEmbedder_InstrumentFailures(T *testing.T) {
	T.Parallel()

	T.Run("with error creating request counter", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, name+"_requests", counterName)

				return metricstest.Int64Counter(t, "x"), errors.New("arbitrary")
			},
		}

		e, err := NewEmbedder(t.Context(), &Config{APIKey: "test-key"}, WithMetricsProvider(mp))
		must.Error(t, err)
		must.Nil(t, e)
		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating error counter", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				if counterName == name+"_errors" {
					return metricstest.Int64Counter(t, "x"), errors.New("arbitrary")
				}

				return metricstest.Int64Counter(t, "x"), nil
			},
		}

		e, err := NewEmbedder(t.Context(), &Config{APIKey: "test-key"}, WithMetricsProvider(mp))
		must.Error(t, err)
		must.Nil(t, e)
		test.SliceLen(t, 2, mp.NewInt64CounterCalls())
	})

	T.Run("with error creating latency histogram", func(t *testing.T) {
		t.Parallel()

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(_ string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return metricstest.Int64Counter(t, "x"), nil
			},
			NewFloat64HistogramFunc: func(histName string, _ ...metric.Float64HistogramOption) (metrics.Float64Histogram, error) {
				test.EqOp(t, name+"_latency_ms", histName)

				return metricstest.Float64Histogram(t, "x"), errors.New("arbitrary")
			},
		}

		e, err := NewEmbedder(t.Context(), &Config{APIKey: "test-key"}, WithMetricsProvider(mp))
		must.Error(t, err)
		must.Nil(t, e)
		test.SliceLen(t, 1, mp.NewFloat64HistogramCalls())
	})
}

func TestEmbedder_GenerateEmbeddings_Batch(T *testing.T) {
	T.Parallel()

	T.Run("no inputs makes no request", func(t *testing.T) {
		t.Parallel()

		e := &Embedder{
			instruments: metricstest.OperationSet(t, "test"),
			cfg:         &Config{},
			o11y:        observability.NewObserverForTest("test"),
			// A nil client would panic if a request were attempted, which is the
			// assertion: an empty batch must short-circuit before the round trip.
			client: nil,
		}

		results, err := e.GenerateEmbeddings(t.Context(), nil)
		must.NoError(t, err)
		test.SliceEmpty(t, results)
	})

	T.Run("a batch spanning two models is refused", func(t *testing.T) {
		t.Parallel()

		e := &Embedder{
			instruments: metricstest.OperationSet(t, "test"),
			cfg:         &Config{},
			o11y:        observability.NewObserverForTest("test"),
			client:      nil,
		}

		// Refused rather than split: one request embeds against one model, and
		// splitting would make the round-trip count depend on input ordering.
		results, err := e.GenerateEmbeddings(t.Context(), []*embeddings.Input{
			{Content: "first", Model: "model-a"},
			{Content: "second", Model: "model-b"},
		})
		must.Error(t, err)
		test.Nil(t, results)
	})

	T.Run("a batch spanning two purposes is refused", func(t *testing.T) {
		t.Parallel()

		e := &Embedder{
			instruments: metricstest.OperationSet(t, "test"),
			cfg:         &Config{},
			o11y:        observability.NewObserverForTest("test"),
			client:      nil,
		}

		// One request carries one input_type, so a batch that means two of them
		// cannot be honored — and honoring only the first would embed half the
		// batch on the wrong side of the space, silently.
		results, err := e.GenerateEmbeddings(t.Context(), []*embeddings.Input{
			{Content: "first", Purpose: embeddings.PurposeDocument},
			{Content: "second", Purpose: embeddings.PurposeQuery},
		})
		must.Error(t, err)
		test.Nil(t, results)
	})
}

func TestEmbedder_Purpose(T *testing.T) {
	T.Parallel()

	cohereEmbeddingResponse := map[string]any{
		"embeddings": map[string]any{
			"float": [][]float64{
				{0.1, 0.2, 0.3},
			},
		},
	}

	// A query must go out as "search_query" to land near the documents that
	// answer it. Sending everything as a document is a ranking regression with
	// no error to catch it, so the wire value is what gets asserted here.
	for _, tc := range []struct {
		name              string
		expectedInputType string
		purpose           embeddings.Purpose
	}{
		{name: "unset input is a document", purpose: embeddings.PurposeDocument, expectedInputType: inputTypeDocument},
		{name: "query input is a query", purpose: embeddings.PurposeQuery, expectedInputType: inputTypeQuery},
	} {
		T.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var reqBody embeddingRequest
				must.NoError(t, json.NewDecoder(r.Body).Decode(&reqBody))
				must.EqOp(t, tc.expectedInputType, reqBody.InputType)
				w.Header().Set("Content-Type", "application/json")
				must.NoError(t, json.NewEncoder(w).Encode(cohereEmbeddingResponse))
			}))
			t.Cleanup(ts.Close)

			e, obs := newRecordingEmbedder(t, &Config{APIKey: "test-key", BaseURL: ts.URL})

			result, err := e.GenerateEmbedding(t.Context(), &embeddings.Input{
				Content: "hello",
				Purpose: tc.purpose,
			})
			must.NoError(t, err)
			must.NotNil(t, result)

			obs.ObservedOperationWithData(t, map[string]any{
				"embedding.purpose": tc.purpose.String(),
			})
		})
	}

	T.Run("the zero value matches PurposeDocument", func(t *testing.T) {
		t.Parallel()

		// Existing callers set no purpose at all, and must keep getting the
		// document side they were getting before the field existed.
		var zero embeddings.Purpose
		test.EqOp(t, embeddings.PurposeDocument, zero)
	})

	T.Run("an undefined purpose is refused", func(t *testing.T) {
		t.Parallel()

		e := &Embedder{
			instruments: metricstest.OperationSet(t, "test"),
			cfg:         &Config{APIKey: "test-key"},
			o11y:        observability.NewObserverForTest("test"),
			// A nil client would panic on a request, which is the assertion:
			// an unrecognized purpose fails before the round trip.
			client: nil,
		}

		result, err := e.GenerateEmbedding(t.Context(), &embeddings.Input{
			Content: "hello",
			Purpose: embeddings.Purpose(math.MaxUint8),
		})
		test.ErrorIs(t, err, embeddings.ErrUnknownPurpose)
		test.Nil(t, result)
	})
}

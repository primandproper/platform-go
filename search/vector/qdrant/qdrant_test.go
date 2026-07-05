package qdrant

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v4/circuitbreaking"
	cbmock "github.com/primandproper/platform-go/v4/circuitbreaking/mock"
	cbnoop "github.com/primandproper/platform-go/v4/circuitbreaking/noop"
	platformerrors "github.com/primandproper/platform-go/v4/errors"
	"github.com/primandproper/platform-go/v4/identifiers"
	"github.com/primandproper/platform-go/v4/observability"
	"github.com/primandproper/platform-go/v4/observability/keys"
	vectorsearch "github.com/primandproper/platform-go/v4/search/vector"
	"github.com/primandproper/platform-go/v4/testutils/containers"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

const qdrantImage = "qdrant/qdrant:v1.13.0"

type doc struct {
	Kind  string `json:"kind"`
	Title string `json:"title"`
}

// qdrantStub is a configurable httptest handler for qdrant REST endpoints.
type qdrantStub struct {
	pointsSearchBody       string
	collectionGetStatus    int
	collectionPutStatus    int
	pointsPutStatus        int
	pointsDeleteStatus     int
	collectionDeleteStatus int
	pointsSearchStatus     int
}

func (s *qdrantStub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	okBody := `{"result":true,"status":"ok","time":0}`
	path := r.URL.Path

	switch {
	case r.Method == http.MethodGet && strings.Contains(path, "/collections/"):
		status := s.collectionGetStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(okBody))
		}

	case r.Method == http.MethodPut && strings.Contains(path, "/points"):
		status := s.pointsPutStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(okBody))
		}

	case r.Method == http.MethodPut && strings.Contains(path, "/collections/"):
		status := s.collectionPutStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(okBody))
		}

	case r.Method == http.MethodPost && strings.Contains(path, "/points/delete"):
		status := s.pointsDeleteStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(okBody))
		}

	case r.Method == http.MethodDelete && strings.Contains(path, "/collections/"):
		status := s.collectionDeleteStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(okBody))
		}

	case r.Method == http.MethodPost && strings.Contains(path, "/points/search"):
		status := s.pointsSearchStatus
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		body := s.pointsSearchBody
		if body == "" {
			body = `{"result":[]}`
		}
		_, _ = w.Write([]byte(body))

	default:
		http.Error(w, fmt.Sprintf("unexpected %s %s", r.Method, path), http.StatusBadRequest)
	}
}

// buildStubIndex creates an indexManager backed by an httptest server using the given stub.
// The server is closed when the test finishes.
func buildStubIndex(t *testing.T, stub *qdrantStub, cb circuitbreaking.CircuitBreaker) *indexManager[doc] {
	t.Helper()

	srv := httptest.NewServer(stub)
	t.Cleanup(srv.Close)

	if cb == nil {
		cb = cbnoop.NewCircuitBreaker()
	}

	idx, err := ProvideIndex[doc](
		t.Context(),
		nil, nil, nil,
		&Config{BaseURL: srv.URL, Dimension: 3, Metric: vectorsearch.DistanceCosine, Timeout: time.Second},
		"test",
		cb,
	)
	must.NoError(t, err)

	return idx.(*indexManager[doc])
}

// buildRecordingIndex builds a stub-backed indexManager with a RecordingObserver
// swapped in, so a test can assert which fields an operation observed. The
// returned observer captures the LAST operation Begun, so drive a single method
// per recording index.
func buildRecordingIndex(t *testing.T, stub *qdrantStub, cb circuitbreaking.CircuitBreaker) (*indexManager[doc], *observability.RecordingObserver) {
	t.Helper()

	idx := buildStubIndex(t, stub, cb)

	obs := observability.NewRecordingObserver()
	idx.o11y = obs

	return idx, obs
}

// --------- unit tests (no container required) ---------

func TestMetricToDistance(T *testing.T) {
	T.Parallel()

	cases := []struct {
		metric vectorsearch.DistanceMetric
		want   string
	}{
		{vectorsearch.DistanceCosine, "Cosine"},
		{vectorsearch.DistanceDotProduct, "Dot"},
		{vectorsearch.DistanceEuclidean, "Euclid"},
	}
	for _, c := range cases {
		T.Run(string(c.metric), func(t *testing.T) {
			t.Parallel()
			got, err := metricToDistance(c.metric)
			must.NoError(t, err)
			test.EqOp(t, c.want, got)
		})
	}

	T.Run("invalid", func(t *testing.T) {
		t.Parallel()
		_, err := metricToDistance("nonsense")
		must.ErrorIs(t, err, vectorsearch.ErrInvalidMetric)
	})
}

func TestStringifyID(T *testing.T) {
	T.Parallel()

	T.Run("string", func(t *testing.T) {
		t.Parallel()
		s, err := stringifyID("abc")
		must.NoError(t, err)
		test.EqOp(t, "abc", s)
	})

	T.Run("float", func(t *testing.T) {
		t.Parallel()
		s, err := stringifyID(float64(42))
		must.NoError(t, err)
		test.EqOp(t, "42", s)
	})

	T.Run("large float does not use scientific notation", func(t *testing.T) {
		t.Parallel()
		// JSON numbers decode into float64; 10000000 must not become "1e+07".
		s, err := stringifyID(float64(10000000))
		must.NoError(t, err)
		test.EqOp(t, "10000000", s)
	})

	T.Run("number", func(t *testing.T) {
		t.Parallel()
		s, err := stringifyID(json.Number("17"))
		must.NoError(t, err)
		test.EqOp(t, "17", s)
	})

	T.Run("unsupported", func(t *testing.T) {
		t.Parallel()
		_, err := stringifyID(true)
		must.Error(t, err)
	})
}

func TestPointID(T *testing.T) {
	T.Parallel()

	T.Run("unsigned integer becomes a uint64", func(t *testing.T) {
		t.Parallel()
		got, err := pointID("10000000")
		must.NoError(t, err)
		test.EqOp(t, uint64(10000000), got.(uint64))
	})

	T.Run("uuid is passed through as a string", func(t *testing.T) {
		t.Parallel()
		got, err := pointID("11111111-1111-1111-1111-111111111111")
		must.NoError(t, err)
		test.EqOp(t, "11111111-1111-1111-1111-111111111111", got.(string))
	})

	T.Run("empty id is rejected", func(t *testing.T) {
		t.Parallel()
		_, err := pointID("")
		must.ErrorIs(t, err, platformerrors.ErrInvalidIDProvided)
	})

	T.Run("xid identifier is rejected up front", func(t *testing.T) {
		t.Parallel()
		// identifiers.New() returns a 20-char xid, which qdrant cannot accept.
		_, err := pointID(identifiers.New())
		must.ErrorIs(t, err, ErrUnsupportedID)
	})

	T.Run("arbitrary string is rejected", func(t *testing.T) {
		t.Parallel()
		_, err := pointID("not-a-valid-id")
		must.ErrorIs(t, err, ErrUnsupportedID)
	})
}

func TestScoreToDistance(T *testing.T) {
	T.Parallel()

	T.Run("cosine similarity inverts to a [0,2] distance", func(t *testing.T) {
		t.Parallel()
		// A closer match (higher score) must yield a lower distance.
		test.InDelta(t, 0.05, float64(scoreToDistance("Cosine", 0.95)), 0.0001)
		test.InDelta(t, 0.2, float64(scoreToDistance("Cosine", 0.8)), 0.0001)
		test.True(t, scoreToDistance("Cosine", 0.95) < scoreToDistance("Cosine", 0.8))
	})

	T.Run("dot product negates so higher similarity is lower distance", func(t *testing.T) {
		t.Parallel()
		test.InDelta(t, -0.9, float64(scoreToDistance("Dot", 0.9)), 0.0001)
		test.True(t, scoreToDistance("Dot", 0.9) < scoreToDistance("Dot", 0.1))
	})

	T.Run("euclid is already a distance and is left unchanged", func(t *testing.T) {
		t.Parallel()
		test.InDelta(t, 1.5, float64(scoreToDistance("Euclid", 1.5)), 0.0001)
		test.True(t, scoreToDistance("Euclid", 0.2) < scoreToDistance("Euclid", 1.5))
	})
}

func TestUnmarshalPayload(T *testing.T) {
	T.Parallel()

	T.Run("nil round-trip", func(t *testing.T) {
		t.Parallel()
		out, err := unmarshalPayload[doc](nil)
		must.NoError(t, err)
		test.Nil(t, out)
	})

	T.Run("populated", func(t *testing.T) {
		t.Parallel()
		out, err := unmarshalPayload[doc](json.RawMessage(`{"kind":"doc","title":"hi"}`))
		must.NoError(t, err)
		must.NotNil(t, out)
		test.EqOp(t, "doc", out.Kind)
		test.EqOp(t, "hi", out.Title)
	})

	T.Run("null", func(t *testing.T) {
		t.Parallel()
		out, err := unmarshalPayload[doc](json.RawMessage("null"))
		must.NoError(t, err)
		test.Nil(t, out)
	})

	T.Run("invalid JSON", func(t *testing.T) {
		t.Parallel()
		_, err := unmarshalPayload[doc](json.RawMessage(`{not valid`))
		must.Error(t, err)
	})
}

func TestPayloadFromMetadata(T *testing.T) {
	T.Parallel()

	T.Run("nil metadata", func(t *testing.T) {
		t.Parallel()
		test.Nil(t, payloadFromMetadata[doc](nil))
	})

	T.Run("non-nil metadata", func(t *testing.T) {
		t.Parallel()
		d := &doc{Kind: "k", Title: "t"}
		result := payloadFromMetadata(d)
		must.NotNil(t, result)
		test.Eq[any](t, d, result)
	})
}

func TestWrapStatusError(T *testing.T) {
	T.Parallel()

	T.Run("wraps ErrUnexpectedStatus", func(t *testing.T) {
		t.Parallel()
		err := wrapStatusError(500, []byte("internal error"))
		must.ErrorIs(t, err, ErrUnexpectedStatus)
		test.StrContains(t, err.Error(), "500")
		test.StrContains(t, err.Error(), "internal error")
	})
}

func TestCollectionPath(T *testing.T) {
	T.Parallel()

	T.Run("no suffix", func(t *testing.T) {
		t.Parallel()
		im := &indexManager[doc]{baseURL: "http://localhost:6333", collection: "my_col"}
		test.EqOp(t, "http://localhost:6333/collections/my_col", im.collectionPath(""))
	})

	T.Run("with suffix", func(t *testing.T) {
		t.Parallel()
		im := &indexManager[doc]{baseURL: "http://localhost:6333", collection: "my_col"}
		test.EqOp(t, "http://localhost:6333/collections/my_col/points?wait=true", im.collectionPath("/points?wait=true"))
	})

	T.Run("collection name is URL-escaped", func(t *testing.T) {
		t.Parallel()
		im := &indexManager[doc]{baseURL: "http://localhost:6333", collection: "has space"}
		test.EqOp(t, "http://localhost:6333/collections/has%20space", im.collectionPath(""))
	})
}

func TestProvideIndex(T *testing.T) {
	T.Parallel()

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()
		_, err := ProvideIndex[doc](t.Context(), nil, nil, nil, nil, "test", cbnoop.NewCircuitBreaker())
		must.ErrorIs(t, err, vectorsearch.ErrNilConfig)
	})

	T.Run("empty collection", func(t *testing.T) {
		t.Parallel()
		_, err := ProvideIndex[doc](t.Context(), nil, nil, nil, &Config{
			BaseURL:   "http://example",
			Dimension: 3,
			Metric:    vectorsearch.DistanceCosine,
		}, "", cbnoop.NewCircuitBreaker())
		must.Error(t, err)
	})

	T.Run("invalid metric", func(t *testing.T) {
		t.Parallel()
		_, err := ProvideIndex[doc](t.Context(), nil, nil, nil, &Config{
			BaseURL:   "http://example",
			Dimension: 3,
			Metric:    "weird",
		}, "test", cbnoop.NewCircuitBreaker())
		must.Error(t, err)
	})

	T.Run("invalid dimension", func(t *testing.T) {
		t.Parallel()
		_, err := ProvideIndex[doc](t.Context(), nil, nil, nil, &Config{
			BaseURL:   "http://example",
			Dimension: 0,
			Metric:    vectorsearch.DistanceCosine,
		}, "test", cbnoop.NewCircuitBreaker())
		must.Error(t, err)
	})

	T.Run("invalid config missing base URL", func(t *testing.T) {
		t.Parallel()
		_, err := ProvideIndex[doc](t.Context(), nil, nil, nil, &Config{
			Dimension: 3,
			Metric:    vectorsearch.DistanceCosine,
		}, "test", cbnoop.NewCircuitBreaker())
		must.Error(t, err)
	})

	T.Run("collection already exists", func(t *testing.T) {
		t.Parallel()
		// GET returns 200 so ensureCollection skips creation
		stub := &qdrantStub{collectionGetStatus: http.StatusOK}
		srv := httptest.NewServer(stub)
		t.Cleanup(srv.Close)

		idx, err := ProvideIndex[doc](
			t.Context(), nil, nil, nil,
			&Config{BaseURL: srv.URL, Dimension: 3, Metric: vectorsearch.DistanceCosine, Timeout: time.Second},
			"test",
			cbnoop.NewCircuitBreaker(),
		)
		must.NoError(t, err)
		must.NotNil(t, idx)
	})

	T.Run("ensureCollection GET fails with unexpected status", func(t *testing.T) {
		t.Parallel()
		stub := &qdrantStub{collectionGetStatus: http.StatusInternalServerError}
		srv := httptest.NewServer(stub)
		t.Cleanup(srv.Close)

		_, err := ProvideIndex[doc](
			t.Context(), nil, nil, nil,
			&Config{BaseURL: srv.URL, Dimension: 3, Metric: vectorsearch.DistanceCosine, Timeout: time.Second},
			"test",
			cbnoop.NewCircuitBreaker(),
		)
		must.Error(t, err)
	})

	T.Run("ensureCollection PUT fails", func(t *testing.T) {
		t.Parallel()
		stub := &qdrantStub{
			collectionGetStatus: http.StatusNotFound,
			collectionPutStatus: http.StatusInternalServerError,
		}
		srv := httptest.NewServer(stub)
		t.Cleanup(srv.Close)

		_, err := ProvideIndex[doc](
			t.Context(), nil, nil, nil,
			&Config{BaseURL: srv.URL, Dimension: 3, Metric: vectorsearch.DistanceCosine, Timeout: time.Second},
			"test",
			cbnoop.NewCircuitBreaker(),
		)
		must.Error(t, err)
	})

	T.Run("ensureCollection unreachable server", func(t *testing.T) {
		t.Parallel()
		_, err := ProvideIndex[doc](
			t.Context(), nil, nil, nil,
			&Config{BaseURL: "http://127.0.0.1:1", Dimension: 3, Metric: vectorsearch.DistanceCosine, Timeout: 100 * time.Millisecond},
			"test",
			cbnoop.NewCircuitBreaker(),
		)
		must.Error(t, err)
	})

	T.Run("default timeout when zero", func(t *testing.T) {
		t.Parallel()
		stub := &qdrantStub{collectionGetStatus: http.StatusOK}
		srv := httptest.NewServer(stub)
		t.Cleanup(srv.Close)

		idx, err := ProvideIndex[doc](
			t.Context(), nil, nil, nil,
			&Config{BaseURL: srv.URL, Dimension: 3, Metric: vectorsearch.DistanceCosine, Timeout: 0},
			"test",
			cbnoop.NewCircuitBreaker(),
		)
		must.NoError(t, err)
		must.NotNil(t, idx)
	})

	T.Run("sets api key header", func(t *testing.T) {
		t.Parallel()
		var gotAPIKey string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAPIKey = r.Header.Get("api-key")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":true,"status":"ok","time":0}`))
		}))
		t.Cleanup(srv.Close)

		_, err := ProvideIndex[doc](
			t.Context(), nil, nil, nil,
			&Config{BaseURL: srv.URL, Dimension: 3, Metric: vectorsearch.DistanceCosine, APIKey: "secret", Timeout: time.Second},
			"test",
			cbnoop.NewCircuitBreaker(),
		)
		must.NoError(t, err)
		test.EqOp(t, "secret", gotAPIKey)
	})
}

// httptest-based test verifies the request shape we send to qdrant without needing a real qdrant.
func TestProvideIndex_StubsCollectionCreation(T *testing.T) {
	T.Parallel()

	var (
		gotMethod string
		gotPath   string
		gotBody   map[string]any
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/collections/stub"):
			w.WriteHeader(http.StatusNotFound)
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, "/collections/stub"):
			gotMethod = r.Method
			gotPath = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&gotBody)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":true,"status":"ok","time":0}`))
		default:
			http.Error(w, "unexpected", http.StatusBadRequest)
		}
	}))
	defer srv.Close()

	idx, err := ProvideIndex[doc](
		T.Context(),
		nil, nil, nil,
		&Config{BaseURL: srv.URL, Dimension: 3, Metric: vectorsearch.DistanceCosine, Timeout: time.Second},
		"stub",
		cbnoop.NewCircuitBreaker(),
	)
	must.NoError(T, err)
	must.NotNil(T, idx)

	test.EqOp(T, http.MethodPut, gotMethod)
	test.True(T, strings.HasSuffix(gotPath, "/collections/stub"))
	must.NotNil(T, gotBody)
	vectors, ok := gotBody["vectors"].(map[string]any)
	must.True(T, ok)
	test.Eq[any](T, float64(3), vectors["size"])
	test.Eq[any](T, "Cosine", vectors["distance"])
}

func TestUpsert(T *testing.T) {
	T.Parallel()

	T.Run("empty vectors is a no-op", func(t *testing.T) {
		t.Parallel()
		idx := buildStubIndex(t, &qdrantStub{}, nil)
		must.NoError(t, idx.Upsert(t.Context()))
	})

	T.Run("circuit breaker broken", func(t *testing.T) {
		t.Parallel()
		cb := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return true },
		}

		idx := buildStubIndex(t, &qdrantStub{}, cb)

		err := idx.Upsert(t.Context(), vectorsearch.Vector[doc]{ID: "a", Embedding: []float32{1, 0, 0}})
		must.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)

		must.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("rejects empty ID", func(t *testing.T) {
		t.Parallel()
		idx := buildStubIndex(t, &qdrantStub{}, nil)
		err := idx.Upsert(t.Context(), vectorsearch.Vector[doc]{ID: "", Embedding: []float32{1, 0, 0}})
		must.ErrorIs(t, err, platformerrors.ErrInvalidIDProvided)
	})

	T.Run("rejects empty embedding", func(t *testing.T) {
		t.Parallel()
		idx := buildStubIndex(t, &qdrantStub{}, nil)
		err := idx.Upsert(t.Context(), vectorsearch.Vector[doc]{ID: "1", Embedding: nil})
		must.ErrorIs(t, err, vectorsearch.ErrEmptyEmbedding)
	})

	T.Run("rejects wrong dimension", func(t *testing.T) {
		t.Parallel()
		idx := buildStubIndex(t, &qdrantStub{}, nil)
		err := idx.Upsert(t.Context(), vectorsearch.Vector[doc]{ID: "1", Embedding: []float32{1, 0}})
		must.ErrorIs(t, err, vectorsearch.ErrDimensionMismatch)
	})

	T.Run("rejects unsupported ID format", func(t *testing.T) {
		t.Parallel()
		idx := buildStubIndex(t, &qdrantStub{}, nil)
		// An xid — what identifiers.New() produces — is neither a uint nor a UUID, so
		// qdrant would reject it. We must fail up front rather than on a late 4xx.
		err := idx.Upsert(t.Context(), vectorsearch.Vector[doc]{ID: identifiers.New(), Embedding: []float32{1, 0, 0}})
		must.ErrorIs(t, err, ErrUnsupportedID)
	})

	T.Run("successful upsert", func(t *testing.T) {
		t.Parallel()
		idx, obs := buildRecordingIndex(t, &qdrantStub{}, nil)
		err := idx.Upsert(t.Context(),
			vectorsearch.Vector[doc]{ID: "1", Embedding: []float32{1, 0, 0}, Metadata: &doc{Kind: "doc", Title: "alpha"}},
			vectorsearch.Vector[doc]{ID: "11111111-1111-1111-1111-111111111111", Embedding: []float32{0, 1, 0}},
		)
		must.NoError(t, err)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.IndexNameKey: idx.collection,
		})
	})

	T.Run("server returns error status", func(t *testing.T) {
		t.Parallel()
		idx, obs := buildRecordingIndex(t, &qdrantStub{pointsPutStatus: http.StatusInternalServerError}, nil)
		err := idx.Upsert(t.Context(), vectorsearch.Vector[doc]{ID: "1", Embedding: []float32{1, 0, 0}})
		must.Error(t, err)
		must.ErrorIs(t, err, ErrUnexpectedStatus)

		// Even on failure the index name must still have been observed, and the
		// failure recorded on the operation.
		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.IndexNameKey: idx.collection,
		})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("unreachable server", func(t *testing.T) {
		t.Parallel()
		stub := &qdrantStub{}
		srv := httptest.NewServer(stub)
		idx, err := ProvideIndex[doc](
			t.Context(), nil, nil, nil,
			&Config{BaseURL: srv.URL, Dimension: 3, Metric: vectorsearch.DistanceCosine, Timeout: time.Second},
			"test",
			cbnoop.NewCircuitBreaker(),
		)
		must.NoError(t, err)
		// Close the server to simulate unreachable
		srv.Close()

		err = idx.Upsert(t.Context(), vectorsearch.Vector[doc]{ID: "1", Embedding: []float32{1, 0, 0}})
		must.Error(t, err)
	})
}

func TestDelete(T *testing.T) {
	T.Parallel()

	T.Run("empty ids is a no-op", func(t *testing.T) {
		t.Parallel()
		idx := buildStubIndex(t, &qdrantStub{}, nil)
		must.NoError(t, idx.Delete(t.Context()))
	})

	T.Run("circuit breaker broken", func(t *testing.T) {
		t.Parallel()
		cb := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return true },
		}

		idx := buildStubIndex(t, &qdrantStub{}, cb)

		err := idx.Delete(t.Context(), "some-id")
		must.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)

		must.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("successful delete", func(t *testing.T) {
		t.Parallel()
		idx := buildStubIndex(t, &qdrantStub{}, nil)
		must.NoError(t, idx.Delete(t.Context(), "1", "11111111-1111-1111-1111-111111111111"))
	})

	T.Run("rejects unsupported ID format", func(t *testing.T) {
		t.Parallel()
		idx := buildStubIndex(t, &qdrantStub{}, nil)
		err := idx.Delete(t.Context(), identifiers.New())
		must.ErrorIs(t, err, ErrUnsupportedID)
	})

	T.Run("server returns error status", func(t *testing.T) {
		t.Parallel()
		idx := buildStubIndex(t, &qdrantStub{pointsDeleteStatus: http.StatusInternalServerError}, nil)
		err := idx.Delete(t.Context(), "1")
		must.Error(t, err)
		must.ErrorIs(t, err, ErrUnexpectedStatus)
	})

	T.Run("unreachable server", func(t *testing.T) {
		t.Parallel()
		stub := &qdrantStub{}
		srv := httptest.NewServer(stub)
		idx, err := ProvideIndex[doc](
			t.Context(), nil, nil, nil,
			&Config{BaseURL: srv.URL, Dimension: 3, Metric: vectorsearch.DistanceCosine, Timeout: time.Second},
			"test",
			cbnoop.NewCircuitBreaker(),
		)
		must.NoError(t, err)
		srv.Close()

		err = idx.Delete(t.Context(), "1")
		must.Error(t, err)
	})
}

func TestWipe(T *testing.T) {
	T.Parallel()

	T.Run("circuit breaker broken", func(t *testing.T) {
		t.Parallel()
		cb := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return true },
		}

		idx := buildStubIndex(t, &qdrantStub{}, cb)

		err := idx.Wipe(t.Context())
		must.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)

		must.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("successful wipe", func(t *testing.T) {
		t.Parallel()
		idx := buildStubIndex(t, &qdrantStub{}, nil)
		must.NoError(t, idx.Wipe(t.Context()))
	})

	T.Run("delete collection fails", func(t *testing.T) {
		t.Parallel()
		idx := buildStubIndex(t, &qdrantStub{collectionDeleteStatus: http.StatusForbidden}, nil)
		err := idx.Wipe(t.Context())
		must.Error(t, err)
		must.ErrorIs(t, err, ErrUnexpectedStatus)
	})

	T.Run("delete returns 404 still succeeds", func(t *testing.T) {
		t.Parallel()
		idx := buildStubIndex(t, &qdrantStub{collectionDeleteStatus: http.StatusNotFound}, nil)
		must.NoError(t, idx.Wipe(t.Context()))
	})

	T.Run("recreate collection fails after delete", func(t *testing.T) {
		t.Parallel()
		callCount := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			// Initial GET during ProvideIndex — collection exists
			case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/collections/"):
				if callCount == 0 {
					// First GET (ProvideIndex ensureCollection) — exists
					callCount++
					w.WriteHeader(http.StatusOK)
					_, _ = w.Write([]byte(`{"result":true}`))
				} else {
					// Second GET (Wipe recreate ensureCollection) — not found so it tries to PUT
					w.WriteHeader(http.StatusNotFound)
				}
			case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/collections/"):
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"result":true}`))
			case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/collections/"):
				// Recreate fails
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"status":"error"}`))
			default:
				http.Error(w, "unexpected", http.StatusBadRequest)
			}
		}))
		t.Cleanup(srv.Close)

		idx, err := ProvideIndex[doc](
			t.Context(), nil, nil, nil,
			&Config{BaseURL: srv.URL, Dimension: 3, Metric: vectorsearch.DistanceCosine, Timeout: time.Second},
			"test",
			cbnoop.NewCircuitBreaker(),
		)
		must.NoError(t, err)

		err = idx.Wipe(t.Context())
		must.Error(t, err)
	})

	T.Run("unreachable server", func(t *testing.T) {
		t.Parallel()
		stub := &qdrantStub{}
		srv := httptest.NewServer(stub)
		idx, err := ProvideIndex[doc](
			t.Context(), nil, nil, nil,
			&Config{BaseURL: srv.URL, Dimension: 3, Metric: vectorsearch.DistanceCosine, Timeout: time.Second},
			"test",
			cbnoop.NewCircuitBreaker(),
		)
		must.NoError(t, err)
		srv.Close()

		err = idx.Wipe(t.Context())
		must.Error(t, err)
	})
}

func TestQuery(T *testing.T) {
	T.Parallel()

	T.Run("rejects empty embedding", func(t *testing.T) {
		t.Parallel()
		idx := buildStubIndex(t, &qdrantStub{}, nil)
		_, err := idx.Query(t.Context(), vectorsearch.QueryRequest{Embedding: nil, TopK: 5})
		must.ErrorIs(t, err, vectorsearch.ErrEmptyEmbedding)
	})

	T.Run("rejects wrong dimension", func(t *testing.T) {
		t.Parallel()
		idx := buildStubIndex(t, &qdrantStub{}, nil)
		_, err := idx.Query(t.Context(), vectorsearch.QueryRequest{Embedding: []float32{1, 0}, TopK: 5})
		must.ErrorIs(t, err, vectorsearch.ErrDimensionMismatch)
	})

	T.Run("circuit breaker broken", func(t *testing.T) {
		t.Parallel()
		cb := &cbmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return true },
		}

		idx := buildStubIndex(t, &qdrantStub{}, cb)

		_, err := idx.Query(t.Context(), vectorsearch.QueryRequest{Embedding: []float32{1, 0, 0}, TopK: 5})
		must.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)

		must.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("defaults TopK to 10", func(t *testing.T) {
		t.Parallel()
		var gotBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/points/search") {
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"result":[]}`))
				return
			}
			// ensureCollection
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":true}`))
		}))
		t.Cleanup(srv.Close)

		idx, err := ProvideIndex[doc](
			t.Context(), nil, nil, nil,
			&Config{BaseURL: srv.URL, Dimension: 3, Metric: vectorsearch.DistanceCosine, Timeout: time.Second},
			"test",
			cbnoop.NewCircuitBreaker(),
		)
		must.NoError(t, err)

		_, err = idx.Query(t.Context(), vectorsearch.QueryRequest{Embedding: []float32{1, 0, 0}, TopK: 0})
		must.NoError(t, err)
		must.NotNil(t, gotBody)
		test.Eq[any](t, float64(10), gotBody["limit"])
	})

	T.Run("successful query returns results", func(t *testing.T) {
		t.Parallel()
		searchResp := `{"result":[{"id":"abc","score":0.95,"payload":{"kind":"doc","title":"hello"}},{"id":"def","score":0.8,"payload":null}]}`
		idx, obs := buildRecordingIndex(t, &qdrantStub{pointsSearchBody: searchResp}, nil)

		results, err := idx.Query(t.Context(), vectorsearch.QueryRequest{Embedding: []float32{1, 0, 0}, TopK: 5})
		must.NoError(t, err)
		must.SliceLen(t, 2, results)

		obs.ObservedOperationWithData(t, map[string]any{
			keys.IndexNameKey: idx.collection,
		})

		test.EqOp(t, "abc", results[0].ID)
		// Cosine similarity 0.95 is converted to a distance (1 - 0.95) so that lower
		// means closer, matching the pgvector provider and the interface contract.
		test.InDelta(t, 0.05, float64(results[0].Distance), 0.001)
		must.NotNil(t, results[0].Metadata)
		test.EqOp(t, "doc", results[0].Metadata.Kind)
		test.EqOp(t, "hello", results[0].Metadata.Title)

		test.EqOp(t, "def", results[1].ID)
		test.Nil(t, results[1].Metadata)
	})

	T.Run("query with numeric ID", func(t *testing.T) {
		t.Parallel()
		searchResp := `{"result":[{"id":42,"score":0.5,"payload":null}]}`
		idx := buildStubIndex(t, &qdrantStub{pointsSearchBody: searchResp}, nil)

		results, err := idx.Query(t.Context(), vectorsearch.QueryRequest{Embedding: []float32{1, 0, 0}, TopK: 1})
		must.NoError(t, err)
		must.SliceLen(t, 1, results)
		test.EqOp(t, "42", results[0].ID)
	})

	T.Run("query with filter", func(t *testing.T) {
		t.Parallel()
		var gotBody map[string]any
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/points/search") {
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"result":[]}`))
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"result":true}`))
		}))
		t.Cleanup(srv.Close)

		idx, err := ProvideIndex[doc](
			t.Context(), nil, nil, nil,
			&Config{BaseURL: srv.URL, Dimension: 3, Metric: vectorsearch.DistanceCosine, Timeout: time.Second},
			"test",
			cbnoop.NewCircuitBreaker(),
		)
		must.NoError(t, err)

		im := idx.(*indexManager[doc])
		obs := observability.NewRecordingObserver()
		im.o11y = obs

		filter := map[string]any{"must": []any{map[string]any{"key": "kind", "match": map[string]any{"value": "doc"}}}}
		_, err = im.Query(t.Context(), vectorsearch.QueryRequest{Embedding: []float32{1, 0, 0}, TopK: 5, Filter: filter})
		must.NoError(t, err)
		must.NotNil(t, gotBody)
		test.NotNil(t, gotBody["filter"])

		obs.ObservedOperationWithData(t, map[string]any{
			keys.IndexNameKey:   im.collection,
			keys.SearchQueryKey: filter,
		})
	})

	T.Run("server returns error status", func(t *testing.T) {
		t.Parallel()
		idx, obs := buildRecordingIndex(t, &qdrantStub{pointsSearchStatus: http.StatusInternalServerError}, nil)
		_, err := idx.Query(t.Context(), vectorsearch.QueryRequest{Embedding: []float32{1, 0, 0}, TopK: 5})
		must.Error(t, err)
		must.ErrorIs(t, err, ErrUnexpectedStatus)

		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.IndexNameKey: idx.collection,
		})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("invalid JSON response", func(t *testing.T) {
		t.Parallel()
		idx := buildStubIndex(t, &qdrantStub{pointsSearchBody: `{not json`}, nil)
		_, err := idx.Query(t.Context(), vectorsearch.QueryRequest{Embedding: []float32{1, 0, 0}, TopK: 5})
		must.Error(t, err)
	})

	T.Run("invalid payload in response", func(t *testing.T) {
		t.Parallel()
		// payload is a string where a doc struct is expected
		searchResp := `{"result":[{"id":"x","score":0.5,"payload":"not-a-doc"}]}`
		idx := buildStubIndex(t, &qdrantStub{pointsSearchBody: searchResp}, nil)
		_, err := idx.Query(t.Context(), vectorsearch.QueryRequest{Embedding: []float32{1, 0, 0}, TopK: 5})
		must.Error(t, err)
	})

	T.Run("unsupported ID type in response", func(t *testing.T) {
		t.Parallel()
		searchResp := `{"result":[{"id":true,"score":0.5,"payload":null}]}`
		idx := buildStubIndex(t, &qdrantStub{pointsSearchBody: searchResp}, nil)
		_, err := idx.Query(t.Context(), vectorsearch.QueryRequest{Embedding: []float32{1, 0, 0}, TopK: 5})
		must.Error(t, err)
	})

	T.Run("unreachable server", func(t *testing.T) {
		t.Parallel()
		stub := &qdrantStub{}
		srv := httptest.NewServer(stub)
		idx, err := ProvideIndex[doc](
			t.Context(), nil, nil, nil,
			&Config{BaseURL: srv.URL, Dimension: 3, Metric: vectorsearch.DistanceCosine, Timeout: time.Second},
			"test",
			cbnoop.NewCircuitBreaker(),
		)
		must.NoError(t, err)
		srv.Close()

		_, err = idx.Query(t.Context(), vectorsearch.QueryRequest{Embedding: []float32{1, 0, 0}, TopK: 5})
		must.Error(t, err)
	})
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()
		var cfg *Config
		err := cfg.ValidateWithContext(t.Context())
		must.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})

	T.Run("valid config", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			BaseURL:   "http://localhost:6333",
			Dimension: 128,
			Metric:    vectorsearch.DistanceCosine,
		}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("missing base URL", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			Dimension: 128,
			Metric:    vectorsearch.DistanceCosine,
		}
		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("missing dimension", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			BaseURL: "http://localhost:6333",
			Metric:  vectorsearch.DistanceCosine,
		}
		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("missing metric", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			BaseURL:   "http://localhost:6333",
			Dimension: 128,
		}
		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("invalid metric value", func(t *testing.T) {
		t.Parallel()
		cfg := &Config{
			BaseURL:   "http://localhost:6333",
			Dimension: 128,
			Metric:    "invalid",
		}
		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("all valid metrics pass", func(t *testing.T) {
		t.Parallel()
		for _, m := range []vectorsearch.DistanceMetric{
			vectorsearch.DistanceCosine,
			vectorsearch.DistanceDotProduct,
			vectorsearch.DistanceEuclidean,
		} {
			cfg := &Config{
				BaseURL:   "http://localhost:6333",
				Dimension: 128,
				Metric:    m,
			}
			must.NoError(t, cfg.ValidateWithContext(t.Context()), must.Sprintf("metric %q should be valid", m))
		}
	})
}

// --------- container-backed integration tests ---------

func buildContainerBackedQdrant(t *testing.T) (cfg *Config, shutdown func(context.Context) error) {
	t.Helper()

	ctx := t.Context()
	req := testcontainers.GenericContainerRequest{
		ContainerRequest: testcontainers.ContainerRequest{
			Image:        qdrantImage,
			ExposedPorts: []string{"6333/tcp"},
			WaitingFor:   wait.ForHTTP("/readyz").WithPort("6333/tcp").WithStartupTimeout(2 * time.Minute),
		},
		Started: true,
	}
	container, err := containers.StartWithRetry(ctx, func(ctx context.Context) (testcontainers.Container, error) {
		return testcontainers.GenericContainer(ctx, req)
	})
	must.NoError(t, err)
	must.NotNil(t, container)

	host, err := container.Host(ctx)
	must.NoError(t, err)
	port, err := container.MappedPort(ctx, "6333/tcp")
	must.NoError(t, err)

	cfg = &Config{
		BaseURL:   "http://" + net.JoinHostPort(host, port.Port()),
		Dimension: 3,
		Metric:    vectorsearch.DistanceCosine,
		Timeout:   30 * time.Second,
	}
	return cfg, func(ctx context.Context) error { return container.Terminate(ctx) }
}

func TestQdrantIndex_Container(T *testing.T) {
	T.Parallel()

	containers.SkipIfNotRunning(T)

	cfg, shutdown := buildContainerBackedQdrant(T)
	T.Cleanup(func() { _ = shutdown(context.Background()) })

	provide := func(t *testing.T, name string) vectorsearch.Index[doc] {
		t.Helper()
		idx, err := ProvideIndex[doc](t.Context(), nil, nil, nil, cfg, name, cbnoop.NewCircuitBreaker())
		must.NoError(t, err)
		return idx
	}

	T.Run("Upsert and Query roundtrip", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		idx := provide(t, "rt_"+identifiers.New())

		must.NoError(t, idx.Upsert(ctx,
			vectorsearch.Vector[doc]{ID: "11111111-1111-1111-1111-111111111111", Embedding: []float32{1, 0, 0}, Metadata: &doc{Kind: "doc", Title: "alpha"}},
			vectorsearch.Vector[doc]{ID: "22222222-2222-2222-2222-222222222222", Embedding: []float32{0, 1, 0}, Metadata: &doc{Kind: "doc", Title: "beta"}},
			vectorsearch.Vector[doc]{ID: "33333333-3333-3333-3333-333333333333", Embedding: []float32{0, 0, 1}, Metadata: &doc{Kind: "doc", Title: "gamma"}},
		))

		results, err := idx.Query(ctx, vectorsearch.QueryRequest{Embedding: []float32{1, 0, 0}, TopK: 3})
		must.NoError(t, err)
		must.SliceLen(t, 3, results)
		test.EqOp(t, "11111111-1111-1111-1111-111111111111", results[0].ID)
		must.NotNil(t, results[0].Metadata)
		test.EqOp(t, "alpha", results[0].Metadata.Title)
	})

	T.Run("numeric IDs round-trip without scientific notation", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		idx := provide(t, "num_"+identifiers.New())

		must.NoError(t, idx.Upsert(ctx,
			vectorsearch.Vector[doc]{ID: "10000000", Embedding: []float32{1, 0, 0}, Metadata: &doc{Kind: "doc", Title: "big"}},
		))

		results, err := idx.Query(ctx, vectorsearch.QueryRequest{Embedding: []float32{1, 0, 0}, TopK: 1})
		must.NoError(t, err)
		must.SliceLen(t, 1, results)
		test.EqOp(t, "10000000", results[0].ID)

		// The same ID string must still target that point on delete.
		must.NoError(t, idx.Delete(ctx, "10000000"))
		after, err := idx.Query(ctx, vectorsearch.QueryRequest{Embedding: []float32{1, 0, 0}, TopK: 1})
		must.NoError(t, err)
		test.SliceEmpty(t, after)
	})

	T.Run("TopK is respected", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		idx := provide(t, "topk_"+identifiers.New())

		must.NoError(t, idx.Upsert(ctx,
			vectorsearch.Vector[doc]{ID: "11111111-aaaa-aaaa-aaaa-111111111111", Embedding: []float32{1, 0, 0}},
			vectorsearch.Vector[doc]{ID: "22222222-aaaa-aaaa-aaaa-222222222222", Embedding: []float32{0, 1, 0}},
			vectorsearch.Vector[doc]{ID: "33333333-aaaa-aaaa-aaaa-333333333333", Embedding: []float32{0, 0, 1}},
		))

		results, err := idx.Query(ctx, vectorsearch.QueryRequest{Embedding: []float32{1, 0, 0}, TopK: 2})
		must.NoError(t, err)
		test.SliceLen(t, 2, results)
	})

	T.Run("filter is applied", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		idx := provide(t, "filt_"+identifiers.New())

		must.NoError(t, idx.Upsert(ctx,
			vectorsearch.Vector[doc]{ID: "11111111-bbbb-bbbb-bbbb-111111111111", Embedding: []float32{1, 0, 0}, Metadata: &doc{Kind: "doc"}},
			vectorsearch.Vector[doc]{ID: "22222222-bbbb-bbbb-bbbb-222222222222", Embedding: []float32{1, 0, 0}, Metadata: &doc{Kind: "image"}},
		))

		filter := map[string]any{
			"must": []any{
				map[string]any{
					"key":   "kind",
					"match": map[string]any{"value": "doc"},
				},
			},
		}
		results, err := idx.Query(ctx, vectorsearch.QueryRequest{Embedding: []float32{1, 0, 0}, TopK: 10, Filter: filter})
		must.NoError(t, err)
		must.SliceLen(t, 1, results)
		must.NotNil(t, results[0].Metadata)
		test.EqOp(t, "doc", results[0].Metadata.Kind)
	})

	T.Run("Query rejects empty embedding", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		idx := provide(t, "emb_"+identifiers.New())

		_, err := idx.Query(ctx, vectorsearch.QueryRequest{Embedding: nil, TopK: 5})
		must.ErrorIs(t, err, vectorsearch.ErrEmptyEmbedding)
	})

	T.Run("Query rejects wrong dimension", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		idx := provide(t, "dim_"+identifiers.New())

		_, err := idx.Query(ctx, vectorsearch.QueryRequest{Embedding: []float32{1, 0}, TopK: 5})
		must.ErrorIs(t, err, vectorsearch.ErrDimensionMismatch)
	})

	T.Run("Delete removes specific points", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		idx := provide(t, "del_"+identifiers.New())

		must.NoError(t, idx.Upsert(ctx,
			vectorsearch.Vector[doc]{ID: "11111111-cccc-cccc-cccc-111111111111", Embedding: []float32{1, 0, 0}},
			vectorsearch.Vector[doc]{ID: "22222222-cccc-cccc-cccc-222222222222", Embedding: []float32{0, 1, 0}},
		))

		must.NoError(t, idx.Delete(ctx, "11111111-cccc-cccc-cccc-111111111111"))

		results, err := idx.Query(ctx, vectorsearch.QueryRequest{Embedding: []float32{0, 1, 0}, TopK: 10})
		must.NoError(t, err)
		must.SliceLen(t, 1, results)
		test.EqOp(t, "22222222-cccc-cccc-cccc-222222222222", results[0].ID)
	})

	T.Run("Wipe drops and recreates", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		idx := provide(t, "wipe_"+identifiers.New())

		must.NoError(t, idx.Upsert(ctx,
			vectorsearch.Vector[doc]{ID: "11111111-dddd-dddd-dddd-111111111111", Embedding: []float32{1, 0, 0}},
		))
		must.NoError(t, idx.Wipe(ctx))

		results, err := idx.Query(ctx, vectorsearch.QueryRequest{Embedding: []float32{1, 0, 0}, TopK: 10})
		must.NoError(t, err)
		test.SliceEmpty(t, results)

		// Confirm the collection still accepts writes after wipe.
		must.NoError(t, idx.Upsert(ctx,
			vectorsearch.Vector[doc]{ID: "22222222-dddd-dddd-dddd-222222222222", Embedding: []float32{1, 0, 0}},
		))
	})

	T.Run("ProvideIndex is idempotent", func(t *testing.T) {
		t.Parallel()
		ctx := t.Context()
		name := "idem_" + identifiers.New()
		idx1, err := ProvideIndex[doc](ctx, nil, nil, nil, cfg, name, cbnoop.NewCircuitBreaker())
		must.NoError(t, err)
		idx2, err := ProvideIndex[doc](ctx, nil, nil, nil, cfg, name, cbnoop.NewCircuitBreaker())
		must.NoError(t, err)

		must.NoError(t, idx1.Upsert(ctx, vectorsearch.Vector[doc]{ID: "11111111-eeee-eeee-eeee-111111111111", Embedding: []float32{1, 0, 0}}))

		results, err := idx2.Query(ctx, vectorsearch.QueryRequest{Embedding: []float32{1, 0, 0}, TopK: 1})
		must.NoError(t, err)
		must.SliceLen(t, 1, results)
	})
}

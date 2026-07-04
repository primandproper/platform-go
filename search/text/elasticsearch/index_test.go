package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/primandproper/platform-go/v3/circuitbreaking"
	mockcircuitbreaking "github.com/primandproper/platform-go/v3/circuitbreaking/mock"
	"github.com/primandproper/platform-go/v3/observability"
	"github.com/primandproper/platform-go/v3/observability/keys"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

type example struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type invalidJSON struct {
	Channel chan int `json:"channel"`
}

func buildTestIndexManagerForUnit(t *testing.T, cb circuitbreaking.CircuitBreaker) (*indexManager[example], *observability.RecordingObserver) {
	t.Helper()

	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{"http://localhost:19291"}, // intentionally wrong
	})
	if err != nil {
		t.Fatal(err)
	}

	obs := observability.NewRecordingObserver()

	return &indexManager[example]{
		o11y:           obs,
		circuitBreaker: cb,
		esClient:       client,
		indexName:      "test",
	}, obs
}

func buildTestIndexManagerWithServer(t *testing.T, server *httptest.Server, cb circuitbreaking.CircuitBreaker) (*indexManager[example], *observability.RecordingObserver) {
	t.Helper()

	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{server.URL},
	})
	if err != nil {
		t.Fatal(err)
	}

	obs := observability.NewRecordingObserver()

	return &indexManager[example]{
		o11y:           obs,
		circuitBreaker: cb,
		esClient:       client,
		indexName:      "test",
	}, obs
}

func TestIndexManager_Index_CircuitBroken(T *testing.T) {
	T.Parallel()

	T.Run("with broken circuit breaker", func(t *testing.T) {
		t.Parallel()

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return true },
		}

		im, _ := buildTestIndexManagerForUnit(t, cb)

		err := im.Index(context.Background(), "id", map[string]string{"id": "test"})
		test.Error(t, err)
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with unmarshalable value", func(t *testing.T) {
		t.Parallel()

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
		}

		im, _ := buildTestIndexManagerForUnit(t, cb)

		err := im.Index(context.Background(), "id", make(chan int))
		test.Error(t, err)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with unreachable server", func(t *testing.T) {
		t.Parallel()

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		im, _ := buildTestIndexManagerForUnit(t, cb)

		err := im.Index(context.Background(), "id", map[string]string{"id": "test"})
		test.Error(t, err)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.FailedCalls())
	})
}

func TestIndexManager_Index_Unit(T *testing.T) {
	T.Parallel()

	T.Run("with successful index", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"_index":"test","_id":"123","result":"created"}`)
		}))
		t.Cleanup(server.Close)

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}

		im, obs := buildTestIndexManagerWithServer(t, server, cb)

		value := &example{ID: "123", Name: "test"}
		err := im.Index(context.Background(), "123", value)
		test.NoError(t, err)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())

		obs.ObservedOperationWithData(t, map[string]any{
			"id": "123",
		})
	})

	T.Run("with non-success status code", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":{"type":"mapper_parsing_exception","reason":"failed to parse"}}`)
		}))
		t.Cleanup(server.Close)

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		im, obs := buildTestIndexManagerWithServer(t, server, cb)

		value := &example{ID: "123", Name: "test"}
		err := im.Index(context.Background(), "123", value)
		test.Error(t, err)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.FailedCalls())

		// Even though the index failed, the values must still have been observed.
		obs.ObservedOperationWithData(t, map[string]any{
			"id": "123",
		})
	})
}

func TestIndexManager_Search_CircuitBroken(T *testing.T) {
	T.Parallel()

	T.Run("with broken circuit breaker", func(t *testing.T) {
		t.Parallel()

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return true },
		}

		im, _ := buildTestIndexManagerForUnit(t, cb)

		results, err := im.Search(context.Background(), "query")
		test.Error(t, err)
		test.Nil(t, results)
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with empty query", func(t *testing.T) {
		t.Parallel()

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
		}

		im, _ := buildTestIndexManagerForUnit(t, cb)

		results, err := im.Search(context.Background(), "")
		test.Error(t, err)
		test.Nil(t, results)
		test.ErrorIs(t, err, ErrEmptyQueryProvided)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with unreachable server", func(t *testing.T) {
		t.Parallel()

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		im, _ := buildTestIndexManagerForUnit(t, cb)

		results, err := im.Search(context.Background(), "test query")
		test.Error(t, err)
		test.Nil(t, results)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.FailedCalls())
	})
}

func TestIndexManager_Search_Unit(T *testing.T) {
	T.Parallel()

	T.Run("with successful search results", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var sent searchQuery
			must.NoError(t, json.NewDecoder(r.Body).Decode(&sent))
			test.EqOp(t, "test", sent.Query.MultiMatch.Query)
			test.SliceContains(t, sent.Query.MultiMatch.Fields, "*")

			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"hits":{"total":{"value":1},"hits":[{"_id":"123","_source":{"id":"123","name":"test"}}]}}`)
		}))
		t.Cleanup(server.Close)

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}

		im, obs := buildTestIndexManagerWithServer(t, server, cb)

		results, err := im.Search(context.Background(), "test")
		test.NoError(t, err)
		must.SliceLen(t, 1, results)
		test.EqOp(t, "123", results[0].ID)
		test.EqOp(t, "test", results[0].Name)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())

		obs.ObservedOperationWithData(t, map[string]any{
			keys.SearchQueryKey: "test",
		})
	})

	T.Run("with error response", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `{"error":{"type":"search_phase_execution_exception","reason":"all shards failed"}}`)
		}))
		t.Cleanup(server.Close)

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		im, _ := buildTestIndexManagerWithServer(t, server, cb)

		results, err := im.Search(context.Background(), "test")
		test.Error(t, err)
		test.Nil(t, results)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.FailedCalls())
	})

	T.Run("with invalid JSON in success response", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `not valid json`)
		}))
		t.Cleanup(server.Close)

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		im, _ := buildTestIndexManagerWithServer(t, server, cb)

		results, err := im.Search(context.Background(), "test")
		test.Error(t, err)
		test.Nil(t, results)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.FailedCalls())
	})
}

func TestIndexManager_Search_ErrorResponseDecodeFailure_Unit(T *testing.T) {
	T.Parallel()

	T.Run("with invalid JSON in error response body", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(w, `this is not valid json`)
		}))
		t.Cleanup(server.Close)

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		im, _ := buildTestIndexManagerWithServer(t, server, cb)

		results, err := im.Search(context.Background(), "test")
		test.Error(t, err)
		test.Nil(t, results)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.FailedCalls())
	})
}

func TestIndexManager_Search_SourceUnmarshalError_Unit(T *testing.T) {
	T.Parallel()

	T.Run("with invalid source in hit", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"hits":{"total":{"value":1},"hits":[{"_id":"123","_source":"not a valid object"}]}}`)
		}))
		t.Cleanup(server.Close)

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		im, _ := buildTestIndexManagerWithServer(t, server, cb)

		results, err := im.Search(context.Background(), "test")
		test.Error(t, err)
		test.Nil(t, results)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.FailedCalls())
	})
}

func TestIndexManager_Delete_CircuitBroken(T *testing.T) {
	T.Parallel()

	T.Run("with broken circuit breaker", func(t *testing.T) {
		t.Parallel()

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return true },
		}

		im, _ := buildTestIndexManagerForUnit(t, cb)

		err := im.Delete(context.Background(), "id")
		test.Error(t, err)
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with unreachable server", func(t *testing.T) {
		t.Parallel()

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		im, _ := buildTestIndexManagerForUnit(t, cb)

		err := im.Delete(context.Background(), "some-id")
		test.Error(t, err)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.FailedCalls())
	})
}

func TestIndexManager_Delete_Unit(T *testing.T) {
	T.Parallel()

	T.Run("with successful delete", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"_index":"test","_id":"123","result":"deleted"}`)
		}))
		t.Cleanup(server.Close)

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}

		im, obs := buildTestIndexManagerWithServer(t, server, cb)

		err := im.Delete(context.Background(), "123")
		test.NoError(t, err)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())

		obs.ObservedOperationWithData(t, map[string]any{
			"id": "123",
		})
	})

	T.Run("with non-success status code", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error":{"type":"internal","reason":"boom"}}`)
		}))
		t.Cleanup(server.Close)

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		im, obs := buildTestIndexManagerWithServer(t, server, cb)

		err := im.Delete(context.Background(), "123")
		test.Error(t, err)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.FailedCalls())
		test.SliceEmpty(t, cb.SucceededCalls())

		obs.ObservedOperationWithData(t, map[string]any{
			"id": "123",
		})
	})
}

func TestIndexManager_Wipe_Unit(T *testing.T) {
	T.Parallel()

	T.Run("with successful wipe", func(t *testing.T) {
		t.Parallel()

		var gotMethod, gotPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotMethod, gotPath = r.Method, r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{"deleted":3,"failures":[]}`)
		}))
		t.Cleanup(server.Close)

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}

		im, _ := buildTestIndexManagerWithServer(t, server, cb)

		err := im.Wipe(context.Background())
		test.NoError(t, err)
		test.SliceLen(t, 1, cb.SucceededCalls())
		// A delete-by-query hits POST /<index>/_delete_by_query.
		test.EqOp(t, http.MethodPost, gotMethod)
		test.StrContains(t, gotPath, "_delete_by_query")
	})

	T.Run("with non-success status code", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = fmt.Fprint(w, `{"error":{"type":"internal","reason":"boom"}}`)
		}))
		t.Cleanup(server.Close)

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		im, _ := buildTestIndexManagerWithServer(t, server, cb)

		err := im.Wipe(context.Background())
		test.Error(t, err)
		test.SliceLen(t, 1, cb.FailedCalls())
		test.SliceEmpty(t, cb.SucceededCalls())
	})

	T.Run("circuit broken", func(t *testing.T) {
		t.Parallel()

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return true },
		}

		im, _ := buildTestIndexManagerWithServer(t, httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})), cb)

		err := im.Wipe(context.Background())
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
	})
}

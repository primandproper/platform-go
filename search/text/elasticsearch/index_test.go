package elasticsearch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	circuitbreakingmock "github.com/primandproper/platform-go/v13/circuitbreaking/mock"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	textsearch "github.com/primandproper/platform-go/v13/search/text"

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

func buildTestIndexManagerForUnit(t *testing.T, cb circuitbreaking.CircuitBreaker) (*IndexManager[example], *observability.RecordingObserver) {
	t.Helper()

	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{"http://localhost:19291"}, // intentionally wrong
	})
	if err != nil {
		t.Fatal(err)
	}

	obs := observability.NewRecordingObserver()

	instruments, err := textsearch.NewInstruments(serviceName, "test", nil)
	must.NoError(t, err)

	return &IndexManager[example]{
		o11y:           obs,
		circuitBreaker: cb,
		esClient:       client,
		indexName:      "test",
		instruments:    instruments,
	}, obs
}

func buildTestIndexManagerWithServer(t *testing.T, server *httptest.Server, cb circuitbreaking.CircuitBreaker) (*IndexManager[example], *observability.RecordingObserver) {
	t.Helper()

	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{server.URL},
	})
	if err != nil {
		t.Fatal(err)
	}

	obs := observability.NewRecordingObserver()

	instruments, err := textsearch.NewInstruments(serviceName, "test", nil)
	must.NoError(t, err)

	return &IndexManager[example]{
		o11y:           obs,
		circuitBreaker: cb,
		esClient:       client,
		indexName:      "test",
		instruments:    instruments,
	}, obs
}

func TestIndexManager_Index_CircuitBroken(T *testing.T) {
	T.Parallel()

	T.Run("with broken circuit breaker", func(t *testing.T) {
		t.Parallel()

		cb := &circuitbreakingmock.CircuitBreakerMock{
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

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
		}

		im, _ := buildTestIndexManagerForUnit(t, cb)

		err := im.Index(context.Background(), "id", make(chan int))
		test.Error(t, err)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with unreachable server", func(t *testing.T) {
		t.Parallel()

		cb := &circuitbreakingmock.CircuitBreakerMock{
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

		cb := &circuitbreakingmock.CircuitBreakerMock{
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

		cb := &circuitbreakingmock.CircuitBreakerMock{
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

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return true },
		}

		im, _ := buildTestIndexManagerForUnit(t, cb)

		results, err := im.Search(context.Background(), textsearch.SearchRequest{Query: "query"})
		test.Error(t, err)
		test.Nil(t, results)
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with empty query", func(t *testing.T) {
		t.Parallel()

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
		}

		im, _ := buildTestIndexManagerForUnit(t, cb)

		results, err := im.Search(context.Background(), textsearch.SearchRequest{Query: ""})
		test.Error(t, err)
		test.Nil(t, results)
		test.ErrorIs(t, err, textsearch.ErrEmptyQueryProvided)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with unreachable server", func(t *testing.T) {
		t.Parallel()

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		im, _ := buildTestIndexManagerForUnit(t, cb)

		results, err := im.Search(context.Background(), textsearch.SearchRequest{Query: "test query"})
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

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}

		im, obs := buildTestIndexManagerWithServer(t, server, cb)

		results, err := im.Search(context.Background(), textsearch.SearchRequest{Query: "test"})
		test.NoError(t, err)
		must.SliceLen(t, 1, results.Hits)
		test.EqOp(t, "123", results.Hits[0].ID)
		test.EqOp(t, "test", results.Hits[0].Name)
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

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		im, _ := buildTestIndexManagerWithServer(t, server, cb)

		results, err := im.Search(context.Background(), textsearch.SearchRequest{Query: "test"})
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

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		im, _ := buildTestIndexManagerWithServer(t, server, cb)

		results, err := im.Search(context.Background(), textsearch.SearchRequest{Query: "test"})
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

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		im, _ := buildTestIndexManagerWithServer(t, server, cb)

		results, err := im.Search(context.Background(), textsearch.SearchRequest{Query: "test"})
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

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		im, _ := buildTestIndexManagerWithServer(t, server, cb)

		results, err := im.Search(context.Background(), textsearch.SearchRequest{Query: "test"})
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

		cb := &circuitbreakingmock.CircuitBreakerMock{
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

		cb := &circuitbreakingmock.CircuitBreakerMock{
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

		cb := &circuitbreakingmock.CircuitBreakerMock{
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

		cb := &circuitbreakingmock.CircuitBreakerMock{
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

		cb := &circuitbreakingmock.CircuitBreakerMock{
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

		cb := &circuitbreakingmock.CircuitBreakerMock{
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

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return true },
		}

		im, _ := buildTestIndexManagerWithServer(t, httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})), cb)

		err := im.Wipe(context.Background())
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
	})
}

func TestIndexManager_Search_Pagination(T *testing.T) {
	T.Parallel()

	// esSearchServer replies with one hit and the given total, recording the
	// request body so the from/size actually sent can be asserted.
	esSearchServer := func(t *testing.T, total int, body *string) *httptest.Server {
		t.Helper()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if body != nil {
				raw, _ := io.ReadAll(r.Body)
				*body = string(raw)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("X-Elastic-Product", "Elasticsearch")
			_, _ = fmt.Fprintf(w, `{"hits":{"total":{"value":%d},"hits":[{"_id":"1","_source":{"id":"1","name":"first"}}]}}`, total)
		}))
		t.Cleanup(server.Close)

		return server
	}

	T.Run("issues a next cursor while hits remain", func(t *testing.T) {
		t.Parallel()

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}

		im, _ := buildTestIndexManagerWithServer(t, esSearchServer(t, 10, nil), cb)

		results, err := im.Search(context.Background(), textsearch.SearchRequest{Query: "test", Limit: 1})
		must.NoError(t, err)
		test.False(t, results.Done())

		position, decodeErr := textsearch.DecodeCursor("elasticsearch", results.NextCursor)
		must.NoError(t, decodeErr)
		test.EqOp(t, 1, position)
	})

	T.Run("no next cursor once the total is reached", func(t *testing.T) {
		t.Parallel()

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}

		im, _ := buildTestIndexManagerWithServer(t, esSearchServer(t, 1, nil), cb)

		results, err := im.Search(context.Background(), textsearch.SearchRequest{Query: "test", Limit: 1})
		must.NoError(t, err)
		test.True(t, results.Done())
	})

	T.Run("the cursor becomes the from of the next request", func(t *testing.T) {
		t.Parallel()

		var body string
		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}

		im, _ := buildTestIndexManagerWithServer(t, esSearchServer(t, 100, &body), cb)

		cursor, err := textsearch.EncodeCursor("elasticsearch", 40)
		must.NoError(t, err)

		_, searchErr := im.Search(context.Background(), textsearch.SearchRequest{Query: "test", Limit: 5, Cursor: cursor})
		must.NoError(t, searchErr)
		test.StrContains(t, body, `"from":40`)
		test.StrContains(t, body, `"size":5`)
	})

	T.Run("an unset limit sends the shared default, not Elasticsearch's 10", func(t *testing.T) {
		t.Parallel()

		var body string
		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}

		im, _ := buildTestIndexManagerWithServer(t, esSearchServer(t, 100, &body), cb)

		_, err := im.Search(context.Background(), textsearch.SearchRequest{Query: "test"})
		must.NoError(t, err)
		test.StrContains(t, body, fmt.Sprintf(`"size":%d`, textsearch.DefaultSearchLimit))
	})

	T.Run("paging past the result window is refused by name", func(t *testing.T) {
		t.Parallel()

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
		}

		im, _ := buildTestIndexManagerForUnit(t, cb)

		// from + size beyond max_result_window is rejected by the cluster, so it
		// is caught here rather than surfacing as an opaque 500.
		cursor, err := textsearch.EncodeCursor("elasticsearch", 9999)
		must.NoError(t, err)

		results, searchErr := im.Search(context.Background(), textsearch.SearchRequest{Query: "test", Limit: 25, Cursor: cursor})
		test.ErrorIs(t, searchErr, textsearch.ErrResultWindowExceeded)
		test.Nil(t, results)
	})

	T.Run("a cursor from another backend is refused", func(t *testing.T) {
		t.Parallel()

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
		}

		im, _ := buildTestIndexManagerForUnit(t, cb)

		cursor, err := textsearch.EncodeCursor("algolia", 2)
		must.NoError(t, err)

		results, searchErr := im.Search(context.Background(), textsearch.SearchRequest{Query: "test", Cursor: cursor})
		test.ErrorIs(t, searchErr, textsearch.ErrInvalidCursor)
		test.Nil(t, results)
	})
}

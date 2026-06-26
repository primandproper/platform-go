package elasticsearch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/primandproper/platform-go/circuitbreaking"
	mockcircuitbreaking "github.com/primandproper/platform-go/circuitbreaking/mock"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	"github.com/primandproper/platform-go/observability/tracing"

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

func buildTestIndexManagerForUnit(t *testing.T, cb circuitbreaking.CircuitBreaker) *indexManager[example] {
	t.Helper()

	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{"http://localhost:19291"}, // intentionally wrong
	})
	if err != nil {
		t.Fatal(err)
	}

	return &indexManager[example]{
		logger:         loggingnoop.NewLogger(),
		tracer:         tracing.NewTracerForTest("test"),
		circuitBreaker: cb,
		esClient:       client,
		indexName:      "test",
	}
}

func buildTestIndexManagerWithServer(t *testing.T, server *httptest.Server, cb circuitbreaking.CircuitBreaker) *indexManager[example] {
	t.Helper()

	client, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses: []string{server.URL},
	})
	if err != nil {
		t.Fatal(err)
	}

	return &indexManager[example]{
		logger:         loggingnoop.NewLogger(),
		tracer:         tracing.NewTracerForTest("test"),
		circuitBreaker: cb,
		esClient:       client,
		indexName:      "test",
	}
}

func TestIndexManager_Index_CircuitBroken(T *testing.T) {
	T.Parallel()

	T.Run("with broken circuit breaker", func(t *testing.T) {
		t.Parallel()

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return true },
		}

		im := buildTestIndexManagerForUnit(t, cb)

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

		im := buildTestIndexManagerForUnit(t, cb)

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

		im := buildTestIndexManagerForUnit(t, cb)

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

		im := buildTestIndexManagerWithServer(t, server, cb)

		err := im.Index(context.Background(), "123", &example{ID: "123", Name: "test"})
		test.NoError(t, err)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())
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

		im := buildTestIndexManagerWithServer(t, server, cb)

		err := im.Index(context.Background(), "123", &example{ID: "123", Name: "test"})
		test.Error(t, err)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.FailedCalls())
	})
}

func TestIndexManager_Search_CircuitBroken(T *testing.T) {
	T.Parallel()

	T.Run("with broken circuit breaker", func(t *testing.T) {
		t.Parallel()

		cb := &mockcircuitbreaking.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return true },
		}

		im := buildTestIndexManagerForUnit(t, cb)

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

		im := buildTestIndexManagerForUnit(t, cb)

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

		im := buildTestIndexManagerForUnit(t, cb)

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

		im := buildTestIndexManagerWithServer(t, server, cb)

		results, err := im.Search(context.Background(), "test")
		test.NoError(t, err)
		must.SliceLen(t, 1, results)
		test.EqOp(t, "123", results[0].ID)
		test.EqOp(t, "test", results[0].Name)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())
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

		im := buildTestIndexManagerWithServer(t, server, cb)

		// NOTE: the search function has a named return 'err' that is overwritten
		// by the deferred res.Body.Close() call, so the error is lost. The code
		// does exercise the IsError() branch and calls circuitBreaker.Failed(),
		// but ultimately returns nil error due to the defer clobbering it.
		results, err := im.Search(context.Background(), "test")
		test.NoError(t, err)
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

		im := buildTestIndexManagerWithServer(t, server, cb)

		// NOTE: same issue as error response test - the deferred res.Body.Close()
		// overwrites the named return 'err' with nil.
		results, err := im.Search(context.Background(), "test")
		test.NoError(t, err)
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

		im := buildTestIndexManagerWithServer(t, server, cb)

		// NOTE: the named return 'err' from the deferred res.Body.Close() clobbers
		// the error, so this returns nil error despite the decode failure.
		results, err := im.Search(context.Background(), "test")
		test.NoError(t, err)
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

		im := buildTestIndexManagerWithServer(t, server, cb)

		// NOTE: the named return 'err' from the deferred res.Body.Close() clobbers
		// the error, so this returns nil error despite the unmarshal failure.
		results, err := im.Search(context.Background(), "test")
		test.NoError(t, err)
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

		im := buildTestIndexManagerForUnit(t, cb)

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

		im := buildTestIndexManagerForUnit(t, cb)

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

		im := buildTestIndexManagerWithServer(t, server, cb)

		err := im.Delete(context.Background(), "123")
		test.NoError(t, err)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())
	})
}

func TestIndexManager_Wipe_Unit(T *testing.T) {
	T.Parallel()

	T.Run("returns unimplemented error", func(t *testing.T) {
		t.Parallel()

		im := &indexManager[example]{}

		err := im.Wipe(context.Background())
		test.Error(t, err)
		test.EqOp(t, "unimplemented", err.Error())
	})
}

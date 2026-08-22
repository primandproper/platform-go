package algolia

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	circuitbreakingmock "github.com/primandproper/platform-go/v13/circuitbreaking/mock"
	cbnoop "github.com/primandproper/platform-go/v13/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	textsearch "github.com/primandproper/platform-go/v13/search/text"

	algoliasearch "github.com/algolia/algoliasearch-client-go/v3/algolia/search"
	algoliatransport "github.com/algolia/algoliasearch-client-go/v3/algolia/transport"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var _ algoliatransport.Requester = (*testRequester)(nil)

type testRequester struct {
	handler http.Handler
}

func (r *testRequester) Request(req *http.Request) (*http.Response, error) {
	recorder := &responseRecorder{
		headers: http.Header{},
		body:    &strings.Builder{},
		code:    http.StatusOK,
	}
	r.handler.ServeHTTP(recorder, req)

	return &http.Response{
		StatusCode: recorder.code,
		Header:     recorder.headers,
		Body:       io.NopCloser(strings.NewReader(recorder.body.String())),
	}, nil
}

type responseRecorder struct {
	headers http.Header
	body    *strings.Builder
	code    int
}

func (r *responseRecorder) Header() http.Header {
	return r.headers
}

func (r *responseRecorder) Write(b []byte) (int, error) {
	return r.body.Write(b)
}

func (r *responseRecorder) WriteHeader(statusCode int) {
	r.code = statusCode
}

func buildTestIndexManagerWithMockServer(t *testing.T, handler http.Handler, cb circuitbreaking.CircuitBreaker) (*IndexManager[example], *observability.RecordingObserver) {
	t.Helper()

	client := algoliasearch.NewClientWithConfig(algoliasearch.Configuration{
		AppID:     "fake",
		APIKey:    "fake",
		Hosts:     []string{"localhost"},
		Requester: &testRequester{handler: handler},
	})

	obs := observability.NewRecordingObserver()

	instruments, err := textsearch.NewInstruments(serviceName, "test", nil)
	must.NoError(t, err)

	return &IndexManager[example]{
		o11y:           obs,
		circuitBreaker: cb,
		client:         client.InitIndex("test"),
		instruments:    instruments,
	}, obs
}

func buildTestIndexManager(t *testing.T) *IndexManager[example] {
	t.Helper()

	im, err := NewIndexManager[example](
		&Config{AppID: "fake", APIKey: "fake"},
		"test",
		cbnoop.NewCircuitBreaker(),
	)
	if err != nil {
		t.Fatal(err)
	}

	return im
}

func buildTestIndexManagerWithCircuitBreaker(t *testing.T, cb circuitbreaking.CircuitBreaker) *IndexManager[example] {
	t.Helper()

	im, err := NewIndexManager[example](
		&Config{AppID: "fake", APIKey: "fake"},
		"test",
		cb,
	)
	if err != nil {
		t.Fatal(err)
	}

	return im
}

func TestIndexManager_Index(T *testing.T) {
	T.Parallel()

	T.Run("with broken circuit breaker", func(t *testing.T) {
		t.Parallel()

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return true },
		}

		im := buildTestIndexManagerWithCircuitBreaker(t, cb)

		err := im.Index(context.Background(), "id", map[string]string{"id": "test"})
		test.Error(t, err)
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with unmarshalable value", func(t *testing.T) {
		t.Parallel()

		im := buildTestIndexManager(t)

		err := im.Index(context.Background(), "id", make(chan int))
		test.Error(t, err)
	})

	T.Run("with valid value but invalid credentials", func(t *testing.T) {
		t.Parallel()

		im := buildTestIndexManager(t)

		err := im.Index(context.Background(), "id", map[string]string{"id": "test", "name": "example"})
		test.Error(t, err)
	})

	T.Run("with non-object JSON value", func(t *testing.T) {
		t.Parallel()

		im := buildTestIndexManager(t)

		err := im.Index(context.Background(), "id", "just a string")
		test.Error(t, err)
	})

	T.Run("with successful index", func(t *testing.T) {
		t.Parallel()

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"createdAt":"2021-01-01T00:00:00Z","objectID":"123","taskID":123}`))
		})

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}

		im, obs := buildTestIndexManagerWithMockServer(t, handler, cb)

		value := map[string]string{"id": "123", "name": "example"}
		err := im.Index(context.Background(), "123", value)
		test.NoError(t, err)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		// A successful index must record success on the breaker.
		test.SliceLen(t, 1, cb.SucceededCalls())

		obs.ObservedOperationWithData(t, map[string]any{
			idKey: "123",
		})
	})

	T.Run("index failure trips the breaker", func(t *testing.T) {
		t.Parallel()

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		im, _ := buildTestIndexManagerWithMockServer(t, handler, cb)

		err := im.Index(context.Background(), "123", map[string]string{"id": "123"})
		test.Error(t, err)
		test.SliceLen(t, 1, cb.FailedCalls())
	})
}

func TestIndexManager_Search(T *testing.T) {
	T.Parallel()

	T.Run("with broken circuit breaker", func(t *testing.T) {
		t.Parallel()

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return true },
		}

		im := buildTestIndexManagerWithCircuitBreaker(t, cb)

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

		im := buildTestIndexManagerWithCircuitBreaker(t, cb)

		results, err := im.Search(context.Background(), textsearch.SearchRequest{Query: ""})
		test.Error(t, err)
		test.Nil(t, results)
		test.ErrorIs(t, err, textsearch.ErrEmptyQueryProvided)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with valid query but invalid credentials", func(t *testing.T) {
		t.Parallel()

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		im := buildTestIndexManagerWithCircuitBreaker(t, cb)

		results, err := im.Search(context.Background(), textsearch.SearchRequest{Query: "test query"})
		test.Error(t, err)
		test.Nil(t, results)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.FailedCalls())
	})

	T.Run("with successful search results", func(t *testing.T) {
		t.Parallel()

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"hits":[{"objectID":"123"}],"nbHits":1,"page":0,"nbPages":1,"hitsPerPage":20,"processingTimeMS":1}`))
		})

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}

		im, obs := buildTestIndexManagerWithMockServer(t, handler, cb)

		results, err := im.Search(context.Background(), textsearch.SearchRequest{Query: "test query"})
		test.NoError(t, err)
		test.NotNil(t, results)
		test.SliceLen(t, 1, results.Hits)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())

		obs.ObservedOperationWithData(t, map[string]any{
			keys.SearchQueryKey: "test query",
		})
	})

	T.Run("with empty search results", func(t *testing.T) {
		t.Parallel()

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"hits":[],"nbHits":0,"page":0,"nbPages":0,"hitsPerPage":20,"processingTimeMS":1}`))
		})

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}

		im, _ := buildTestIndexManagerWithMockServer(t, handler, cb)

		results, err := im.Search(context.Background(), textsearch.SearchRequest{Query: "test query"})
		test.NoError(t, err)
		test.NotNil(t, results)
		test.SliceEmpty(t, results.Hits)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())
	})

	T.Run("with multiple search results", func(t *testing.T) {
		t.Parallel()

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"hits":[{"objectID":"abc","name":"first"},{"objectID":"def","name":"second"},{"objectID":"ghi","name":"third"}],"nbHits":3,"page":0,"nbPages":1,"hitsPerPage":20,"processingTimeMS":1}`))
		})

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}

		im, _ := buildTestIndexManagerWithMockServer(t, handler, cb)

		results, err := im.Search(context.Background(), textsearch.SearchRequest{Query: "test query"})
		test.NoError(t, err)
		test.SliceLen(t, 3, results.Hits)
		test.EqOp(t, "abc", results.Hits[0].ID)
		test.EqOp(t, "first", results.Hits[0].Name)
		test.EqOp(t, "def", results.Hits[1].ID)
		test.EqOp(t, "second", results.Hits[1].Name)
		test.EqOp(t, "ghi", results.Hits[2].ID)
		test.EqOp(t, "third", results.Hits[2].Name)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())
	})

	T.Run("when unmarshalling search result fails", func(t *testing.T) {
		t.Parallel()

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"hits":[{"objectID":"123","name":["not","a","string"]}],"nbHits":1,"page":0,"nbPages":1,"hitsPerPage":20,"processingTimeMS":1}`))
		})

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
		}

		im, obs := buildTestIndexManagerWithMockServer(t, handler, cb)

		results, err := im.Search(context.Background(), textsearch.SearchRequest{Query: "test query"})
		test.Error(t, err)
		test.Nil(t, results)
		test.SliceLen(t, 1, cb.CannotProceedCalls())

		// Even though the search failed, the query must still have been observed.
		obs.ObservedOperationWithData(t, map[string]any{
			keys.SearchQueryKey: "test query",
		})
	})

	T.Run("with successful search results without objectID", func(t *testing.T) {
		t.Parallel()

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"hits":[{"name":"example"}],"nbHits":1,"page":0,"nbPages":1,"hitsPerPage":20,"processingTimeMS":1}`))
		})

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}

		im, _ := buildTestIndexManagerWithMockServer(t, handler, cb)

		results, err := im.Search(context.Background(), textsearch.SearchRequest{Query: "test query"})
		test.NoError(t, err)
		test.NotNil(t, results)
		test.SliceLen(t, 1, results.Hits)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())
	})
}

func TestIndexManager_Delete(T *testing.T) {
	T.Parallel()

	T.Run("with broken circuit breaker", func(t *testing.T) {
		t.Parallel()

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return true },
		}

		im := buildTestIndexManagerWithCircuitBreaker(t, cb)

		err := im.Delete(context.Background(), "id")
		test.Error(t, err)
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with invalid credentials", func(t *testing.T) {
		t.Parallel()

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		im := buildTestIndexManagerWithCircuitBreaker(t, cb)

		err := im.Delete(context.Background(), "some-id")
		test.Error(t, err)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.FailedCalls())
	})

	T.Run("with successful deletion", func(t *testing.T) {
		t.Parallel()

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"deletedAt":"2021-01-01T00:00:00Z","taskID":123}`))
		})

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}

		im, _ := buildTestIndexManagerWithMockServer(t, handler, cb)

		err := im.Delete(context.Background(), "some-id")
		test.NoError(t, err)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())
	})
}

func TestIndexManager_Wipe(T *testing.T) {
	T.Parallel()

	T.Run("with broken circuit breaker", func(t *testing.T) {
		t.Parallel()

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return true },
		}

		im := buildTestIndexManagerWithCircuitBreaker(t, cb)

		err := im.Wipe(context.Background())
		test.Error(t, err)
		test.ErrorIs(t, err, circuitbreaking.ErrCircuitBroken)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
	})

	T.Run("with invalid credentials", func(t *testing.T) {
		t.Parallel()

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		im := buildTestIndexManagerWithCircuitBreaker(t, cb)

		err := im.Wipe(context.Background())
		test.Error(t, err)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.FailedCalls())
	})

	T.Run("with successful wipe", func(t *testing.T) {
		t.Parallel()

		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"updatedAt":"2021-01-01T00:00:00Z","taskID":123}`))
		})

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}

		im, _ := buildTestIndexManagerWithMockServer(t, handler, cb)

		err := im.Wipe(context.Background())
		test.NoError(t, err)
		test.SliceLen(t, 1, cb.CannotProceedCalls())
		test.SliceLen(t, 1, cb.SucceededCalls())
	})
}

func TestIndexManager_Search_Pagination(T *testing.T) {
	T.Parallel()

	T.Run("issues a next cursor while pages remain", func(t *testing.T) {
		t.Parallel()

		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"hits":[{"objectID":"123"}],"nbHits":3,"page":0,"nbPages":3,"hitsPerPage":1,"processingTimeMS":1}`))
		})

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}

		im, _ := buildTestIndexManagerWithMockServer(t, handler, cb)

		results, err := im.Search(context.Background(), textsearch.SearchRequest{Query: "test", Limit: 1})
		must.NoError(t, err)
		test.False(t, results.Done())

		// The cursor is opaque to callers but must resume where this page ended.
		position, decodeErr := textsearch.DecodeCursor("algolia", results.NextCursor)
		must.NoError(t, decodeErr)
		test.EqOp(t, 1, position)
	})

	T.Run("the last page has no next cursor", func(t *testing.T) {
		t.Parallel()

		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"hits":[{"objectID":"123"}],"nbHits":3,"page":2,"nbPages":3,"hitsPerPage":1,"processingTimeMS":1}`))
		})

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}

		im, _ := buildTestIndexManagerWithMockServer(t, handler, cb)

		cursor, err := textsearch.EncodeCursor("algolia", 2)
		must.NoError(t, err)

		results, searchErr := im.Search(context.Background(), textsearch.SearchRequest{Query: "test", Limit: 1, Cursor: cursor})
		must.NoError(t, searchErr)
		test.True(t, results.Done())
	})

	T.Run("an empty page ends the walk even when nbPages disagrees", func(t *testing.T) {
		t.Parallel()

		// Algolia reports nbPages from the total, so a page past the end still
		// claims more pages exist. Without the len check that is an endless walk.
		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"hits":[],"nbHits":3,"page":0,"nbPages":3,"hitsPerPage":1,"processingTimeMS":1}`))
		})

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}

		im, _ := buildTestIndexManagerWithMockServer(t, handler, cb)

		results, err := im.Search(context.Background(), textsearch.SearchRequest{Query: "test", Limit: 1})
		must.NoError(t, err)
		test.True(t, results.Done())
	})

	T.Run("a cursor from another backend is refused", func(t *testing.T) {
		t.Parallel()

		handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"hits":[],"nbHits":0,"page":0,"nbPages":0,"hitsPerPage":20,"processingTimeMS":1}`))
		})

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			FailedFunc:        func() {},
		}

		im, _ := buildTestIndexManagerWithMockServer(t, handler, cb)

		cursor, err := textsearch.EncodeCursor("elasticsearch", 10)
		must.NoError(t, err)

		results, searchErr := im.Search(context.Background(), textsearch.SearchRequest{Query: "test", Cursor: cursor})
		test.ErrorIs(t, searchErr, textsearch.ErrInvalidCursor)
		test.Nil(t, results)
	})

	T.Run("the requested limit reaches the query", func(t *testing.T) {
		t.Parallel()

		var gotBody string
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			gotBody = string(body)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"hits":[],"nbHits":0,"page":0,"nbPages":0,"hitsPerPage":7,"processingTimeMS":1}`))
		})

		cb := &circuitbreakingmock.CircuitBreakerMock{
			CannotProceedFunc: func() bool { return false },
			SucceededFunc:     func() {},
		}

		im, _ := buildTestIndexManagerWithMockServer(t, handler, cb)

		_, err := im.Search(context.Background(), textsearch.SearchRequest{Query: "test", Limit: 7})
		must.NoError(t, err)
		test.StrContains(t, gotBody, "hitsPerPage=7")
	})
}

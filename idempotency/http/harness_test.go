package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/primandproper/platform-go/v13/cache"
	cachememory "github.com/primandproper/platform-go/v13/cache/memory"
	cachemock "github.com/primandproper/platform-go/v13/cache/mock"
	"github.com/primandproper/platform-go/v13/distributedlock"
	dlmemory "github.com/primandproper/platform-go/v13/distributedlock/memory"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/idempotency"

	"github.com/shoenig/test/must"
)

const testKey = "d3f1a0c4-5b6e-4a2f-9c8d-1e2f3a4b5c6d"

// newTestManager builds an HTTP manager over in-memory infrastructure.
func newTestManager(tb testing.TB, opts ...idempotency.Option) *idempotency.Manager[Response] {
	tb.Helper()

	store, err := cachememory.NewInMemoryCache[idempotency.Record[Response]](0)
	must.NoError(tb, err)

	locker, err := dlmemory.NewLocker()
	must.NoError(tb, err)

	scoped, err := distributedlock.NewScopedLocker(locker)
	must.NoError(tb, err)

	m, err := NewManager(store, scoped, opts...)
	must.NoError(tb, err)

	return m
}

// newFailingStoreManager builds a manager whose store cannot be read, for
// exercising the store failure policy.
func newFailingStoreManager(tb testing.TB, opts ...idempotency.Option) *idempotency.Manager[Response] {
	tb.Helper()

	store := &cachemock.CacheMock[idempotency.Record[Response]]{
		GetFunc: func(context.Context, string) (*idempotency.Record[Response], error) {
			return nil, platformerrors.New("store is down")
		},
		SetFunc: func(context.Context, string, *idempotency.Record[Response], ...cache.WriteOption) error {
			return nil
		},
		DeleteFunc: func(context.Context, string) error { return nil },
	}

	locker, err := dlmemory.NewLocker()
	must.NoError(tb, err)

	scoped, err := distributedlock.NewScopedLocker(locker)
	must.NoError(tb, err)

	m, err := NewManager(store, scoped, opts...)
	must.NoError(tb, err)

	return m
}

// countingHandler records how many times it ran, so a replay is
// distinguishable from a re-execution.
type countingHandler struct {
	handler http.HandlerFunc
	calls   atomic.Int64
}

func newCountingHandler(handler http.HandlerFunc) *countingHandler {
	return &countingHandler{handler: handler}
}

func (h *countingHandler) ServeHTTP(res http.ResponseWriter, req *http.Request) {
	h.calls.Add(1)
	h.handler(res, req)
}

func (h *countingHandler) Calls() int64 { return h.calls.Load() }

// okHandler writes 201 and a small JSON body, the shape this package exists
// for.
func okHandler() *countingHandler {
	return newCountingHandler(func(res http.ResponseWriter, _ *http.Request) {
		res.Header().Set("Content-Type", "application/json")
		res.WriteHeader(http.StatusCreated)
		_, _ = res.Write([]byte(`{"id":"ch_1"}`))
	})
}

// wrap installs the middleware around handler.
func wrap(tb testing.TB, handler http.Handler, manager *idempotency.Manager[Response], opts ...Option) http.Handler {
	tb.Helper()

	// NewMiddleware is declared as returning routing.Middleware, so the
	// compiler already proves the signature a Router accepts. No assertion
	// here would add anything.
	mw, err := NewMiddleware(manager, opts...)
	must.NoError(tb, err)

	return mw(handler)
}

// do runs one request through handler and returns the recorder.
func do(handler http.Handler, req *http.Request) *httptest.ResponseRecorder {
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	return res
}

// post builds a keyed POST.
func post(ctx context.Context, key, path, body string) *http.Request {
	req := httptest.NewRequestWithContext(ctx, http.MethodPost, path, strings.NewReader(body))
	if key != "" {
		req.Header.Set(HeaderName, key)
	}

	return req
}

package http

import (
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/filtering"
	"github.com/primandproper/platform-go/v13/operations"
	operationsmock "github.com/primandproper/platform-go/v13/operations/mock"
	"github.com/primandproper/platform-go/v13/routing"
	"github.com/primandproper/platform-go/v13/routing/backends/chi"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// ownerKey is the context key these tests put an owner under, standing in for
// whatever a consumer's authentication middleware does.
type ownerContextKey struct{}

// resolverFromContext is the OwnerResolver the tests wire in.
func resolverFromContext(ctx context.Context) (string, error) {
	owner, ok := ctx.Value(ownerContextKey{}).(string)
	if !ok {
		return "", platformerrors.New("no owner on the request")
	}

	return owner, nil
}

// asOwner wraps a handler so every request under it carries an owner.
func asOwner(owner string, next nethttp.Handler) nethttp.Handler {
	return nethttp.HandlerFunc(func(res nethttp.ResponseWriter, req *nethttp.Request) {
		next.ServeHTTP(res, req.WithContext(context.WithValue(req.Context(), ownerContextKey{}, owner)))
	})
}

// mount builds a router with the handlers on it, under an owner.
func mount(t *testing.T, svc operations.Service, owner string, opts ...Option) nethttp.Handler {
	t.Helper()

	backend := chi.NewBackend(&chi.Config{ServiceName: "operations-test"})
	router := routing.New(backend, encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON))

	handlers, err := New(svc, append([]Option{WithOwnerResolver(resolverFromContext)}, opts...)...)
	must.NoError(t, err)

	handlers.Mount(router)

	must.NoError(t, router.Err())

	return asOwner(owner, router.Handler())
}

// serviceReturning is a Service mock whose Get answers with one operation.
func serviceReturning(op *operations.Operation) *operationsmock.ServiceMock {
	svc := &operationsmock.ServiceMock{}

	svc.GetFunc = func(_ context.Context, id string) (*operations.Operation, error) {
		if op != nil && op.ID == id {
			return op, nil
		}

		return nil, platformerrors.Wrapf(operations.ErrOperationNotFound, "operation %q", id)
	}

	return svc
}

func TestNew(T *testing.T) {
	T.Parallel()

	T.Run("rejects a nil service", func(t *testing.T) {
		t.Parallel()

		_, err := New(nil, WithOwnerResolver(Unscoped))

		test.ErrorIs(t, err, operations.ErrNilService)
	})

	// The resolver has no default on purpose: a default of "no scoping" would
	// make the safe wiring and the wiring that serves every tenant's operations
	// to anyone look identical.
	T.Run("requires an owner resolver", func(t *testing.T) {
		t.Parallel()

		_, err := New(&operationsmock.ServiceMock{})

		test.ErrorIs(t, err, ErrNilOwnerResolver)
	})

	T.Run("Unscoped is a resolver", func(t *testing.T) {
		t.Parallel()

		handlers, err := New(&operationsmock.ServiceMock{}, WithOwnerResolver(Unscoped))

		must.NoError(t, err)
		must.NotNil(t, handlers)

		owner, err := Unscoped(t.Context())
		must.NoError(t, err)
		test.EqOp(t, "", owner)
	})
}

func TestHandlers_get(T *testing.T) {
	T.Parallel()

	T.Run("serves the owner's own operation", func(t *testing.T) {
		t.Parallel()

		op := &operations.Operation{ID: "op1", Owner: "u1", Kind: "export", State: operations.StateRunning}
		handler := mount(t, serviceReturning(op), "u1")

		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(nethttp.MethodGet, "/operations/op1", nethttp.NoBody))

		test.EqOp(t, nethttp.StatusOK, res.Code)
		test.StrContains(t, res.Body.String(), `"id":"op1"`)
		test.StrContains(t, res.Body.String(), `"done":false`)
	})

	// The one that matters: a 403 for an operation that exists and a 404 for one
	// that does not is an oracle telling whoever is guessing IDs which of their
	// guesses are real. Both answers have to be the same.
	T.Run("somebody else's operation is a 404, not a 403", func(t *testing.T) {
		t.Parallel()

		op := &operations.Operation{ID: "op1", Owner: "u1", Kind: "export", State: operations.StateRunning}
		handler := mount(t, serviceReturning(op), "u2")

		theirs := httptest.NewRecorder()
		handler.ServeHTTP(theirs, httptest.NewRequest(nethttp.MethodGet, "/operations/op1", nethttp.NoBody))

		missing := httptest.NewRecorder()
		handler.ServeHTTP(missing, httptest.NewRequest(nethttp.MethodGet, "/operations/nope", nethttp.NoBody))

		test.EqOp(t, nethttp.StatusNotFound, theirs.Code)
		test.EqOp(t, nethttp.StatusNotFound, missing.Code)
		test.EqOp(t, theirs.Body.String(), missing.Body.String())
	})

	T.Run("a resolver that fails fails the request", func(t *testing.T) {
		t.Parallel()

		op := &operations.Operation{ID: "op1", Owner: "u1", State: operations.StateRunning}

		backend := chi.NewBackend(&chi.Config{ServiceName: "operations-test"})
		router := routing.New(backend, encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON))

		handlers, err := New(serviceReturning(op), WithOwnerResolver(resolverFromContext))
		must.NoError(t, err)

		handlers.Mount(router)

		// No owner on the context at all, which is what an unauthenticated
		// request reaching this surface looks like.
		res := httptest.NewRecorder()
		router.Handler().ServeHTTP(res, httptest.NewRequest(nethttp.MethodGet, "/operations/op1", nethttp.NoBody))

		test.Greater(t, 399, res.Code)
	})
}

func TestHandlers_cancel(T *testing.T) {
	T.Parallel()

	T.Run("cancels the owner's own operation", func(t *testing.T) {
		t.Parallel()

		op := &operations.Operation{ID: "op1", Owner: "u1", State: operations.StateRunning}

		svc := serviceReturning(op)

		var cancelled string

		svc.CancelFunc = func(_ context.Context, id string) (*operations.Operation, error) {
			cancelled = id

			return &operations.Operation{
				ID: id, Owner: "u1", State: operations.StateRunning, CancelRequested: true,
			}, nil
		}

		handler := mount(t, svc, "u1")

		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(nethttp.MethodPost, "/operations/op1/cancel", nethttp.NoBody))

		test.EqOp(t, nethttp.StatusOK, res.Code)
		test.EqOp(t, "op1", cancelled)
		test.StrContains(t, res.Body.String(), `"cancelRequested":true`)
	})

	// Cancel is a write, and a write reached by a guessed ID would be a way to
	// stop other people's work without ever being able to read it.
	T.Run("refuses somebody else's operation without cancelling it", func(t *testing.T) {
		t.Parallel()

		op := &operations.Operation{ID: "op1", Owner: "u1", State: operations.StateRunning}

		svc := serviceReturning(op)

		called := false

		svc.CancelFunc = func(context.Context, string) (*operations.Operation, error) {
			called = true

			return nil, nil
		}

		handler := mount(t, svc, "u2")

		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(nethttp.MethodPost, "/operations/op1/cancel", nethttp.NoBody))

		test.EqOp(t, nethttp.StatusNotFound, res.Code)
		test.False(t, called)
	})
}

func TestHandlers_list(T *testing.T) {
	T.Parallel()

	T.Run("scopes the listing to the resolved owner", func(t *testing.T) {
		t.Parallel()

		svc := &operationsmock.ServiceMock{}

		var seen *operations.ListScope

		svc.ListFunc = func(
			_ context.Context,
			scope *operations.ListScope,
			filter *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[operations.Operation], error) {
			seen = scope

			return filtering.NewQueryFilteredResult(
				[]*operations.Operation{}, 0, 0,
				func(o *operations.Operation) string { return o.ID }, filter,
			), nil
		}

		handler := mount(t, svc, "u1")

		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(nethttp.MethodGet, "/operations?kind=export", nethttp.NoBody))

		test.EqOp(t, nethttp.StatusOK, res.Code)
		must.NotNil(t, seen)
		test.EqOp(t, "u1", seen.Owner)
		test.EqOp(t, "export", seen.Kind)
	})

	T.Run("narrows by state", func(t *testing.T) {
		t.Parallel()

		svc := &operationsmock.ServiceMock{}

		var seen *operations.ListScope

		svc.ListFunc = func(
			_ context.Context,
			scope *operations.ListScope,
			filter *filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[operations.Operation], error) {
			seen = scope

			return filtering.NewQueryFilteredResult(
				[]*operations.Operation{}, 0, 0,
				func(o *operations.Operation) string { return o.ID }, filter,
			), nil
		}

		handler := mount(t, svc, "u1")

		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(nethttp.MethodGet, "/operations?state=failed", nethttp.NoBody))

		test.EqOp(t, nethttp.StatusOK, res.Code)
		must.NotNil(t, seen)
		must.SliceLen(t, 1, seen.States)
		test.EqOp(t, operations.StateFailed, seen.States[0])
	})

	// A state nothing writes would silently match nothing, which reads as "you
	// have no failed operations" rather than as "that is not a state".
	T.Run("rejects a state that is not one", func(t *testing.T) {
		t.Parallel()

		svc := &operationsmock.ServiceMock{}

		called := false

		svc.ListFunc = func(
			context.Context,
			*operations.ListScope,
			*filtering.QueryFilter,
		) (*filtering.QueryFilteredResult[operations.Operation], error) {
			called = true

			return nil, nil
		}

		handler := mount(t, svc, "u1")

		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(nethttp.MethodGet, "/operations?state=nonsense", nethttp.NoBody))

		test.Greater(t, 399, res.Code)
		test.False(t, called)
	})
}

func TestHandlers_Accepted(T *testing.T) {
	T.Parallel()

	handlers, err := New(&operationsmock.ServiceMock{}, WithOwnerResolver(Unscoped))
	must.NoError(T, err)

	test.Nil(T, handlers.Accepted(nil))

	acceptance := handlers.Accepted(&operations.Operation{ID: "op1", State: operations.StatePending})

	must.NotNil(T, acceptance)
	test.EqOp(T, "/operations/op1", acceptance.Location)
	test.EqOp(T, "/operations/op1/events", acceptance.Events)

	// The URLs are relative and rooted at the mount point, which is the one
	// thing that stays correct behind every proxy and path rewrite a deployment
	// might put in front of this.
	encoded, err := json.Marshal(acceptance)
	must.NoError(T, err)
	test.StrContains(T, string(encoded), `"location":"/operations/op1"`)
}

func TestHandlers_basePath(T *testing.T) {
	T.Parallel()

	op := &operations.Operation{ID: "op1", Owner: "u1", State: operations.StateRunning}

	handler := mount(T, serviceReturning(op), "u1", WithBasePath("/v1/jobs"))

	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(nethttp.MethodGet, "/v1/jobs/op1", nethttp.NoBody))

	test.EqOp(T, nethttp.StatusOK, res.Code)

	// Accepted follows the mount point, so a consumer's own 202 keeps pointing
	// at the endpoints that actually exist.
	handlers, err := New(serviceReturning(op),
		WithOwnerResolver(Unscoped), WithBasePath("/v1/jobs"))
	must.NoError(T, err)

	test.EqOp(T, "/v1/jobs/op1", handlers.Accepted(op).Location)
}

func TestOperationIDFromPath(T *testing.T) {
	T.Parallel()

	test.EqOp(T, "op1", operationIDFromPath("/operations/op1/events"))
	test.EqOp(T, "op1", operationIDFromPath("/v1/jobs/op1/events"))
	test.EqOp(T, "op1", operationIDFromPath("/operations/op1/events/"))
}

func TestHandlers_openAPI(T *testing.T) {
	T.Parallel()

	backend := chi.NewBackend(&chi.Config{ServiceName: "operations-test"})
	router := routing.New(backend, encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON))

	watcher, err := operations.NewWatcher(T.Context(), &operations.WatcherConfig{}, stubStore{})
	must.NoError(T, err)

	T.Cleanup(func() { _ = watcher.Close() })

	handlers, err := New(&operationsmock.ServiceMock{},
		WithOwnerResolver(Unscoped), WithWatcher(watcher))
	must.NoError(T, err)

	handlers.Mount(router)
	must.NoError(T, router.Err())

	spec, err := router.MarshalSpec()
	must.NoError(T, err)

	document := string(spec)

	// The typed routes carry OpenAPI for free.
	test.StrContains(T, document, "/operations/{operationID}")
	test.StrContains(T, document, "/operations/{operationID}/cancel")

	// The stream cannot go through the typed registration, so it is written into
	// the document by hand — and describing it as text/event-stream is the point
	// of doing so, since that is not something a generated client guesses.
	test.StrContains(T, document, "/operations/{operationID}/events")
	test.StrContains(T, document, "text/event-stream")
}

// The point of the per-route methods: a consumer mounts the reads and leaves the
// cancellation off, and gets a router that serves the first and 405s the second.
func TestHandlers_selectiveMount(T *testing.T) {
	T.Parallel()

	op := &operations.Operation{ID: "op1", Owner: "u1", State: operations.StateRunning}

	backend := chi.NewBackend(&chi.Config{ServiceName: "operations-test"})
	router := routing.New(backend, encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON))

	handlers, err := New(serviceReturning(op), WithOwnerResolver(resolverFromContext))
	must.NoError(T, err)

	getRoute := handlers.MountGet(router)
	handlers.MountList(router)

	must.NoError(T, router.Err())

	// The Route describes what was registered, so a consumer building links or
	// tests off it is pointed at the endpoint that exists.
	must.NotNil(T, getRoute)
	test.EqOp(T, nethttp.MethodGet, getRoute.Method)
	test.StrContains(T, getRoute.Path, "/operations/")

	handler := asOwner("u1", router.Handler())

	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(nethttp.MethodGet, "/operations/op1", nethttp.NoBody))
	test.EqOp(T, nethttp.StatusOK, res.Code)

	// Never mounted, so nothing answers it.
	res = httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(nethttp.MethodPost, "/operations/op1/cancel", nethttp.NoBody))
	test.Greater(T, 399, res.Code)
}

// Mount reports every route it registered, and omits the stream when there is no
// Watcher to serve it.
func TestHandlers_mountReportsRoutes(T *testing.T) {
	T.Parallel()

	op := &operations.Operation{ID: "op1", Owner: "u1", State: operations.StateRunning}

	backend := chi.NewBackend(&chi.Config{ServiceName: "operations-test"})
	router := routing.New(backend, encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON))

	handlers, err := New(serviceReturning(op), WithOwnerResolver(resolverFromContext))
	must.NoError(T, err)

	test.SliceLen(T, 3, handlers.Mount(router))
	must.NoError(T, router.Err())

	// And with one, the stream is the fourth.
	watched := routing.New(
		chi.NewBackend(&chi.Config{ServiceName: "operations-test"}),
		encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON),
	)

	watcher, err := operations.NewWatcher(T.Context(), &operations.WatcherConfig{}, stubStore{})
	must.NoError(T, err)

	withWatcher, err := New(serviceReturning(op),
		WithOwnerResolver(resolverFromContext),
		WithWatcher(watcher))
	must.NoError(T, err)

	test.SliceLen(T, 4, withWatcher.Mount(watched))
	must.NoError(T, watched.Err())
}

// Without a Watcher the endpoint is not registered at all: one that accepted a
// connection and then said nothing forever is worse than a 404, because a client
// cannot tell it from an operation that is taking a long time.
func TestHandlers_noWatcherNoStream(T *testing.T) {
	T.Parallel()

	op := &operations.Operation{ID: "op1", Owner: "u1", State: operations.StateRunning}
	handler := mount(T, serviceReturning(op), "u1")

	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(nethttp.MethodGet, "/operations/op1/events", nethttp.NoBody))

	test.EqOp(T, nethttp.StatusNotFound, res.Code)
}

func TestHandlers_stream(T *testing.T) {
	T.Parallel()

	T.Run("streams snapshots and ends after the terminal one", func(t *testing.T) {
		t.Parallel()

		store := newStreamingStore("op1", "u1")

		watcher, err := operations.NewWatcher(t.Context(), &operations.WatcherConfig{
			Poll:            100 * time.Millisecond,
			MinReadInterval: time.Millisecond,
		}, store)
		must.NoError(t, err)

		t.Cleanup(func() { _ = watcher.Close() })

		go func() { _ = watcher.Run(t.Context()) }()

		svc := &operationsmock.ServiceMock{}
		svc.GetFunc = store.Get

		handler := mount(t, svc, "u1", WithWatcher(watcher))

		go func() {
			time.Sleep(150 * time.Millisecond)
			store.finish()
		}()

		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(nethttp.MethodGet, "/operations/op1/events", nethttp.NoBody))

		body := res.Body.String()

		test.StrContains(t, res.Header().Get("Content-Type"), "text/event-stream")
		test.StrContains(t, body, "event: "+EventOperation)
		test.StrContains(t, body, `"state":"succeeded"`)
		test.StrContains(t, body, `"done":true`)
	})

	// The same ownership check the polling endpoint makes, made before the
	// upgrade so a refusal is an ordinary status rather than a stream that opens
	// and immediately closes.
	T.Run("refuses somebody else's operation before upgrading", func(t *testing.T) {
		t.Parallel()

		store := newStreamingStore("op1", "u1")

		watcher, err := operations.NewWatcher(t.Context(), &operations.WatcherConfig{}, store)
		must.NoError(t, err)

		t.Cleanup(func() { _ = watcher.Close() })

		svc := &operationsmock.ServiceMock{}
		svc.GetFunc = store.Get

		handler := mount(t, svc, "u2", WithWatcher(watcher))

		res := httptest.NewRecorder()
		handler.ServeHTTP(res, httptest.NewRequest(nethttp.MethodGet, "/operations/op1/events", nethttp.NoBody))

		test.EqOp(t, nethttp.StatusNotFound, res.Code)
		test.StrContains(t, res.Header().Get("Content-Type"), "application/json")
	})
}

package http

import (
	"bytes"
	"context"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"path"
	"sync/atomic"
	"testing"

	"github.com/primandproper/platform-go/v14/mediaregistry"
	mediaregistrymock "github.com/primandproper/platform-go/v14/mediaregistry/mock"

	"github.com/primandproper/primitives-go/database"
	databasemock "github.com/primandproper/primitives-go/database/mock"
	"github.com/primandproper/primitives-go/encoding"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/routing"
	"github.com/primandproper/primitives-go/routing/backends/chi"
	"github.com/primandproper/primitives-go/tenancy"
	"github.com/primandproper/primitives-go/uploads"

	"github.com/shoenig/test/must"
)

// The fixtures every test in the package builds from.
const (
	testObjectID   = "object_1"
	testObjectKey  = "receipts/acme/invoice.pdf"
	testOwnerID    = "user_1"
	testOtherOwner = "user_2"
	testTenant     = "tenant_1"
	testOtherTntnt = "tenant_2"
	testPDF        = "application/pdf"
)

// testBody is the object's bytes. Long enough that a range can ask for a middle
// slice of it and mean something.
const testBody = "0123456789abcdefghijklmnopqrstuvwxyz"

// callerContextKey is what these tests put a caller under, standing in for
// whatever a consumer's authentication middleware does.
type callerContextKey struct{}

// resolverFromContext is the CallerResolver the tests wire in.
func resolverFromContext(ctx context.Context) (Caller, error) {
	caller, ok := ctx.Value(callerContextKey{}).(Caller)
	if !ok {
		return Caller{}, platformerrors.New("no caller on the request")
	}

	return caller, nil
}

// asCaller wraps a handler so every request under it carries a caller.
func asCaller(caller Caller, next nethttp.Handler) nethttp.Handler {
	return nethttp.HandlerFunc(func(res nethttp.ResponseWriter, req *nethttp.Request) {
		next.ServeHTTP(res, req.WithContext(context.WithValue(req.Context(), callerContextKey{}, caller)))
	})
}

// memoryObjects is an UploadManager over objects held in memory. It implements
// nothing but the core interface, which is what makes it the double for a
// provider that cannot open a range.
//
// opens counts what reached storage, which is how the guard tests assert that
// nothing did.
type memoryObjects struct {
	objects map[string][]byte
	err     error
	opens   atomic.Int64
}

var _ uploads.UploadManager = (*memoryObjects)(nil)

func newObjects() *memoryObjects {
	return &memoryObjects{objects: map[string][]byte{testObjectKey: []byte(testBody)}}
}

func (m *memoryObjects) Open(_ context.Context, key string) (io.ReadCloser, error) {
	m.opens.Add(1)

	if m.err != nil {
		return nil, m.err
	}

	object, ok := m.objects[key]
	if !ok {
		return nil, platformerrors.Newf("no object at %q", key)
	}

	return io.NopCloser(bytes.NewReader(object)), nil
}

func (m *memoryObjects) Save(context.Context, string, io.Reader, ...uploads.SaveOption) error {
	return nil
}

func (m *memoryObjects) Delete(context.Context, string) error { return nil }

func (m *memoryObjects) Exists(context.Context, string) (bool, error) { return true, nil }

func (m *memoryObjects) Close() error { return nil }

// rangingObjects is the same store with the optional capability, which is what
// objectstorage.Uploader has and what makes the ranged path reachable.
//
// closed counts the readers it handed out that were closed again, so a test can
// assert the handler releases what it opens.
type rangingObjects struct {
	*memoryObjects
	closed atomic.Int64
}

var (
	_ uploads.UploadManager = (*rangingObjects)(nil)
	_ uploads.RangeReader   = (*rangingObjects)(nil)
)

func newRangingObjects() *rangingObjects {
	return &rangingObjects{memoryObjects: newObjects()}
}

func (m *rangingObjects) OpenRange(_ context.Context, key string, offset, length int64) (io.ReadCloser, error) {
	m.opens.Add(1)

	if m.err != nil {
		return nil, m.err
	}

	object, ok := m.objects[key]
	if !ok {
		return nil, platformerrors.Newf("no object at %q", key)
	}

	if offset > int64(len(object)) {
		offset = int64(len(object))
	}

	object = object[offset:]

	if length >= 0 && length < int64(len(object)) {
		object = object[:length]
	}

	return &countingCloser{Reader: bytes.NewReader(object), closed: &m.closed}, nil
}

// countingCloser records that it was closed.
type countingCloser struct {
	io.Reader
	closed *atomic.Int64
}

func (c *countingCloser) Close() error {
	c.closed.Add(1)

	return nil
}

// testObject is the row the store answers with.
func testObject() *mediaregistry.Object {
	return &mediaregistry.Object{
		ID:          testObjectID,
		Key:         testObjectKey,
		ContentType: testPDF,
		OwnerID:     testOwnerID,
		Scope:       tenancy.Of(testTenant),
		Size:        int64(len(testBody)),
	}
}

// storeReturning is a Store whose GetObject answers with one row, in one scope.
// Every other scope reads as absent, which is what the SQL store does with a
// row it cannot see.
func storeReturning(object *mediaregistry.Object) *mediaregistrymock.StoreMock {
	store := &mediaregistrymock.StoreMock{}

	store.GetObjectFunc = func(
		_ context.Context,
		_ database.SQLQueryExecutor,
		scope tenancy.Scope,
		objectID string,
	) (*mediaregistry.Object, error) {
		if object != nil && object.ID == objectID && scope == object.Scope {
			return object, nil
		}

		return nil, platformerrors.Wrapf(mediaregistry.ErrObjectNotFound, "object %q", objectID)
	}

	return store
}

// readerOnlyClient is a database.Client that does nothing but hand back a
// reader, which is all this package asks of one.
func readerOnlyClient() *databasemock.ClientMock {
	client := &databasemock.ClientMock{}
	client.ReaderFunc = func() database.SQLQueryExecutor { return nil }

	return client
}

// mount builds a router with the handler on it, under a caller.
func mount(
	t *testing.T,
	store mediaregistry.Store,
	manager uploads.UploadManager,
	caller Caller,
	opts ...Option,
) nethttp.Handler {
	t.Helper()

	backend := chi.NewBackend(&chi.Config{ServiceName: "uploads-registry-test"})
	router := routing.New(backend, encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON))

	handler, err := New(store, readerOnlyClient(), manager,
		append([]Option{WithCallerResolver(resolverFromContext)}, opts...)...)
	must.NoError(t, err)

	handler.Mount(router)

	must.NoError(t, router.Err())

	return asCaller(caller, router.Handler())
}

// get issues a request for an object, with whatever headers the test adds.
func get(t *testing.T, handler nethttp.Handler, objectID string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), nethttp.MethodGet, path.Join(BasePath, objectID), nethttp.NoBody)
	for name, value := range headers {
		req.Header.Set(name, value)
	}

	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)

	return res
}

// ownedCaller is the caller the fixture row entitles.
func ownedCaller() Caller {
	return Caller{Scope: tenancy.Of(testTenant), PrincipalID: testOwnerID}
}

// serveDirect calls the handler without the router in front of it and without
// the middleware that puts a caller on the request. It is how the resolver is
// made to fail, and how a path the router would never route can still be handed
// to the handler.
func serveDirect(t *testing.T, handler *Handler, requestPath string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), nethttp.MethodGet, requestPath, nethttp.NoBody)
	res := httptest.NewRecorder()

	handler.serve(res, req)

	return res
}

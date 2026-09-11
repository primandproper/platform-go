package http

import (
	nethttp "net/http"
	"net/http/httptest"
	"testing"

	"github.com/primandproper/primitives-go/v2/encoding"
	"github.com/primandproper/primitives-go/v2/routing"
	"github.com/primandproper/primitives-go/v2/routing/backends/chi"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// router builds an empty router of the kind these tests mount onto.
func router(t *testing.T) *routing.Router {
	t.Helper()

	return routing.New(
		chi.NewBackend(&chi.Config{ServiceName: "uploads-registry-test"}),
		encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON),
	)
}

func TestHandler_Mount(T *testing.T) {
	T.Parallel()

	T.Run("registers the route and describes it", func(t *testing.T) {
		t.Parallel()

		handler, err := New(storeReturning(testObject()), readerOnlyClient(), newRangingObjects(),
			WithCallerResolver(resolverFromContext))
		must.NoError(t, err)

		r := router(t)

		route := handler.Mount(r)
		must.NotNil(t, route)
		must.NoError(t, r.Err())

		test.EqOp(t, nethttp.MethodGet, route.Method)
		test.EqOp(t, "/objects/{objectID}", route.Path)
		test.EqOp(t, serveOperationID, route.OperationID)

		spec := r.Spec()
		must.NotNil(t, spec)

		item, ok := spec.Paths.MapOfPathItemValues[route.Path]
		must.True(t, ok)

		operation, ok := item.MapOfOperationValues["get"]
		must.True(t, ok)
		must.NotNil(t, operation.ID)
		test.EqOp(t, serveOperationID, *operation.ID)
		test.SliceLen(t, 1, operation.Parameters)
		test.MapContainsKey(t, operation.Responses.MapOfResponseOrRefValues, "200")
	})

	T.Run("mounts under the base path it was given", func(t *testing.T) {
		t.Parallel()

		handler, err := New(storeReturning(testObject()), readerOnlyClient(), newRangingObjects(),
			WithCallerResolver(resolverFromContext),
			WithBasePath("/api/v1/files"),
			WithTags("files", "media"))
		must.NoError(t, err)

		r := router(t)

		route := handler.Mount(r)
		test.EqOp(t, "/api/v1/files/{objectID}", route.Path)
		test.EqOp(t, "/api/v1/files/"+testObjectID, handler.ObjectPath(testObjectID))

		item, ok := r.Spec().Paths.MapOfPathItemValues[route.Path]
		must.True(t, ok)
		test.Eq(t, []string{"files", "media"}, item.MapOfOperationValues["get"].Tags)
	})

	T.Run("an empty base path leaves the default in place", func(t *testing.T) {
		t.Parallel()

		handler, err := New(storeReturning(testObject()), readerOnlyClient(), newRangingObjects(),
			WithCallerResolver(resolverFromContext), WithBasePath(""))
		must.NoError(t, err)

		test.EqOp(t, BasePath+"/"+testObjectID, handler.ObjectPath(testObjectID))
	})

	T.Run("the mounted route serves the object", func(t *testing.T) {
		t.Parallel()

		handler, err := New(storeReturning(testObject()), readerOnlyClient(), newRangingObjects(),
			WithCallerResolver(resolverFromContext), WithBasePath("/api/v1/files"))
		must.NoError(t, err)

		r := router(t)
		handler.Mount(r)
		must.NoError(t, r.Err())

		req := httptest.NewRequestWithContext(t.Context(), nethttp.MethodGet, "/api/v1/files/"+testObjectID, nethttp.NoBody)
		res := httptest.NewRecorder()

		asCaller(ownedCaller(), r.Handler()).ServeHTTP(res, req)

		test.EqOp(t, nethttp.StatusOK, res.Code)
		test.EqOp(t, testBody, res.Body.String())
	})
}

func TestObjectIDFromPath(T *testing.T) {
	T.Parallel()

	cases := map[string]struct {
		path     string
		expected string
	}{
		"the last segment":               {path: "/objects/object_1", expected: "object_1"},
		"under a prefix":                 {path: "/api/v1/files/object_1", expected: "object_1"},
		"a trailing slash names nothing": {path: "/objects/object_1/", expected: ""},
		"and neither does a bare one":    {path: "/objects/", expected: ""},
		"a bare segment":                 {path: "object_1", expected: "object_1"},
	}

	for name, testCase := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			test.EqOp(t, testCase.expected, objectIDFromPath(testCase.path))
		})
	}
}

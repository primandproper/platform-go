package http

import (
	"context"
	nethttp "net/http"
	"path"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/uploads/registry"
	registrymock "github.com/primandproper/platform-go/v14/uploads/registry/mock"

	"github.com/primandproper/primitives-go/database"
	platformerrors "github.com/primandproper/primitives-go/errors"
	"github.com/primandproper/primitives-go/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNew(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		handler, err := New(&registrymock.StoreMock{}, readerOnlyClient(), newObjects(),
			WithCallerResolver(resolverFromContext))
		must.NoError(t, err)
		must.NotNil(t, handler)
	})

	T.Run("refuses a nil store", func(t *testing.T) {
		t.Parallel()

		handler, err := New(nil, readerOnlyClient(), newObjects(), WithCallerResolver(resolverFromContext))
		test.Nil(t, handler)
		must.ErrorIs(t, err, registry.ErrNilStore)
	})

	T.Run("refuses a nil database client", func(t *testing.T) {
		t.Parallel()

		handler, err := New(&registrymock.StoreMock{}, nil, newObjects(), WithCallerResolver(resolverFromContext))
		test.Nil(t, handler)
		must.ErrorIs(t, err, registry.ErrNilDatabaseClient)
	})

	T.Run("refuses a nil upload manager", func(t *testing.T) {
		t.Parallel()

		handler, err := New(&registrymock.StoreMock{}, readerOnlyClient(), nil, WithCallerResolver(resolverFromContext))
		test.Nil(t, handler)
		must.ErrorIs(t, err, registry.ErrNilUploadManager)
	})

	T.Run("refuses handlers with no caller resolver", func(t *testing.T) {
		t.Parallel()

		handler, err := New(&registrymock.StoreMock{}, readerOnlyClient(), newObjects())
		test.Nil(t, handler)
		must.ErrorIs(t, err, ErrNilCallerResolver)
	})

	T.Run("ignores a nil option", func(t *testing.T) {
		t.Parallel()

		handler, err := New(&registrymock.StoreMock{}, readerOnlyClient(), newObjects(),
			WithCallerResolver(resolverFromContext), nil)
		must.NoError(t, err)
		must.NotNil(t, handler)
	})
}

func TestHandler_serve(T *testing.T) {
	T.Parallel()

	T.Run("serves the object to its owner", func(t *testing.T) {
		t.Parallel()

		manager := newRangingObjects()
		res := get(t, mount(t, storeReturning(testObject()), manager, ownedCaller()), testObjectID, nil)

		test.EqOp(t, nethttp.StatusOK, res.Code)
		test.EqOp(t, testBody, res.Body.String())
		test.EqOp(t, testPDF, res.Header().Get(contentTypeHeader))
		test.EqOp(t, dispositionInline, res.Header().Get(contentDispositionHeader))
		test.EqOp(t, contentTypeOptionsValue, res.Header().Get(contentTypeOptionsHeader))
		test.EqOp(t, cacheControlValue, res.Header().Get(cacheControlHeader))
		test.EqOp(t, "bytes", res.Header().Get(acceptRangesHeader))

		// Everything it opened, it closed.
		test.EqOp(t, manager.opens.Load(), manager.closed.Load())
	})

	T.Run("serves a byte range", func(t *testing.T) {
		t.Parallel()

		manager := newRangingObjects()
		handler := mount(t, storeReturning(testObject()), manager, ownedCaller())

		res := get(t, handler, testObjectID, map[string]string{"Range": "bytes=6-10"})

		test.EqOp(t, nethttp.StatusPartialContent, res.Code)
		test.EqOp(t, testBody[6:11], res.Body.String())
		test.EqOp(t, "bytes 6-10/36", res.Header().Get("Content-Range"))
		test.EqOp(t, testPDF, res.Header().Get(contentTypeHeader))

		// One range, one read of storage: nothing before the offset was opened
		// and nothing after the object was.
		test.EqOp(t, int64(1), manager.opens.Load())
	})

	T.Run("serves a suffix range", func(t *testing.T) {
		t.Parallel()

		handler := mount(t, storeReturning(testObject()), newRangingObjects(), ownedCaller())

		res := get(t, handler, testObjectID, map[string]string{"Range": "bytes=-4"})

		test.EqOp(t, nethttp.StatusPartialContent, res.Code)
		test.EqOp(t, testBody[len(testBody)-4:], res.Body.String())
	})

	T.Run("refuses a range past the end of the object", func(t *testing.T) {
		t.Parallel()

		handler := mount(t, storeReturning(testObject()), newRangingObjects(), ownedCaller())

		res := get(t, handler, testObjectID, map[string]string{"Range": "bytes=500-600"})

		test.EqOp(t, nethttp.StatusRequestedRangeNotSatisfiable, res.Code)
	})

	T.Run("serves the whole object when the manager cannot open a range", func(t *testing.T) {
		t.Parallel()

		handler := mount(t, storeReturning(testObject()), newObjects(), ownedCaller())

		res := get(t, handler, testObjectID, map[string]string{"Range": "bytes=6-10"})

		// The range is ignored rather than answered, and the header says so.
		test.EqOp(t, nethttp.StatusOK, res.Code)
		test.EqOp(t, testBody, res.Body.String())
		test.EqOp(t, acceptRangesNone, res.Header().Get(acceptRangesHeader))
		test.EqOp(t, "", res.Header().Get("Content-Range"))
	})

	T.Run("answers a conditional request from the row's time", func(t *testing.T) {
		t.Parallel()

		object := testObject()
		object.CreatedAt = time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)

		handler := mount(t, storeReturning(object), newRangingObjects(), ownedCaller())

		res := get(t, handler, testObjectID, map[string]string{
			"If-Modified-Since": object.CreatedAt.Format(nethttp.TimeFormat),
		})

		test.EqOp(t, nethttp.StatusNotModified, res.Code)
	})
}

func TestHandler_serveRefusals(T *testing.T) {
	T.Parallel()

	T.Run("404s an object that does not exist", func(t *testing.T) {
		t.Parallel()

		manager := newRangingObjects()
		res := get(t, mount(t, storeReturning(testObject()), manager, ownedCaller()), "nope", nil)

		test.EqOp(t, nethttp.StatusNotFound, res.Code)
		test.EqOp(t, int64(0), manager.opens.Load())
	})

	T.Run("404s an object the caller does not own", func(t *testing.T) {
		t.Parallel()

		manager := newRangingObjects()
		caller := Caller{Scope: tenancy.Of(testTenant), PrincipalID: testOtherOwner}

		res := get(t, mount(t, storeReturning(testObject()), manager, caller), testObjectID, nil)

		// The same answer an absent object gets, and nothing reached storage.
		test.EqOp(t, nethttp.StatusNotFound, res.Code)
		test.EqOp(t, int64(0), manager.opens.Load())
		test.StrContains(t, res.Body.String(), "object not found")
	})

	T.Run("404s an object in another tenant", func(t *testing.T) {
		t.Parallel()

		manager := newRangingObjects()

		// The owner is right; only the tenant is wrong. The store binds the
		// scope, so the row is not refused — it is absent.
		caller := Caller{Scope: tenancy.Of(testOtherTntnt), PrincipalID: testOwnerID}

		res := get(t, mount(t, storeReturning(testObject()), manager, caller), testObjectID, nil)

		test.EqOp(t, nethttp.StatusNotFound, res.Code)
		test.EqOp(t, int64(0), manager.opens.Load())
	})

	T.Run("404s a request that names no object, without reading anything", func(t *testing.T) {
		t.Parallel()

		store := storeReturning(testObject())
		manager := newRangingObjects()

		handler, err := New(store, readerOnlyClient(), manager, WithCallerResolver(resolverFromContext))
		must.NoError(t, err)

		res := serveDirect(t, handler, BasePath+"/")

		test.EqOp(t, nethttp.StatusNotFound, res.Code)
		test.SliceEmpty(t, store.GetObjectCalls())
		test.EqOp(t, int64(0), manager.opens.Load())
	})

	T.Run("clears the object's headers from a refusal", func(t *testing.T) {
		t.Parallel()

		caller := Caller{Scope: tenancy.Of(testTenant), PrincipalID: testOtherOwner}
		res := get(t, mount(t, storeReturning(testObject()), newRangingObjects(), caller), testObjectID, nil)

		test.EqOp(t, nethttp.StatusNotFound, res.Code)
		test.EqOp(t, "", res.Header().Get(contentDispositionHeader))
		test.EqOp(t, cacheControlValue, res.Header().Get(cacheControlHeader))
		test.StrContains(t, res.Header().Get(contentTypeHeader), "json")
	})

	T.Run("500s a caller that cannot be resolved", func(t *testing.T) {
		t.Parallel()

		manager := newRangingObjects()

		// Mounted without the middleware that puts a caller on the request, so
		// the resolver fails rather than reporting an anonymous one.
		handler, err := New(storeReturning(testObject()), readerOnlyClient(), manager,
			WithCallerResolver(resolverFromContext))
		must.NoError(t, err)

		res := serveDirect(t, handler, path.Join(BasePath, testObjectID))

		test.EqOp(t, nethttp.StatusInternalServerError, res.Code)
		test.EqOp(t, int64(0), manager.opens.Load())
	})

	T.Run("500s an entitlement that cannot be decided", func(t *testing.T) {
		t.Parallel()

		manager := newRangingObjects()
		boom := platformerrors.New("the permissions service is down")

		handler := mount(t, storeReturning(testObject()), manager, ownedCaller(),
			WithEntitlement(func(context.Context, Caller, *registry.Object) (bool, error) {
				return false, boom
			}))

		res := get(t, handler, testObjectID, nil)

		// Not a 404. A question nobody managed to ask is not a "no".
		test.EqOp(t, nethttp.StatusInternalServerError, res.Code)
		test.EqOp(t, int64(0), manager.opens.Load())
	})

	T.Run("404s a store that reports neither a row nor an error", func(t *testing.T) {
		t.Parallel()

		store := &registrymock.StoreMock{}
		store.GetObjectFunc = func(
			context.Context,
			database.SQLQueryExecutor,
			tenancy.Scope,
			string,
		) (*registry.Object, error) {
			// A Store that says nothing is exactly what this test guards.
			return nil, nil
		}

		manager := newRangingObjects()
		res := get(t, mount(t, store, manager, ownedCaller()), testObjectID, nil)

		test.EqOp(t, nethttp.StatusNotFound, res.Code)
		test.EqOp(t, int64(0), manager.opens.Load())
	})

	T.Run("500s a store that cannot be read", func(t *testing.T) {
		t.Parallel()

		store := &registrymock.StoreMock{}
		store.GetObjectFunc = func(
			context.Context,
			database.SQLQueryExecutor,
			tenancy.Scope,
			string,
		) (*registry.Object, error) {
			return nil, platformerrors.New("the database is unwell")
		}

		res := get(t, mount(t, store, newRangingObjects(), ownedCaller()), testObjectID, nil)

		test.EqOp(t, nethttp.StatusInternalServerError, res.Code)
	})

	T.Run("500s a bucket that will not open", func(t *testing.T) {
		t.Parallel()

		manager := newObjects()
		manager.err = platformerrors.New("the bucket is unreachable")

		res := get(t, mount(t, storeReturning(testObject()), manager, ownedCaller()), testObjectID, nil)

		test.EqOp(t, nethttp.StatusInternalServerError, res.Code)
	})
}

func TestHandler_entitlement(T *testing.T) {
	T.Parallel()

	T.Run("serves a caller the consumer's own rule entitles", func(t *testing.T) {
		t.Parallel()

		caller := Caller{Scope: tenancy.Of(testTenant), PrincipalID: testOtherOwner}

		handler := mount(t, storeReturning(testObject()), newRangingObjects(), caller,
			WithEntitlement(func(_ context.Context, c Caller, object *registry.Object) (bool, error) {
				return c.PrincipalID == testOtherOwner && object.Key == testObjectKey, nil
			}))

		res := get(t, handler, testObjectID, nil)

		test.EqOp(t, nethttp.StatusOK, res.Code)
		test.EqOp(t, testBody, res.Body.String())
	})

	T.Run("refuses an owner the consumer's own rule does not entitle", func(t *testing.T) {
		t.Parallel()

		handler := mount(t, storeReturning(testObject()), newRangingObjects(), ownedCaller(),
			WithEntitlement(func(context.Context, Caller, *registry.Object) (bool, error) {
				return false, nil
			}))

		res := get(t, handler, testObjectID, nil)

		test.EqOp(t, nethttp.StatusNotFound, res.Code)
	})

	T.Run("a nil entitlement leaves the owner comparison in place", func(t *testing.T) {
		t.Parallel()

		caller := Caller{Scope: tenancy.Of(testTenant), PrincipalID: testOtherOwner}

		res := get(t, mount(t, storeReturning(testObject()), newRangingObjects(), caller,
			WithEntitlement(nil)), testObjectID, nil)

		test.EqOp(t, nethttp.StatusNotFound, res.Code)
	})
}

func TestOwnerOnly(T *testing.T) {
	T.Parallel()

	T.Run("entitles the owner", func(t *testing.T) {
		t.Parallel()

		entitled, err := OwnerOnly(t.Context(), ownedCaller(), testObject())
		must.NoError(t, err)
		test.True(t, entitled)
	})

	T.Run("refuses everybody else", func(t *testing.T) {
		t.Parallel()

		caller := Caller{Scope: tenancy.Of(testTenant), PrincipalID: testOtherOwner}

		entitled, err := OwnerOnly(t.Context(), caller, testObject())
		must.NoError(t, err)
		test.False(t, entitled)
	})

	T.Run("refuses a caller with no principal", func(t *testing.T) {
		t.Parallel()

		entitled, err := OwnerOnly(t.Context(), Caller{Scope: tenancy.Of(testTenant)}, testObject())
		must.NoError(t, err)
		test.False(t, entitled)
	})

	T.Run("refuses a row with no owner rather than reading it as anyone", func(t *testing.T) {
		t.Parallel()

		object := testObject()
		object.OwnerID = ""

		entitled, err := OwnerOnly(t.Context(), ownedCaller(), object)
		must.NoError(t, err)
		test.False(t, entitled)
	})
}

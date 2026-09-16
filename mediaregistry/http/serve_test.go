package http

import (
	"context"
	"encoding/json"
	"errors"
	nethttp "net/http"
	"path"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/mediaregistry"
	mediaregistrymock "github.com/primandproper/platform-go/v14/mediaregistry/mock"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/encoding"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	httpx "github.com/primandproper/primitives-go/v2/errors/http"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNew(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		handler, err := New(&mediaregistrymock.StoreMock{}, readerOnlyClient(), newObjects(),
			WithCallerResolver(resolverFromContext))
		must.NoError(t, err)
		must.NotNil(t, handler)
	})

	T.Run("refuses a nil store", func(t *testing.T) {
		t.Parallel()

		handler, err := New(nil, readerOnlyClient(), newObjects(), WithCallerResolver(resolverFromContext))
		test.Nil(t, handler)
		must.ErrorIs(t, err, mediaregistry.ErrNilStore)
	})

	T.Run("refuses a nil database client", func(t *testing.T) {
		t.Parallel()

		handler, err := New(&mediaregistrymock.StoreMock{}, nil, newObjects(), WithCallerResolver(resolverFromContext))
		test.Nil(t, handler)
		must.ErrorIs(t, err, mediaregistry.ErrNilDatabaseClient)
	})

	T.Run("refuses a nil upload manager", func(t *testing.T) {
		t.Parallel()

		handler, err := New(&mediaregistrymock.StoreMock{}, readerOnlyClient(), nil, WithCallerResolver(resolverFromContext))
		test.Nil(t, handler)
		must.ErrorIs(t, err, mediaregistry.ErrNilUploadManager)
	})

	T.Run("refuses handlers with no caller resolver", func(t *testing.T) {
		t.Parallel()

		handler, err := New(&mediaregistrymock.StoreMock{}, readerOnlyClient(), newObjects())
		test.Nil(t, handler)
		must.ErrorIs(t, err, ErrNilCallerResolver)
	})

	T.Run("ignores a nil option", func(t *testing.T) {
		t.Parallel()

		handler, err := New(&mediaregistrymock.StoreMock{}, readerOnlyClient(), newObjects(),
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
			WithEntitlement(func(context.Context, Caller, *mediaregistry.Object) (bool, error) {
				return false, boom
			}))

		res := get(t, handler, testObjectID, nil)

		// Not a 404. A question nobody managed to ask is not a "no".
		test.EqOp(t, nethttp.StatusInternalServerError, res.Code)
		test.EqOp(t, int64(0), manager.opens.Load())
	})

	T.Run("404s a store that reports neither a row nor an error", func(t *testing.T) {
		t.Parallel()

		store := &mediaregistrymock.StoreMock{}
		store.GetObjectFunc = func(
			context.Context,
			database.SQLQueryExecutor,
			tenancy.Scope,
			string,
		) (*mediaregistry.Object, error) {
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

		store := &mediaregistrymock.StoreMock{}
		store.GetObjectFunc = func(
			context.Context,
			database.SQLQueryExecutor,
			tenancy.Scope,
			string,
		) (*mediaregistry.Object, error) {
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

// The shipped provider is the one that ranges, and it reaches storage from
// inside net/http rather than before it. These are that path.
func TestHandler_serveRangingRefusals(T *testing.T) {
	T.Parallel()

	// failing builds the ranging manager that will not open, the handler over
	// it, and the recorder its span reports to.
	failing := func(t *testing.T) (*rangingObjects, nethttp.Handler, *observability.RecordingObserver) {
		t.Helper()

		manager := newRangingObjects()
		manager.err = platformerrors.New("the bucket is unreachable")

		handler := newHandler(t, storeReturning(testObject()), manager)

		return manager, mountHandler(t, handler, ownedCaller()), recording(handler)
	}

	T.Run("500s a bucket that will not open", func(t *testing.T) {
		t.Parallel()

		manager, handler, recorder := failing(t)

		res := get(t, handler, testObjectID, nil)

		// Not a 200 with a Content-Length and no bytes, which is what
		// ServeContent commits to before it finds out.
		test.EqOp(t, nethttp.StatusInternalServerError, res.Code)
		test.EqOp(t, int64(1), manager.opens.Load())

		// The refusal's own envelope, under the refusal's own headers.
		test.EqOp(t, "", res.Header().Get(contentLengthHeader))
		test.EqOp(t, "", res.Header().Get(lastModifiedHeader))
		test.EqOp(t, "", res.Header().Get(acceptRangesHeader))
		test.EqOp(t, "", res.Header().Get(contentDispositionHeader))
		test.EqOp(t, encoding.ContentTypeJSON.String(), res.Header().Get(contentTypeHeader))

		var envelope httpx.APIResponse[any]

		must.NoError(t, json.Unmarshal(res.Body.Bytes(), &envelope))
		must.NotNil(t, envelope.Error)

		// And it reached the span rather than only the client.
		must.SliceNotEmpty(t, recorder.Operations)
		must.ErrorIs(t, errors.Join(recorder.Operations[0].Errors...), manager.err)
	})

	T.Run("500s a bucket that will not open the range it was asked for", func(t *testing.T) {
		t.Parallel()

		manager, handler, recorder := failing(t)

		res := get(t, handler, testObjectID, map[string]string{"Range": "bytes=6-10"})

		// The 206 ServeContent chose is never sent: no byte of the range was
		// ever produced, so there is still a refusal to write.
		test.EqOp(t, nethttp.StatusInternalServerError, res.Code)
		test.EqOp(t, "", res.Header().Get(contentRangeHeader))
		must.SliceNotEmpty(t, recorder.Operations)
		must.ErrorIs(t, errors.Join(recorder.Operations[0].Errors...), manager.err)
	})

	T.Run("records a read that fails once the response has gone", func(t *testing.T) {
		t.Parallel()

		manager := newRangingObjects()
		manager.breakAfter = 4
		manager.readErr = platformerrors.New("the bucket stopped answering")

		handler := newHandler(t, storeReturning(testObject()), manager)
		recorder := recording(handler)

		res := get(t, mountHandler(t, handler, ownedCaller()), testObjectID, nil)

		// The status went out with the first bytes, so all that is left is a
		// body that stopped — and the line on the span saying why.
		test.EqOp(t, nethttp.StatusOK, res.Code)
		test.EqOp(t, testBody[:4], res.Body.String())
		must.SliceNotEmpty(t, recorder.Operations)
		must.ErrorIs(t, errors.Join(recorder.Operations[0].Errors...), manager.readErr)
	})

	T.Run("an answer with no body still sends its status", func(t *testing.T) {
		t.Parallel()

		manager := newRangingObjects()

		res := get(t, mount(t, storeReturning(testObject()), manager, ownedCaller()), testObjectID,
			map[string]string{"Range": "bytes=500-600"})

		// Held and then sent by write, since nothing was ever written under it.
		test.EqOp(t, nethttp.StatusRequestedRangeNotSatisfiable, res.Code)

		// And decided without asking storage for anything.
		test.EqOp(t, int64(0), manager.opens.Load())
	})

	T.Run("a conditional request costs no read of storage", func(t *testing.T) {
		t.Parallel()

		object := testObject()
		object.CreatedAt = time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)

		manager := newRangingObjects()

		res := get(t, mount(t, storeReturning(object), manager, ownedCaller()), testObjectID, map[string]string{
			"If-Modified-Since": object.CreatedAt.Format(nethttp.TimeFormat),
		})

		test.EqOp(t, nethttp.StatusNotModified, res.Code)

		// The bucket is reached from the first Read and a 304 never makes one,
		// which is the property a pre-emptive open would have cost.
		test.EqOp(t, int64(0), manager.opens.Load())
	})
}

func TestHandler_entitlement(T *testing.T) {
	T.Parallel()

	T.Run("serves a caller the consumer's own rule entitles", func(t *testing.T) {
		t.Parallel()

		caller := Caller{Scope: tenancy.Of(testTenant), PrincipalID: testOtherOwner}

		handler := mount(t, storeReturning(testObject()), newRangingObjects(), caller,
			WithEntitlement(func(_ context.Context, c Caller, object *mediaregistry.Object) (bool, error) {
				return c.PrincipalID == testOtherOwner && object.Key == testObjectKey, nil
			}))

		res := get(t, handler, testObjectID, nil)

		test.EqOp(t, nethttp.StatusOK, res.Code)
		test.EqOp(t, testBody, res.Body.String())
	})

	T.Run("refuses an owner the consumer's own rule does not entitle", func(t *testing.T) {
		t.Parallel()

		handler := mount(t, storeReturning(testObject()), newRangingObjects(), ownedCaller(),
			WithEntitlement(func(context.Context, Caller, *mediaregistry.Object) (bool, error) {
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

func TestHandler_refusalAgreesWithTheMapper(T *testing.T) {
	T.Parallel()

	// The serve route decides its own 404 rather than reading one out of a
	// registry a binary may not have populated, and mediaregistry ships a
	// mapper that answers the same refusal. Two answers to one question is two
	// places to change it, so this reads both and compares them: the route's
	// envelope against what the mapper says, rather than against a copy of the
	// route's wording that agrees with whatever it held when it was typed.
	//
	// It lives here because this is the side that has to be driven. The mapper
	// is a value anybody can call; the route is only reachable through a
	// request, so the comparison has to happen where the handler is mounted.
	T.Run("the route's refusal is the mapper's", func(t *testing.T) {
		t.Parallel()

		code, message, ok := mediaregistry.HTTPMapper.Map(mediaregistry.ErrObjectNotFound)
		must.True(t, ok)

		res := get(t, mount(t, storeReturning(testObject()), newRangingObjects(), ownedCaller()), "nope", nil)

		test.EqOp(t, httpx.HTTPStatusForCode(code), res.Code)

		var envelope httpx.APIResponse[any]
		must.NoError(t, json.Unmarshal(res.Body.Bytes(), &envelope))
		must.NotNil(t, envelope.Error)

		test.EqOp(t, code, envelope.Error.Code)
		test.EqOp(t, message, envelope.Error.Message)
	})
}

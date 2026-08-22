package routing_test

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v13/routing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// rawRoute is the shape of a Handle route: it reads its own body, with no
// binding step in front of it. Recording the read separately from the response
// is what lets a test tell a request refused before the handler ran from one the
// handler's own read cut off.
type rawRoute struct {
	err  error
	body string
	ran  bool
}

func (rr *rawRoute) ServeHTTP(res http.ResponseWriter, req *http.Request) {
	rr.ran = true

	body, err := io.ReadAll(req.Body)
	rr.body, rr.err = string(body), err

	if err != nil {
		res.WriteHeader(http.StatusInternalServerError)

		return
	}

	res.WriteHeader(http.StatusTeapot)
}

// declared sends a body whose Content-Length says how big it is.
func declared(t *testing.T, r *routing.Router, body string) *httptest.ResponseRecorder {
	t.Helper()

	return serveRaw(t, r, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/raw", strings.NewReader(body)))
}

// undeclared sends the same bytes with no Content-Length, the case a bound
// cannot refuse up front: io.NopCloser hides the reader's length, exactly as a
// chunked request does.
func undeclared(t *testing.T, r *routing.Router, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/raw", io.NopCloser(strings.NewReader(body)))
	must.EqOp(t, int64(-1), req.ContentLength)

	return serveRaw(t, r, req)
}

func serveRaw(t *testing.T, r *routing.Router, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, req)

	return rec
}

const oversizedBody = `{"payload":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`

func TestRouter_HandleRequestBodyBound(T *testing.T) {
	T.Parallel()

	T.Run("a Handle route inherits the Router's default bound", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t, routing.WithDefaultMaxRequestBody(16))
		rr := &rawRoute{}
		r.Handle(http.MethodPost, "/raw", rr)

		rec := declared(t, r, oversizedBody)

		// 413 rather than 400: told 400, a client sends the same document again.
		test.EqOp(t, http.StatusRequestEntityTooLarge, rec.Code)
		test.False(t, rr.ran)
		test.StrContains(t, rec.Body.String(), "request body exceeds the 16 byte limit")
	})

	T.Run("a body under the bound arrives whole", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t, routing.WithDefaultMaxRequestBody(1<<20))
		rr := &rawRoute{}
		r.Handle(http.MethodPost, "/raw", rr)

		rec := declared(t, r, oversizedBody)

		test.EqOp(t, http.StatusTeapot, rec.Code)
		test.NoError(t, rr.err)
		test.EqOp(t, oversizedBody, rr.body)
	})

	T.Run("a body that declares nothing is cut off mid-read", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t, routing.WithDefaultMaxRequestBody(16))
		rr := &rawRoute{}
		r.Handle(http.MethodPost, "/raw", rr)

		undeclared(t, r, oversizedBody)

		// Nothing could refuse this up front, so the handler runs and fails on
		// the read it was going to do anyway — having read no more than the bound.
		test.True(t, rr.ran)

		var tooLarge *http.MaxBytesError
		must.True(t, errors.As(rr.err, &tooLarge))
		test.EqOp(t, int64(16), tooLarge.Limit)
	})

	T.Run("a Router with no default leaves a Handle route unbounded", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t)
		rr := &rawRoute{}
		r.Handle(http.MethodPost, "/raw", rr)

		rec := declared(t, r, oversizedBody)

		test.EqOp(t, http.StatusTeapot, rec.Code)
		test.EqOp(t, oversizedBody, rr.body)
	})

	T.Run("the bound goes on outside the route's own middleware", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t, routing.WithDefaultMaxRequestBody(16))

		// The middleware a webhook route registers reads the body itself, to
		// verify a signature over it. It must not be the one unbounded read.
		var seen int
		var seenErr error
		verifier := func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
				body, err := io.ReadAll(req.Body)
				seen, seenErr = len(body), err

				next.ServeHTTP(res, req)
			})
		}

		rr := &rawRoute{}
		r.Handle(http.MethodPost, "/raw", rr, verifier)

		test.EqOp(t, http.StatusRequestEntityTooLarge, declared(t, r, oversizedBody).Code)
		test.EqOp(t, 0, seen)
		test.NoError(t, seenErr)
		test.False(t, rr.ran)
	})

	T.Run("a Group's Handle routes inherit it too", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t, routing.WithDefaultMaxRequestBody(16))
		rr := &rawRoute{}
		r.Group("/g", func(sub *routing.Router) {
			sub.Handle(http.MethodPost, "/raw", rr)
		})

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/g/raw", strings.NewReader(oversizedBody))

		test.EqOp(t, http.StatusRequestEntityTooLarge, serveRaw(t, r, req).Code)
		test.False(t, rr.ran)
	})
}

func TestRouter_MaxRequestBodyScope(T *testing.T) {
	T.Parallel()

	T.Run("zero opts a Handle route out of the Router's default", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t, routing.WithDefaultMaxRequestBody(16))
		rr := &rawRoute{}
		r.MaxRequestBody(0).Handle(http.MethodPost, "/raw", rr)

		rec := declared(t, r, oversizedBody)

		test.EqOp(t, http.StatusTeapot, rec.Code)
		test.EqOp(t, oversizedBody, rr.body)
	})

	T.Run("a negative bound is no bound either", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t, routing.WithDefaultMaxRequestBody(16))
		rr := &rawRoute{}
		r.MaxRequestBody(-1).Handle(http.MethodPost, "/raw", rr)

		test.EqOp(t, http.StatusTeapot, declared(t, r, oversizedBody).Code)
	})

	T.Run("a larger bound covers a route the default would refuse", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t, routing.WithDefaultMaxRequestBody(16))
		big, small := &rawRoute{}, &rawRoute{}
		r.MaxRequestBody(1<<20).Handle(http.MethodPost, "/raw", big)
		r.Handle(http.MethodPost, "/other", small)

		test.EqOp(t, http.StatusTeapot, declared(t, r, oversizedBody).Code)
		test.EqOp(t, oversizedBody, big.body)

		// The derived Router is a copy: the one it came from still bounds its own.
		other := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/other", strings.NewReader(oversizedBody))
		test.EqOp(t, http.StatusRequestEntityTooLarge, serveRaw(t, r, other).Code)
		test.False(t, small.ran)
	})

	T.Run("a typed route registered through it reads the new bound as its default", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t, routing.WithDefaultMaxRequestBody(1<<20))
		documentRoute(r.MaxRequestBody(8))
		must.NoError(t, r.Err())

		test.EqOp(t, http.StatusRequestEntityTooLarge, doRequest(t, r, http.MethodPut, "/areas/12/geojson", geoJSON).Code)
	})
}

func TestLimitRequestBody(T *testing.T) {
	T.Parallel()

	T.Run("bounds a handler that has no Router at all", func(t *testing.T) {
		t.Parallel()

		rr := &rawRoute{}
		handler := routing.LimitRequestBody(16)(rr)

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/raw", strings.NewReader(oversizedBody)))

		test.EqOp(t, http.StatusRequestEntityTooLarge, rec.Code)
		test.False(t, rr.ran)
	})

	T.Run("zero or less is no bound", func(t *testing.T) {
		t.Parallel()

		for _, n := range []int64{0, -1} {
			rr := &rawRoute{}
			handler := routing.LimitRequestBody(n)(rr)

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/raw", strings.NewReader(oversizedBody)))

			test.EqOp(t, http.StatusTeapot, rec.Code)
			test.EqOp(t, oversizedBody, rr.body)
		}
	})

	T.Run("a body exactly at the bound is not over it", func(t *testing.T) {
		t.Parallel()

		rr := &rawRoute{}
		handler := routing.LimitRequestBody(int64(len(oversizedBody)))(rr)

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/raw", strings.NewReader(oversizedBody)))

		test.EqOp(t, http.StatusTeapot, rec.Code)
		test.NoError(t, rr.err)
		test.EqOp(t, oversizedBody, rr.body)
	})

	T.Run("a request carrying no body at all passes through", func(t *testing.T) {
		t.Parallel()

		// net/http never hands a server a nil Body, but a handler assembled in a
		// test does, and wrapping nil in a MaxBytesReader would panic the first
		// read rather than bound it.
		var ran bool
		handler := routing.LimitRequestBody(16)(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			ran = req.Body == nil

			res.WriteHeader(http.StatusTeapot)
		}))

		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/raw", http.NoBody)
		req.Body = nil

		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)

		test.True(t, ran)
		test.EqOp(t, http.StatusTeapot, rec.Code)
	})
}

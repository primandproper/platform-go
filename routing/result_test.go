package routing_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v13/encoding"
	"github.com/primandproper/platform-go/v13/routing"
	"github.com/primandproper/platform-go/v13/routing/backends/chi"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

type widget struct {
	Name string `json:"name"`
	ID   uint64 `json:"id"`
}

func resultRouter(t *testing.T, opts ...routing.RouterOption) *routing.Router {
	t.Helper()

	return routing.New(chi.NewBackend(&chi.Config{ServiceName: "result-test"}),
		encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON), opts...)
}

func call(t *testing.T, r *routing.Router, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	must.NoError(t, r.Err())

	rec := httptest.NewRecorder()
	r.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, path, http.NoBody))

	return rec
}

func TestResult(T *testing.T) {
	T.Parallel()

	T.Run("the named status is answered", func(t *testing.T) {
		t.Parallel()

		for _, tc := range []struct {
			name  string
			named int
			want  int
		}{
			{name: "created", named: http.StatusCreated, want: http.StatusCreated},
			{name: "ok over a registered 201", named: http.StatusOK, want: http.StatusOK},
			{name: "unavailable", named: http.StatusServiceUnavailable, want: http.StatusServiceUnavailable},
			// The two ends of the range a ResponseWriter accepts. They are here
			// rather than with the out-of-range cases below because where the
			// edge sits is the only interesting property of a bound, and a test
			// that names 42 and 1000 cannot tell `< 100` from `<= 100`.
			{name: "the lowest writable status", named: 100, want: 100},
			{name: "the highest writable status", named: 999, want: 999},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()

				r := resultRouter(t)
				routing.Post(r, "/w", func(context.Context, routing.Empty) (routing.Result[widget], error) {
					return routing.Result[widget]{Value: widget{ID: 1, Name: "w"}, Status: tc.named}, nil
				}, routing.WithEnvelope(false))

				test.EqOp(t, tc.want, call(t, r, http.MethodPost, "/w").Code)
			})
		}
	})

	T.Run("a zero status is the registered one", func(t *testing.T) {
		t.Parallel()

		r := resultRouter(t)
		// POST registers 201, and the Result names nothing.
		routing.Post(r, "/w", func(context.Context, routing.Empty) (routing.Result[widget], error) {
			return routing.Result[widget]{Value: widget{ID: 1, Name: "w"}}, nil
		}, routing.WithEnvelope(false))

		test.EqOp(t, http.StatusCreated, call(t, r, http.MethodPost, "/w").Code)
	})

	T.Run("WithResponseStatus is still the default a zero Result falls back to", func(t *testing.T) {
		t.Parallel()

		r := resultRouter(t)
		routing.Post(r, "/w", func(context.Context, routing.Empty) (routing.Result[widget], error) {
			return routing.Result[widget]{Value: widget{ID: 1}}, nil
		}, routing.WithEnvelope(false), routing.WithResponseStatus(http.StatusAccepted))

		test.EqOp(t, http.StatusAccepted, call(t, r, http.MethodPost, "/w").Code)
	})

	T.Run("an out of range status is answered as a fault", func(t *testing.T) {
		t.Parallel()

		for _, named := range []int{42, 1000, -1} {
			r := resultRouter(t)
			routing.Get(r, "/w", func(context.Context, routing.Empty) (routing.Result[widget], error) {
				return routing.Result[widget]{Value: widget{ID: 1}, Status: named}, nil
			}, routing.WithEnvelope(false))

			test.EqOp(t, http.StatusInternalServerError, call(t, r, http.MethodGet, "/w").Code)
		}
	})

	T.Run("a returned error wins over the named status", func(t *testing.T) {
		t.Parallel()

		r := resultRouter(t)
		routing.Get(r, "/w", func(context.Context, routing.Empty) (routing.Result[widget], error) {
			return routing.Result[widget]{Status: http.StatusCreated}, http.ErrNoLocation
		}, routing.WithEnvelope(false))

		rec := call(t, r, http.MethodGet, "/w")

		test.EqOp(t, http.StatusInternalServerError, rec.Code)
		test.StrNotContains(t, rec.Body.String(), `"id"`)
	})
}

func TestResult_header(T *testing.T) {
	T.Parallel()

	T.Run("a named header reaches the response", func(t *testing.T) {
		t.Parallel()

		r := resultRouter(t)
		routing.Post(r, "/w", func(context.Context, routing.Empty) (routing.Result[widget], error) {
			return routing.Result[widget]{
				Value:  widget{ID: 3},
				Status: http.StatusCreated,
				Header: http.Header{"Location": {"/w/3"}},
			}, nil
		}, routing.WithEnvelope(false))

		rec := call(t, r, http.MethodPost, "/w")

		test.EqOp(t, http.StatusCreated, rec.Code)
		test.EqOp(t, "/w/3", rec.Header().Get("Location"))
	})

	T.Run("a nil header sets nothing", func(t *testing.T) {
		t.Parallel()

		r := resultRouter(t)
		routing.Get(r, "/w", func(context.Context, routing.Empty) (routing.Result[widget], error) {
			return routing.Result[widget]{Value: widget{ID: 3}}, nil
		}, routing.WithEnvelope(false))

		test.EqOp(t, http.StatusOK, call(t, r, http.MethodGet, "/w").Code)
	})

	T.Run("a name is spelled in any case and still lands canonical", func(t *testing.T) {
		t.Parallel()

		r := resultRouter(t)
		routing.Get(r, "/w", func(context.Context, routing.Empty) (routing.Result[widget], error) {
			return routing.Result[widget]{
				Value:  widget{ID: 3},
				Header: http.Header{"retry-after": {"120"}},
			}, nil
		}, routing.WithEnvelope(false))

		test.EqOp(t, "120", call(t, r, http.MethodGet, "/w").Header().Get("Retry-After"))
	})

	T.Run("multiple values under one name all survive", func(t *testing.T) {
		t.Parallel()

		r := resultRouter(t)
		routing.Get(r, "/w", func(context.Context, routing.Empty) (routing.Result[widget], error) {
			return routing.Result[widget]{
				Value:  widget{ID: 3},
				Header: http.Header{"Set-Cookie": {"a=1", "b=2"}},
			}, nil
		}, routing.WithEnvelope(false))

		test.SliceLen(t, 2, call(t, r, http.MethodGet, "/w").Header().Values("Set-Cookie"))
	})

	T.Run("a reserved header is answered as a fault", func(t *testing.T) {
		t.Parallel()

		for _, name := range []string{"Content-Type", "content-length", "Transfer-Encoding", "Connection"} {
			r := resultRouter(t)
			routing.Get(r, "/w", func(context.Context, routing.Empty) (routing.Result[widget], error) {
				return routing.Result[widget]{
					Value:  widget{ID: 3},
					Header: http.Header{name: {"whatever"}},
				}, nil
			}, routing.WithEnvelope(false))

			test.EqOp(t, http.StatusInternalServerError, call(t, r, http.MethodGet, "/w").Code)
		}
	})

	T.Run("one reserved header leaves the others unapplied", func(t *testing.T) {
		t.Parallel()

		r := resultRouter(t)
		routing.Get(r, "/w", func(context.Context, routing.Empty) (routing.Result[widget], error) {
			return routing.Result[widget]{
				Value: widget{ID: 3},
				Header: http.Header{
					"Location":       {"/w/3"},
					"Content-Length": {"99"},
				},
			}, nil
		}, routing.WithEnvelope(false))

		rec := call(t, r, http.MethodGet, "/w")

		must.EqOp(t, http.StatusInternalServerError, rec.Code)
		test.EqOp(t, "", rec.Header().Get("Location"))
	})

	T.Run("a header set on a bodyless response still reaches the client", func(t *testing.T) {
		t.Parallel()

		r := resultRouter(t)
		routing.Delete(r, "/w", func(context.Context, routing.Empty) (routing.Result[routing.Empty], error) {
			return routing.Result[routing.Empty]{
				Status: http.StatusAccepted,
				Header: http.Header{"Retry-After": {"5"}},
			}, nil
		})

		rec := call(t, r, http.MethodDelete, "/w")

		test.EqOp(t, http.StatusAccepted, rec.Code)
		test.EqOp(t, "5", rec.Header().Get("Retry-After"))
		test.EqOp(t, "", rec.Body.String())
	})
}

// TestResult_unwrapping is the property the design rests on: a Result[T] is
// answered exactly as a bare T would have been, so opting a route in changes
// only its status.
func TestResult_unwrapping(T *testing.T) {
	T.Parallel()

	value := widget{ID: 9, Name: "same"}

	for _, tc := range []struct {
		name     string
		envelope bool
	}{
		{name: "enveloped body is byte-identical to the bare value", envelope: true},
		{name: "raw body is byte-identical to the bare value", envelope: false},
	} {
		T.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			envelope := tc.envelope

			bare := resultRouter(t)
			routing.Get(bare, "/w", func(context.Context, routing.Empty) (widget, error) {
				return value, nil
			}, routing.WithEnvelope(envelope))

			wrapped := resultRouter(t)
			routing.Get(wrapped, "/w", func(context.Context, routing.Empty) (routing.Result[widget], error) {
				return routing.Result[widget]{Value: value}, nil
			}, routing.WithEnvelope(envelope))

			bareRec := call(t, bare, http.MethodGet, "/w")
			wrappedRec := call(t, wrapped, http.MethodGet, "/w")

			test.EqOp(t, bareRec.Code, wrappedRec.Code)
			test.EqOp(t, bareRec.Body.String(), wrappedRec.Body.String())
			test.StrNotContains(t, wrappedRec.Body.String(), "Value")
			test.StrNotContains(t, wrappedRec.Body.String(), "Status")
		})
	}

	T.Run("Result[Empty] writes a status and no body", func(t *testing.T) {
		t.Parallel()

		r := resultRouter(t)
		routing.Delete(r, "/w", func(context.Context, routing.Empty) (routing.Result[routing.Empty], error) {
			return routing.Result[routing.Empty]{Status: http.StatusAccepted}, nil
		})

		rec := call(t, r, http.MethodDelete, "/w")

		test.EqOp(t, http.StatusAccepted, rec.Code)
		test.EqOp(t, "", rec.Body.String())
	})

	T.Run("the documented schema is the wrapped type's", func(t *testing.T) {
		t.Parallel()

		r := resultRouter(t)
		routing.Get(r, "/w", func(context.Context, routing.Empty) (routing.Result[widget], error) {
			return routing.Result[widget]{Value: value}, nil
		}, routing.WithEnvelope(false))
		r.MountOpenAPI("/openapi.json", "/docs")

		rec := call(t, r, http.MethodGet, "/openapi.json")
		must.EqOp(t, http.StatusOK, rec.Code)

		var spec map[string]any
		must.NoError(t, json.Unmarshal(rec.Body.Bytes(), &spec))
		must.MapContainsKey(t, spec, "paths")

		body := rec.Body.String()
		test.StrContains(t, body, `"name"`)
		test.StrContains(t, body, `"id"`)
		// The wrapper's own fields must not reach the document.
		test.StrNotContains(t, body, `"Value"`)
		test.StrNotContains(t, body, `"Status"`)
		test.StrNotContains(t, strings.ToLower(body), "result[")
	})
}

package routing_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	httpx "github.com/primandproper/platform-go/v13/errors/http"
	"github.com/primandproper/platform-go/v13/routing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// putDocumentInput is the shape the typed model could not express before: a
// bound path parameter next to a body that is itself a document.
type putDocumentInput struct {
	Document routing.RawBody
	AreaID   uint64 `path:"areaID"`
}

// storedDocument reports what the handler was handed.
type storedDocument struct {
	Document string `json:"document"`
	AreaID   uint64 `json:"areaID"`
}

const geoJSON = `{"type":"Polygon","coordinates":[[[0,0],[1,0],[1,1],[0,0]]]}`

// documentRoute registers the canonical raw-body route.
func documentRoute(r *routing.Router, opts ...routing.Option) {
	routing.Put(r, "/areas/{areaID:uint64}/geojson",
		func(_ context.Context, in putDocumentInput) (storedDocument, error) {
			return storedDocument{AreaID: in.AreaID, Document: string(in.Document)}, nil
		},
		append([]routing.Option{routing.WithEnvelope(false)}, opts...)...)
}

func TestRouter_RawBody(T *testing.T) {
	T.Parallel()

	T.Run("the handler gets the body verbatim alongside its path parameter", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t)
		documentRoute(r)
		must.NoError(t, r.Err())

		rec := doRequest(t, r, http.MethodPut, "/areas/12/geojson", geoJSON)

		test.EqOp(t, http.StatusOK, rec.Code)

		var got storedDocument
		must.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		test.EqOp(t, uint64(12), got.AreaID)

		// Byte-for-byte: the point of the type is that nothing between the socket
		// and the handler reformats, revalidates, or re-encodes the document.
		test.EqOp(t, geoJSON, got.Document)
	})

	T.Run("a body that is not JSON at all arrives untouched", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t)
		documentRoute(r, routing.WithRequestContentType("application/octet-stream"))
		must.NoError(t, r.Err())

		rec := doRequest(t, r, http.MethodPut, "/areas/12/geojson", "not json, not even close: }{")

		test.EqOp(t, http.StatusOK, rec.Code)

		var got storedDocument
		must.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		test.EqOp(t, "not json, not even close: }{", got.Document)
	})

	T.Run("an empty body is the handler's to reject", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t)
		documentRoute(r)
		must.NoError(t, r.Err())

		rec := doRequest(t, r, http.MethodPut, "/areas/12/geojson", "")

		test.EqOp(t, http.StatusOK, rec.Code)

		var got storedDocument
		must.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		test.EqOp(t, "", got.Document)
	})
}

func TestRouter_MaxRequestBody(T *testing.T) {
	T.Parallel()

	T.Run("a raw body over the route's bound is a 413", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t)
		documentRoute(r, routing.WithMaxRequestBody(16), routing.WithEnvelope(true))
		must.NoError(t, r.Err())

		rec := doRequest(t, r, http.MethodPut, "/areas/12/geojson", geoJSON)

		// 413 rather than the 400 a decoding failure maps to: the client's
		// document was not malformed, it was too big, which is the only one of the
		// two it can do anything about.
		test.EqOp(t, http.StatusRequestEntityTooLarge, rec.Code)

		var got envelope[storedDocument]
		must.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		must.NotNil(t, got.Error)
		test.EqOp(t, string(httpx.ErrDecodingRequestInput), got.Error.Code)
		test.StrContains(t, got.Error.Message, "16 byte limit")
	})

	T.Run("a decoded body over the bound is a 413 too", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t)
		routing.Post(r, "/orgs/{orgID:uint64}/users", func(_ context.Context, in createUserInput) (userOutput, error) {
			return userOutput{ID: in.OrgID}, nil
		}, routing.WithMaxRequestBody(8))
		must.NoError(t, r.Err())

		rec := doRequest(t, r, http.MethodPost, "/orgs/7/users", `{"name":"Ada","email":"ada@example.com"}`)

		test.EqOp(t, http.StatusRequestEntityTooLarge, rec.Code)
	})

	T.Run("a body within the bound is served normally", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t)
		documentRoute(r, routing.WithMaxRequestBody(int64(len(geoJSON))))
		must.NoError(t, r.Err())

		test.EqOp(t, http.StatusOK, doRequest(t, r, http.MethodPut, "/areas/12/geojson", geoJSON).Code)
	})

	T.Run("the handler does not run for a body over the bound", func(t *testing.T) {
		t.Parallel()

		var ran bool

		r := buildTestRouter(t)
		routing.Put(r, "/areas/{areaID:uint64}/geojson",
			func(_ context.Context, _ putDocumentInput) (storedDocument, error) {
				ran = true

				return storedDocument{}, nil
			}, routing.WithMaxRequestBody(4))
		must.NoError(t, r.Err())

		test.EqOp(t, http.StatusRequestEntityTooLarge, doRequest(t, r, http.MethodPut, "/areas/12/geojson", geoJSON).Code)
		test.False(t, ran)
	})

	T.Run("the Router's default applies to routes that name no bound", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t, routing.WithDefaultMaxRequestBody(16))
		documentRoute(r)
		must.NoError(t, r.Err())

		test.EqOp(t, http.StatusRequestEntityTooLarge, doRequest(t, r, http.MethodPut, "/areas/12/geojson", geoJSON).Code)
	})

	T.Run("a route's own bound wins over the Router's", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t, routing.WithDefaultMaxRequestBody(16))
		documentRoute(r, routing.WithMaxRequestBody(1<<20))
		must.NoError(t, r.Err())

		// The case the per-route bound exists for: one endpoint that takes a large
		// document, without raising the ceiling for every other endpoint.
		test.EqOp(t, http.StatusOK, doRequest(t, r, http.MethodPut, "/areas/12/geojson", geoJSON).Code)
	})

	T.Run("a bound of zero opts out", func(t *testing.T) {
		t.Parallel()

		// A negative bound is the same answer, deliberately: anything that is
		// not a size means "no bound", so a caller forwarding a config field
		// gets one reading rather than a route that is bounded at a nonsense
		// number. resolveMaxRequestBody returns zero for both, which is why the
		// test it does that with cannot distinguish the guard it is written
		// with from one written `<= 0` — the two agree on every input, and a
		// mutation report naming that line is reporting an equivalent mutant
		// rather than a branch nothing asserts.
		for _, bound := range []int64{0, -1} {
			r := buildTestRouter(t, routing.WithDefaultMaxRequestBody(16))
			documentRoute(r, routing.WithMaxRequestBody(bound))
			must.NoError(t, r.Err())

			test.EqOp(t, http.StatusOK, doRequest(t, r, http.MethodPut, "/areas/12/geojson", geoJSON).Code)
		}
	})

	T.Run("a raw body nobody bounded is bounded anyway", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t)
		documentRoute(r)
		must.NoError(t, r.Err())

		oversized := strings.Repeat("x", int(routing.DefaultRawBodyLimit)+1)

		test.EqOp(t, http.StatusRequestEntityTooLarge, doRequest(t, r, http.MethodPut, "/areas/12/geojson", oversized).Code)
	})

	T.Run("an error encoder can recognize the over-limit body", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t, routing.WithErrorEncoder(func(_ context.Context, err error) (int, any) {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				return http.StatusRequestEntityTooLarge, flatError{Error: "too big"}
			}

			return http.StatusInternalServerError, flatError{Error: err.Error()}
		}))
		documentRoute(r, routing.WithMaxRequestBody(16))
		must.NoError(t, r.Err())

		rec := doRequest(t, r, http.MethodPut, "/areas/12/geojson", geoJSON)

		test.EqOp(t, http.StatusRequestEntityTooLarge, rec.Code)

		var got flatError
		must.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		test.EqOp(t, "too big", got.Error)
	})
}

func TestRouter_RawBodyRegistration(T *testing.T) {
	T.Parallel()

	T.Run("a second RawBody field panics", func(t *testing.T) {
		t.Parallel()

		type twoRaw struct {
			First  routing.RawBody
			Second routing.RawBody
		}

		r := buildTestRouter(t)

		defer func() { test.NotNil(t, recover()) }()

		routing.Post(r, "/x", func(context.Context, twoRaw) (routing.Empty, error) {
			return routing.Empty{}, nil
		})
	})

	T.Run("a RawBody field beside body fields panics", func(t *testing.T) {
		t.Parallel()

		type rawAndFields struct {
			Name     string `json:"name"`
			Document routing.RawBody
		}

		r := buildTestRouter(t)

		defer func() { test.NotNil(t, recover()) }()

		routing.Post(r, "/x", func(context.Context, rawAndFields) (routing.Empty, error) {
			return routing.Empty{}, nil
		})
	})

	T.Run("a RawBody field on a method with no body panics", func(t *testing.T) {
		t.Parallel()

		type rawOnGet struct {
			Document routing.RawBody
		}

		r := buildTestRouter(t)

		defer func() { test.NotNil(t, recover()) }()

		routing.Get(r, "/x", func(context.Context, rawOnGet) (routing.Empty, error) {
			return routing.Empty{}, nil
		})
	})

	T.Run("a RawBody field with a json name panics", func(t *testing.T) {
		t.Parallel()

		type namedRaw struct {
			Document routing.RawBody `json:"document"`
		}

		r := buildTestRouter(t)

		defer func() { test.NotNil(t, recover()) }()

		routing.Post(r, "/x", func(context.Context, namedRaw) (routing.Empty, error) {
			return routing.Empty{}, nil
		})
	})

	T.Run("json:\"-\" is the statement the bare field already makes", func(t *testing.T) {
		t.Parallel()

		type excludedRaw struct {
			Document routing.RawBody `json:"-"`
		}

		r := buildTestRouter(t)
		routing.Post(r, "/x", func(_ context.Context, in excludedRaw) (storedDocument, error) {
			return storedDocument{Document: string(in.Document)}, nil
		}, routing.WithEnvelope(false))
		must.NoError(t, r.Err())

		rec := doRequest(t, r, http.MethodPost, "/x", geoJSON)

		test.EqOp(t, http.StatusCreated, rec.Code)

		var got storedDocument
		must.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		test.EqOp(t, geoJSON, got.Document)
	})
}

func TestRouter_RawBodySpec(T *testing.T) {
	T.Parallel()

	T.Run("the body is documented under the route's media type as free-form JSON", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t)
		documentRoute(r, routing.WithRequestContentType("application/geo+json"))
		must.NoError(t, r.Err())

		body := requestBodyContent(t, r, "/areas/{areaID}/geojson", "put")
		must.MapContainsKey(t, body, "application/geo+json")
		must.MapLen(t, 1, body)

		// Free-form rather than the `{"type": "string"}` a []byte reflects to: a
		// generated client reading that would send a JSON string containing the
		// document instead of the document.
		media, _ := body["application/geo+json"].(map[string]any)
		schema, _ := media["schema"].(map[string]any)
		test.MapEmpty(t, schema)

		// The parameter is still described; the raw body does not cost the route
		// everything else the reflector knows about it.
		item, ok := r.Spec().Paths.MapOfPathItemValues["/areas/{areaID}/geojson"]
		must.True(t, ok)
		test.SliceLen(t, 1, item.MapOfOperationValues["put"].Parameters)
	})

	T.Run("an unnamed media type is opaque bytes", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t)
		documentRoute(r)
		must.NoError(t, r.Err())

		body := requestBodyContent(t, r, "/areas/{areaID}/geojson", "put")
		must.MapContainsKey(t, body, "application/octet-stream")

		media, _ := body["application/octet-stream"].(map[string]any)
		schema, _ := media["schema"].(map[string]any)
		test.EqOp(t, "string", schema["type"])
		test.EqOp(t, "binary", schema["format"])
	})

	T.Run("a decoded body can name its media type too", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t)
		routing.Post(r, "/orgs/{orgID:uint64}/users", func(_ context.Context, in createUserInput) (userOutput, error) {
			return userOutput{ID: in.OrgID}, nil
		}, routing.WithRequestContentType("application/vnd.example.user+json"))
		must.NoError(t, r.Err())

		body := requestBodyContent(t, r, "/orgs/{orgID}/users", "post")
		must.MapContainsKey(t, body, "application/vnd.example.user+json")
		must.MapLen(t, 1, body)
	})
}

// requestBodyContent digs the content map of an operation's request body out of
// the marshaled spec, which is the form the document is actually read in.
func requestBodyContent(t *testing.T, r *routing.Router, path, method string) map[string]any {
	t.Helper()

	raw, err := r.MarshalSpec()
	must.NoError(t, err)

	var doc struct {
		Paths map[string]map[string]struct {
			RequestBody struct {
				Content map[string]any `json:"content"`
			} `json:"requestBody"`
		} `json:"paths"`
	}

	must.NoError(t, json.Unmarshal(raw, &doc))

	item, ok := doc.Paths[path]
	must.True(t, ok, must.Sprintf("no path %q in spec", path))

	op, ok := item[method]
	must.True(t, ok, must.Sprintf("no %s operation on %q", method, path))

	return op.RequestBody.Content
}

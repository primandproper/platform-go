package routing

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v6/encoding"
	httpx "github.com/primandproper/platform-go/v6/errors/http"
	loggingnoop "github.com/primandproper/platform-go/v6/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v6/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/trace"
)

// fakeBackend is a minimal routing.Backend used to exercise the Router in
// isolation (no chi). It records registered handlers and returns caller-set path
// values, so tests can drive any registered route directly.
type fakeBackend struct {
	handlers map[string]http.Handler
	pathVals map[string]string
	useCalls int
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{handlers: map[string]http.Handler{}, pathVals: map[string]string{}}
}

func (f *fakeBackend) Handle(method, pattern string, handler http.Handler) {
	f.handlers[method+" "+pattern] = handler
}

func (f *fakeBackend) Use(middleware ...Middleware) { f.useCalls += len(middleware) }

func (f *fakeBackend) PathValue(_ *http.Request, name string) string { return f.pathVals[name] }

func (f *fakeBackend) Handler() http.Handler { return http.NewServeMux() }

func (f *fakeBackend) serve(t *testing.T, method, pattern string, req *http.Request) *httptest.ResponseRecorder {
	t.Helper()

	h, ok := f.handlers[method+" "+pattern]
	must.True(t, ok)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	return rec
}

func newTestRouter(t *testing.T, backend Backend, opts ...RouterOption) *Router {
	t.Helper()

	logger := loggingnoop.NewLogger()
	tp := tracingnoop.NewTracerProvider()
	enc := encoding.NewServerEncoderDecoder(logger, tp, encoding.ContentTypeJSON)

	return New(backend, enc, logger, tp, opts...)
}

// failWriter is an http.ResponseWriter whose Write always fails, to exercise the
// write-error logging branches.
type failWriter struct{ hdr http.Header }

func (w *failWriter) Header() http.Header {
	if w.hdr == nil {
		w.hdr = http.Header{}
	}

	return w.hdr
}

func (*failWriter) Write([]byte) (int, error) { return 0, errors.New("write failed") }

func (*failWriter) WriteHeader(int) {}

// --- option setters -------------------------------------------------------

func TestRouteOptions(T *testing.T) {
	T.Parallel()

	rc := &routeConfig{}
	WithSummary("s")(rc)
	WithDescription("d")(rc)
	WithOperationID("op")(rc)
	WithTags("a", "b")(rc)
	WithDeprecated()(rc)
	WithResponseStatus(http.StatusAccepted)(rc)
	WithContentType(encoding.ContentTypeXML)(rc)
	WithEnvelope(false)(rc)
	WithSecurity("bearer", "read")(rc)
	WithAdditionalResponse(http.StatusNotFound, struct{}{}, "not found")(rc)
	WithMiddleware(func(h http.Handler) http.Handler { return h })(rc)

	test.EqOp(T, "s", rc.summary)
	test.EqOp(T, "d", rc.description)
	test.EqOp(T, "op", rc.operationID)
	test.SliceLen(T, 2, rc.tags)
	test.True(T, rc.deprecated)
	test.EqOp(T, http.StatusAccepted, rc.successStatus)
	test.EqOp(T, encoding.ContentTypeXML, rc.contentType)
	test.False(T, rc.envelope)
	must.SliceLen(T, 1, rc.security)
	test.EqOp(T, "bearer", rc.security[0].name)
	must.SliceLen(T, 1, rc.additionalResponses)
	test.EqOp(T, http.StatusNotFound, rc.additionalResponses[0].status)
	test.SliceLen(T, 1, rc.middleware)
}

func TestRouterOptions(T *testing.T) {
	T.Parallel()

	c := &routerConfig{}
	WithTitle("t")(c)
	WithVersion("v")(c)
	WithInfoDescription("d")(c)
	WithServer("http://example.test")(c)
	WithDefaultEnvelope(false)(c)

	test.EqOp(T, "t", c.title)
	test.EqOp(T, "v", c.version)
	test.EqOp(T, "d", c.description)
	test.SliceLen(T, 1, c.servers)
	test.False(T, c.envelope)
}

func TestNew_InfoAndServers(T *testing.T) {
	T.Parallel()

	r := newTestRouter(T, newFakeBackend(),
		WithTitle("Svc"),
		WithVersion("2.0.0"),
		WithInfoDescription("the description"),
		WithServer("https://api.example.test"),
	)

	spec := r.Spec()
	test.EqOp(T, "the description", *spec.Info.Description)
	must.SliceLen(T, 1, spec.Servers)
	test.EqOp(T, "https://api.example.test", spec.Servers[0].URL)
}

// --- Router surface -------------------------------------------------------

func TestRouter_UseAndBackend(T *testing.T) {
	T.Parallel()

	fb := newFakeBackend()
	r := newTestRouter(T, fb)

	mw1 := func(h http.Handler) http.Handler { return h }
	mw2 := func(h http.Handler) http.Handler { return http.HandlerFunc(h.ServeHTTP) }
	r.Use(mw1, mw2)
	test.EqOp(T, 2, fb.useCalls)
	test.EqOp(T, Backend(fb), r.Backend())
}

func TestRouter_encoderFor(T *testing.T) {
	T.Parallel()

	r := newTestRouter(T, newFakeBackend())

	test.EqOp(T, r.enc, r.encoderFor(nil))

	first := r.encoderFor(encoding.ContentTypeXML)
	test.NotNil(T, first)
	// second call returns the cached instance.
	test.EqOp(T, first, r.encoderFor(encoding.ContentTypeXML))
}

func TestRouter_ErrOnDuplicateOperation(T *testing.T) {
	T.Parallel()

	r := newTestRouter(T, newFakeBackend())
	h := func(_ context.Context, _ Empty) (Empty, error) { return Empty{}, nil }

	Get(r, "/dup", h)
	test.NoError(T, r.Err())

	// A second identical operation fails to record and is accumulated.
	Get(r, "/dup", h)
	test.Error(T, r.Err())
}

// --- verbs + Handle + Group ----------------------------------------------

func TestRouter_AllVerbs(T *testing.T) {
	T.Parallel()

	fb := newFakeBackend()
	r := newTestRouter(T, fb)
	h := func(_ context.Context, _ Empty) (Empty, error) { return Empty{}, nil }

	Get(r, "/a", h)
	Post(r, "/a", h)
	Put(r, "/a", h)
	Patch(r, "/a", h)
	Delete(r, "/a", h)
	Head(r, "/a", h)
	must.NoError(T, r.Err())

	for _, m := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete, http.MethodHead} {
		_, ok := fb.handlers[m+" /a"]
		test.True(T, ok)
	}
}

func TestRouter_HandleRaw(T *testing.T) {
	T.Parallel()

	fb := newFakeBackend()
	r := newTestRouter(T, fb)

	called := false
	r.Handle(http.MethodGet, "/raw/{id:uint64}", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusTeapot)
	}), func(h http.Handler) http.Handler { return h })

	rec := fb.serve(T, http.MethodGet, "/raw/{id}", httptest.NewRequest(http.MethodGet, "/raw/1", http.NoBody))
	test.True(T, called)
	test.EqOp(T, http.StatusTeapot, rec.Code)
}

// --- record operation (all option branches) -------------------------------

func TestRecordOperation_AllBranches(T *testing.T) {
	T.Parallel()

	r := newTestRouter(T, newFakeBackend())
	h := func(_ context.Context, _ struct {
		ID uint64 `path:"id"`
	}) (person, error) {
		return person{}, nil
	}

	Post(r, "/full/{id:uint64}", h,
		WithSummary("s"),
		WithDescription("d"),
		WithOperationID("customOp"),
		WithDeprecated(),
		WithTags("things"),
		WithSecurity("bearer", "read"),
		WithAdditionalResponse(http.StatusNotFound, httpx.APIError{}, "not found"),
	)
	must.NoError(T, r.Err())

	item, ok := r.Spec().Paths.MapOfPathItemValues["/full/{id}"]
	must.True(T, ok)
	op := item.MapOfOperationValues["post"]
	must.NotNil(T, op.ID)
	test.EqOp(T, "customOp", *op.ID)
	must.NotNil(T, op.Deprecated)
	test.True(T, *op.Deprecated)
	must.NotNil(T, op.Description)
	test.SliceLen(T, 1, op.Security)
}

type person struct {
	Name string `json:"name"`
	ID   uint64 `json:"id"`
}

// --- binding branches -----------------------------------------------------

type paramsInput struct {
	Q      string `query:"q"`
	Header string `header:"X-Thing"`
	Cookie string `cookie:"sid"`
	ID     uint64 `path:"id"`
}

func TestBind_AllParamLocations(T *testing.T) {
	T.Parallel()

	fb := newFakeBackend()
	fb.pathVals["id"] = "42"
	r := newTestRouter(T, fb)

	var seen paramsInput
	Get(r, "/p/{id:uint64}", func(_ context.Context, in paramsInput) (Empty, error) {
		seen = in
		return Empty{}, nil
	}, WithEnvelope(false))

	req := httptest.NewRequest(http.MethodGet, "/p/42?q=hi", http.NoBody)
	req.Header.Set("X-Thing", "hval")
	req.AddCookie(&http.Cookie{Name: "sid", Value: "cval"})

	rec := fb.serve(T, http.MethodGet, "/p/{id}", req)
	test.EqOp(T, http.StatusOK, rec.Code)
	test.EqOp(T, uint64(42), seen.ID)
	test.EqOp(T, "hi", seen.Q)
	test.EqOp(T, "hval", seen.Header)
	test.EqOp(T, "cval", seen.Cookie)
}

func TestBind_OptionalParamsAbsent(T *testing.T) {
	T.Parallel()

	fb := newFakeBackend()
	fb.pathVals["id"] = "1"
	r := newTestRouter(T, fb)

	Get(r, "/p/{id:uint64}", func(_ context.Context, in paramsInput) (Empty, error) {
		test.EqOp(T, "", in.Q)
		test.EqOp(T, "", in.Header)
		test.EqOp(T, "", in.Cookie)
		return Empty{}, nil
	}, WithEnvelope(false))

	// No query, header, or cookie present — all optional, so bind succeeds.
	rec := fb.serve(T, http.MethodGet, "/p/{id}", httptest.NewRequest(http.MethodGet, "/p/1", http.NoBody))
	test.EqOp(T, http.StatusOK, rec.Code)
}

func TestBind_MissingRequiredPath(T *testing.T) {
	T.Parallel()

	fb := newFakeBackend() // no path values set
	r := newTestRouter(T, fb)

	Get(r, "/x/{id:uint64}", func(_ context.Context, _ struct {
		ID uint64 `path:"id"`
	}) (Empty, error) {
		return Empty{}, nil
	})

	rec := fb.serve(T, http.MethodGet, "/x/{id}", httptest.NewRequest(http.MethodGet, "/x/", http.NoBody))
	test.EqOp(T, http.StatusBadRequest, rec.Code)
}

func TestBind_BodyDecodeError(T *testing.T) {
	T.Parallel()

	fb := newFakeBackend()
	r := newTestRouter(T, fb)

	Post(r, "/b", func(_ context.Context, _ person) (Empty, error) { return Empty{}, nil })

	req := httptest.NewRequest(http.MethodPost, "/b", strings.NewReader(`{not json`))
	req.Header.Set(encoding.ContentTypeHeaderKey, "application/json")

	rec := fb.serve(T, http.MethodPost, "/b", req)
	test.EqOp(T, http.StatusBadRequest, rec.Code)
}

type embeddedParams struct {
	Page int `query:"page"`
}

type embeddedInput struct {
	Body   string `json:"body"`
	Ignore string `json:"-"`
	Weird  string `query:""`
	embeddedParams
	ID uint64 `path:"id"`
}

func TestNewBindPlan_EmbeddedAndEdgeTags(T *testing.T) {
	T.Parallel()

	plan := newBindPlan[embeddedInput]([]ParamSpec{{Name: "id", Token: "uint64"}}, http.MethodPost)

	// page (embedded), id (path); Weird has an empty query name so it is not a param.
	test.True(T, plan.hasBody)

	var hasPage, hasID bool
	for i := range plan.params {
		switch plan.params[i].name {
		case "page":
			hasPage = true
		case "id":
			hasID = true
		}
	}
	test.True(T, hasPage)
	test.True(T, hasID)
}

type withUnexported struct {
	Name   string `json:"name"`
	hidden string
}

func TestNewBindPlan_SkipsUnexported(T *testing.T) {
	T.Parallel()

	plan := newBindPlan[withUnexported](nil, http.MethodPost)
	test.True(T, plan.hasBody)

	// read hidden so it is not flagged unused; its presence exercises the
	// unexported-field skip in collectFields.
	var v withUnexported
	_ = v.hidden
}

type badOut struct {
	Bad chan int `json:"bad"`
}

func TestRecordOperation_AddOperationError(T *testing.T) {
	T.Parallel()

	r := newTestRouter(T, newFakeBackend())

	// A response type swaggest cannot reflect (a channel field) makes AddOperation
	// fail; the error is accumulated rather than panicking.
	Get(r, "/bad", func(_ context.Context, _ Empty) (badOut, error) { return badOut{}, nil })
	test.Error(T, r.Err())
}

func TestRegErrors_AddNil(T *testing.T) {
	T.Parallel()

	e := &regErrors{}
	e.add(nil)
	test.SliceLen(T, 0, e.snapshot())
}

func TestSetScalar_Extra(T *testing.T) {
	T.Parallel()

	T.Run("int", func(t *testing.T) {
		t.Parallel()
		var n int
		must.NoError(t, setScalar(reflect.ValueOf(&n).Elem(), "-5"))
		test.EqOp(t, -5, n)
	})

	T.Run("pointer allocates", func(t *testing.T) {
		t.Parallel()
		var p *int
		must.NoError(t, setScalar(reflect.ValueOf(&p).Elem(), "9"))
		must.NotNil(t, p)
		test.EqOp(t, 9, *p)
	})

	T.Run("unsupported kind errors", func(t *testing.T) {
		t.Parallel()
		var c complex128
		test.Error(t, setScalar(reflect.ValueOf(&c).Elem(), "1"))
	})

	T.Run("bad bool errors", func(t *testing.T) {
		t.Parallel()
		var b bool
		test.Error(t, setScalar(reflect.ValueOf(&b).Elem(), "notbool"))
	})

	T.Run("bad float errors", func(t *testing.T) {
		t.Parallel()
		var f float64
		test.Error(t, setScalar(reflect.ValueOf(&f).Elem(), "notfloat"))
	})

	T.Run("bad int errors", func(t *testing.T) {
		t.Parallel()
		var n int
		test.Error(t, setScalar(reflect.ValueOf(&n).Elem(), "notint"))
	})
}

func TestRawParam_DefaultLocation(T *testing.T) {
	T.Parallel()

	_, ok := rawParam(newFakeBackend(), httptest.NewRequest(http.MethodGet, "/", http.NoBody), &paramField{in: "bogus"})
	test.False(T, ok)
}

func TestBindError(T *testing.T) {
	T.Parallel()

	inner := errors.New("inner")
	be := &bindError{msg: "outer", err: inner}
	test.EqOp(T, "outer: inner", be.Error())
	test.EqOp(T, inner, be.Unwrap())

	bare := &bindError{msg: "just msg"}
	test.EqOp(T, "just msg", bare.Error())
	test.Nil(T, bare.Unwrap())
}

// --- pathparser helpers ---------------------------------------------------

func TestTokenMatchesType_Extra(T *testing.T) {
	T.Parallel()

	test.False(T, tokenMatchesType("bogus", reflect.TypeFor[int]()))
	test.False(T, tokenMatchesType("int", reflect.TypeFor[string]()))
	test.False(T, tokenMatchesType("float", reflect.TypeFor[string]()))
	test.False(T, tokenMatchesType("uint64", reflect.TypeFor[string]()))
	// double pointer exercises the deref loop more than once.
	test.True(T, tokenMatchesType("int", reflect.TypeFor[**int]()))
}

func TestIsEmptyTypeAndResponseStructure(T *testing.T) {
	T.Parallel()

	test.True(T, isEmptyType[Empty]())
	test.False(T, isEmptyType[int]())

	test.Nil(T, responseStructure[Empty](true))
	test.NotNil(T, responseStructure[int](false))
	test.NotNil(T, responseStructure[int](true))
}

func TestApplyMiddleware_SkipsNil(T *testing.T) {
	T.Parallel()

	base := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	wrapped := applyMiddleware(base, []Middleware{nil, func(h http.Handler) http.Handler { return h }})
	test.NotNil(T, wrapped)
}

func TestDefaultOperationID_Root(T *testing.T) {
	T.Parallel()

	test.EqOp(T, "get_root", defaultOperationID(http.MethodGet, "/"))
}

func TestDetailsFromCtx_WithTraceID(T *testing.T) {
	T.Parallel()

	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10},
		SpanID:  trace.SpanID{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08},
	})
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	details := detailsFromCtx(ctx)
	test.EqOp(T, "0102030405060708090a0b0c0d0e0f10", details.TraceID)
}

// --- MountOpenAPI branches ------------------------------------------------

func TestMountOpenAPI_SpecOnly(T *testing.T) {
	T.Parallel()

	fb := newFakeBackend()
	r := newTestRouter(T, fb)
	r.MountOpenAPI("/spec.json", "") // empty uiPath: no docs route

	_, hasSpec := fb.handlers["GET /spec.json"]
	test.True(T, hasSpec)
	_, hasDocs := fb.handlers["GET "]
	test.False(T, hasDocs)
}

func TestMountOpenAPI_MarshalError(T *testing.T) {
	T.Parallel()

	fb := newFakeBackend()
	r := newTestRouter(T, fb)
	r.MountOpenAPI("/spec.json", "/docs")

	// Poison the spec so MarshalSpec fails, exercising the 500 branch.
	r.reflector.Spec.MapOfAnything = map[string]any{"x-bad": make(chan int)}

	rec := fb.serve(T, http.MethodGet, "/spec.json", httptest.NewRequest(http.MethodGet, "/spec.json", http.NoBody))
	test.EqOp(T, http.StatusInternalServerError, rec.Code)
}

func TestMountOpenAPI_WriteErrors(T *testing.T) {
	T.Parallel()

	fb := newFakeBackend()
	r := newTestRouter(T, fb)
	r.MountOpenAPI("/spec.json", "/docs")

	// Both handlers should swallow (log) a write failure without panicking.
	specHandler := fb.handlers["GET /spec.json"]
	specHandler.ServeHTTP(&failWriter{}, httptest.NewRequest(http.MethodGet, "/spec.json", http.NoBody))

	docsHandler := fb.handlers["GET /docs"]
	docsHandler.ServeHTTP(&failWriter{}, httptest.NewRequest(http.MethodGet, "/docs", http.NoBody))
}

package routing_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v5/encoding"
	httpx "github.com/primandproper/platform-go/v5/errors/http"
	loggingnoop "github.com/primandproper/platform-go/v5/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v5/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v5/observability/tracing/noop"
	"github.com/primandproper/platform-go/v5/routing"
	"github.com/primandproper/platform-go/v5/routing/chi"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func buildTestRouter(t *testing.T, opts ...routing.RouterOption) *routing.Router {
	t.Helper()

	logger := loggingnoop.NewLogger()
	tracerProvider := tracingnoop.NewTracerProvider()
	backend := chi.NewBackend(logger, tracerProvider, metricsnoop.NewMetricsProvider(), &chi.Config{ServiceName: t.Name()})
	enc := encoding.NewServerEncoderDecoder(logger, tracerProvider, encoding.ContentTypeJSON)

	return routing.New(backend, enc, logger, tracerProvider, opts...)
}

func doRequest(t *testing.T, r *routing.Router, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	req.Header.Set(encoding.ContentTypeHeaderKey, "application/json")
	rec := httptest.NewRecorder()

	r.Handler().ServeHTTP(rec, req)

	return rec
}

type createUserInput struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	OrgID uint64 `path:"orgID"`
	Dry   bool   `query:"dry"`
}

type userOutput struct {
	Name  string `json:"name"`
	Email string `json:"email"`
	ID    uint64 `json:"id"`
	Dry   bool   `json:"dry"`
}

type envelope[T any] struct {
	Data  T `json:"data"`
	Error *struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

func TestRouter_TypedPOST(T *testing.T) {
	T.Parallel()

	T.Run("decodes body + path + query, encodes enveloped response with 201", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t)
		routing.Post(r, "/orgs/{orgID:uint64}/users", func(_ context.Context, in createUserInput) (userOutput, error) {
			return userOutput{ID: in.OrgID, Name: in.Name, Email: in.Email, Dry: in.Dry}, nil
		})
		must.NoError(t, r.Err())

		rec := doRequest(t, r, http.MethodPost, "/orgs/7/users?dry=true", `{"name":"Ada","email":"ada@example.com"}`)

		test.EqOp(t, http.StatusCreated, rec.Code)

		var got envelope[userOutput]
		must.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		test.EqOp(t, uint64(7), got.Data.ID)
		test.EqOp(t, "Ada", got.Data.Name)
		test.EqOp(t, "ada@example.com", got.Data.Email)
		test.True(t, got.Data.Dry)
	})
}

type getUserInput struct {
	OrgID uint64 `path:"orgID"`
}

func TestRouter_TypedGET(T *testing.T) {
	T.Parallel()

	T.Run("binds uint64 path param and returns 200", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t)
		routing.Get(r, "/orgs/{orgID:uint64}", func(_ context.Context, in getUserInput) (userOutput, error) {
			return userOutput{ID: in.OrgID}, nil
		})
		must.NoError(t, r.Err())

		rec := doRequest(t, r, http.MethodGet, "/orgs/99", "")

		test.EqOp(t, http.StatusOK, rec.Code)

		var got envelope[userOutput]
		must.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		test.EqOp(t, uint64(99), got.Data.ID)
	})

	T.Run("invalid uint64 path value yields 400", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t)
		routing.Get(r, "/orgs/{orgID:uint64}", func(_ context.Context, in getUserInput) (userOutput, error) {
			return userOutput{ID: in.OrgID}, nil
		})

		rec := doRequest(t, r, http.MethodGet, "/orgs/not-a-number", "")

		test.EqOp(t, http.StatusBadRequest, rec.Code)

		var got envelope[userOutput]
		must.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		must.NotNil(t, got.Error)
		test.EqOp(t, string(httpx.ErrValidatingRequestInput), got.Error.Code)
	})
}

func TestRouter_HandlerError(T *testing.T) {
	T.Parallel()

	T.Run("maps a platform error to its HTTP status and envelope", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t)
		routing.Get(r, "/orgs/{orgID:uint64}", func(_ context.Context, _ getUserInput) (userOutput, error) {
			return userOutput{}, sql.ErrNoRows
		})

		rec := doRequest(t, r, http.MethodGet, "/orgs/1", "")

		test.EqOp(t, http.StatusNotFound, rec.Code)

		var got envelope[userOutput]
		must.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		must.NotNil(t, got.Error)
		test.EqOp(t, string(httpx.ErrDataNotFound), got.Error.Code)
	})
}

type validatedInput struct {
	Name string `json:"name"`
}

func (v *validatedInput) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, v, validation.Field(&v.Name, validation.Required))
}

func TestRouter_Validation(T *testing.T) {
	T.Parallel()

	T.Run("input failing ValidateWithContext yields 400", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t)
		routing.Post(r, "/widgets", func(_ context.Context, _ validatedInput) (routing.Empty, error) {
			return routing.Empty{}, nil
		})

		rec := doRequest(t, r, http.MethodPost, "/widgets", `{"name":""}`)

		test.EqOp(t, http.StatusBadRequest, rec.Code)
	})
}

type deleteInput struct {
	ID uint64 `path:"id"`
}

func TestRouter_EmptyResponse(T *testing.T) {
	T.Parallel()

	T.Run("Empty output writes only the configured status with no body", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t)
		routing.Delete(r, "/things/{id:uint64}", func(_ context.Context, _ deleteInput) (routing.Empty, error) {
			return routing.Empty{}, nil
		}, routing.WithResponseStatus(http.StatusNoContent))

		rec := doRequest(t, r, http.MethodDelete, "/things/5", "")

		test.EqOp(t, http.StatusNoContent, rec.Code)
		test.EqOp(t, 0, rec.Body.Len())
	})
}

func TestRouter_RawEnvelopeDisabled(T *testing.T) {
	T.Parallel()

	T.Run("WithEnvelope(false) encodes the output directly", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t)
		routing.Get(r, "/raw/{orgID:uint64}", func(_ context.Context, in getUserInput) (userOutput, error) {
			return userOutput{ID: in.OrgID, Name: "x"}, nil
		}, routing.WithEnvelope(false))

		rec := doRequest(t, r, http.MethodGet, "/raw/3", "")

		test.EqOp(t, http.StatusOK, rec.Code)

		var got userOutput
		must.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		test.EqOp(t, uint64(3), got.ID)
		test.EqOp(t, "x", got.Name)
	})
}

func TestRouter_PathFieldMismatchPanics(T *testing.T) {
	T.Parallel()

	T.Run("declaring a path param with no matching field panics", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t)

		defer func() { test.NotNil(t, recover()) }()

		routing.Get(r, "/x/{missing:string}", func(_ context.Context, _ getUserInput) (userOutput, error) {
			return userOutput{}, nil
		})
	})

	T.Run("declaring a path param with an incompatible field type panics", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t)

		defer func() { test.NotNil(t, recover()) }()

		routing.Get(r, "/orgs/{orgID:string}", func(_ context.Context, _ getUserInput) (userOutput, error) {
			return userOutput{}, nil
		})
	})
}

func TestRouter_SpecAssembly(T *testing.T) {
	T.Parallel()

	T.Run("registered routes appear in the spec and it marshals as OpenAPI 3", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t, routing.WithTitle("Test API"), routing.WithVersion("1.2.3"))
		routing.Post(r, "/orgs/{orgID:uint64}/users", func(_ context.Context, in createUserInput) (userOutput, error) {
			return userOutput{ID: in.OrgID}, nil
		}, routing.WithSummary("Create a user"), routing.WithTags("users"))
		routing.Get(r, "/orgs/{orgID:uint64}", func(_ context.Context, in getUserInput) (userOutput, error) {
			return userOutput{ID: in.OrgID}, nil
		})
		must.NoError(t, r.Err())

		spec := r.Spec()
		must.NotNil(t, spec)
		test.EqOp(t, "Test API", spec.Info.Title)
		test.EqOp(t, "1.2.3", spec.Info.Version)

		_, hasUsersPath := spec.Paths.MapOfPathItemValues["/orgs/{orgID}/users"]
		test.True(t, hasUsersPath)
		_, hasOrgPath := spec.Paths.MapOfPathItemValues["/orgs/{orgID}"]
		test.True(t, hasOrgPath)

		raw, err := r.MarshalSpec()
		must.NoError(t, err)

		var doc map[string]any
		must.NoError(t, json.Unmarshal(raw, &doc))
		version, _ := doc["openapi"].(string)
		test.True(t, strings.HasPrefix(version, "3.0"))
	})
}

func TestRouter_MountOpenAPI(T *testing.T) {
	T.Parallel()

	T.Run("serves the spec as JSON and a docs page", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t)
		routing.Get(r, "/ping", func(_ context.Context, _ routing.Empty) (userOutput, error) {
			return userOutput{Name: "pong"}, nil
		})
		r.MountOpenAPI("/openapi.json", "/docs")
		must.NoError(t, r.Err())

		rec := doRequest(t, r, http.MethodGet, "/openapi.json", "")
		test.EqOp(t, http.StatusOK, rec.Code)
		test.StrContains(t, rec.Header().Get("Content-Type"), "application/json")

		var doc map[string]any
		must.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
		test.MapContainsKey(t, doc, "openapi")
		test.MapContainsKey(t, doc, "paths")

		docsRec := doRequest(t, r, http.MethodGet, "/docs", "")
		test.EqOp(t, http.StatusOK, docsRec.Code)
		test.StrContains(t, docsRec.Header().Get("Content-Type"), "text/html")
	})
}

func TestRouter_Group(T *testing.T) {
	T.Parallel()

	T.Run("group prefixes paths and inherits tags", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t)
		r.Group("/v1", func(sub *routing.Router) {
			routing.Get(sub, "/orgs/{orgID:uint64}", func(_ context.Context, in getUserInput) (userOutput, error) {
				return userOutput{ID: in.OrgID}, nil
			})
		}, "v1")
		must.NoError(t, r.Err())

		rec := doRequest(t, r, http.MethodGet, "/v1/orgs/12", "")
		test.EqOp(t, http.StatusOK, rec.Code)

		_, ok := r.Spec().Paths.MapOfPathItemValues["/v1/orgs/{orgID}"]
		test.True(t, ok)
	})
}

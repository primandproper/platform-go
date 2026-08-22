package routing_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	httpx "github.com/primandproper/platform-go/v13/errors/http"
	"github.com/primandproper/platform-go/v13/routing"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// flatError is the shape of a service whose error format predates this router:
// one string field, no envelope, no code.
type flatError struct {
	Error string `json:"error"`
}

// flatEncoder renders every error as flatError, with 409 for an in-use resource.
func flatEncoder(_ context.Context, err error) (status int, body any) {
	switch {
	case errors.Is(err, platformerrors.ErrResourceInUse):
		return http.StatusConflict, flatError{Error: "resource is in use"}
	case errors.Is(err, sql.ErrNoRows):
		return http.StatusNotFound, flatError{Error: "not found"}
	default:
		return http.StatusInternalServerError, flatError{Error: err.Error()}
	}
}

func TestRouter_WithErrorEncoder(T *testing.T) {
	T.Parallel()

	T.Run("renders a handler error in the service's own format", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t, routing.WithErrorEncoder(flatEncoder))
		routing.Delete(r, "/things/{id:uint64}", func(_ context.Context, _ deleteInput) (routing.Empty, error) {
			return routing.Empty{}, platformerrors.ErrResourceInUse
		})
		must.NoError(t, r.Err())

		rec := doRequest(t, r, http.MethodDelete, "/things/5", "")

		test.EqOp(t, http.StatusConflict, rec.Code)

		var got flatError
		must.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		test.EqOp(t, "resource is in use", got.Error)

		// The platform envelope's fields must be absent, not merely unread.
		var raw map[string]any
		must.NoError(t, json.Unmarshal(rec.Body.Bytes(), &raw))
		test.MapNotContainsKey(t, raw, "details")
		test.MapNotContainsKey(t, raw, "data")
	})

	T.Run("binding failures reach the encoder carrying their platform code", func(t *testing.T) {
		t.Parallel()

		var seen []httpx.ErrorCode

		r := buildTestRouter(t, routing.WithErrorEncoder(func(_ context.Context, err error) (int, any) {
			coded, ok := errors.AsType[routing.CodedError](err)
			must.True(t, ok)
			seen = append(seen, coded.ErrorCode())

			return http.StatusBadRequest, flatError{Error: err.Error()}
		}))
		routing.Post(r, "/widgets", func(_ context.Context, _ validatedInput) (routing.Empty, error) {
			return routing.Empty{}, nil
		})
		must.NoError(t, r.Err())

		// An undecodable body, then a decodable one that fails validation.
		test.EqOp(t, http.StatusBadRequest, doRequest(t, r, http.MethodPost, "/widgets", `{`).Code)
		test.EqOp(t, http.StatusBadRequest, doRequest(t, r, http.MethodPost, "/widgets", `{"name":""}`).Code)

		test.Eq(t, []httpx.ErrorCode{httpx.ErrDecodingRequestInput, httpx.ErrValidatingRequestInput}, seen)
	})

	T.Run("a nil body writes the status alone", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t, routing.WithErrorEncoder(func(context.Context, error) (int, any) {
			return http.StatusGone, nil
		}))
		routing.Get(r, "/orgs/{orgID:uint64}", func(_ context.Context, _ getUserInput) (userOutput, error) {
			return userOutput{}, sql.ErrNoRows
		})

		rec := doRequest(t, r, http.MethodGet, "/orgs/1", "")

		test.EqOp(t, http.StatusGone, rec.Code)
		test.EqOp(t, 0, rec.Body.Len())
	})

	T.Run("an unwritable status becomes a 500 instead of panicking", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t, routing.WithErrorEncoder(func(context.Context, error) (int, any) {
			return 0, flatError{Error: "boom"}
		}))
		routing.Get(r, "/orgs/{orgID:uint64}", func(_ context.Context, _ getUserInput) (userOutput, error) {
			return userOutput{}, sql.ErrNoRows
		})

		rec := doRequest(t, r, http.MethodGet, "/orgs/1", "")

		test.EqOp(t, http.StatusInternalServerError, rec.Code)

		var got flatError
		must.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		test.EqOp(t, "boom", got.Error)
	})

	T.Run("a status at the edge of the writable range is written as it is", func(t *testing.T) {
		t.Parallel()

		// 100 and 999 are the range a ResponseWriter accepts; the guard exists
		// because anything outside it panics. Both edges are inside the range,
		// so an encoder that names one is obeyed rather than clamped — which is
		// the half of the guard that the 0 above cannot say anything about.
		for _, status := range []int{100, 999} {
			t.Run(strconv.Itoa(status), func(t *testing.T) {
				t.Parallel()

				r := buildTestRouter(t, routing.WithErrorEncoder(func(context.Context, error) (int, any) {
					return status, flatError{Error: "boom"}
				}))
				routing.Get(r, "/orgs/{orgID:uint64}", func(_ context.Context, _ getUserInput) (userOutput, error) {
					return userOutput{}, sql.ErrNoRows
				})

				test.EqOp(t, status, doRequest(t, r, http.MethodGet, "/orgs/1", "").Code)
			})
		}
	})

	T.Run("a status outside the writable range becomes a 500", func(t *testing.T) {
		t.Parallel()

		for _, status := range []int{99, 1000} {
			t.Run(strconv.Itoa(status), func(t *testing.T) {
				t.Parallel()

				r := buildTestRouter(t, routing.WithErrorEncoder(func(context.Context, error) (int, any) {
					return status, flatError{Error: "boom"}
				}))
				routing.Get(r, "/orgs/{orgID:uint64}", func(_ context.Context, _ getUserInput) (userOutput, error) {
					return userOutput{}, sql.ErrNoRows
				})

				test.EqOp(t, http.StatusInternalServerError, doRequest(t, r, http.MethodGet, "/orgs/1", "").Code)
			})
		}
	})

	T.Run("an encoder can delegate to DefaultErrorBody", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t, routing.WithErrorEncoder(func(ctx context.Context, err error) (int, any) {
			if errors.Is(err, platformerrors.ErrResourceInUse) {
				return http.StatusConflict, flatError{Error: "resource is in use"}
			}

			return routing.DefaultErrorBody(ctx, err)
		}))
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

	T.Run("a nil encoder leaves the default rendering in place", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t, routing.WithErrorEncoder(nil))
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

	T.Run("groups inherit the router's encoder", func(t *testing.T) {
		t.Parallel()

		r := buildTestRouter(t, routing.WithErrorEncoder(flatEncoder))
		r.Group("/v1", func(sub *routing.Router) {
			routing.Get(sub, "/orgs/{orgID:uint64}", func(_ context.Context, _ getUserInput) (userOutput, error) {
				return userOutput{}, sql.ErrNoRows
			})
		})
		must.NoError(t, r.Err())

		rec := doRequest(t, r, http.MethodGet, "/v1/orgs/1", "")

		test.EqOp(t, http.StatusNotFound, rec.Code)

		var got flatError
		must.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
		test.EqOp(t, "not found", got.Error)
	})

	T.Run("the error is still recorded on the operation", func(t *testing.T) {
		t.Parallel()

		rec := &errorSpanProcessor{}
		tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec), sdktrace.WithSampler(sdktrace.AlwaysSample()))

		r := buildTestRouter(t, routing.WithErrorEncoder(flatEncoder), routing.WithTracerProvider(tp))
		routing.Get(r, "/orgs/{orgID:uint64}", func(_ context.Context, _ getUserInput) (userOutput, error) {
			return userOutput{}, errors.New("the database is on fire")
		})

		test.EqOp(t, http.StatusInternalServerError, doRequest(t, r, http.MethodGet, "/orgs/1", "").Code)

		// Rendering the error the service's way must not cost the service the
		// router's error observability — the workaround this option replaces
		// bypassed it entirely.
		//
		// A 500 rather than the 404 this once used, because the status is now what
		// decides severity: a fault marks the span, a client mistake does not. See
		// TestRouter_ErrorSeverity for the other side of that.
		test.SliceContains(t, rec.errored(), "get_orgs_orgID")
	})
}

// errorSpanProcessor records the names of spans that ended with an error status.
type errorSpanProcessor struct {
	names []string
	mu    sync.Mutex
}

func (p *errorSpanProcessor) OnStart(context.Context, sdktrace.ReadWriteSpan) {}

func (p *errorSpanProcessor) OnEnd(s sdktrace.ReadOnlySpan) {
	if s.Status().Code != codes.Error {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.names = append(p.names, s.Name())
}

func (p *errorSpanProcessor) Shutdown(context.Context) error   { return nil }
func (p *errorSpanProcessor) ForceFlush(context.Context) error { return nil }

func (p *errorSpanProcessor) errored() []string {
	p.mu.Lock()
	defer p.mu.Unlock()

	return append([]string(nil), p.names...)
}

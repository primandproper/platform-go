package client_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v10/encoding"
	platformerrors "github.com/primandproper/platform-go/v10/errors"
	httpx "github.com/primandproper/platform-go/v10/errors/http"
	"github.com/primandproper/platform-go/v10/ratelimiting"
	"github.com/primandproper/platform-go/v10/routing"
	"github.com/primandproper/platform-go/v10/routing/backends/chi"
	"github.com/primandproper/platform-go/v10/routing/client"

	"github.com/google/uuid"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// The published contract: one statement of the route, imported by both ends.
type (
	getOrderRequest struct {
		Verbose  *bool     `query:"verbose"`
		Currency string    `query:"currency"`
		Trace    string    `header:"X-Trace"`
		Session  string    `cookie:"session"`
		OrderID  uuid.UUID `path:"orderID"`
	}

	placeOrderRequest struct {
		At       time.Time `json:"at"`
		Item     string    `json:"item"`
		Secret   string    `header:"X-Api-Key"`
		OrgID    uint64    `path:"orgID"`
		Quantity int       `json:"quantity"`
	}

	order struct {
		Item     string    `json:"item"`
		Currency string    `json:"currency"`
		Trace    string    `json:"trace"`
		Session  string    `json:"session"`
		ID       uuid.UUID `json:"id"`
		OrgID    uint64    `json:"orgID"`
		Quantity int       `json:"quantity"`
		Verbose  bool      `json:"verbose"`
	}
)

var (
	getOrder = routing.Endpoint[getOrderRequest, order]{
		Method:  http.MethodGet,
		Pattern: "/orders/{orderID:uuid}",
	}

	placeOrder = routing.Endpoint[placeOrderRequest, order]{
		Method:  http.MethodPost,
		Pattern: "/orgs/{orgID:uint64}/orders",
	}

	cancelOrder = routing.Endpoint[getOrderRequest, routing.Empty]{
		Method:  http.MethodDelete,
		Pattern: "/orders/{orderID:uuid}",
	}
)

// serve stands up a real routing.Router over an httptest server and returns a
// client pointed at it, so every assertion below crosses an actual HTTP boundary
// and comes back through the same bind plan the client inverted.
func serve(t *testing.T, register func(r *routing.Router), opts ...client.Option) *client.Client {
	t.Helper()

	backend := chi.NewBackend(&chi.Config{ServiceName: "orders-test"})
	r := routing.New(backend, encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON))

	register(r)
	must.NoError(t, r.Err())

	srv := httptest.NewServer(r.Handler())
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL, append([]client.Option{client.WithHTTPClient(srv.Client())}, opts...)...)
	must.NoError(t, err)

	return c
}

func TestCall_ParametersRoundTrip(T *testing.T) {
	T.Parallel()

	var seen getOrderRequest

	c := serve(T, func(r *routing.Router) {
		routing.Register(r, getOrder, func(_ context.Context, in getOrderRequest) (order, error) {
			seen = in

			return order{
				ID:       in.OrderID,
				Currency: in.Currency,
				Trace:    in.Trace,
				Session:  in.Session,
				Verbose:  in.Verbose != nil && *in.Verbose,
			}, nil
		})
	})

	verbose := true
	req := getOrderRequest{
		OrderID:  uuid.New(),
		Currency: "USD",
		Trace:    "abc-123",
		Session:  "s3cr3t",
		Verbose:  &verbose,
	}

	got, err := client.Call(T.Context(), c, getOrder, req)
	must.NoError(T, err)

	// The whole point: what the handler received is what the caller passed.
	test.Eq(T, req, seen)
	test.EqOp(T, req.OrderID, got.ID)
	test.EqOp(T, "USD", got.Currency)
	test.EqOp(T, "abc-123", got.Trace)
	test.EqOp(T, "s3cr3t", got.Session)
	test.True(T, got.Verbose)
}

func TestCall_NilPointerParamIsAbsent(T *testing.T) {
	T.Parallel()

	var seen getOrderRequest

	c := serve(T, func(r *routing.Router) {
		routing.Register(r, getOrder, func(_ context.Context, in getOrderRequest) (order, error) {
			seen = in

			return order{}, nil
		})
	})

	_, err := client.Call(T.Context(), c, getOrder, getOrderRequest{OrderID: uuid.New()})
	must.NoError(T, err)

	test.Nil(T, seen.Verbose)
	test.EqOp(T, "", seen.Currency)
}

func TestCall_BodyOmitsParameterFields(T *testing.T) {
	T.Parallel()

	var (
		seen    placeOrderRequest
		rawBody string
	)

	c := serve(T, func(r *routing.Router) {
		r.Use(func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
				body, _ := io.ReadAll(req.Body)
				rawBody = string(body)
				req.Body = io.NopCloser(bytes.NewReader(body))

				next.ServeHTTP(res, req)
			})
		})

		routing.Register(r, placeOrder, func(_ context.Context, in placeOrderRequest) (order, error) {
			seen = in

			return order{Item: in.Item, OrgID: in.OrgID, Quantity: in.Quantity}, nil
		})
	})

	req := placeOrderRequest{
		OrgID:    7,
		Item:     "widget",
		Quantity: 3,
		Secret:   "do-not-put-me-in-the-body",
		At:       time.Date(2026, time.August, 8, 0, 0, 0, 0, time.UTC),
	}

	got, err := client.Call(T.Context(), c, placeOrder, req)
	must.NoError(T, err)

	test.Eq(T, req, seen)
	test.EqOp(T, uint64(7), got.OrgID)

	// A header value has one place on the wire, and the body is not it.
	test.StrNotContains(T, rawBody, "do-not-put-me-in-the-body")
	test.StrContains(T, rawBody, "widget")
}

func TestCall_EmptyOutReadsNoBody(T *testing.T) {
	T.Parallel()

	c := serve(T, func(r *routing.Router) {
		routing.Register(r, cancelOrder, func(_ context.Context, _ getOrderRequest) (routing.Empty, error) {
			return routing.Empty{}, nil
		}, routing.WithResponseStatus(http.StatusNoContent))
	})

	out, err := client.Call(T.Context(), c, cancelOrder, getOrderRequest{OrderID: uuid.New()})
	must.NoError(T, err)
	test.Eq(T, routing.Empty{}, out)
}

func TestCall_ErrorsRoundTripToSentinels(T *testing.T) {
	T.Parallel()

	cases := []struct {
		returned error
		wantIs   error
		name     string
		wantCode httpx.ErrorCode
		wantHTTP int
	}{
		{
			name:     "not found",
			returned: sql.ErrNoRows,
			wantIs:   sql.ErrNoRows,
			wantCode: httpx.ErrDataNotFound,
			wantHTTP: http.StatusNotFound,
		},
		{
			name:     "rate limited",
			returned: ratelimiting.ErrRateLimited,
			wantIs:   ratelimiting.ErrRateLimited,
			wantCode: httpx.ErrTooManyRequests,
			wantHTTP: http.StatusTooManyRequests,
		},
		{
			name:     "permission denied",
			returned: platformerrors.ErrPermissionDenied,
			wantIs:   platformerrors.ErrPermissionDenied,
			wantCode: httpx.ErrUserIsNotAuthorized,
			wantHTTP: http.StatusForbidden,
		},
		{
			name:     "resource in use",
			returned: platformerrors.ErrResourceInUse,
			wantIs:   platformerrors.ErrResourceInUse,
			wantCode: httpx.ErrResourceConflict,
			wantHTTP: http.StatusConflict,
		},
	}

	for _, tc := range cases {
		T.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			c := serve(t, func(r *routing.Router) {
				routing.Register(r, getOrder, func(_ context.Context, _ getOrderRequest) (order, error) {
					return order{}, tc.returned
				})
			})

			_, err := client.Call(t.Context(), c, getOrder, getOrderRequest{OrderID: uuid.New()})
			must.Error(t, err)

			// The caller branches exactly as it would on a local call.
			test.ErrorIs(t, err, tc.wantIs)

			var apiErr *client.Error
			must.True(t, errors.As(err, &apiErr))
			test.EqOp(t, tc.wantCode, apiErr.Code)
			test.EqOp(t, tc.wantHTTP, apiErr.Status)
			test.EqOp(t, http.MethodGet, apiErr.Method)
			test.EqOp(t, "/orders/{orderID}", apiErr.Path)
		})
	}
}

// A code with no single sentinel behind it still reports its code and status; it
// just does not claim to know which error produced it.
func TestCall_UnmappableCodeCarriesCodeOnly(T *testing.T) {
	T.Parallel()

	c := serve(T, func(r *routing.Router) {
		routing.Register(r, getOrder, func(_ context.Context, _ getOrderRequest) (order, error) {
			return order{}, platformerrors.ErrInvalidIDProvided
		})
	})

	_, err := client.Call(T.Context(), c, getOrder, getOrderRequest{OrderID: uuid.New()})
	must.Error(T, err)

	var apiErr *client.Error
	must.True(T, errors.As(err, &apiErr))
	test.EqOp(T, httpx.ErrValidatingRequestInput, apiErr.Code)
	test.Nil(T, apiErr.Unwrap())
}

func TestCall_MissingRequiredPathParam(T *testing.T) {
	T.Parallel()

	type patternInput struct {
		Slug string `path:"slug"`
	}

	ep := routing.Endpoint[patternInput, order]{Method: http.MethodGet, Pattern: "/things/{slug}"}

	c := serve(T, func(r *routing.Router) {
		routing.Register(r, ep, func(_ context.Context, _ patternInput) (order, error) {
			return order{}, nil
		})
	})

	_, err := client.Call(T.Context(), c, ep, patternInput{})
	must.Error(T, err)
	test.ErrorIs(T, err, platformerrors.ErrEmptyInputParameter)
}

// A path value carrying reserved characters goes on the wire percent-escaped, so
// that it addresses one segment rather than silently becoming two.
//
// This asserts what leaves the client, not what a handler receives, because the
// backends do not agree on the other end: net/http's ServeMux decodes a path
// value, chi hands back the raw escaped text, and gin and httprouter route the
// escaped form to nothing at all. That disagreement predates this package and is
// the server's to settle; escaping here is correct either way, and not escaping
// would be wrong for all four.
func TestCall_PathValuesAreEscaped(T *testing.T) {
	T.Parallel()

	type slugInput struct {
		Slug string `path:"slug"`
	}

	ep := routing.Endpoint[slugInput, slugInput]{Method: http.MethodGet, Pattern: "/things/{slug}"}

	var requestURI string

	srv := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		requestURI = req.RequestURI
		res.WriteHeader(http.StatusNoContent)
	}))
	T.Cleanup(srv.Close)

	c, err := client.New(srv.URL, client.WithHTTPClient(srv.Client()))
	must.NoError(T, err)

	_, err = client.Call(T.Context(), c, ep, slugInput{Slug: "a/b c"})
	must.NoError(T, err)
	test.EqOp(T, "/things/a%2Fb%20c", requestURI)
}

func TestCall_WithoutEnvelope(T *testing.T) {
	T.Parallel()

	c := serve(T, func(r *routing.Router) {
		routing.Register(r, getOrder, func(_ context.Context, in getOrderRequest) (order, error) {
			return order{ID: in.OrderID, Currency: "EUR"}, nil
		}, routing.WithEnvelope(false))
	}, client.WithEnvelope(false))

	id := uuid.New()

	got, err := client.Call(T.Context(), c, getOrder, getOrderRequest{OrderID: id})
	must.NoError(T, err)
	test.EqOp(T, id, got.ID)
	test.EqOp(T, "EUR", got.Currency)
}

func TestCall_BaseURLPrefix(T *testing.T) {
	T.Parallel()

	backend := chi.NewBackend(&chi.Config{ServiceName: "orders-test"})
	r := routing.New(backend, encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON))

	r.Group("/v1", func(sub *routing.Router) {
		routing.Register(sub, getOrder, func(_ context.Context, in getOrderRequest) (order, error) {
			return order{ID: in.OrderID}, nil
		})
	})
	must.NoError(T, r.Err())

	srv := httptest.NewServer(r.Handler())
	T.Cleanup(srv.Close)

	c, err := client.New(srv.URL+"/v1/", client.WithHTTPClient(srv.Client()))
	must.NoError(T, err)

	id := uuid.New()

	got, callErr := client.Call(T.Context(), c, getOrder, getOrderRequest{OrderID: id})
	must.NoError(T, callErr)
	test.EqOp(T, id, got.ID)
}

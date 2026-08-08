package client_test

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/primandproper/platform-go/v10/encoding"
	platformerrors "github.com/primandproper/platform-go/v10/errors"
	httpx "github.com/primandproper/platform-go/v10/errors/http"
	"github.com/primandproper/platform-go/v10/routing"
	"github.com/primandproper/platform-go/v10/routing/client"

	"github.com/google/uuid"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNew(T *testing.T) {
	T.Parallel()

	T.Run("keeps the base URL it was given", func(t *testing.T) {
		t.Parallel()

		c, err := client.New("https://api.example.com/v1")
		must.NoError(t, err)
		test.EqOp(t, "https://api.example.com/v1", c.BaseURL())
	})

	T.Run("drops a trailing slash so joining cannot double it", func(t *testing.T) {
		t.Parallel()

		c, err := client.New("https://api.example.com/v1/")
		must.NoError(t, err)
		test.EqOp(t, "https://api.example.com/v1", c.BaseURL())
	})

	T.Run("rejects an empty base URL", func(t *testing.T) {
		t.Parallel()

		_, err := client.New("   ")
		test.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)
	})

	T.Run("rejects a relative base URL", func(t *testing.T) {
		t.Parallel()

		_, err := client.New("/orders")
		test.Error(t, err)
	})

	T.Run("rejects an unparseable base URL", func(t *testing.T) {
		t.Parallel()

		_, err := client.New("://nope")
		test.Error(t, err)
	})

	T.Run("a non-positive response cap restores the default", func(t *testing.T) {
		t.Parallel()

		_, err := client.New("https://api.example.com", client.WithMaxResponseBytes(0))
		test.NoError(t, err)
	})

	T.Run("ignores a nil option", func(t *testing.T) {
		t.Parallel()

		_, err := client.New("https://api.example.com", nil)
		test.NoError(t, err)
	})
}

type echoInput struct {
	Name string `json:"name"`
}

// Auth is embedded by pointer, so a copy of an input carrying it shares it with
// whatever the caller still holds.
type Auth struct {
	Token string `header:"Authorization"`
}

var echoEndpoint = routing.Endpoint[echoInput, echoInput]{
	Method:  http.MethodPost,
	Pattern: "/echo",
}

// fixedResponse stands up a server that answers every request the same way, for
// the cases where what matters is the response rather than the route.
func fixedResponse(t *testing.T, status int, body string, opts ...client.Option) *client.Client {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
		res.Header().Set("Content-Type", "application/json")
		res.WriteHeader(status)
		_, _ = res.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	c, err := client.New(srv.URL, append([]client.Option{client.WithHTTPClient(srv.Client())}, opts...)...)
	must.NoError(t, err)

	return c
}

func TestCall_ErrorWithoutThePlatformEnvelope(T *testing.T) {
	T.Parallel()

	// What a service with its own error format (routing.WithErrorEncoder) sends.
	c := fixedResponse(T, http.StatusBadGateway, `{"error_message": "upstream is down"}`)

	_, err := client.Call(T.Context(), c, echoEndpoint, echoInput{Name: "x"})
	must.Error(T, err)

	var apiErr *client.Error
	must.True(T, errors.As(err, &apiErr))
	test.EqOp(T, http.StatusBadGateway, apiErr.Status)
	test.EqOp(T, httpx.ErrorCode(""), apiErr.Code)
	test.Nil(T, apiErr.Unwrap())
	// The service's own explanation survives, even unrecognized.
	test.StrContains(T, apiErr.Message, "upstream is down")
}

func TestCall_ErrorWithNoBodyAtAll(T *testing.T) {
	T.Parallel()

	c := fixedResponse(T, http.StatusServiceUnavailable, "")

	_, err := client.Call(T.Context(), c, echoEndpoint, echoInput{Name: "x"})
	must.Error(T, err)

	var apiErr *client.Error
	must.True(T, errors.As(err, &apiErr))
	test.EqOp(T, "Service Unavailable", apiErr.Message)
	test.StrContains(T, apiErr.Error(), "POST /echo")
}

func TestCall_UndecodableResponse(T *testing.T) {
	T.Parallel()

	c := fixedResponse(T, http.StatusOK, `{"data": {"name": `)

	_, err := client.Call(T.Context(), c, echoEndpoint, echoInput{Name: "x"})
	test.Error(T, err)
}

func TestCall_OversizedResponse(T *testing.T) {
	T.Parallel()

	body := `{"data":{"name":"` + strings.Repeat("a", 4096) + `"}}`
	c := fixedResponse(T, http.StatusOK, body, client.WithMaxResponseBytes(64))

	_, err := client.Call(T.Context(), c, echoEndpoint, echoInput{Name: "x"})
	must.Error(T, err)
	test.StrContains(T, err.Error(), "exceeded")
}

func TestCall_TransportFailure(T *testing.T) {
	T.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // nothing is listening any more.

	c, err := client.New(srv.URL, client.WithHTTPClient(srv.Client()))
	must.NoError(T, err)

	_, err = client.Call(T.Context(), c, echoEndpoint, echoInput{Name: "x"})
	test.Error(T, err)
}

func TestCall_RejectsAnUnusablePattern(T *testing.T) {
	T.Parallel()

	c, err := client.New("https://api.example.com")
	must.NoError(T, err)

	T.Run("an unknown path parameter token is an error, not a panic", func(t *testing.T) {
		t.Parallel()

		ep := routing.Endpoint[echoInput, echoInput]{Method: http.MethodGet, Pattern: "/x/{id:frobnicate}"}

		_, callErr := client.Call(t.Context(), c, ep, echoInput{})
		test.Error(t, callErr)
	})

	T.Run("a path parameter with no matching field is an error, not a panic", func(t *testing.T) {
		t.Parallel()

		ep := routing.Endpoint[echoInput, echoInput]{Method: http.MethodGet, Pattern: "/x/{id:uint64}"}

		_, callErr := client.Call(t.Context(), c, ep, echoInput{})
		test.Error(t, callErr)
	})
}

// The Error is a routing.CodedError, so a handler that calls one service on
// behalf of another can return what it got and have the code carry through.
func TestError_IsACodedError(T *testing.T) {
	T.Parallel()

	c := fixedResponse(T, http.StatusNotFound,
		`{"error":{"message":"no such order","code":"E104"},"details":{}}`)

	_, err := client.Call(T.Context(), c, echoEndpoint, echoInput{Name: "x"})
	must.Error(T, err)

	var coded routing.CodedError
	must.True(T, errors.As(err, &coded))
	test.EqOp(T, httpx.ErrDataNotFound, coded.ErrorCode())
}

// A codec other than JSON reaches the wire in both directions.
func TestCall_HonorsTheCodec(T *testing.T) {
	T.Parallel()

	var contentType, accept string

	srv := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		contentType = req.Header.Get("Content-Type")
		accept = req.Header.Get("Accept")
		res.WriteHeader(http.StatusNoContent)
	}))
	T.Cleanup(srv.Close)

	c, err := client.New(srv.URL,
		client.WithHTTPClient(srv.Client()),
		client.WithCodec(encoding.NewClientEncoder(encoding.ContentTypeXML)),
	)
	must.NoError(T, err)

	_, err = client.Call(T.Context(), c, echoEndpoint, echoInput{Name: "x"})
	must.NoError(T, err)

	test.EqOp(T, encoding.ContentTypeXML.String(), contentType)
	test.EqOp(T, encoding.ContentTypeXML.String(), accept)
}

// A GET carries no body even when its input has body fields, matching what the
// server would and would not decode.
func TestCall_NoBodyOnABodylessMethod(T *testing.T) {
	T.Parallel()

	var (
		contentType string
		bodyLength  int64
	)

	srv := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		contentType = req.Header.Get("Content-Type")
		bodyLength = req.ContentLength
		res.WriteHeader(http.StatusNoContent)
	}))
	T.Cleanup(srv.Close)

	c, err := client.New(srv.URL, client.WithHTTPClient(srv.Client()))
	must.NoError(T, err)

	ep := routing.Endpoint[echoInput, routing.Empty]{Method: http.MethodGet, Pattern: "/echo"}

	_, err = client.Call(T.Context(), c, ep, echoInput{Name: "x"})
	must.NoError(T, err)

	test.EqOp(T, "", contentType)
	test.EqOp(T, int64(0), bodyLength)
}

// Zeroing parameter fields out of the body must not reach back into the value
// the caller still holds — including through a struct embedded by pointer.
func TestCall_LeavesTheCallersInputAlone(T *testing.T) {
	T.Parallel()

	type createInput struct {
		*Auth

		Item string    `json:"item"`
		ID   uuid.UUID `path:"id"`
	}

	srv := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
		res.WriteHeader(http.StatusNoContent)
	}))
	T.Cleanup(srv.Close)

	c, err := client.New(srv.URL, client.WithHTTPClient(srv.Client()))
	must.NoError(T, err)

	ep := routing.Endpoint[createInput, routing.Empty]{Method: http.MethodPost, Pattern: "/things/{id:uuid}"}

	shared := &Auth{Token: "Bearer abc"}
	in := createInput{Auth: shared, Item: "widget", ID: uuid.New()}

	_, err = client.Call(T.Context(), c, ep, in)
	must.NoError(T, err)

	test.EqOp(T, "Bearer abc", in.Token)
	test.EqOp(T, "Bearer abc", shared.Token)
}
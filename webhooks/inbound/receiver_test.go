package inbound

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/messagequeue"
	mqmock "github.com/primandproper/platform-go/v13/messagequeue/mock"
	"github.com/primandproper/platform-go/v13/routing"
	"github.com/primandproper/platform-go/v13/routing/backends/chi"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

var errPublishFailed = platformerrors.New("the broker is having a day")

// stubVerifier is a Verifier that answers however a test wants it to.
type stubVerifier struct {
	err      error
	provider string
	seen     [][]byte
}

func (v *stubVerifier) Provider() string { return v.provider }

func (v *stubVerifier) Verify(_ context.Context, _ http.Header, body []byte) error {
	v.seen = append(v.seen, body)

	return v.err
}

// capturingPublisher records what it was handed, or fails with err.
func capturingPublisher(err error, published *[]*Delivery) messagequeue.Publisher {
	return &mqmock.PublisherMock{
		PublishFunc: func(_ context.Context, data any, _ ...messagequeue.PublishOption) error {
			if err != nil {
				return err
			}

			delivery, ok := data.(*Delivery)
			if !ok {
				return platformerrors.New("published something that was not a Delivery")
			}

			*published = append(*published, delivery)

			return nil
		},
		StopFunc: func() {},
	}
}

// post builds a POST carrying body, with the given headers set.
func post(t *testing.T, body []byte, headers map[string]string) *http.Request {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "/webhooks/acme", bytes.NewReader(body))
	must.NoError(t, err)

	for name, value := range headers {
		req.Header.Set(name, value)
	}

	return req
}

func TestNewReceiver(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		receiver, err := NewReceiver(&stubVerifier{provider: "acme"}, &mqmock.PublisherMock{}, nil)

		must.NoError(t, err)
		test.NotNil(t, receiver)
	})

	// Neither dependency has a safe default, so neither may be omitted: a missing verifier is a
	// public endpoint onto an internal topic, and a missing publisher acks and drops.
	T.Run("refuses to build without a verifier", func(t *testing.T) {
		t.Parallel()

		receiver, err := NewReceiver(nil, &mqmock.PublisherMock{})

		test.ErrorIs(t, err, ErrNilVerifier)
		test.Nil(t, receiver)
	})

	T.Run("refuses to build without a publisher", func(t *testing.T) {
		t.Parallel()

		receiver, err := NewReceiver(&stubVerifier{provider: "acme"}, nil)

		test.ErrorIs(t, err, ErrNilPublisher)
		test.Nil(t, receiver)
	})
}

func TestReceiver_ServeHTTP(T *testing.T) {
	T.Parallel()

	body := []byte(`{"id":"evt_123"}`)

	T.Run("verifies, publishes, and acks", func(t *testing.T) {
		t.Parallel()

		var published []*Delivery

		verifier := &stubVerifier{provider: "acme"}
		receiver, err := NewReceiver(verifier, capturingPublisher(nil, &published))
		must.NoError(t, err)

		res := httptest.NewRecorder()
		receiver.ServeHTTP(res, post(t, body, map[string]string{"X-Acme-Delivery": "d-1"}))

		test.EqOp(t, http.StatusNoContent, res.Code)
		test.SliceLen(t, 0, res.Body.Bytes())

		must.SliceLen(t, 1, published)
		test.EqOp(t, "acme", published[0].Provider)
		test.Eq(t, body, published[0].Body)
		test.EqOp(t, "d-1", published[0].Headers.Get("X-Acme-Delivery"))
		test.False(t, published[0].ReceivedAt.IsZero())

		// The bytes verified are the bytes published. There is no second read that could see
		// anything different.
		must.SliceLen(t, 1, verifier.seen)
		test.Eq(t, published[0].Body, verifier.seen[0])
	})

	T.Run("refuses a delivery that does not verify", func(t *testing.T) {
		t.Parallel()

		var published []*Delivery

		receiver, err := NewReceiver(
			&stubVerifier{provider: "acme", err: ErrInvalidSignature},
			capturingPublisher(nil, &published),
		)
		must.NoError(t, err)

		res := httptest.NewRecorder()
		receiver.ServeHTTP(res, post(t, body, nil))

		test.EqOp(t, http.StatusBadRequest, res.Code)
		test.SliceLen(t, 0, published)
	})

	// The case the whole design turns on: nothing was acked, so the delivery is still the
	// provider's and its retry is what covers the outage.
	T.Run("does not ack a delivery it could not publish", func(t *testing.T) {
		t.Parallel()

		var published []*Delivery

		receiver, err := NewReceiver(&stubVerifier{provider: "acme"}, capturingPublisher(errPublishFailed, &published))
		must.NoError(t, err)

		res := httptest.NewRecorder()
		receiver.ServeHTTP(res, post(t, body, nil))

		test.EqOp(t, http.StatusServiceUnavailable, res.Code)
		test.SliceLen(t, 0, published)
	})

	T.Run("refuses a body over the cap", func(t *testing.T) {
		t.Parallel()

		var published []*Delivery

		verifier := &stubVerifier{provider: "acme"}
		receiver, err := NewReceiver(verifier, capturingPublisher(nil, &published), WithMaxBodyBytes(16))
		must.NoError(t, err)

		res := httptest.NewRecorder()
		receiver.ServeHTTP(res, post(t, bytes.Repeat([]byte("a"), 17), nil))

		test.EqOp(t, http.StatusRequestEntityTooLarge, res.Code)
		// Nothing was hashed and nothing was published: the cap is applied before the body is
		// anybody's problem.
		test.SliceLen(t, 0, verifier.seen)
		test.SliceLen(t, 0, published)
	})

	T.Run("accepts a body exactly at the cap", func(t *testing.T) {
		t.Parallel()

		var published []*Delivery

		receiver, err := NewReceiver(&stubVerifier{provider: "acme"}, capturingPublisher(nil, &published), WithMaxBodyBytes(16))
		must.NoError(t, err)

		res := httptest.NewRecorder()
		receiver.ServeHTTP(res, post(t, bytes.Repeat([]byte("a"), 16), nil))

		test.EqOp(t, http.StatusNoContent, res.Code)
		test.SliceLen(t, 1, published)
	})

	T.Run("refuses a body it cannot read", func(t *testing.T) {
		t.Parallel()

		var published []*Delivery

		receiver, err := NewReceiver(&stubVerifier{provider: "acme"}, capturingPublisher(nil, &published))
		must.NoError(t, err)

		req := post(t, body, nil)
		req.Body = &errReader{}

		res := httptest.NewRecorder()
		receiver.ServeHTTP(res, req)

		test.EqOp(t, http.StatusBadRequest, res.Code)
		test.SliceLen(t, 0, published)
	})

	T.Run("handles a request with no body at all", func(t *testing.T) {
		t.Parallel()

		var published []*Delivery

		receiver, err := NewReceiver(&stubVerifier{provider: "acme"}, capturingPublisher(nil, &published))
		must.NoError(t, err)

		req := post(t, nil, nil)
		req.Body = nil

		res := httptest.NewRecorder()
		receiver.ServeHTTP(res, req)

		test.EqOp(t, http.StatusNoContent, res.Code)
		must.SliceLen(t, 1, published)
		test.SliceLen(t, 0, published[0].Body)
	})

	// Nothing a provider sends belongs in these, so anything arriving in one came from whoever
	// made the request — and a queue is chosen for durability, not for secrecy.
	T.Run("drops credential headers", func(t *testing.T) {
		t.Parallel()

		var published []*Delivery

		receiver, err := NewReceiver(&stubVerifier{provider: "acme"}, capturingPublisher(nil, &published))
		must.NoError(t, err)

		res := httptest.NewRecorder()
		receiver.ServeHTTP(res, post(t, body, map[string]string{
			"Authorization":       "Bearer hunter2",
			"Proxy-Authorization": "Basic hunter2",
			"Cookie":              "session=hunter2",
			"X-Acme-Delivery":     "d-1",
		}))

		must.SliceLen(t, 1, published)
		test.EqOp(t, "", published[0].Headers.Get("Authorization"))
		test.EqOp(t, "", published[0].Headers.Get("Proxy-Authorization"))
		test.EqOp(t, "", published[0].Headers.Get("Cookie"))
		test.EqOp(t, "d-1", published[0].Headers.Get("X-Acme-Delivery"))
	})

	T.Run("narrows headers to the configured allowlist", func(t *testing.T) {
		t.Parallel()

		var published []*Delivery

		receiver, err := NewReceiver(
			&stubVerifier{provider: "acme"},
			capturingPublisher(nil, &published),
			WithForwardedHeaders("x-acme-delivery", ""),
		)
		must.NoError(t, err)

		res := httptest.NewRecorder()
		receiver.ServeHTTP(res, post(t, body, map[string]string{
			"X-Acme-Delivery": "d-1",
			"X-Acme-Topic":    "orders",
		}))

		must.SliceLen(t, 1, published)
		test.MapLen(t, 1, published[0].Headers)
		test.EqOp(t, "d-1", published[0].Headers.Get("X-Acme-Delivery"))
	})

	T.Run("publishes no headers when none survive the allowlist", func(t *testing.T) {
		t.Parallel()

		var published []*Delivery

		receiver, err := NewReceiver(
			&stubVerifier{provider: "acme"},
			capturingPublisher(nil, &published),
			WithForwardedHeaders("X-Acme-Delivery"),
		)
		must.NoError(t, err)

		res := httptest.NewRecorder()
		receiver.ServeHTTP(res, post(t, body, map[string]string{"X-Acme-Topic": "orders"}))

		must.SliceLen(t, 1, published)
		test.Nil(t, published[0].Headers)
	})
}

func TestReceiver_Mount(T *testing.T) {
	T.Parallel()

	T.Run("serves the mounted path and nothing else", func(t *testing.T) {
		t.Parallel()

		var published []*Delivery

		receiver, err := NewReceiver(&stubVerifier{provider: "acme"}, capturingPublisher(nil, &published))
		must.NoError(t, err)

		router := routing.New(
			chi.NewBackend(&chi.Config{ServiceName: t.Name()}),
			encoding.NewServerEncoderDecoder(encoding.ContentTypeJSON),
		)

		receiver.Mount(router, "/webhooks/acme")

		server := httptest.NewServer(router.Handler())
		t.Cleanup(server.Close)

		res := do(t, server, http.MethodPost, []byte(`{}`))
		test.EqOp(t, http.StatusNoContent, res.StatusCode)
		test.SliceLen(t, 1, published)

		// POST only. Accepting other methods would widen a public endpoint for no caller.
		test.EqOp(t, http.StatusMethodNotAllowed, do(t, server, http.MethodGet, nil).StatusCode)
	})
}

// do sends one request to the mounted endpoint and returns the response, body already closed.
func do(t *testing.T, server *httptest.Server, method string, body []byte) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), method, server.URL+"/webhooks/acme", bytes.NewReader(body))
	must.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")

	res, err := server.Client().Do(req)
	must.NoError(t, err)
	t.Cleanup(func() { _ = res.Body.Close() })

	return res
}

// errReader is a body that fails on read.
type errReader struct{}

func (*errReader) Read([]byte) (int, error) { return 0, platformerrors.New("read error") }
func (*errReader) Close() error             { return nil }

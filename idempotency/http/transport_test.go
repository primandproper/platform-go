package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/idempotency"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// recordingRoundTripper captures what the transport handed downstream.
type recordingRoundTripper struct {
	err   error
	seen  []*http.Request
	calls atomic.Int64
}

func (rt *recordingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	rt.calls.Add(1)
	rt.seen = append(rt.seen, req)

	if rt.err != nil {
		return nil, rt.err
	}

	return &http.Response{
		StatusCode: http.StatusCreated,
		Body:       http.NoBody,
		Header:     http.Header{},
		Request:    req,
	}, nil
}

func (rt *recordingRoundTripper) last() *http.Request {
	return rt.seen[len(rt.seen)-1]
}

func newRequest(tb testing.TB, ctx context.Context, method string) *http.Request {
	tb.Helper()

	req, err := http.NewRequestWithContext(ctx, method, "https://example.test/charges", strings.NewReader("{}"))
	must.NoError(tb, err)

	return req
}

func TestTransport(T *testing.T) {
	T.Parallel()

	T.Run("stamps the key carried by the context", func(t *testing.T) {
		t.Parallel()

		base := &recordingRoundTripper{}
		ctx, key := idempotency.WithNewKey(t.Context())

		res, err := NewTransport(base).RoundTrip(newRequest(t, ctx, http.MethodPost))
		must.NoError(t, err)
		t.Cleanup(func() { _ = res.Body.Close() })

		test.EqOp(t, string(key), base.last().Header.Get(HeaderName))
	})

	// The single most important behavior here. A RoundTripper cannot tell a
	// retry from a deliberate duplicate, so inventing a key would look like
	// protection and provide none.
	T.Run("stamps nothing when the context carries no key", func(t *testing.T) {
		t.Parallel()

		base := &recordingRoundTripper{}

		res, err := NewTransport(base).RoundTrip(newRequest(t, t.Context(), http.MethodPost))
		must.NoError(t, err)
		t.Cleanup(func() { _ = res.Body.Close() })

		test.EqOp(t, "", base.last().Header.Get(HeaderName))
	})

	T.Run("never overwrites a key the caller set", func(t *testing.T) {
		t.Parallel()

		base := &recordingRoundTripper{}
		ctx, _ := idempotency.WithNewKey(t.Context())

		req := newRequest(t, ctx, http.MethodPost)
		req.Header.Set(HeaderName, "caller-owned")

		res, err := NewTransport(base).RoundTrip(req)
		must.NoError(t, err)
		t.Cleanup(func() { _ = res.Body.Close() })

		test.EqOp(t, "caller-owned", base.last().Header.Get(HeaderName))
	})

	T.Run("leaves safe methods alone", func(t *testing.T) {
		t.Parallel()

		base := &recordingRoundTripper{}
		ctx, _ := idempotency.WithNewKey(t.Context())

		res, err := NewTransport(base).RoundTrip(newRequest(t, ctx, http.MethodGet))
		must.NoError(t, err)
		t.Cleanup(func() { _ = res.Body.Close() })

		test.EqOp(t, "", base.last().Header.Get(HeaderName))
	})

	// The contract the whole design rests on: mint once, outside the loop, and
	// every attempt carries the same key.
	T.Run("every attempt under one context sends the same key", func(t *testing.T) {
		t.Parallel()

		base := &recordingRoundTripper{}
		transport := NewTransport(base)
		ctx, key := idempotency.WithNewKey(t.Context())

		for range 3 {
			res, err := transport.RoundTrip(newRequest(t, ctx, http.MethodPost))
			must.NoError(t, err)
			must.NoError(t, res.Body.Close())
		}

		must.SliceLen(t, 3, base.seen)
		for _, req := range base.seen {
			test.EqOp(t, string(key), req.Header.Get(HeaderName))
		}
	})

	T.Run("separate contexts get separate keys", func(t *testing.T) {
		t.Parallel()

		first, firstKey := idempotency.WithNewKey(t.Context())
		second, secondKey := idempotency.WithNewKey(t.Context())

		test.NotEqOp(t, firstKey, secondKey)

		base := &recordingRoundTripper{}
		transport := NewTransport(base)

		for _, ctx := range []context.Context{first, second} {
			res, err := transport.RoundTrip(newRequest(t, ctx, http.MethodPost))
			must.NoError(t, err)
			must.NoError(t, res.Body.Close())
		}

		test.NotEqOp(t, base.seen[0].Header.Get(HeaderName), base.seen[1].Header.Get(HeaderName))
	})

	// RoundTrip must not modify the request it was handed.
	T.Run("does not mutate the caller's request", func(t *testing.T) {
		t.Parallel()

		base := &recordingRoundTripper{}
		ctx, _ := idempotency.WithNewKey(t.Context())
		req := newRequest(t, ctx, http.MethodPost)

		res, err := NewTransport(base).RoundTrip(req)
		must.NoError(t, err)
		t.Cleanup(func() { _ = res.Body.Close() })

		test.EqOp(t, "", req.Header.Get(HeaderName))
	})

	T.Run("calls the base exactly once and passes its result through", func(t *testing.T) {
		t.Parallel()

		base := &recordingRoundTripper{}
		ctx, _ := idempotency.WithNewKey(t.Context())

		res, err := NewTransport(base).RoundTrip(newRequest(t, ctx, http.MethodPost))
		must.NoError(t, err)
		t.Cleanup(func() { _ = res.Body.Close() })

		test.EqOp(t, int64(1), base.calls.Load())
		test.EqOp(t, http.StatusCreated, res.StatusCode)
	})

	T.Run("passes a base error through untouched", func(t *testing.T) {
		t.Parallel()

		boom := platformerrors.New("dial failed")
		ctx, _ := idempotency.WithNewKey(t.Context())

		_, err := NewTransport(&recordingRoundTripper{err: boom}).RoundTrip(newRequest(t, ctx, http.MethodPost))
		test.ErrorIs(t, err, boom)
	})

	T.Run("options override the defaults", func(t *testing.T) {
		t.Parallel()

		base := &recordingRoundTripper{}
		ctx, key := idempotency.WithNewKey(t.Context())

		transport := NewTransport(base, WithTransportHeaderName("X-Idem"), WithTransportMethods(http.MethodGet))

		res, err := transport.RoundTrip(newRequest(t, ctx, http.MethodGet))
		must.NoError(t, err)
		t.Cleanup(func() { _ = res.Body.Close() })

		test.EqOp(t, string(key), base.last().Header.Get("X-Idem"))
	})

	T.Run("a nil base falls back to the default transport", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, NewTransport(nil))
	})

	T.Run("ignores nil options", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, NewTransport(&recordingRoundTripper{}, nil))
	})

	T.Run("a zero value leaves the default in place", func(t *testing.T) {
		t.Parallel()

		base := &recordingRoundTripper{}
		ctx, key := idempotency.WithNewKey(t.Context())

		// An empty name and an empty method list are what an unset config field
		// forwards, and both guards have to refuse them: a transport that
		// stamped an empty header name, or that stamped nothing at all, would
		// leave the server side of this package with no key to deduplicate on
		// while looking configured from here.
		transport := NewTransport(base, WithTransportHeaderName(""), WithTransportMethods())

		res, err := transport.RoundTrip(newRequest(t, ctx, http.MethodPost))
		must.NoError(t, err)
		t.Cleanup(func() { _ = res.Body.Close() })

		test.EqOp(t, string(key), base.last().Header.Get(HeaderName))
	})
}

// TestEndToEnd wires the client transport to the server middleware over a real
// connection. It is the test that catches the two halves disagreeing about the
// header name, which no unit test on either side can.
func TestEndToEnd(T *testing.T) {
	T.Parallel()

	T.Run("a retry replays instead of re-running the handler", func(t *testing.T) {
		t.Parallel()

		handler := okHandler()
		srv := httptest.NewServer(wrap(t, handler, newTestManager(t)))
		t.Cleanup(srv.Close)

		client := srv.Client()
		client.Transport = NewTransport(client.Transport)

		// Minted once, outside the retry loop.
		ctx, _ := idempotency.WithNewKey(t.Context())

		var last *http.Response
		for range 3 {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/charges", strings.NewReader(`{"amount":10}`))
			must.NoError(t, err)

			res, err := client.Do(req)
			must.NoError(t, err)

			if last != nil {
				must.NoError(t, last.Body.Close())
			}
			last = res
		}
		t.Cleanup(func() { _ = last.Body.Close() })

		// Three round trips, one execution.
		test.EqOp(t, int64(1), handler.Calls())
		test.EqOp(t, http.StatusCreated, last.StatusCode)
		test.EqOp(t, "true", last.Header.Get(ReplayHeader))
	})

	// Without a key in the context nothing is stamped, so the server has
	// nothing to deduplicate on and every attempt runs.
	T.Run("without a minted key every attempt runs", func(t *testing.T) {
		t.Parallel()

		handler := okHandler()
		srv := httptest.NewServer(wrap(t, handler, newTestManager(t)))
		t.Cleanup(srv.Close)

		client := srv.Client()
		client.Transport = NewTransport(client.Transport)

		for range 3 {
			req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/charges", strings.NewReader("{}"))
			must.NoError(t, err)

			res, err := client.Do(req)
			must.NoError(t, err)
			must.NoError(t, res.Body.Close())
		}

		test.EqOp(t, int64(3), handler.Calls())
	})
}

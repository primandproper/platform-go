package http

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/idempotency"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

func TestNewMiddleware(T *testing.T) {
	T.Parallel()

	T.Run("rejects a nil manager", func(t *testing.T) {
		t.Parallel()

		_, err := NewMiddleware(nil)
		test.ErrorIs(t, err, ErrNilManager)
	})
}

func TestMiddleware_PassThrough(T *testing.T) {
	T.Parallel()

	// Opting in must be the only way to be affected by this middleware.

	T.Run("a request without a key is untouched", func(t *testing.T) {
		t.Parallel()

		var seen string
		handler := newCountingHandler(func(res http.ResponseWriter, req *http.Request) {
			body, err := io.ReadAll(req.Body)
			must.NoError(t, err)
			seen = string(body)
			res.WriteHeader(http.StatusCreated)
		})

		wrapped := wrap(t, handler, newTestManager(t))

		for range 2 {
			res := do(wrapped, post(t.Context(), "", "/charges", `{"amount":10}`))
			test.EqOp(t, http.StatusCreated, res.Code)
		}

		// Ran both times, and the body still reached it.
		test.EqOp(t, int64(2), handler.Calls())
		test.EqOp(t, `{"amount":10}`, seen)
	})

	T.Run("a safe method is untouched even with a key", func(t *testing.T) {
		t.Parallel()

		handler := okHandler()
		wrapped := wrap(t, handler, newTestManager(t))

		for range 2 {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/charges", nil)
			req.Header.Set(HeaderName, testKey)

			test.EqOp(t, http.StatusCreated, do(wrapped, req).Code)
		}

		test.EqOp(t, int64(2), handler.Calls())
	})

	T.Run("WithMethods narrows participation", func(t *testing.T) {
		t.Parallel()

		handler := okHandler()
		wrapped := wrap(t, handler, newTestManager(t), WithMethods(http.MethodPut))

		for range 2 {
			do(wrapped, post(t.Context(), testKey, "/charges", "{}"))
		}

		test.EqOp(t, int64(2), handler.Calls())
	})
}

func TestMiddleware_Replay(T *testing.T) {
	T.Parallel()

	T.Run("replays the recorded response without running the handler", func(t *testing.T) {
		t.Parallel()

		handler := okHandler()
		wrapped := wrap(t, handler, newTestManager(t))

		first := do(wrapped, post(t.Context(), testKey, "/charges", `{"amount":10}`))
		second := do(wrapped, post(t.Context(), testKey, "/charges", `{"amount":10}`))

		test.EqOp(t, int64(1), handler.Calls())

		test.EqOp(t, http.StatusCreated, first.Code)
		test.EqOp(t, "", first.Header().Get(ReplayHeader))

		test.EqOp(t, http.StatusCreated, second.Code)
		test.EqOp(t, `{"id":"ch_1"}`, second.Body.String())
		test.EqOp(t, "application/json", second.Header().Get("Content-Type"))
		test.EqOp(t, "true", second.Header().Get(ReplayHeader))
	})

	T.Run("replays a 4xx", func(t *testing.T) {
		t.Parallel()

		handler := newCountingHandler(func(res http.ResponseWriter, _ *http.Request) {
			res.WriteHeader(http.StatusBadRequest)
			_, _ = res.Write([]byte(`{"error":"nope"}`))
		})
		wrapped := wrap(t, handler, newTestManager(t))

		do(wrapped, post(t.Context(), testKey, "/charges", "{}"))
		second := do(wrapped, post(t.Context(), testKey, "/charges", "{}"))

		// A rejection is stable, so replaying it is both correct and cheaper.
		test.EqOp(t, int64(1), handler.Calls())
		test.EqOp(t, http.StatusBadRequest, second.Code)
		test.EqOp(t, `{"error":"nope"}`, second.Body.String())
	})

	// A 5xx usually means the work never landed. Pinning it for the TTL would
	// leave the client unable to ever succeed with that key.
	T.Run("does not record a 5xx", func(t *testing.T) {
		t.Parallel()

		failing := true
		var mu sync.Mutex
		handler := newCountingHandler(func(res http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			shouldFail := failing
			mu.Unlock()

			if shouldFail {
				res.WriteHeader(http.StatusInternalServerError)

				return
			}

			res.WriteHeader(http.StatusCreated)
		})
		wrapped := wrap(t, handler, newTestManager(t))

		first := do(wrapped, post(t.Context(), testKey, "/charges", "{}"))
		test.EqOp(t, http.StatusInternalServerError, first.Code)

		mu.Lock()
		failing = false
		mu.Unlock()

		second := do(wrapped, post(t.Context(), testKey, "/charges", "{}"))
		test.EqOp(t, http.StatusCreated, second.Code)
		test.EqOp(t, int64(2), handler.Calls())
	})

	T.Run("a handler that writes nothing replays as 200", func(t *testing.T) {
		t.Parallel()

		handler := newCountingHandler(func(http.ResponseWriter, *http.Request) {})
		wrapped := wrap(t, handler, newTestManager(t))

		do(wrapped, post(t.Context(), testKey, "/charges", "{}"))
		second := do(wrapped, post(t.Context(), testKey, "/charges", "{}"))

		test.EqOp(t, int64(1), handler.Calls())
		test.EqOp(t, http.StatusOK, second.Code)
	})

	T.Run("replays only allowlisted headers", func(t *testing.T) {
		t.Parallel()

		handler := newCountingHandler(func(res http.ResponseWriter, _ *http.Request) {
			res.Header().Set("Content-Type", "application/json")
			res.Header().Set("Set-Cookie", "session=abc")
			res.Header().Set("X-Custom", "v")
			res.WriteHeader(http.StatusCreated)
		})
		wrapped := wrap(t, handler, newTestManager(t))

		do(wrapped, post(t.Context(), testKey, "/charges", "{}"))
		second := do(wrapped, post(t.Context(), testKey, "/charges", "{}"))

		test.EqOp(t, "application/json", second.Header().Get("Content-Type"))
		// A stale session cookie replayed hours later is worse than no cookie.
		test.EqOp(t, "", second.Header().Get("Set-Cookie"))
		test.EqOp(t, "", second.Header().Get("X-Custom"))
	})

	T.Run("WithReplayedHeaders widens the allowlist", func(t *testing.T) {
		t.Parallel()

		handler := newCountingHandler(func(res http.ResponseWriter, _ *http.Request) {
			res.Header().Set("X-Custom", "v")
			res.WriteHeader(http.StatusCreated)
		})
		wrapped := wrap(t, handler, newTestManager(t), WithReplayedHeaders("X-Custom"))

		do(wrapped, post(t.Context(), testKey, "/charges", "{}"))
		second := do(wrapped, post(t.Context(), testKey, "/charges", "{}"))

		test.EqOp(t, "v", second.Header().Get("X-Custom"))
	})

	T.Run("omits an over-sized body but still replays the status", func(t *testing.T) {
		t.Parallel()

		big := strings.Repeat("a", 128)
		handler := newCountingHandler(func(res http.ResponseWriter, _ *http.Request) {
			res.WriteHeader(http.StatusCreated)
			_, _ = res.Write([]byte(big))
		})
		wrapped := wrap(t, handler, newTestManager(t), WithMaxResponseBytes(16))

		first := do(wrapped, post(t.Context(), testKey, "/charges", "{}"))
		second := do(wrapped, post(t.Context(), testKey, "/charges", "{}"))

		// The first caller got everything.
		test.EqOp(t, big, first.Body.String())

		// The retry gets the status, so the charge does not repeat, and an
		// honest marker instead of a body.
		test.EqOp(t, int64(1), handler.Calls())
		test.EqOp(t, http.StatusCreated, second.Code)
		test.EqOp(t, "true", second.Header().Get(BodyOmittedHeader))
		test.EqOp(t, "", second.Body.String())
	})
}

func TestMiddleware_Conflict(T *testing.T) {
	T.Parallel()

	T.Run("answers 409 while the handler is still running", func(t *testing.T) {
		t.Parallel()

		var (
			started = make(chan struct{})
			release = make(chan struct{})
			once    sync.Once
		)

		handler := newCountingHandler(func(res http.ResponseWriter, _ *http.Request) {
			once.Do(func() { close(started) })
			<-release
			res.WriteHeader(http.StatusCreated)
		})
		wrapped := wrap(t, handler, newTestManager(t))

		go func() {
			do(wrapped, post(t.Context(), testKey, "/charges", "{}"))
		}()

		<-started

		second := do(wrapped, post(t.Context(), testKey, "/charges", "{}"))
		close(release)

		test.EqOp(t, http.StatusConflict, second.Code)
		test.EqOp(t, "1", second.Header().Get("Retry-After"))
		test.StrContains(t, second.Body.String(), string(httpErrCodeInFlight))
	})
}

func TestMiddleware_Mismatch(T *testing.T) {
	T.Parallel()

	// Everything here is the same key used for a different request. Replaying
	// the first response would hide a client bug.

	T.Run("a different body is 422", func(t *testing.T) {
		t.Parallel()

		handler := okHandler()
		wrapped := wrap(t, handler, newTestManager(t))

		do(wrapped, post(t.Context(), testKey, "/charges", `{"amount":10}`))
		second := do(wrapped, post(t.Context(), testKey, "/charges", `{"amount":1000}`))

		test.EqOp(t, int64(1), handler.Calls())
		test.EqOp(t, http.StatusUnprocessableEntity, second.Code)
	})

	T.Run("a different path is 422", func(t *testing.T) {
		t.Parallel()

		handler := okHandler()
		wrapped := wrap(t, handler, newTestManager(t))

		do(wrapped, post(t.Context(), testKey, "/charges", "{}"))
		second := do(wrapped, post(t.Context(), testKey, "/refunds", "{}"))

		test.EqOp(t, http.StatusUnprocessableEntity, second.Code)
	})

	T.Run("a different principal is 422", func(t *testing.T) {
		t.Parallel()

		handler := okHandler()
		wrapped := wrap(t, handler, newTestManager(t), WithPrincipalExtractor(func(req *http.Request) (string, error) {
			return req.Header.Get("X-User"), nil
		}))

		first := post(t.Context(), testKey, "/charges", "{}")
		first.Header.Set("X-User", "alice")
		do(wrapped, first)

		// Without the principal in the fingerprint, bob would be handed
		// alice's response.
		second := post(t.Context(), testKey, "/charges", "{}")
		second.Header.Set("X-User", "bob")

		test.EqOp(t, http.StatusUnprocessableEntity, do(wrapped, second).Code)
	})

	T.Run("reordered query parameters are not a mismatch", func(t *testing.T) {
		t.Parallel()

		handler := okHandler()
		wrapped := wrap(t, handler, newTestManager(t))

		do(wrapped, post(t.Context(), testKey, "/charges?a=1&b=2", "{}"))
		second := do(wrapped, post(t.Context(), testKey, "/charges?b=2&a=1", "{}"))

		// Same request, written differently. Reporting key reuse here would be
		// a false alarm on an ordinary retry.
		test.EqOp(t, int64(1), handler.Calls())
		test.EqOp(t, http.StatusCreated, second.Code)
		test.EqOp(t, "true", second.Header().Get(ReplayHeader))
	})

	T.Run("a different query value is a mismatch", func(t *testing.T) {
		t.Parallel()

		handler := okHandler()
		wrapped := wrap(t, handler, newTestManager(t))

		do(wrapped, post(t.Context(), testKey, "/charges?a=1", "{}"))
		second := do(wrapped, post(t.Context(), testKey, "/charges?a=2", "{}"))

		test.EqOp(t, http.StatusUnprocessableEntity, second.Code)
	})
}

func TestMiddleware_Rejections(T *testing.T) {
	T.Parallel()

	T.Run("an over-long key is 400", func(t *testing.T) {
		t.Parallel()

		handler := okHandler()
		wrapped := wrap(t, handler, newTestManager(t))

		res := do(wrapped, post(t.Context(), strings.Repeat("k", 300), "/charges", "{}"))

		test.EqOp(t, http.StatusBadRequest, res.Code)
		test.EqOp(t, int64(0), handler.Calls())
	})

	T.Run("a key with disallowed characters is 400", func(t *testing.T) {
		t.Parallel()

		handler := okHandler()
		wrapped := wrap(t, handler, newTestManager(t))

		res := do(wrapped, post(t.Context(), "has space", "/charges", "{}"))

		test.EqOp(t, http.StatusBadRequest, res.Code)
		test.EqOp(t, int64(0), handler.Calls())
	})

	// Fingerprinting a prefix would let two different requests share a
	// fingerprint, so an oversized body is refused outright.
	T.Run("an over-sized request body is 413", func(t *testing.T) {
		t.Parallel()

		handler := okHandler()
		wrapped := wrap(t, handler, newTestManager(t), WithMaxRequestBodyBytes(8))

		res := do(wrapped, post(t.Context(), testKey, "/charges", strings.Repeat("a", 64)))

		test.EqOp(t, http.StatusRequestEntityTooLarge, res.Code)
		test.EqOp(t, int64(0), handler.Calls())
	})

	T.Run("a principal extractor failure does not run the handler", func(t *testing.T) {
		t.Parallel()

		handler := okHandler()
		wrapped := wrap(t, handler, newTestManager(t), WithPrincipalExtractor(func(*http.Request) (string, error) {
			return "", platformerrors.New("no principal")
		}))

		res := do(wrapped, post(t.Context(), testKey, "/charges", "{}"))

		test.EqOp(t, http.StatusInternalServerError, res.Code)
		test.EqOp(t, int64(0), handler.Calls())
	})

	T.Run("a fingerprint failure does not run the handler", func(t *testing.T) {
		t.Parallel()

		handler := okHandler()
		wrapped := wrap(t, handler, newTestManager(t), WithFingerprint(func(*http.Request, []byte) (idempotency.Fingerprint, error) {
			return "", platformerrors.New("cannot fingerprint")
		}))

		res := do(wrapped, post(t.Context(), testKey, "/charges", "{}"))

		test.EqOp(t, http.StatusInternalServerError, res.Code)
		test.EqOp(t, int64(0), handler.Calls())
	})
}

// TestMiddleware_BodyRestoration is the capitalism regression: the Stripe
// webhook handler reads req.Body itself and verifies a signature over those
// bytes, so a body consumed for fingerprinting and not put back is a broken
// handler downstream.
func TestMiddleware_BodyRestoration(T *testing.T) {
	T.Parallel()

	T.Run("the handler still reads the full body", func(t *testing.T) {
		t.Parallel()

		const body = `{"amount":10,"currency":"usd"}`

		var seen string
		handler := newCountingHandler(func(res http.ResponseWriter, req *http.Request) {
			read, err := io.ReadAll(req.Body)
			must.NoError(t, err)
			seen = string(read)
			res.WriteHeader(http.StatusCreated)
		})
		wrapped := wrap(t, handler, newTestManager(t))

		do(wrapped, post(t.Context(), testKey, "/webhooks/stripe", body))

		test.EqOp(t, body, seen)
	})

	T.Run("an empty body is handled", func(t *testing.T) {
		t.Parallel()

		handler := newCountingHandler(func(res http.ResponseWriter, req *http.Request) {
			read, err := io.ReadAll(req.Body)
			must.NoError(t, err)
			test.SliceEmpty(t, read)
			res.WriteHeader(http.StatusCreated)
		})
		wrapped := wrap(t, handler, newTestManager(t))

		test.EqOp(t, http.StatusCreated, do(wrapped, post(t.Context(), testKey, "/charges", "")).Code)
	})
}

func TestMiddleware_Panic(T *testing.T) {
	T.Parallel()

	// Recovery middleware is installed outside this one, so a panic must keep
	// unwinding — but the claim must not survive it.
	T.Run("propagates the panic and releases the claim", func(t *testing.T) {
		t.Parallel()

		handler := newCountingHandler(func(http.ResponseWriter, *http.Request) {
			panic("boom")
		})
		manager := newTestManager(t)
		wrapped := wrap(t, handler, manager)

		func() {
			defer func() { test.NotNil(t, recover()) }()

			do(wrapped, post(t.Context(), testKey, "/charges", "{}"))
		}()

		// The claim is gone, so a retry genuinely runs the handler again
		// rather than being told the work is in flight forever.
		func() {
			defer func() { test.NotNil(t, recover()) }()

			do(wrapped, post(t.Context(), testKey, "/charges", "{}"))
		}()

		test.EqOp(t, int64(2), handler.Calls())
	})
}

func TestMiddleware_StoreFailure(T *testing.T) {
	T.Parallel()

	// Fail-closed is the default because the guarded work costs money: a store
	// outage should become downtime, not duplicate charges.
	T.Run("fails closed without running the handler", func(t *testing.T) {
		t.Parallel()

		handler := okHandler()
		wrapped := wrap(t, handler, newFailingStoreManager(t))

		res := do(wrapped, post(t.Context(), testKey, "/charges", "{}"))

		test.EqOp(t, http.StatusInternalServerError, res.Code)
		test.EqOp(t, int64(0), handler.Calls())
	})

	T.Run("fails open by running the handler when configured to", func(t *testing.T) {
		t.Parallel()

		handler := okHandler()
		wrapped := wrap(t, handler, newFailingStoreManager(t,
			idempotency.WithStoreFailurePolicy(idempotency.FailOpen),
		))

		res := do(wrapped, post(t.Context(), testKey, "/charges", "{}"))

		test.EqOp(t, http.StatusCreated, res.Code)
		test.EqOp(t, int64(1), handler.Calls())
	})
}

// httpErrCodeInFlight is the platform error code a 409 carries. Spelled out
// here so the assertion fails loudly if the code ever changes.
const httpErrCodeInFlight = "E113"

func TestNewMiddleware_Options(T *testing.T) {
	T.Parallel()

	T.Run("WithHeaderName changes the header read", func(t *testing.T) {
		t.Parallel()

		handler := okHandler()
		wrapped := wrap(t, handler, newTestManager(t), WithHeaderName("X-Idem"))

		// The default header no longer participates.
		plain := post(t.Context(), testKey, "/charges", "{}")
		do(wrapped, plain)
		do(wrapped, plain)
		test.EqOp(t, int64(2), handler.Calls())

		keyed := post(t.Context(), "", "/charges", "{}")
		keyed.Header.Set("X-Idem", testKey)
		do(wrapped, keyed)

		second := post(t.Context(), "", "/charges", "{}")
		second.Header.Set("X-Idem", testKey)
		test.EqOp(t, "true", do(wrapped, second).Header().Get(ReplayHeader))
	})

	T.Run("an empty replay header name suppresses the marker", func(t *testing.T) {
		t.Parallel()

		wrapped := wrap(t, okHandler(), newTestManager(t), WithReplayHeaderName(""))

		do(wrapped, post(t.Context(), testKey, "/charges", "{}"))
		second := do(wrapped, post(t.Context(), testKey, "/charges", "{}"))

		test.EqOp(t, http.StatusCreated, second.Code)
		test.EqOp(t, "", second.Header().Get(ReplayHeader))
	})

	T.Run("WithRetryAfter changes the 409 hint", func(t *testing.T) {
		t.Parallel()

		var (
			started = make(chan struct{})
			release = make(chan struct{})
			once    sync.Once
		)

		handler := newCountingHandler(func(res http.ResponseWriter, _ *http.Request) {
			once.Do(func() { close(started) })
			<-release
			res.WriteHeader(http.StatusCreated)
		})
		wrapped := wrap(t, handler, newTestManager(t), WithRetryAfter(30*time.Second))

		go func() { do(wrapped, post(t.Context(), testKey, "/charges", "{}")) }()
		<-started

		second := do(wrapped, post(t.Context(), testKey, "/charges", "{}"))
		close(release)

		test.EqOp(t, "30", second.Header().Get("Retry-After"))
	})

	T.Run("accepts observability options", func(t *testing.T) {
		t.Parallel()

		wrapped := wrap(t, okHandler(), newTestManager(t),
			WithLogger(loggingnoop.NewLogger()),
			WithTracerProvider(tracingnoop.NewTracerProvider()),
			WithMetricsProvider(metrics.EnsureMetricsProvider(nil)),
		)

		test.EqOp(t, http.StatusCreated, do(wrapped, post(t.Context(), testKey, "/charges", "{}")).Code)
	})

	// The counter is built in the constructor, so a provider that cannot build
	// it has to surface there rather than at the first request.
	T.Run("surfaces a failure to build the counter", func(t *testing.T) {
		t.Parallel()

		boom := platformerrors.New("no meter")
		provider := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(string, ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				return nil, boom
			},
		}

		_, err := NewMiddleware(newTestManager(t), WithMetricsProvider(provider))
		test.ErrorIs(t, err, boom)
	})

	T.Run("ignores nil options", func(t *testing.T) {
		t.Parallel()

		wrapped := wrap(t, okHandler(), newTestManager(t), nil)

		test.EqOp(t, http.StatusCreated, do(wrapped, post(t.Context(), testKey, "/charges", "{}")).Code)
	})

	T.Run("a zero value leaves the default in place", func(t *testing.T) {
		t.Parallel()

		// Every option here follows the module's "absent means the default"
		// convention, and each one draws that line at a guard. Zero is the
		// value that says where the line was drawn — a negative limit is
		// refused by `> 0` and `>= 0` alike, and an empty method list by
		// `len(methods) > 0` and `>= 0` alike — so it is the only argument that
		// distinguishes the convention from its absence. It is also the value a
		// caller reaches by accident, forwarding an unset field of their own
		// config struct.
		cfg := newConfig(
			WithHeaderName(""),
			WithMethods(),
			WithMaxRequestBodyBytes(0),
			WithMaxResponseBytes(0),
			WithRetryAfter(0),
		)

		test.EqOp(t, HeaderName, cfg.headerName)
		test.Eq(t, defaultMethods, cfg.methods)
		test.EqOp(t, int64(DefaultMaxRequestBodyBytes), cfg.maxRequestBody)
		test.EqOp(t, DefaultMaxResponseBytes, cfg.maxResponseBytes)
		test.EqOp(t, DefaultRetryAfter, cfg.retryAfter)
	})
}

func TestMiddleware_TraceDetails(T *testing.T) {
	T.Parallel()

	// The error envelope carries a trace ID so a rejection can be correlated
	// with the request that caused it, matching what the router does.
	T.Run("an error envelope carries the active trace ID", func(t *testing.T) {
		t.Parallel()

		tracerProvider := tracingnoop.NewTracerProvider()
		wrapped := wrap(t, okHandler(), newTestManager(t), WithTracerProvider(tracerProvider))

		res := do(wrapped, post(t.Context(), "has space", "/charges", "{}"))

		test.EqOp(t, http.StatusBadRequest, res.Code)
		test.StrContains(t, res.Body.String(), "details")
	})
}

func TestMiddleware_NilBody(T *testing.T) {
	T.Parallel()

	// httptest builds a request with no body at all when given nil, which is
	// also what a bodiless DELETE looks like on the wire.
	T.Run("handles a request with no body", func(t *testing.T) {
		t.Parallel()

		handler := okHandler()
		wrapped := wrap(t, handler, newTestManager(t))

		build := func() *http.Request {
			req := httptest.NewRequestWithContext(t.Context(), http.MethodDelete, "/charges/1", nil)
			req.Body = nil
			req.Header.Set(HeaderName, testKey)

			return req
		}

		first := do(wrapped, build())
		second := do(wrapped, build())

		test.EqOp(t, http.StatusCreated, first.Code)
		test.EqOp(t, int64(1), handler.Calls())
		test.EqOp(t, "true", second.Header().Get(ReplayHeader))
	})
}

// erroringBody fails on Read, standing in for a connection that dropped
// mid-upload — a body failure that is not an over-size rejection.
type erroringBody struct {
	err error
}

func (b *erroringBody) Read([]byte) (int, error) { return 0, b.err }
func (b *erroringBody) Close() error             { return nil }

func TestMiddleware_BodyReadFailure(T *testing.T) {
	T.Parallel()

	T.Run("a body that cannot be read is a 500, not a 413", func(t *testing.T) {
		t.Parallel()

		handler := okHandler()
		wrapped := wrap(t, handler, newTestManager(t))

		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/charges", nil)
		req.Body = &erroringBody{err: platformerrors.New("connection reset")}
		req.Header.Set(HeaderName, testKey)

		res := do(wrapped, req)

		// 413 is reserved for a body that was too large; this one was simply
		// unreadable, and the handler must not run on a partial fingerprint.
		test.EqOp(t, http.StatusInternalServerError, res.Code)
		test.EqOp(t, int64(0), handler.Calls())
	})
}

func TestMiddleware_ReplayWriteFailure(T *testing.T) {
	T.Parallel()

	// The replay's status is already on the wire when the body write fails, so
	// the failure is logged rather than turned into a second response.
	T.Run("a failed replay write does not panic", func(t *testing.T) {
		t.Parallel()

		handler := okHandler()
		wrapped := wrap(t, handler, newTestManager(t))

		do(wrapped, post(t.Context(), testKey, "/charges", "{}"))

		res := &failingWriter{plainWriter: newPlainWriter(), err: platformerrors.New("connection reset")}
		wrapped.ServeHTTP(res, post(t.Context(), testKey, "/charges", "{}"))

		test.EqOp(t, int64(1), handler.Calls())
		test.EqOp(t, http.StatusCreated, res.status)
	})
}

func TestDetailsFromCtx(T *testing.T) {
	T.Parallel()

	// The envelope carries a trace ID so a rejection can be correlated with the
	// request that produced it, matching what the router puts on its own.
	T.Run("carries the active trace ID", func(t *testing.T) {
		t.Parallel()

		traceID, err := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
		must.NoError(t, err)

		spanID, err := trace.SpanIDFromHex("0102030405060708")
		must.NoError(t, err)

		ctx := trace.ContextWithSpanContext(t.Context(), trace.NewSpanContext(trace.SpanContextConfig{
			TraceID: traceID,
			SpanID:  spanID,
		}))

		test.EqOp(t, traceID.String(), detailsFromCtx(ctx).TraceID)
	})

	T.Run("is empty without a span", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", detailsFromCtx(t.Context()).TraceID)
	})
}

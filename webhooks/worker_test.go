package webhooks

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	cbnoop "github.com/primandproper/platform-go/v13/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v13/cryptography/requestsigning"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/retry"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// allowAnyURL relaxes the SSRF policy so tests can deliver to an httptest
// server on loopback. It is the seam WithWorkerURLChecker exists for; nothing
// in production should pass this.
func allowAnyURL(context.Context, string) error { return nil }

func newTestWorker(t *testing.T, store Store, opts ...WorkerOption) *Worker {
	t.Helper()

	w, err := NewWorker(t.Context(), &WorkerConfig{}, store,
		append([]WorkerOption{WithWorkerURLChecker(allowAnyURL)}, opts...)...)
	must.NoError(t, err)

	return w
}

// testDispatch builds a claimed dispatch aimed at url.
func testDispatch(url string, attempts int) *ClaimedDispatch {
	return &ClaimedDispatch{
		Endpoint: &Endpoint{
			ID:          "endpoint-1",
			URL:         url,
			ContentType: DefaultContentType,
			Secret:      Secret{Current: []byte("secret")},
		},
		Payload:   testBody,
		EventType: "order.created",
		Dispatch: Dispatch{
			ID:          "dispatch-1",
			DeliveryID:  "delivery-1",
			EndpointID:  "endpoint-1",
			OrderingKey: "order-7",
			Attempts:    attempts,
		},
	}
}

func TestNewWorker(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		w, err := NewWorker(t.Context(), &WorkerConfig{}, &fakeStore{})
		must.NoError(t, err)
		test.NotNil(t, w)
	})

	T.Run("nil config", func(t *testing.T) {
		t.Parallel()

		_, err := NewWorker(t.Context(), nil, &fakeStore{})
		test.Error(t, err)
	})

	T.Run("nil store", func(t *testing.T) {
		t.Parallel()

		_, err := NewWorker(t.Context(), &WorkerConfig{}, nil)
		test.ErrorIs(t, err, ErrNilStore)
	})

	// The lease has to outlast the request it covers, or two workers deliver the
	// same payload concurrently.
	T.Run("rejects a lease shorter than the request timeout", func(t *testing.T) {
		t.Parallel()

		_, err := NewWorker(t.Context(), &WorkerConfig{
			RequestTimeout: 30 * time.Second,
			LeaseDuration:  10 * time.Second,
		}, &fakeStore{})

		// Asserted on the rendering rather than with errors.Is: ozzo's
		// validation.Errors is a map with no Unwrap, so the sentinel does not
		// survive into the chain.
		must.Error(t, err)
		test.StrContains(t, err.Error(), ErrLeaseTooShort.Error())
	})

	// Refusing redirects is a security property of this package, not a default
	// a caller opts into — so it is applied to a supplied client too.
	T.Run("forces the redirect policy onto a supplied client", func(t *testing.T) {
		t.Parallel()

		client := &http.Client{}

		_, err := NewWorker(t.Context(), &WorkerConfig{}, &fakeStore{}, WithHTTPClient(client))
		must.NoError(t, err)

		must.NotNil(t, client.CheckRedirect)
		test.ErrorIs(t, client.CheckRedirect(nil, nil), http.ErrUseLastResponse)
	})
}

func TestWorker_deliver(T *testing.T) {
	T.Parallel()

	T.Run("signs the request the subscriber receives", func(t *testing.T) {
		t.Parallel()

		var (
			gotBody   []byte
			gotHeader http.Header
		)

		server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			gotBody, _ = io.ReadAll(req.Body)
			gotHeader = req.Header.Clone()

			res.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		w := newTestWorker(t, &fakeStore{})

		attempt, err := w.deliver(t.Context(), testDispatch(server.URL, 1))
		must.NoError(t, err)
		must.NotNil(t, attempt)

		test.EqOp(t, http.StatusOK, attempt.StatusCode)
		test.EqOp(t, "", attempt.Error)
		test.True(t, attempt.Succeeded())

		// The body reaches the subscriber byte for byte.
		test.Eq(t, testBody, gotBody)

		// And the signature verifies against exactly those bytes. This is the
		// round trip that matters: Sign and Verify agreeing in a unit test
		// proves less than the signature on a real request body doing so.
		timestamp, err := strconv.ParseInt(gotHeader.Get(requestsigning.TimestampHeader), 10, 64)
		must.NoError(t, err)

		must.NoError(t, requestsigning.Verify(
			Secret{Current: []byte("secret")},
			gotBody,
			gotHeader.Get(requestsigning.SignatureHeader),
			requestsigning.WithVerificationTime(time.Unix(timestamp, 0)),
		))

		test.EqOp(t, "order.created", gotHeader.Get(EventTypeHeader))
		test.EqOp(t, "delivery-1", gotHeader.Get(DeliveryIDHeader))
		test.EqOp(t, "1", gotHeader.Get(AttemptHeader))
		test.EqOp(t, DefaultContentType, gotHeader.Get("Content-Type"))
		test.EqOp(t, DefaultUserAgent, gotHeader.Get("User-Agent"))
	})

	T.Run("sends static headers", func(t *testing.T) {
		t.Parallel()

		var gotHeader http.Header

		server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			gotHeader = req.Header.Clone()
			res.WriteHeader(http.StatusNoContent)
		}))
		defer server.Close()

		w := newTestWorker(t, &fakeStore{})

		dispatch := testDispatch(server.URL, 1)
		dispatch.Endpoint.Headers = map[string]string{"X-Tenant": "acme"}

		_, err := w.deliver(t.Context(), dispatch)
		must.NoError(t, err)

		test.EqOp(t, "acme", gotHeader.Get("X-Tenant"))
	})

	// A subscriber cannot overwrite the header that authenticates us to it.
	T.Run("a static header cannot forge the signature", func(t *testing.T) {
		t.Parallel()

		var gotHeader http.Header

		server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			gotHeader = req.Header.Clone()
			res.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		w := newTestWorker(t, &fakeStore{})

		dispatch := testDispatch(server.URL, 1)
		dispatch.Endpoint.Headers = map[string]string{requestsigning.SignatureHeader: "v1,t=0,s=deadbeef"}

		_, err := w.deliver(t.Context(), dispatch)
		must.NoError(t, err)

		test.NotEqOp(t, "v1,t=0,s=deadbeef", gotHeader.Get(requestsigning.SignatureHeader))
	})

	T.Run("a non-2xx status is a failure", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
			res.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		w := newTestWorker(t, &fakeStore{})

		attempt, err := w.deliver(t.Context(), testDispatch(server.URL, 1))

		must.Error(t, err)
		test.ErrorIs(t, err, ErrNonSuccessStatus)
		must.NotNil(t, attempt)
		test.EqOp(t, http.StatusInternalServerError, attempt.StatusCode)
		test.NotEqOp(t, "", attempt.Error)

		// 5xx is transient — it must stay retryable.
		test.False(t, errors.Is(err, retry.ErrUnretryable))
	})

	// The subscriber understood and refused. Retrying twenty-five times changes
	// nothing and spends a budget a transient failure would have needed.
	T.Run("a 4xx is terminal", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
			res.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		w := newTestWorker(t, &fakeStore{})

		_, err := w.deliver(t.Context(), testDispatch(server.URL, 1))

		must.Error(t, err)
		test.ErrorIs(t, err, retry.ErrUnretryable)
	})

	T.Run("429 and 408 stay retryable", func(t *testing.T) {
		t.Parallel()

		for _, status := range []int{http.StatusTooManyRequests, http.StatusRequestTimeout} {
			server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
				res.WriteHeader(status)
			}))

			w := newTestWorker(t, &fakeStore{})

			_, err := w.deliver(t.Context(), testDispatch(server.URL, 1))

			must.Error(t, err)
			test.False(t, errors.Is(err, retry.ErrUnretryable))

			server.Close()
		}
	})

	// Following it would deliver a signed payload to a host that was never
	// registered and never checked.
	T.Run("does not follow redirects", func(t *testing.T) {
		t.Parallel()

		reached := false

		elsewhere := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
			reached = true
			res.WriteHeader(http.StatusOK)
		}))
		defer elsewhere.Close()

		server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			http.Redirect(res, req, elsewhere.URL, http.StatusFound)
		}))
		defer server.Close()

		w := newTestWorker(t, &fakeStore{})

		attempt, err := w.deliver(t.Context(), testDispatch(server.URL, 1))

		must.Error(t, err)
		test.False(t, reached)
		must.NotNil(t, attempt)
		test.EqOp(t, http.StatusFound, attempt.StatusCode)
	})

	T.Run("a transport failure is recorded", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := server.URL
		server.Close() // nothing is listening now

		w := newTestWorker(t, &fakeStore{})

		attempt, err := w.deliver(t.Context(), testDispatch(url, 1))

		must.Error(t, err)
		must.NotNil(t, attempt)
		test.EqOp(t, 0, attempt.StatusCode)
		test.NotEqOp(t, "", attempt.Error)
	})

	// DNS is mutable, so the URL is re-checked at delivery rather than trusted
	// from registration.
	T.Run("re-checks the URL at delivery", func(t *testing.T) {
		t.Parallel()

		w, err := NewWorker(t.Context(), &WorkerConfig{}, &fakeStore{})
		must.NoError(t, err)

		attempt, err := w.deliver(t.Context(), testDispatch("https://169.254.169.254/latest/meta-data/", 1))

		must.Error(t, err)
		// Terminal: a URL that is no longer a legal target will not become one
		// by waiting, and re-resolving it every backoff is a slow internal scan.
		test.ErrorIs(t, err, retry.ErrUnretryable)
		must.NotNil(t, attempt)
		test.NotEqOp(t, "", attempt.Error)
	})

	T.Run("an endpoint with no secret is terminal", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
			res.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		w := newTestWorker(t, &fakeStore{})

		dispatch := testDispatch(server.URL, 1)
		dispatch.Endpoint.Secret = Secret{}

		_, err := w.deliver(t.Context(), dispatch)

		must.Error(t, err)
		test.ErrorIs(t, err, retry.ErrUnretryable)
	})
}

func TestWorker_circuitBreaking(T *testing.T) {
	T.Parallel()

	T.Run("skips delivery when the circuit is open", func(t *testing.T) {
		t.Parallel()

		called := false

		server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
			called = true
			res.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		w := newTestWorker(t, &fakeStore{}, WithCircuitBreakerFactory(
			func(string) (circuitbreaking.CircuitBreaker, error) {
				return &stubBreaker{open: true}, nil
			},
		))

		attempt, err := w.deliver(t.Context(), testDispatch(server.URL, 1))

		test.ErrorIs(t, err, ErrCircuitOpen)
		test.False(t, called)

		// The attempt is still recorded: "we did not try, because the circuit
		// was open" is the most confusing gap to meet without a record of it.
		must.NotNil(t, attempt)
		test.NotEqOp(t, "", attempt.Error)
	})

	// An endpoint down for an hour must not exhaust the budget of everything
	// queued behind it — those deliveries would all need replaying by hand.
	T.Run("a short circuit does not consume an attempt", func(t *testing.T) {
		t.Parallel()

		var (
			gotDead        bool
			gotNextAttempt time.Time
			gotAttempts    int
		)

		store := &fakeStore{
			recordFailure: func(_ context.Context, _ string, attempts int, nextAttempt time.Time, _ string, dead bool) error {
				gotDead = dead
				gotNextAttempt = nextAttempt
				gotAttempts = attempts

				return nil
			},
		}

		w := newTestWorker(t, store, WithCircuitBreakerFactory(
			func(string) (circuitbreaking.CircuitBreaker, error) {
				return &stubBreaker{open: true}, nil
			},
		))

		// Already at the attempt limit: without the exemption this would die.
		dispatch := testDispatch("https://93.184.216.34/hooks", int(w.cfg.Backoff.MaxAttempts))

		w.handle(t.Context(), dispatch)

		test.False(t, gotDead)

		// The rollback is persisted, not merely used for the backoff: the count
		// written back is the one from before this claim incremented it.
		test.EqOp(t, dispatch.Attempts-1, gotAttempts)

		// And it waits on the breaker's timescale, not an exponential backoff
		// that would outlast the outage.
		test.True(t, gotNextAttempt.After(time.Now()))
		test.True(t, gotNextAttempt.Before(time.Now().Add(2*w.cfg.CircuitOpenRetryDelay)))
	})

	T.Run("records success and failure against the breaker", func(t *testing.T) {
		t.Parallel()

		breaker := &stubBreaker{}

		okServer := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
			res.WriteHeader(http.StatusOK)
		}))
		defer okServer.Close()

		badServer := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
			res.WriteHeader(http.StatusInternalServerError)
		}))
		defer badServer.Close()

		w := newTestWorker(t, &fakeStore{}, WithCircuitBreakerFactory(
			func(string) (circuitbreaking.CircuitBreaker, error) { return breaker, nil },
		))

		_, err := w.deliver(t.Context(), testDispatch(okServer.URL, 1))
		must.NoError(t, err)
		test.EqOp(t, 1, breaker.succeeded())
		test.EqOp(t, 0, breaker.failed())

		_, err = w.deliver(t.Context(), testDispatch(badServer.URL, 1))
		must.Error(t, err)
		test.EqOp(t, 1, breaker.succeeded())
		test.EqOp(t, 1, breaker.failed())
	})

	T.Run("caches one breaker per endpoint", func(t *testing.T) {
		t.Parallel()

		built := 0

		w := newTestWorker(t, &fakeStore{}, WithCircuitBreakerFactory(
			func(string) (circuitbreaking.CircuitBreaker, error) {
				built++

				return cbnoop.NewCircuitBreaker(), nil
			},
		))

		for range 3 {
			_, err := w.breakerFor("endpoint-1")
			must.NoError(t, err)
		}

		_, err := w.breakerFor("endpoint-2")
		must.NoError(t, err)

		test.EqOp(t, 2, built)
	})

	T.Run("a factory failure surfaces", func(t *testing.T) {
		t.Parallel()

		expected := platformerrors.New("no breaker for you")

		w := newTestWorker(t, &fakeStore{}, WithCircuitBreakerFactory(
			func(string) (circuitbreaking.CircuitBreaker, error) { return nil, expected },
		))

		_, err := w.breakerFor("endpoint-1")
		test.ErrorIs(t, err, expected)
	})
}

func TestWorker_handle(T *testing.T) {
	T.Parallel()

	T.Run("marks a successful delivery", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
			res.WriteHeader(http.StatusAccepted)
		}))
		defer server.Close()

		var (
			marked   string
			recorded *Attempt
		)

		w := newTestWorker(t, &fakeStore{
			markDelivered: func(_ context.Context, dispatchID string, _ time.Time) error {
				marked = dispatchID

				return nil
			},
			recordAttempt: func(_ context.Context, attempt *Attempt) error {
				recorded = attempt

				return nil
			},
		})

		w.handle(t.Context(), testDispatch(server.URL, 1))

		test.EqOp(t, "dispatch-1", marked)
		must.NotNil(t, recorded)
		test.EqOp(t, http.StatusAccepted, recorded.StatusCode)
		test.EqOp(t, 1, recorded.AttemptCount)
		test.True(t, recorded.Duration > 0)
	})

	T.Run("schedules a retry below the attempt limit", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
			res.WriteHeader(http.StatusBadGateway)
		}))
		defer server.Close()

		var (
			gotDead    bool
			gotLastErr string
		)

		w := newTestWorker(t, &fakeStore{
			recordFailure: func(_ context.Context, _ string, _ int, _ time.Time, lastErr string, dead bool) error {
				gotDead = dead
				gotLastErr = lastErr

				return nil
			},
		})

		w.handle(t.Context(), testDispatch(server.URL, 1))

		test.False(t, gotDead)
		test.NotEqOp(t, "", gotLastErr)
	})

	// Without the terminal state one permanently broken subscriber blocks its
	// ordering key forever.
	T.Run("goes dead at the attempt limit", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
			res.WriteHeader(http.StatusBadGateway)
		}))
		defer server.Close()

		var gotDead bool

		w := newTestWorker(t, &fakeStore{
			recordFailure: func(_ context.Context, _ string, _ int, _ time.Time, _ string, dead bool) error {
				gotDead = dead

				return nil
			},
		})

		w.handle(t.Context(), testDispatch(server.URL, int(w.cfg.Backoff.MaxAttempts)))

		test.True(t, gotDead)
	})

	// An unretryable failure skips the remaining budget entirely.
	T.Run("goes dead immediately on an unretryable failure", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
			res.WriteHeader(http.StatusUnauthorized)
		}))
		defer server.Close()

		var gotDead bool

		w := newTestWorker(t, &fakeStore{
			recordFailure: func(_ context.Context, _ string, _ int, _ time.Time, _ string, dead bool) error {
				gotDead = dead

				return nil
			},
		})

		w.handle(t.Context(), testDispatch(server.URL, 1))

		test.True(t, gotDead)
	})

	// The attempt log is what an operator reads to explain a gap, so it is
	// written even when the delivery failed.
	T.Run("records an attempt for a failed delivery", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
			res.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer server.Close()

		var recorded *Attempt

		w := newTestWorker(t, &fakeStore{
			recordAttempt: func(_ context.Context, attempt *Attempt) error {
				recorded = attempt

				return nil
			},
		})

		w.handle(t.Context(), testDispatch(server.URL, 2))

		must.NotNil(t, recorded)
		test.EqOp(t, http.StatusServiceUnavailable, recorded.StatusCode)
		test.EqOp(t, 2, recorded.AttemptCount)
		test.NotEqOp(t, "", recorded.Error)
		test.False(t, recorded.Succeeded())
	})
}

func TestWorker_cycle(T *testing.T) {
	T.Parallel()

	T.Run("delivers a claimed batch", func(t *testing.T) {
		t.Parallel()

		var (
			mu       sync.Mutex
			received int
		)

		server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
			mu.Lock()
			received++
			mu.Unlock()

			res.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		batch := make([]ClaimedDispatch, 0, 5)
		for i := range 5 {
			dispatch := testDispatch(server.URL, 1)
			dispatch.ID = "dispatch-" + strconv.Itoa(i)
			dispatch.EndpointID = "endpoint-" + strconv.Itoa(i)
			dispatch.Endpoint.ID = dispatch.EndpointID

			batch = append(batch, *dispatch)
		}

		var (
			markedMu sync.Mutex
			marked   []string
		)

		w := newTestWorker(t, &fakeStore{
			claim: func(context.Context, time.Time, int, time.Time) ([]ClaimedDispatch, error) {
				return batch, nil
			},
			markDelivered: func(_ context.Context, dispatchID string, _ time.Time) error {
				markedMu.Lock()
				marked = append(marked, dispatchID)
				markedMu.Unlock()

				return nil
			},
		})

		w.cycle(t.Context())

		test.EqOp(t, 5, received)
		test.SliceLen(t, 5, marked)
	})

	T.Run("an empty claim does nothing", func(t *testing.T) {
		t.Parallel()

		claimed := false

		w := newTestWorker(t, &fakeStore{
			claim: func(context.Context, time.Time, int, time.Time) ([]ClaimedDispatch, error) {
				claimed = true

				return nil, nil
			},
			markDelivered: func(context.Context, string, time.Time) error {
				t.Error("nothing should have been delivered")

				return nil
			},
		})

		w.cycle(t.Context())

		test.True(t, claimed)
	})

	T.Run("a claim failure is survivable", func(t *testing.T) {
		t.Parallel()

		w := newTestWorker(t, &fakeStore{
			claim: func(context.Context, time.Time, int, time.Time) ([]ClaimedDispatch, error) {
				return nil, platformerrors.New("database is on fire")
			},
		})

		// There is no caller to hand the error to; the next cycle retries.
		w.cycle(t.Context())
	})
}

func TestWorker_RunAndClose(T *testing.T) {
	T.Parallel()

	T.Run("Close drains the loop", func(t *testing.T) {
		t.Parallel()

		w := newTestWorker(t, &fakeStore{})

		go w.Run()

		must.NoError(t, w.Close(t.Context()))

		// Close is idempotent: the owner may call it from more than one place.
		test.NoError(t, w.Close(t.Context()))
	})
}

// stubBreaker is a CircuitBreaker that reports a fixed state and counts what it
// was told.
type stubBreaker struct {
	mu           sync.Mutex
	failedCount  int
	successCount int
	open         bool
}

var _ circuitbreaking.CircuitBreaker = (*stubBreaker)(nil)

func (s *stubBreaker) Failed() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.failedCount++
}

func (s *stubBreaker) Succeeded() {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.successCount++
}

func (s *stubBreaker) CanProceed() bool { return !s.open }

func (s *stubBreaker) CannotProceed() bool { return s.open }

func (s *stubBreaker) failed() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.failedCount
}

func (s *stubBreaker) succeeded() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.successCount
}

func TestWorker_sampleBacklog(T *testing.T) {
	T.Parallel()

	// The backlog gauges are the package's primary health signal: every other
	// instrument is a rate or a latency, and none of them separates "delivering
	// steadily" from "delivering steadily while falling further behind".
	T.Run("samples depth and age", func(t *testing.T) {
		t.Parallel()

		called := false

		w := newTestWorker(t, &fakeStore{
			backlog: func(context.Context) (int64, time.Time, error) {
				called = true

				return 42, time.Now().Add(-90 * time.Second), nil
			},
		})

		w.sampleBacklog(t.Context())

		test.True(t, called)
	})

	// A drained queue must actively report zero rather than leave a stale
	// reading on the dashboard, so an absent oldest timestamp is an age of zero
	// rather than a skipped sample.
	T.Run("an empty backlog still samples", func(t *testing.T) {
		t.Parallel()

		w := newTestWorker(t, &fakeStore{
			backlog: func(context.Context) (int64, time.Time, error) {
				return 0, time.Time{}, nil
			},
		})

		w.sampleBacklog(t.Context())
	})

	// A clock that has moved behind the oldest row would otherwise produce a
	// negative age.
	T.Run("a future oldest row does not report a negative age", func(t *testing.T) {
		t.Parallel()

		w := newTestWorker(t, &fakeStore{
			backlog: func(context.Context) (int64, time.Time, error) {
				return 1, time.Now().Add(time.Hour), nil
			},
		})

		w.sampleBacklog(t.Context())
	})

	T.Run("a store failure is survivable", func(t *testing.T) {
		t.Parallel()

		w := newTestWorker(t, &fakeStore{
			backlog: func(context.Context) (int64, time.Time, error) {
				return 0, time.Time{}, platformerrors.New("database is on fire")
			},
		})

		// There is no caller to hand the error to; the next tick retries.
		w.sampleBacklog(t.Context())
	})
}

func TestWorker_reap(T *testing.T) {
	T.Parallel()

	// The retention arithmetic is the part worth pinning: reaping from the wrong
	// side of the window would delete live history or never delete anything.
	T.Run("reaps from the far side of the retention window", func(t *testing.T) {
		t.Parallel()

		var (
			gotBefore time.Time
			gotLimit  int
		)

		w := newTestWorker(t, &fakeStore{
			reap: func(_ context.Context, before time.Time, limit int) (int64, error) {
				gotBefore = before
				gotLimit = limit

				return 7, nil
			},
		})

		w.reap(t.Context())

		test.EqOp(t, w.cfg.ReapBatchSize, gotLimit)

		// The cutoff is one retention window in the past, so anything delivered
		// more recently survives.
		expected := time.Now().Add(-w.cfg.Retention)
		test.True(t, gotBefore.Sub(expected).Abs() < time.Minute)
	})

	T.Run("reaping nothing is not an event", func(t *testing.T) {
		t.Parallel()

		w := newTestWorker(t, &fakeStore{
			reap: func(context.Context, time.Time, int) (int64, error) { return 0, nil },
		})

		w.reap(t.Context())
	})

	T.Run("a store failure is survivable", func(t *testing.T) {
		t.Parallel()

		w := newTestWorker(t, &fakeStore{
			reap: func(context.Context, time.Time, int) (int64, error) {
				return 0, platformerrors.New("database is on fire")
			},
		})

		w.reap(t.Context())
	})
}

// newAcceptingServer is a subscriber that accepts every delivery.
func newAcceptingServer(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
		res.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	return server
}

// newRefusingServer is a subscriber that fails every delivery transiently.
func newRefusingServer(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
		res.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(server.Close)

	return server
}

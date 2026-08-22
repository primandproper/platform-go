package oauth2server_test

import (
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	"github.com/primandproper/platform-go/v13/authentication/oauth2server/memory"
	"github.com/primandproper/platform-go/v13/clock"
	clockmock "github.com/primandproper/platform-go/v13/clock/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// testEpoch is where the steppable clock starts. A fixed instant rather than
// time.Now, so what a case proves does not depend on when it ran.
var testEpoch = time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)

// steppableClock is a clock a test moves by hand.
//
// Only Now is implemented, which is the whole of what a Server reads: nothing
// here sleeps or ticks. A store's sweeper does, and the cases below do not start
// one.
type steppableClock struct {
	now time.Time
	mu  sync.Mutex
}

func newSteppableClock() *steppableClock {
	return &steppableClock{now: testEpoch}
}

func (c *steppableClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// clk renders this as the interface the options take. The httptest server reads
// it from its own goroutines, so the read goes through the mutex too.
func (c *steppableClock) clk() clock.Clock {
	return &clockmock.ClockMock{
		NowFunc: func() time.Time {
			c.mu.Lock()
			defer c.mu.Unlock()

			return c.now
		},
	}
}

// newTimedHarness builds a server and its store over one clock a test can move,
// so an expiry can be reached without waiting for it.
func newTimedHarness(t *testing.T, opts ...oauth2server.Option) (*harness, *steppableClock) {
	t.Helper()

	c := newSteppableClock()

	store := memory.NewStore(memory.WithClock(c.clk()))

	return newStoreHarness(t, store, append([]oauth2server.Option{oauth2server.WithClock(c.clk())}, opts...)...), c
}

// The four lifetimes, proved by moving the clock rather than by reading the
// constants back — which is the only way a test can tell a configured lifetime
// from a defaulted one.
func TestServer_Lifetimes(T *testing.T) {
	T.Parallel()

	T.Run("a configured code lifetime is what expires the code", func(t *testing.T) {
		t.Parallel()

		h, clk := newTimedHarness(t, oauth2server.WithAuthorizationCodeTTL(30*time.Second))

		reg := h.registerConfidential()
		code := h.codeFrom(h.authorize(authorizeParams(reg.ClientID), login()))

		clk.advance(31 * time.Second)

		out := h.redeem(reg, code)
		test.EqOp(t, http.StatusBadRequest, out.status)
		test.EqOp(t, oauth2server.ErrorCodeInvalidGrant, out.Error)
	})

	T.Run("a zero code lifetime is an unset field, not an expired code", func(t *testing.T) {
		t.Parallel()

		h, clk := newTimedHarness(t, oauth2server.WithAuthorizationCodeTTL(0))

		reg := h.registerConfidential()
		code := h.codeFrom(h.authorize(authorizeParams(reg.ClientID), login()))

		// Well past a configured thirty seconds and well inside the default
		// minute: a zero that reached codeTTL would already have expired this.
		clk.advance(45 * time.Second)
		must.EqOp(t, http.StatusOK, h.redeem(reg, code).status)
	})

	T.Run("the default code lifetime is a minute", func(t *testing.T) {
		t.Parallel()

		h, clk := newTimedHarness(t)

		reg := h.registerConfidential()
		code := h.codeFrom(h.authorize(authorizeParams(reg.ClientID), login()))

		clk.advance(oauth2server.DefaultAuthorizationCodeTTL + time.Second)
		test.EqOp(t, http.StatusBadRequest, h.redeem(reg, code).status)
	})

	T.Run("a zero refresh lifetime is an unset field, not an expired token", func(t *testing.T) {
		t.Parallel()

		h, clk := newTimedHarness(t, oauth2server.WithRefreshTokenTTL(0))

		reg := h.registerConfidential()
		tokens := h.exchange(reg)

		// A day in, which is nothing against the seven-day default and past any
		// lifetime a zero could have produced.
		clk.advance(24 * time.Hour)

		out := h.token(reg.ClientID, reg.ClientSecret, url.Values{
			"grant_type":    {oauth2server.GrantTypeRefreshToken},
			"refresh_token": {tokens.RefreshToken},
		})
		test.EqOp(t, http.StatusOK, out.status)
	})

	T.Run("an access token stops working on its own lifetime", func(t *testing.T) {
		t.Parallel()

		h, clk := newTimedHarness(t, oauth2server.WithAccessTokenTTL(2*time.Minute))

		reg := h.registerConfidential()
		tokens := h.exchange(reg)

		_, err := h.server.Authenticate(t.Context(), tokens.AccessToken)
		must.NoError(t, err)

		clk.advance(2*time.Minute + time.Second)

		_, err = h.server.Authenticate(t.Context(), tokens.AccessToken)
		test.ErrorIs(t, err, oauth2server.ErrExpired)
	})

	T.Run("a negative registration lifetime is the same as none", func(t *testing.T) {
		t.Parallel()

		h, clk := newTimedHarness(t, oauth2server.WithClientRegistrationTTL(-time.Hour))

		reg := h.registerConfidential()

		// A negative TTL that reached the record would have stamped an expiry
		// in the past, and every request this client makes would be answered as
		// an unknown client.
		clk.advance(365 * 24 * time.Hour)
		test.EqOp(t, http.StatusOK, h.exchange(reg).status)
	})
}

func TestOptions_Metadata(T *testing.T) {
	T.Parallel()

	T.Run("the service documentation URL reaches the discovery document", func(t *testing.T) {
		t.Parallel()

		const docs = "https://docs.example/oauth"

		h := newHarness(t, oauth2server.WithServiceDocumentation(docs))
		test.EqOp(t, docs, h.server.Metadata().ServiceDocumentation)
	})

	T.Run("an unset one is omitted rather than empty", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		test.EqOp(t, "", h.server.Metadata().ServiceDocumentation)

		res := h.get(oauth2server.PathAuthorizationServerMetadata)
		test.StrNotContains(t, readBody(t, res), "service_documentation")
	})
}

package http

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/cache/memory"
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/cookies"
	"github.com/primandproper/platform-go/v13/sessions"
	sessionscache "github.com/primandproper/platform-go/v13/sessions/cache"

	"github.com/shoenig/test/must"
)

// principal is the payload the tests store.
type principal struct {
	UserID string
	Admin  bool
}

// fakeClock is a Clock whose time only moves when a test moves it.
type fakeClock struct {
	now time.Time
	mu  sync.Mutex
}

var _ clock.Clock = (*fakeClock)(nil)

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, time.August, 8, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

func (c *fakeClock) Since(t time.Time) time.Duration                  { return c.Now().Sub(t) }
func (c *fakeClock) Sleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }
func (c *fakeClock) NewTicker(_ time.Duration) clock.Ticker           { panic("not used") }

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// newCookieManager builds a cookie manager with throwaway keys.
func newCookieManager(t *testing.T) cookies.Manager {
	t.Helper()

	manager, err := cookies.NewCookieManager(&cookies.Config{
		Base64EncodedHashKey:  base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")),
		Base64EncodedBlockKey: base64.StdEncoding.EncodeToString([]byte("fedcba9876543210fedcba9876543210")),
		Lifetime:              24 * time.Hour,
	})
	must.NoError(t, err)

	return manager
}

// newTestManager builds a Manager over an in-memory session store and a clock
// the test controls.
func newTestManager(t *testing.T, storeOpts ...sessions.Option) (*Manager[principal], *fakeClock) {
	t.Helper()

	c := newFakeClock()

	cache, err := memory.NewInMemoryCache[sessions.Record[principal]](time.Hour)
	must.NoError(t, err)

	backend, err := sessionscache.NewBackend(cache)
	must.NoError(t, err)

	store, err := sessions.NewStore(backend, append([]sessions.Option{sessions.WithClock(c)}, storeOpts...)...)
	must.NoError(t, err)

	manager, err := NewManager(store, newCookieManager(t))
	must.NoError(t, err)

	return manager, c
}

// issue establishes a session and returns a request carrying its cookie, the
// way a browser would present it on the next call.
func issue(t *testing.T, manager *Manager[principal], data *principal) (*sessions.Session[principal], *http.Request) {
	t.Helper()

	res := httptest.NewRecorder()

	session, err := manager.Issue(t.Context(), res, data)
	must.NoError(t, err)

	return session, requestWithCookies(t, res)
}

// requestWithCookies builds a request carrying whatever cookies a response set.
func requestWithCookies(t *testing.T, res *httptest.ResponseRecorder) *http.Request {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	for _, cookie := range res.Result().Cookies() {
		req.AddCookie(cookie)
	}

	return req
}

// sessionCookie returns the session cookie a response set, if any.
func sessionCookie(t *testing.T, res *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()

	for _, cookie := range res.Result().Cookies() {
		if cookie.Name == DefaultCookieName {
			return cookie
		}
	}

	return nil
}

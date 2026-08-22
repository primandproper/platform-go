package httpclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/cache/memory"
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/retry"

	"github.com/shoenig/test/must"
)

// newClient builds a client for a test, failing rather than making every call
// site handle an error only a broken metrics provider can produce.
func newClient(t *testing.T, opts ...Option) *http.Client {
	t.Helper()

	client, err := NewHTTPClient(opts...)
	must.NoError(t, err)

	return client
}

// observerForTest builds the observability the transports share, the way
// buildClient does. Every pillar is absent, so it records nowhere — these tests
// are about behavior, and the recording paths only have to not panic.
func observerForTest(t *testing.T) *transportObserver {
	t.Helper()

	obs, err := newTransportObserver(nil, nil, nil)
	must.NoError(t, err)

	return obs
}

// retryTransportForTest builds a retry transport already holding the observer
// buildClient would have given it, for tests that exercise it directly rather
// than through a client.
func retryTransportForTest(t *testing.T, policy retry.Policy, opts ...RetryOption) *retryTransport {
	t.Helper()

	transport := newRetryTransport(policy, opts)
	transport.obs = observerForTest(t)

	return transport
}

// cacheURL is the origin the cache tests read from. It is a constant because
// the key is derived from it, and half of those tests turn on two requests
// landing on the same key or on different ones.
const cacheURL = "https://idp.example.com/.well-known/jwks.json"

// cacheTransportForTest builds a cache transport over a real in-memory cache
// and the given base, already holding the observer buildClient would have given
// it.
//
// The cache is real rather than a mock: what these tests are about is which
// requests reach the origin, and a stub that answered Get however the test
// wanted would be asserting on the test's own bookkeeping instead. Its default
// expiry is long, since retention is the cache's business and freshness — the
// thing under test — is the transport's.
func cacheTransportForTest(t *testing.T, base http.RoundTripper, opts ...CacheOption) *cacheTransport {
	t.Helper()

	transport := newCacheTransport(cacheForTest(t), opts)
	transport.base = base
	transport.obs = observerForTest(t)

	return transport
}

// cacheForTest builds the store the cache tests read and write through.
//
// Its default expiry is long on purpose: retention is the cache's business and
// freshness is the transport's, and a store that dropped entries the moment
// they went stale would take the revalidation path out of reach.
func cacheForTest(t *testing.T) cache.Cache[CachedResponse] {
	t.Helper()

	store, err := memory.NewInMemoryCache[CachedResponse](time.Hour)
	must.NoError(t, err)

	t.Cleanup(func() { must.NoError(t, store.Close()) })

	return store
}

// cacheRequest builds a request for the cache tests, which care about method
// and URL and never about a body.
func cacheRequest(ctx context.Context, method, url string) *http.Request {
	return newRequest(ctx, method, url, nil)
}

// readBody reads a response body to completion and closes it, which is what
// makes "did this come from the cache?" answerable by looking at the bytes.
func readBody(t *testing.T, resp *http.Response) string {
	t.Helper()

	body, err := io.ReadAll(resp.Body)
	must.NoError(t, err)
	must.NoError(t, resp.Body.Close())

	return string(body)
}

// steppingClock is a clock.Clock that only moves when a test says so, which is
// how freshness and expiry are asserted without sleeping through them.
type steppingClock struct {
	now time.Time
}

var _ clock.Clock = (*steppingClock)(nil)

func (c *steppingClock) Now() time.Time                  { return c.now }
func (c *steppingClock) Since(t time.Time) time.Duration { return c.now.Sub(t) }

// advance moves the clock forward.
func (c *steppingClock) advance(d time.Duration) { c.now = c.now.Add(d) }

// Sleep and NewTicker are here to satisfy clock.Clock. The cache transport
// reads time and never waits on it, so a test reaching either of these has
// found a behavior change worth failing on rather than faking.
func (c *steppingClock) Sleep(context.Context, time.Duration) error {
	panic("the cache transport does not sleep")
}

func (c *steppingClock) NewTicker(time.Duration) clock.Ticker {
	panic("the cache transport does not tick")
}

// roundTripperFunc adapts a function to http.RoundTripper so a test can state
// the base transport's behavior inline.
type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

// response builds a response the way a real transport would hand one back:
// with a readable, closable body.
func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// header builds a header from alternating names and values.
//
// It goes through Set rather than a map literal because http.Header's map keys
// are canonicalized ("ETag" is stored as "Etag"), and a literal that spells one
// the way the RFC does builds a header whose Get finds nothing.
func header(pairs ...string) http.Header {
	built := http.Header{}
	for i := 0; i+1 < len(pairs); i += 2 {
		built.Set(pairs[i], pairs[i+1])
	}

	return built
}

// withHeader sets a header on a response and returns it, for inline use.
func withHeader(resp *http.Response, key, value string) *http.Response {
	resp.Header.Set(key, value)

	return resp
}

// trackedBody records whether it was closed, which is how the retry tests check
// that superseded responses give their connections back.
type trackedBody struct {
	io.Reader
	closed bool
}

func (b *trackedBody) Close() error {
	b.closed = true

	return nil
}

// immediatePolicy is a retry.Policy that retries up to attempts times with no
// backoff. It honors retry.IsTerminal exactly as the real exponential-backoff
// policy does, which is what makes assertions about ErrUnretryable meaningful.
type immediatePolicy struct {
	attempts int
}

var _ retry.Policy = (*immediatePolicy)(nil)

func (p *immediatePolicy) Execute(ctx context.Context, operation func(context.Context) error) error {
	var err error

	for range p.attempts {
		if err = operation(ctx); err == nil {
			return nil
		}

		if retry.IsTerminal(ctx, err) {
			return err
		}
	}

	return err
}

// classifyRequest is a stand-in for tests that exercise classify directly.
// Classification turns on the response, but the transport still needs a request
// to describe in the lines it logs about one.
func classifyRequest(ctx context.Context) *http.Request {
	return newRequest(ctx, http.MethodGet, "http://example.com", nil)
}

// newRequest builds a request bound to ctx, failing loudly on a bad URL rather
// than making every caller check.
func newRequest(ctx context.Context, method, url string, body io.Reader) *http.Request {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		panic(err)
	}

	return req
}

// get sends a GET bound to ctx. http.Client.Get would do, except that it leaves
// the request on context.Background, and several of these tests turn on what the
// transport does when the caller's context is done.
func get(ctx context.Context, client *http.Client, url string) (*http.Response, error) {
	return client.Do(newRequest(ctx, http.MethodGet, url, nil))
}

// post sends a POST bound to ctx.
func post(ctx context.Context, client *http.Client, url string, body io.Reader) (*http.Response, error) {
	req := newRequest(ctx, http.MethodPost, url, body)
	req.Header.Set("Content-Type", "text/plain")

	return client.Do(req)
}

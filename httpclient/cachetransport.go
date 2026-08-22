package httpclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/observability/keys"

	"go.opentelemetry.io/otel/attribute"
)

// DefaultMaxCacheableBody is the largest response body stored by default.
// Anything above it is returned to the caller and not written, so one oversized
// document cannot evict everything a cache was configured to hold.
const DefaultMaxCacheableBody int64 = 1 << 20

// cacheKeyPrefix begins every key this transport writes, so a cache it shares
// with nothing still supports httpclient's entries being purged on their own
// via DeleteByPrefix.
const cacheKeyPrefix = "httpcache:"

// The outcomes recorded for every request that reaches this transport. They
// partition it: exactly one is recorded per RoundTrip.
const (
	// cacheOutcomeHit is a request answered from the cache, having made no
	// wire request at all.
	cacheOutcomeHit = "hit"

	// cacheOutcomeRevalidated is a stale entry the origin confirmed with a 304.
	// A wire request happened; a body did not come back.
	cacheOutcomeRevalidated = "revalidated"

	// cacheOutcomeMiss is a request the origin answered in full, whether or not
	// the answer was worth storing.
	cacheOutcomeMiss = "miss"

	// cacheOutcomeUncacheable is a request the cache never took part in: a
	// method it does not cache, a caller running its own conditional request,
	// or credentials it was not told it could key on.
	cacheOutcomeUncacheable = "uncacheable"
)

// cacheTransport answers repeated reads of a stable resource without going to
// the origin, and revalidates rather than re-downloading when it must go.
type cacheTransport struct {
	base  http.RoundTripper
	store cache.Cache[CachedResponse]
	clock clock.Clock
	obs   *transportObserver

	ttl        time.Duration
	maxBody    int64
	authorized bool
}

var _ http.RoundTripper = (*cacheTransport)(nil)

// CacheOption tunes the transport WithHTTPCache installs.
type CacheOption func(*cacheTransport)

// WithCacheTTL states how long a response stays fresh when the origin said
// nothing about it.
//
// It is the reason most callers reach for a cache at all: JWKS documents,
// .well-known metadata, and catalog endpoints are frequently served with no
// Cache-Control and no Expires, and the only party who knows how stale a copy
// may safely be is the caller. It is consulted last — any statement the origin
// made about its own resource wins, so naming a TTL cannot make this client
// hold a response longer than the origin permitted.
//
// The TTL counts from receipt. Without it, a response carrying no freshness
// information is stored only if it carries a validator, and then only so a
// later request can be revalidated cheaply. A non-positive duration is ignored.
func WithCacheTTL(ttl time.Duration) CacheOption {
	return func(t *cacheTransport) {
		if ttl > 0 {
			t.ttl = ttl
		}
	}
}

// WithMaxCacheableBody caps the size of a stored body, defaulting to
// DefaultMaxCacheableBody.
//
// A response above the cap is returned to the caller in full and simply not
// written. The cap exists because a cache has a bound — cache/memory's is a
// byte budget, and a Redis has a machine — and one large response admitted into
// it evicts every small one that was earning its keep. A non-positive value
// leaves the default in place.
func WithMaxCacheableBody(maxBody int64) CacheOption {
	return func(t *cacheTransport) {
		if maxBody > 0 {
			t.maxBody = maxBody
		}
	}
}

// WithCacheClock replaces the clock freshness is judged against, which is what
// makes expiry assertable in a test without sleeping. A nil clock leaves the
// wall clock in place.
func WithCacheClock(c clock.Clock) CacheOption {
	return func(t *cacheTransport) {
		if c != nil {
			t.clock = c
		}
	}
}

// WithCacheAuthorized permits caching responses to requests bearing an
// Authorization header, which is refused by default.
//
// Read the default as the safety property it is: a cache/redis shared across a
// fleet, keyed on method and URL alone, will serve one tenant's response to
// another tenant's identical request. That is not a corner case, it is the
// first thing that happens.
//
// Opting in does not relax the key — it extends it. The credential becomes part
// of the cache key, so two callers holding different tokens get different
// entries rather than each other's. What opting in asserts is narrower than it
// looks, and worth checking before doing it: that the response really is a
// function of the credential and nothing outside the request, and that a
// credential's hash sitting in the cache's keyspace is acceptable where that
// cache lives.
//
// Responses marked private or no-store are still refused, as are responses
// setting cookies, whether or not this is on.
func WithCacheAuthorized(enabled bool) CacheOption {
	return func(t *cacheTransport) { t.authorized = enabled }
}

// newCacheTransport resolves the cache options into an unattached transport.
// buildClient fills in the base and the observer.
func newCacheTransport(store cache.Cache[CachedResponse], opts []CacheOption) *cacheTransport {
	t := &cacheTransport{
		store:   store,
		clock:   clock.NewClock(),
		maxBody: DefaultMaxCacheableBody,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(t)
		}
	}

	return t
}

// RoundTrip answers from the cache when it can, revalidates when it must, and
// stores what comes back when it is worth storing.
func (t *cacheTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()

	if !t.cacheableRequest(req) {
		t.record(req, cacheOutcomeUncacheable)

		return t.base.RoundTrip(req)
	}

	key := t.key(req)
	entry := t.lookup(ctx, req, key)

	// Read once, so the instant that judged the entry fresh is the same one the
	// Age it reports is measured from.
	now := t.clock.Now()

	if entry != nil && !mustRevalidate(req) && entry.fresh(now) {
		t.record(req, cacheOutcomeHit)
		t.obs.o11y.Logger().WithRequest(req).Debug("serving response from cache")

		// Returned without touching the base transport, which is what puts the
		// cache above the breaker and the limiter rather than below them: a hit
		// made no request, so it must not report an outcome to a circuit or
		// spend a token from a budget that counts requests on the wire.
		return entry.toResponse(req, now), nil
	}

	outbound, revalidating := t.conditional(req, entry)

	resp, err := t.base.RoundTrip(outbound)
	if err != nil {
		return nil, err
	}

	if revalidating && resp.StatusCode == http.StatusNotModified {
		return t.revalidated(ctx, req, resp, key, entry), nil
	}

	t.record(req, cacheOutcomeMiss)

	return t.maybeStore(ctx, req, resp, key), nil
}

// key derives the cache key: the method and URL, plus the credential when the
// caller has opted authenticated requests in.
//
// The URL is hashed rather than spelled out. Keys are visible wherever the
// cache lives — a Redis SCAN, a slow-log line, an eviction metric — and a URL
// carries query strings, which carry signed parameters and API keys often
// enough that putting them in a shared keyspace is not a risk worth taking for
// legibility. Hashing also keeps the key bounded, which a URL is not.
func (t *cacheTransport) key(req *http.Request) string {
	// Separated rather than concatenated, so no two different requests can
	// hash the same bytes by having their parts run together.
	material := req.Method + "\x00" + req.URL.String()

	if credential := req.Header.Get("Authorization"); credential != "" {
		material += "\x00" + credential
	}

	sum := sha256.Sum256([]byte(material))

	return cacheKeyPrefix + hex.EncodeToString(sum[:])
}

// lookup reads the entry for this request, or nil when there is none to use.
//
// A cache that cannot answer is a miss, not a failure. The point of this layer
// is that the origin is still there; an unreachable Redis should cost hit rate
// and nothing else.
func (t *cacheTransport) lookup(ctx context.Context, req *http.Request, key string) *CachedResponse {
	entry, err := t.store.Get(ctx, key)
	if err != nil {
		if !errors.Is(err, cache.ErrNotFound) {
			t.obs.o11y.Logger().WithRequest(req).WithError(err).Debug("cache lookup failed, treating as a miss")
		}

		return nil
	}

	if entry == nil {
		return nil
	}

	// Stored against different request headers than this request carries, so it
	// answers a question this one did not ask.
	if !entry.matches(req) {
		return nil
	}

	return entry.clone()
}

// conditional returns the request to send, and whether a 304 answering it would
// be answering this transport's validator.
//
// A stale entry with no validator gets none: the origin has no way to say "the
// one you have is still good", so the request is sent plainly and the full
// response is what comes back either way.
func (t *cacheTransport) conditional(req *http.Request, entry *CachedResponse) (*http.Request, bool) {
	if entry == nil {
		return req, false
	}

	etag, lastModified, ok := entry.validator()
	if !ok {
		return req, false
	}

	// RoundTrip must not modify the request it was given, and Clone gives the
	// header its own map rather than sharing the caller's.
	outbound := req.Clone(req.Context())

	if etag != "" {
		outbound.Header.Set("If-None-Match", etag)
	}

	if lastModified != "" {
		outbound.Header.Set("If-Modified-Since", lastModified)
	}

	return outbound, true
}

// revalidated folds a 304 into the stored entry and serves it.
func (t *cacheTransport) revalidated(
	ctx context.Context,
	req *http.Request,
	resp *http.Response,
	key string,
	entry *CachedResponse,
) *http.Response {
	// The 304 carries no body worth keeping, and its connection is worth
	// giving back.
	drainAndClose(resp)

	received := t.clock.Now()

	// The freshness of a revalidated entry comes from the 304's own directives,
	// which is how an origin extends a lifetime without resending the body.
	entry.refresh(resp.Header, originTime(resp.Header, received))
	entry.FreshUntil = t.freshUntil(entry.Header, parseCacheControl(entry.Header), received, entry.OriginTime)

	if err := t.store.Set(ctx, key, entry); err != nil {
		t.obs.o11y.Logger().WithRequest(req).WithError(err).Debug("could not refresh cache entry after revalidation")
	}

	t.record(req, cacheOutcomeRevalidated)
	t.obs.o11y.Logger().WithRequest(req).Debug("origin confirmed the cached response")

	return entry.toResponse(req, received)
}

// maybeStore writes the response when it is worth writing, and returns the
// response the caller reads either way.
func (t *cacheTransport) maybeStore(
	ctx context.Context,
	req *http.Request,
	resp *http.Response,
	key string,
) *http.Response {
	cc := parseCacheControl(resp.Header)
	if !t.storable(resp, cc) {
		// Deliberately before the body is touched. A response that will not be
		// stored must reach the caller as a stream, not as something this
		// transport read into memory for nothing.
		return resp
	}

	received := t.clock.Now()
	origin := originTime(resp.Header, received)
	freshUntil := t.freshUntil(resp.Header, cc, received, origin)

	// Nothing says how long this is good for and nothing lets us ask, so
	// storing it would only produce an entry that can never be served and can
	// never be checked. WithCacheTTL is how a caller supplies the first half.
	if freshUntil.IsZero() && !hasValidator(resp.Header) {
		return resp
	}

	body, ok := captureBody(resp, t.maxBody)
	if !ok {
		return resp
	}

	entry := newCachedResponse(req, resp, body, origin, freshUntil)

	// No WriteOption: how long an entry is retained is the cache's business,
	// and how long it stays fresh is this transport's. They are different
	// questions — a stale entry is still worth holding, because revalidating it
	// costs a request and no body — and answering them with one number would
	// throw away the cheaper half.
	if err := t.store.Set(ctx, key, entry); err != nil {
		t.obs.o11y.Logger().WithRequest(req).WithError(err).Debug("could not store response in cache")
	}

	return resp
}

// record counts how this request was answered.
func (t *cacheTransport) record(req *http.Request, outcome string) {
	t.obs.cacheOutcomes.Add(req.Context(), 1, requestAttrs(req, attribute.String(keys.CacheOutcomeKey, outcome)))
}

// captureBody reads a response body far enough to decide whether it fits, and
// leaves resp holding a body that replays every byte read.
//
// It reports the complete body and true only when the whole thing fit within
// limit. In every other case the caller still gets an intact response — the
// bytes already read, followed by whatever remained — because a transport that
// truncated a body to protect a cache would be trading correctness for hit
// rate.
func captureBody(resp *http.Response, limit int64) ([]byte, bool) {
	if resp.Body == nil {
		return nil, true
	}

	original := resp.Body

	// One byte past the limit, which is what distinguishes a body that exactly
	// fills the cap from one that overruns it.
	buffered, err := io.ReadAll(io.LimitReader(original, limit+1))

	switch {
	case err != nil:
		// The failure belongs to the caller, at the point in the stream where
		// it happened. Swallowing it here would turn a truncated download into
		// a short body that looks complete.
		resp.Body = &replayBody{
			Reader: io.MultiReader(bytes.NewReader(buffered), errorReader{err: err}),
			closer: original,
		}

		return nil, false
	case int64(len(buffered)) > limit:
		resp.Body = &replayBody{
			Reader: io.MultiReader(bytes.NewReader(buffered), original),
			closer: original,
		}

		return nil, false
	default:
		// Fully read, so the connection can go back to the pool now rather than
		// when the caller gets around to closing.
		_ = original.Close() //nolint:errcheck // the body was read to completion; the close is a courtesy to the pool

		resp.Body = io.NopCloser(bytes.NewReader(buffered))

		return buffered, true
	}
}

// replayBody hands back bytes already read from a response, then whatever is
// left of it, and closes the body those bytes came from.
type replayBody struct {
	io.Reader
	closer io.Closer
}

var _ io.ReadCloser = (*replayBody)(nil)

// Close closes the underlying body.
func (b *replayBody) Close() error { return b.closer.Close() }

// errorReader replays a read failure at the position it occurred.
type errorReader struct {
	err error
}

var _ io.Reader = errorReader{}

// Read returns the failure that ended the original read.
func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

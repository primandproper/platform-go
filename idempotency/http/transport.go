package http

import (
	"net/http"
	"slices"

	"github.com/primandproper/platform-go/v13/idempotency"
)

// transport stamps the idempotency key carried by a request's context onto the
// outbound request.
type transport struct {
	base       http.RoundTripper
	headerName string
	methods    []string
}

var _ http.RoundTripper = (*transport)(nil)

// NewTransport wraps base so that requests whose context carries an
// idempotency key send it.
//
// It composes the same way the platform's tracing transport does, so no change
// to httpclient is needed:
//
//	c, _ := httpclient.NewHTTPClient(cfg.Options()...)
//	c.Transport = idempotencyhttp.NewTransport(c.Transport)
//
//	ctx, _ := idempotency.WithNewKey(ctx)   // once, OUTSIDE the retry loop
//	err := policy.Do(ctx, func(ctx context.Context) error {
//		res, err := c.Do(req.Clone(ctx))     // every attempt sends the same key
//		...
//	})
//
// A nil base falls back to http.DefaultTransport.
//
// # It never invents a key
//
// If the context carries no key and the header is not already set, this does
// nothing at all. That is the most important property here, and it is not
// timidity:
//
// A RoundTripper cannot tell a retry from a second, deliberate request — they
// are byte-identical. Minting a key per call would produce a different key on
// every attempt, which offers no protection while looking like it does.
// Deriving one from the request's content would fail the other way, by
// deciding two intentional identical charges are the same one and silently
// dropping the second.
//
// Only the caller knows where a logical operation begins, which is what
// idempotency.WithNewKey expresses. An already-set header always wins, so a
// caller managing keys itself is never overridden.
func NewTransport(base http.RoundTripper, opts ...TransportOption) http.RoundTripper {
	t := &transport{
		base:       base,
		headerName: HeaderName,
		methods:    defaultMethods,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(t)
		}
	}

	if t.base == nil {
		t.base = http.DefaultTransport
	}

	return t
}

// RoundTrip stamps the key and delegates.
func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !t.shouldStamp(req) {
		return t.base.RoundTrip(req)
	}

	key, ok := idempotency.KeyFromContext(req.Context())
	if !ok {
		return t.base.RoundTrip(req)
	}

	// RoundTrip must not modify the request it is given, so the header goes on
	// a shallow copy. Header needs its own clone because the copy shares the
	// original's map.
	stamped := req.Clone(req.Context())
	stamped.Header.Set(t.headerName, string(key))

	return t.base.RoundTrip(stamped)
}

// shouldStamp reports whether this request participates.
func (t *transport) shouldStamp(req *http.Request) bool {
	if req.Header.Get(t.headerName) != "" {
		// The caller is managing keys itself.
		return false
	}

	return slices.Contains(t.methods, req.Method)
}

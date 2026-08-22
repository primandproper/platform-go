package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// request builds a request from a remote address and header pairs.
func request(t *testing.T, remoteAddr string, headers ...string) *http.Request {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/things", http.NoBody)
	req.RemoteAddr = remoteAddr

	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Add(headers[i], headers[i+1])
	}

	return req
}

func TestKeyByRemoteAddr(T *testing.T) {
	T.Parallel()

	T.Run("keys on the connection's host", func(t *testing.T) {
		t.Parallel()

		key, err := KeyByRemoteAddr()(request(t, "203.0.113.7:54321"))
		must.NoError(t, err)
		test.EqOp(t, ipKeyPrefix+"203.0.113.7", key)
	})

	T.Run("ignores what the client claims", func(t *testing.T) {
		t.Parallel()

		// The whole point: on a direct connection X-Forwarded-For is client
		// input, and a limiter that read it could be defeated by varying it.
		key, err := KeyByRemoteAddr()(request(t, "203.0.113.7:54321", "X-Forwarded-For", "10.0.0.1"))
		must.NoError(t, err)
		test.EqOp(t, ipKeyPrefix+"203.0.113.7", key)
	})

	T.Run("falls back to the raw address when it carries no port", func(t *testing.T) {
		t.Parallel()

		key, err := KeyByRemoteAddr()(request(t, "@"))
		must.NoError(t, err)
		test.EqOp(t, ipKeyPrefix+"@", key)
	})
}

func TestKeyByForwardedFor(T *testing.T) {
	T.Parallel()

	T.Run("reads the client entry behind one proxy", func(t *testing.T) {
		t.Parallel()

		key, err := KeyByForwardedFor(1)(request(t, "10.0.0.1:443", "X-Forwarded-For", "203.0.113.7"))
		must.NoError(t, err)
		test.EqOp(t, ipKeyPrefix+"203.0.113.7", key)
	})

	T.Run("reads the client entry behind two proxies", func(t *testing.T) {
		t.Parallel()

		// A CDN in front of a load balancer: the CDN wrote the client entry and
		// the balancer appended the CDN's own address.
		key, err := KeyByForwardedFor(2)(request(t, "10.0.0.1:443", "X-Forwarded-For", "203.0.113.7, 198.51.100.4"))
		must.NoError(t, err)
		test.EqOp(t, ipKeyPrefix+"203.0.113.7", key)
	})

	T.Run("ignores entries the client forged", func(t *testing.T) {
		t.Parallel()

		// The security property. A client sending its own header only pushes
		// forged entries left of where the trusted proxy appends the truth.
		key, err := KeyByForwardedFor(1)(request(t, "10.0.0.1:443",
			"X-Forwarded-For", "1.1.1.1, 2.2.2.2, 3.3.3.3, 203.0.113.7"))
		must.NoError(t, err)
		test.EqOp(t, ipKeyPrefix+"203.0.113.7", key)
	})

	T.Run("joins repeated header lines", func(t *testing.T) {
		t.Parallel()

		// Proxies emit both spellings, and reading only the first line would
		// count the wrong hop.
		key, err := KeyByForwardedFor(2)(request(t, "10.0.0.1:443",
			"X-Forwarded-For", "203.0.113.7",
			"X-Forwarded-For", "198.51.100.4"))
		must.NoError(t, err)
		test.EqOp(t, ipKeyPrefix+"203.0.113.7", key)
	})

	T.Run("falls back to the connection when the header is short", func(t *testing.T) {
		t.Parallel()

		key, err := KeyByForwardedFor(2)(request(t, "10.0.0.1:443", "X-Forwarded-For", "203.0.113.7"))
		must.NoError(t, err)
		test.EqOp(t, ipKeyPrefix+"10.0.0.1", key)
	})

	T.Run("falls back to the connection when the header is absent", func(t *testing.T) {
		t.Parallel()

		key, err := KeyByForwardedFor(1)(request(t, "10.0.0.1:443"))
		must.NoError(t, err)
		test.EqOp(t, ipKeyPrefix+"10.0.0.1", key)
	})

	T.Run("treats a hop count below one as one", func(t *testing.T) {
		t.Parallel()

		key, err := KeyByForwardedFor(0)(request(t, "10.0.0.1:443", "X-Forwarded-For", "203.0.113.7"))
		must.NoError(t, err)
		test.EqOp(t, ipKeyPrefix+"203.0.113.7", key)
	})
}

func TestKeyByHeader(T *testing.T) {
	T.Parallel()

	T.Run("hashes the value rather than keying on it", func(t *testing.T) {
		t.Parallel()

		// A limiter key becomes a Redis key and reaches spans on the way. A
		// credential that lands in a keyspace has been disclosed.
		key, err := KeyByHeader("X-API-Key")(request(t, "203.0.113.7:1", "X-API-Key", "sk_live_supersecret"))
		must.NoError(t, err)
		test.StrHasPrefix(t, headerKeyPrefix, key)
		test.StrNotContains(t, key, "sk_live_supersecret")
	})

	T.Run("is stable for the same value", func(t *testing.T) {
		t.Parallel()

		fn := KeyByHeader("X-API-Key")

		first, err := fn(request(t, "203.0.113.7:1", "X-API-Key", "abc"))
		must.NoError(t, err)

		second, err := fn(request(t, "198.51.100.4:2", "X-API-Key", "abc"))
		must.NoError(t, err)

		test.EqOp(t, first, second)
	})

	T.Run("distinguishes different values", func(t *testing.T) {
		t.Parallel()

		fn := KeyByHeader("X-API-Key")

		first, err := fn(request(t, "203.0.113.7:1", "X-API-Key", "abc"))
		must.NoError(t, err)

		second, err := fn(request(t, "203.0.113.7:1", "X-API-Key", "def"))
		must.NoError(t, err)

		test.NotEqOp(t, first, second)
	})

	T.Run("exempts a request without the header", func(t *testing.T) {
		t.Parallel()

		// Pooling every anonymous caller under one empty key would count them
		// all against a single bucket.
		key, err := KeyByHeader("X-API-Key")(request(t, "203.0.113.7:1"))
		must.NoError(t, err)
		test.EqOp(t, "", key)
	})

	T.Run("exempts a header that is only whitespace", func(t *testing.T) {
		t.Parallel()

		key, err := KeyByHeader("X-API-Key")(request(t, "203.0.113.7:1", "X-API-Key", "   "))
		must.NoError(t, err)
		test.EqOp(t, "", key)
	})
}

func TestFirstNonEmpty(T *testing.T) {
	T.Parallel()

	T.Run("returns the first extractor that produced a key", func(t *testing.T) {
		t.Parallel()

		fn := FirstNonEmpty(
			KeyByHeader("X-API-Key"),
			KeyByRemoteAddr(),
		)

		key, err := fn(request(t, "203.0.113.7:1", "X-API-Key", "abc"))
		must.NoError(t, err)
		test.StrHasPrefix(t, headerKeyPrefix, key)
	})

	T.Run("falls through when an extractor exempts the request", func(t *testing.T) {
		t.Parallel()

		fn := FirstNonEmpty(
			KeyByHeader("X-API-Key"),
			KeyByRemoteAddr(),
		)

		key, err := fn(request(t, "203.0.113.7:1"))
		must.NoError(t, err)
		test.EqOp(t, ipKeyPrefix+"203.0.113.7", key)
	})

	T.Run("stops on an error rather than widening the key", func(t *testing.T) {
		t.Parallel()

		// An extractor that failed has not established that the caller is
		// unidentified, so falling through would loosen the limit on exactly
		// the requests something already went wrong on.
		boom := platformerrors.New("principal lookup failed")
		fn := FirstNonEmpty(
			func(*http.Request) (string, error) { return "", boom },
			KeyByRemoteAddr(),
		)

		key, err := fn(request(t, "203.0.113.7:1"))
		must.ErrorIs(t, err, boom)
		test.EqOp(t, "", key)
	})

	T.Run("skips nil extractors", func(t *testing.T) {
		t.Parallel()

		key, err := FirstNonEmpty(nil, KeyByRemoteAddr())(request(t, "203.0.113.7:1"))
		must.NoError(t, err)
		test.EqOp(t, ipKeyPrefix+"203.0.113.7", key)
	})

	T.Run("exempts the request when every extractor does", func(t *testing.T) {
		t.Parallel()

		key, err := FirstNonEmpty(KeyByHeader("X-API-Key"))(request(t, "203.0.113.7:1"))
		must.NoError(t, err)
		test.EqOp(t, "", key)
	})
}

func TestKeyNamespacing(T *testing.T) {
	T.Parallel()

	// Composed extractors must not be able to collide: an address-derived key
	// and a header-derived one pooling two callers into one bucket is a limit
	// silently halved.
	T.Run("prefixes keep dimensions apart", func(t *testing.T) {
		t.Parallel()

		addr, err := KeyByRemoteAddr()(request(t, "203.0.113.7:1"))
		must.NoError(t, err)

		header, err := KeyByHeader("X-API-Key")(request(t, "203.0.113.7:1", "X-API-Key", "203.0.113.7"))
		must.NoError(t, err)

		test.NotEqOp(t, addr, header)
		test.False(t, strings.HasPrefix(header, ipKeyPrefix))
	})
}

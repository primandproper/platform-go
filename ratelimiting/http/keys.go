package http

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strings"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// ErrNilKeyFunc indicates NewMiddleware was called without a key function.
var ErrNilKeyFunc = platformerrors.New("nil rate limit key function")

// KeyFunc extracts the key a request is counted against — the answer to "N per
// second per what". It is pluggable because the platform has no notion of a
// caller to read one from: a service's principal lives wherever its own
// authentication middleware put it.
//
// Returning an empty key with a nil error exempts the request. That is the
// intended way to say "this one is limited somewhere else": an authenticated
// request whose principal-keyed limiter runs on an inner route, say, rather
// than being counted twice.
//
// An error is treated as a failure of the guard, not a verdict from it, and
// resolves the same way an unreachable limiter does — see WithFailClosed.
type KeyFunc func(req *http.Request) (string, error)

// Key prefixes. Every extractor here namespaces what it returns, so that
// composing two of them with FirstNonEmpty cannot have an IP-derived key
// collide with a header-derived one and pool two callers into one bucket.
const (
	ipKeyPrefix     = "ip:"
	headerKeyPrefix = "hdr:"
)

// KeyByRemoteAddr keys on the address the connection actually came from,
// ignoring every forwarding header.
//
// This is the safe default, and the only one that is safe with nothing in front
// of the server: X-Forwarded-For is written by the client on a direct
// connection, so a limiter keyed on it can be defeated by sending a different
// value each request — which is precisely the traffic a limiter exists to
// catch. Use KeyByForwardedFor only behind a proxy you control.
func KeyByRemoteAddr() KeyFunc {
	return func(req *http.Request) (string, error) {
		return ipKeyPrefix + remoteIP(req), nil
	}
}

// KeyByForwardedFor keys on the client address recorded in X-Forwarded-For,
// for a server behind trustedProxies proxies that each append to it.
//
// trustedProxies is a count, not a list, and getting it right is the whole
// security of this extractor. Each proxy in the chain appends the address it
// received the request from, so the rightmost trustedProxies entries were
// written by infrastructure you control and the one before them is the client.
// A client that sends its own X-Forwarded-For only pushes its forged entries
// further left, where this never looks.
//
// Count every hop that appends: a CDN in front of a load balancer is two, not
// one. Counting too high reads an entry the client wrote and hands it the
// ability to mint a fresh bucket per request; counting too low pools everyone
// behind one proxy into a single bucket. A header with fewer entries than
// expected falls back to the connection's own address rather than guessing.
//
// trustedProxies below 1 is a misconfiguration — there is no proxy to trust —
// and is treated as 1.
func KeyByForwardedFor(trustedProxies int) KeyFunc {
	hops := max(trustedProxies, 1)

	return func(req *http.Request) (string, error) {
		parts := forwardedFor(req)

		index := len(parts) - hops
		if index < 0 || parts[index] == "" {
			return ipKeyPrefix + remoteIP(req), nil
		}

		return ipKeyPrefix + parts[index], nil
	}
}

// KeyByHeader keys on a request header — an API key, a tenant ID, whatever the
// service issues to identify a caller.
//
// The value is hashed, not used verbatim. A limiter key travels: it becomes a
// Redis key, and it reaches spans and logs on the way. An API key is a
// credential, and a credential that ends up in a keyspace someone else can list
// has been disclosed. The hash keys just as well, since a limiter only needs
// two requests from the same caller to land on the same string.
//
// A request without the header is exempted rather than pooled under one empty
// key, which would count every anonymous caller against a single bucket.
// Compose with FirstNonEmpty to fall through to an address instead.
func KeyByHeader(name string) KeyFunc {
	return func(req *http.Request) (string, error) {
		value := strings.TrimSpace(req.Header.Get(name))
		if value == "" {
			return "", nil
		}

		sum := sha256.Sum256([]byte(value))

		return headerKeyPrefix + hex.EncodeToString(sum[:]), nil
	}
}

// FirstNonEmpty tries each KeyFunc in order and returns the first non-empty key.
//
// It is how a service expresses a fallback ladder — count an authenticated
// caller by principal, an API client by its key, and everyone else by address:
//
//	FirstNonEmpty(keyByPrincipal, KeyByHeader("X-API-Key"), KeyByRemoteAddr())
//
// An error from any extractor stops the ladder and is returned. An extractor
// that fails has not established that the caller is unidentified, so falling
// through to a broader key would quietly downgrade the limit for exactly the
// requests something already went wrong on.
func FirstNonEmpty(fns ...KeyFunc) KeyFunc {
	return func(req *http.Request) (string, error) {
		for _, fn := range fns {
			if fn == nil {
				continue
			}

			key, err := fn(req)
			if err != nil {
				return "", err
			}

			if key != "" {
				return key, nil
			}
		}

		return "", nil
	}
}

// remoteIP returns the host portion of the connection's address, falling back
// to the raw value when it carries no port to split off.
func remoteIP(req *http.Request) string {
	host, _, err := net.SplitHostPort(req.RemoteAddr)
	if err != nil {
		return req.RemoteAddr
	}

	return host
}

// forwardedFor returns the trimmed entries of every X-Forwarded-For header on
// the request, in order. Multiple header lines are equivalent to one
// comma-joined line, and proxies emit both spellings.
func forwardedFor(req *http.Request) []string {
	var parts []string
	for _, header := range req.Header.Values("X-Forwarded-For") {
		for part := range strings.SplitSeq(header, ",") {
			parts = append(parts, strings.TrimSpace(part))
		}
	}

	return parts
}

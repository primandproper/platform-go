package httpclient

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/cache"
	platformerrors "github.com/primandproper/platform-go/v13/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestCacheTransport_Hits(T *testing.T) {
	T.Parallel()

	T.Run("a fresh entry is served without reaching the wire", func(t *testing.T) {
		t.Parallel()

		var calls int

		transport := cacheTransportForTest(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++

			return withHeader(response(http.StatusOK, "jwks"), "Cache-Control", "max-age=300"), nil
		}))

		for range 3 {
			resp, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
			must.NoError(t, err)
			test.EqOp(t, http.StatusOK, resp.StatusCode)
			test.EqOp(t, "jwks", readBody(t, resp))
		}

		test.EqOp(t, 1, calls)
	})

	T.Run("the served response carries the age the entry has accumulated", func(t *testing.T) {
		t.Parallel()

		clk := &steppingClock{now: time.Unix(1_700_000_000, 0)}

		transport := cacheTransportForTest(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			return withHeader(response(http.StatusOK, "body"), "Cache-Control", "max-age=300"), nil
		}), WithCacheClock(clk))

		_, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
		must.NoError(t, err)

		clk.advance(90 * time.Second)

		resp, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
		must.NoError(t, err)
		test.EqOp(t, "90", resp.Header.Get("Age"))
	})

	T.Run("an entry that has gone stale is refetched", func(t *testing.T) {
		t.Parallel()

		var calls int

		clk := &steppingClock{now: time.Unix(1_700_000_000, 0)}

		transport := cacheTransportForTest(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++

			return withHeader(response(http.StatusOK, "body"), "Cache-Control", "max-age=60"), nil
		}), WithCacheClock(clk))

		_, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
		must.NoError(t, err)

		clk.advance(61 * time.Second)

		_, err = transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
		must.NoError(t, err)

		test.EqOp(t, 2, calls)
	})

	T.Run("a caller asking for no-cache gets the origin's word", func(t *testing.T) {
		t.Parallel()

		var calls int

		transport := cacheTransportForTest(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++

			return withHeader(response(http.StatusOK, "body"), "Cache-Control", "max-age=300"), nil
		}))

		_, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
		must.NoError(t, err)

		req := cacheRequest(t.Context(), http.MethodGet, cacheURL)
		req.Header.Set("Cache-Control", "no-cache")

		_, err = transport.RoundTrip(req)
		must.NoError(t, err)

		test.EqOp(t, 2, calls)
	})

	T.Run("distinct URLs do not share an entry", func(t *testing.T) {
		t.Parallel()

		transport := cacheTransportForTest(t, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return withHeader(response(http.StatusOK, req.URL.Path), "Cache-Control", "max-age=300"), nil
		}))

		first, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, "https://idp.example.com/a"))
		must.NoError(t, err)
		test.EqOp(t, "/a", readBody(t, first))

		second, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, "https://idp.example.com/b"))
		must.NoError(t, err)
		test.EqOp(t, "/b", readBody(t, second))
	})

	T.Run("a HEAD does not answer a GET", func(t *testing.T) {
		t.Parallel()

		transport := cacheTransportForTest(t, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			return withHeader(response(http.StatusOK, req.Method), "Cache-Control", "max-age=300"), nil
		}))

		head, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodHead, cacheURL))
		must.NoError(t, err)
		test.EqOp(t, http.MethodHead, readBody(t, head))

		get, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
		must.NoError(t, err)
		test.EqOp(t, http.MethodGet, readBody(t, get))
	})
}

func TestCacheTransport_Revalidation(T *testing.T) {
	T.Parallel()

	T.Run("a 304 serves the stored body and refreshes the entry", func(t *testing.T) {
		t.Parallel()

		var (
			calls      int
			conditions []string
		)

		clk := &steppingClock{now: time.Unix(1_700_000_000, 0)}

		transport := cacheTransportForTest(t, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			conditions = append(conditions, req.Header.Get("If-None-Match"))

			if calls == 1 {
				resp := withHeader(response(http.StatusOK, "catalog"), "Cache-Control", "max-age=60")

				return withHeader(resp, "ETag", `"v1"`), nil
			}

			return withHeader(response(http.StatusNotModified, ""), "Cache-Control", "max-age=60"), nil
		}), WithCacheClock(clk))

		_, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
		must.NoError(t, err)

		clk.advance(61 * time.Second)

		revalidated, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
		must.NoError(t, err)

		test.EqOp(t, http.StatusOK, revalidated.StatusCode)
		test.EqOp(t, "catalog", readBody(t, revalidated))
		test.Eq(t, []string{"", `"v1"`}, conditions)

		// The 304 restated max-age, so the entry is fresh again and the next
		// read is a hit rather than a second revalidation.
		hit, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
		must.NoError(t, err)
		test.EqOp(t, "catalog", readBody(t, hit))
		test.EqOp(t, 2, calls)
	})

	T.Run("Last-Modified is offered when there is no ETag", func(t *testing.T) {
		t.Parallel()

		var conditions []string

		clk := &steppingClock{now: time.Unix(1_700_000_000, 0)}
		modified := "Wed, 21 Oct 2015 07:28:00 GMT"

		transport := cacheTransportForTest(t, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			conditions = append(conditions, req.Header.Get("If-Modified-Since"))

			resp := withHeader(response(http.StatusOK, "body"), "Cache-Control", "max-age=60")

			return withHeader(resp, "Last-Modified", modified), nil
		}), WithCacheClock(clk))

		_, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
		must.NoError(t, err)

		clk.advance(61 * time.Second)

		_, err = transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
		must.NoError(t, err)

		test.Eq(t, []string{"", modified}, conditions)
	})

	T.Run("a changed resource replaces the entry", func(t *testing.T) {
		t.Parallel()

		var calls int

		clk := &steppingClock{now: time.Unix(1_700_000_000, 0)}

		transport := cacheTransportForTest(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++

			resp := withHeader(response(http.StatusOK, "v"+strings.Repeat("!", calls)), "Cache-Control", "max-age=60")

			return withHeader(resp, "ETag", `"v1"`), nil
		}), WithCacheClock(clk))

		_, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
		must.NoError(t, err)

		clk.advance(61 * time.Second)

		changed, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
		must.NoError(t, err)
		test.EqOp(t, "v!!", readBody(t, changed))

		hit, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
		must.NoError(t, err)
		test.EqOp(t, "v!!", readBody(t, hit))
	})

	T.Run("a validator alone is enough to store a response nothing calls fresh", func(t *testing.T) {
		t.Parallel()

		var calls int

		transport := cacheTransportForTest(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++

			if calls == 1 {
				return withHeader(response(http.StatusOK, "body"), "ETag", `"v1"`), nil
			}

			return response(http.StatusNotModified, ""), nil
		}))

		_, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
		must.NoError(t, err)

		// Never fresh, so this reaches the origin — but as a conditional
		// request, and the body comes back out of the cache.
		revalidated, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
		must.NoError(t, err)

		test.EqOp(t, http.StatusOK, revalidated.StatusCode)
		test.EqOp(t, "body", readBody(t, revalidated))
		test.EqOp(t, 2, calls)
	})

	T.Run("a caller running its own conditional request is left alone", func(t *testing.T) {
		t.Parallel()

		var seen []string

		transport := cacheTransportForTest(t, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			seen = append(seen, req.Header.Get("If-None-Match"))

			return response(http.StatusNotModified, ""), nil
		}))

		req := cacheRequest(t.Context(), http.MethodGet, cacheURL)
		req.Header.Set("If-None-Match", `"caller"`)

		resp, err := transport.RoundTrip(req)
		must.NoError(t, err)

		// Passed through untouched: the 304 answers the caller's precondition,
		// not one this transport invented.
		test.EqOp(t, http.StatusNotModified, resp.StatusCode)
		test.Eq(t, []string{`"caller"`}, seen)
	})
}

func TestCacheTransport_WhatItRefusesToStore(T *testing.T) {
	T.Parallel()

	// Every case here has to reach the origin twice: the first response was not
	// stored, so the second request has nothing to be answered from.
	cases := map[string]func(*http.Response) *http.Response{
		"no-store":              func(r *http.Response) *http.Response { return withHeader(r, "Cache-Control", "no-store") },
		"private":               func(r *http.Response) *http.Response { return withHeader(r, "Cache-Control", "private") },
		"a cookie":              func(r *http.Response) *http.Response { return withHeader(r, "Set-Cookie", "session=abc") },
		"Vary: *":               func(r *http.Response) *http.Response { return withHeader(r, "Vary", "*") },
		"an uncacheable status": func(r *http.Response) *http.Response { r.StatusCode = http.StatusInternalServerError; return r },
		"no freshness at all":   func(r *http.Response) *http.Response { r.Header.Del("Cache-Control"); return r },
	}

	for name, taint := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			var calls int

			transport := cacheTransportForTest(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
				calls++

				return taint(withHeader(response(http.StatusOK, "body"), "Cache-Control", "max-age=300")), nil
			}))

			for range 2 {
				resp, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
				must.NoError(t, err)
				test.EqOp(t, "body", readBody(t, resp))
			}

			test.EqOp(t, 2, calls)
		})
	}
}

func TestCacheTransport_UncacheableRequests(T *testing.T) {
	T.Parallel()

	cases := map[string]func(*http.Request){
		"a POST":              func(r *http.Request) { r.Method = http.MethodPost },
		"a range request":     func(r *http.Request) { r.Header.Set("Range", "bytes=0-10") },
		"a request no-store":  func(r *http.Request) { r.Header.Set("Cache-Control", "no-store") },
		"an authorized reads": func(r *http.Request) { r.Header.Set("Authorization", "Bearer token") },
	}

	for name, taint := range cases {
		T.Run(name, func(t *testing.T) {
			t.Parallel()

			var calls int

			transport := cacheTransportForTest(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
				calls++

				return withHeader(response(http.StatusOK, "body"), "Cache-Control", "max-age=300"), nil
			}))

			for range 2 {
				req := cacheRequest(t.Context(), http.MethodGet, cacheURL)
				taint(req)

				resp, err := transport.RoundTrip(req)
				must.NoError(t, err)
				test.EqOp(t, "body", readBody(t, resp))
			}

			test.EqOp(t, 2, calls)
		})
	}
}

func TestCacheTransport_Authorization(T *testing.T) {
	T.Parallel()

	T.Run("opting in caches per credential rather than per URL", func(t *testing.T) {
		t.Parallel()

		var calls int

		transport := cacheTransportForTest(t, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			calls++

			resp := response(http.StatusOK, req.Header.Get("Authorization"))

			return withHeader(resp, "Cache-Control", "max-age=300"), nil
		}), WithCacheAuthorized(true))

		read := func(credential string) string {
			req := cacheRequest(t.Context(), http.MethodGet, cacheURL)
			req.Header.Set("Authorization", credential)

			resp, err := transport.RoundTrip(req)
			must.NoError(t, err)

			return readBody(t, resp)
		}

		test.EqOp(t, "Bearer alice", read("Bearer alice"))
		test.EqOp(t, "Bearer bob", read("Bearer bob"))

		// The point of the exercise: bob's second read is answered from bob's
		// entry, and alice's from alice's. Neither ever sees the other's body.
		test.EqOp(t, "Bearer alice", read("Bearer alice"))
		test.EqOp(t, "Bearer bob", read("Bearer bob"))
		test.EqOp(t, 2, calls)
	})

	T.Run("a private response is still refused when authorized caching is on", func(t *testing.T) {
		t.Parallel()

		var calls int

		transport := cacheTransportForTest(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++

			return withHeader(response(http.StatusOK, "body"), "Cache-Control", "private, max-age=300"), nil
		}), WithCacheAuthorized(true))

		for range 2 {
			req := cacheRequest(t.Context(), http.MethodGet, cacheURL)
			req.Header.Set("Authorization", "Bearer token")

			_, err := transport.RoundTrip(req)
			must.NoError(t, err)
		}

		test.EqOp(t, 2, calls)
	})
}

func TestCacheTransport_Vary(T *testing.T) {
	T.Parallel()

	T.Run("an entry stored against different request headers is a miss", func(t *testing.T) {
		t.Parallel()

		var calls int

		transport := cacheTransportForTest(t, roundTripperFunc(func(req *http.Request) (*http.Response, error) {
			calls++

			resp := withHeader(response(http.StatusOK, req.Header.Get("Accept")), "Cache-Control", "max-age=300")

			return withHeader(resp, "Vary", "Accept"), nil
		}))

		read := func(accept string) string {
			req := cacheRequest(t.Context(), http.MethodGet, cacheURL)
			req.Header.Set("Accept", accept)

			resp, err := transport.RoundTrip(req)
			must.NoError(t, err)

			return readBody(t, resp)
		}

		test.EqOp(t, "application/json", read("application/json"))
		test.EqOp(t, "application/json", read("application/json"))
		test.EqOp(t, 1, calls)

		// The stored entry answers a question this request did not ask, so it
		// is a miss — never the wrong body.
		test.EqOp(t, "text/xml", read("text/xml"))
		test.EqOp(t, 2, calls)
	})
}

func TestCacheTransport_BodySize(T *testing.T) {
	T.Parallel()

	T.Run("a body over the cap is returned whole and not stored", func(t *testing.T) {
		t.Parallel()

		var calls int

		body := strings.Repeat("x", 64)

		transport := cacheTransportForTest(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++

			return withHeader(response(http.StatusOK, body), "Cache-Control", "max-age=300"), nil
		}), WithMaxCacheableBody(16))

		for range 2 {
			resp, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
			must.NoError(t, err)
			test.EqOp(t, body, readBody(t, resp))
		}

		test.EqOp(t, 2, calls)
	})

	T.Run("a body exactly at the cap is stored", func(t *testing.T) {
		t.Parallel()

		var calls int

		transport := cacheTransportForTest(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++

			return withHeader(response(http.StatusOK, "0123456789abcdef"), "Cache-Control", "max-age=300"), nil
		}), WithMaxCacheableBody(16))

		for range 2 {
			resp, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
			must.NoError(t, err)
			test.EqOp(t, "0123456789abcdef", readBody(t, resp))
		}

		test.EqOp(t, 1, calls)
	})

	T.Run("a body that fails mid-read reports the failure where it happened", func(t *testing.T) {
		t.Parallel()

		boom := platformerrors.New("connection reset")

		transport := cacheTransportForTest(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			resp := response(http.StatusOK, "")
			resp.Header.Set("Cache-Control", "max-age=300")
			resp.Body = io.NopCloser(io.MultiReader(strings.NewReader("partial"), errorReader{err: boom}))

			return resp, nil
		}))

		resp, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
		must.NoError(t, err)

		read, err := io.ReadAll(resp.Body)
		test.ErrorIs(t, err, boom)
		test.EqOp(t, "partial", string(read))
	})
}

func TestCacheTransport_TTL(T *testing.T) {
	T.Parallel()

	T.Run("the configured TTL covers an origin that says nothing", func(t *testing.T) {
		t.Parallel()

		var calls int

		clk := &steppingClock{now: time.Unix(1_700_000_000, 0)}

		transport := cacheTransportForTest(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++

			return response(http.StatusOK, "jwks"), nil
		}), WithCacheTTL(5*time.Minute), WithCacheClock(clk))

		_, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
		must.NoError(t, err)

		clk.advance(4 * time.Minute)

		_, err = transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
		must.NoError(t, err)
		test.EqOp(t, 1, calls)

		clk.advance(2 * time.Minute)

		_, err = transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
		must.NoError(t, err)
		test.EqOp(t, 2, calls)
	})

	T.Run("the origin's own max-age beats the configured TTL", func(t *testing.T) {
		t.Parallel()

		var calls int

		clk := &steppingClock{now: time.Unix(1_700_000_000, 0)}

		transport := cacheTransportForTest(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++

			return withHeader(response(http.StatusOK, "body"), "Cache-Control", "max-age=30"), nil
		}), WithCacheTTL(time.Hour), WithCacheClock(clk))

		_, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
		must.NoError(t, err)

		clk.advance(31 * time.Second)

		_, err = transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
		must.NoError(t, err)

		// A TTL fills the origin's silence; it does not overrule the origin.
		test.EqOp(t, 2, calls)
	})
}

func TestCacheTransport_UnreachableStore(T *testing.T) {
	T.Parallel()

	T.Run("a cache that cannot be read costs hit rate and nothing else", func(t *testing.T) {
		t.Parallel()

		var calls int

		transport := newCacheTransport(&failingCache{err: cache.ErrUnavailable}, nil)
		transport.obs = observerForTest(t)
		transport.base = roundTripperFunc(func(*http.Request) (*http.Response, error) {
			calls++

			return withHeader(response(http.StatusOK, "body"), "Cache-Control", "max-age=300"), nil
		})

		for range 2 {
			resp, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
			must.NoError(t, err)
			test.EqOp(t, "body", readBody(t, resp))
		}

		test.EqOp(t, 2, calls)
	})
}

func TestCacheTransport_LeavesTheCallersRequestAlone(T *testing.T) {
	T.Parallel()

	T.Run("revalidation headers go on a copy", func(t *testing.T) {
		t.Parallel()

		clk := &steppingClock{now: time.Unix(1_700_000_000, 0)}

		transport := cacheTransportForTest(t, roundTripperFunc(func(*http.Request) (*http.Response, error) {
			resp := withHeader(response(http.StatusOK, "body"), "Cache-Control", "max-age=60")

			return withHeader(resp, "ETag", `"v1"`), nil
		}), WithCacheClock(clk))

		_, err := transport.RoundTrip(cacheRequest(t.Context(), http.MethodGet, cacheURL))
		must.NoError(t, err)

		clk.advance(61 * time.Second)

		req := cacheRequest(t.Context(), http.MethodGet, cacheURL)

		_, err = transport.RoundTrip(req)
		must.NoError(t, err)

		test.EqOp(t, "", req.Header.Get("If-None-Match"))
	})
}

// failingCache is a cache.Cache whose reads and writes always fail, for the
// case where the store is down and the origin is not.
type failingCache struct {
	cache.Cache[CachedResponse]

	err error
}

func (c *failingCache) Get(context.Context, string) (*CachedResponse, error) { return nil, c.err }

func (c *failingCache) Set(context.Context, string, *CachedResponse, ...cache.WriteOption) error {
	return c.err
}

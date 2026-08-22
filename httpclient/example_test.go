package httpclient_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v13/cache/memory"
	circuitbreakingcfg "github.com/primandproper/platform-go/v13/circuitbreaking/config"
	"github.com/primandproper/platform-go/v13/encoding"
	"github.com/primandproper/platform-go/v13/httpclient"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/ratelimiting"
	"github.com/primandproper/platform-go/v13/retry"
	retrycfg "github.com/primandproper/platform-go/v13/retry/config"
)

// A provider integration composes its resilience once, at construction, instead
// of at every call site.
func ExampleNewHTTPClient_resilience() {
	ctx := context.Background()

	policy, err := retrycfg.NewExponentialBackoffPolicy(retrycfg.Config{
		MaxAttempts:  4,
		InitialDelay: 100 * time.Millisecond,
		MaxDelay:     2 * time.Second,
		UseJitter:    true,
	}, retrycfg.WithName("payments"))
	if err != nil {
		panic(err)
	}

	breaker, err := circuitbreakingcfg.NewCircuitBreaker(ctx, &circuitbreakingcfg.Config{
		Name:                   "payments",
		ErrorRate:              50,
		MinimumSampleThreshold: 20,
	})
	if err != nil {
		panic(err)
	}

	// The provider documents 10 requests per second; the burst absorbs the
	// bunching that a retrying client produces.
	limiter, err := ratelimiting.NewInMemoryRateLimiter(10, 20)
	if err != nil {
		panic(err)
	}
	defer limiter.Close()

	// Whatever the service already built. Absent, every resilience layer below
	// resolves to its noop and the client records nowhere.
	pillars := &observability.Pillars{}

	client, err := httpclient.NewHTTPClient(
		httpclient.WithTimeout(30*time.Second), // room for the whole retry loop, not one attempt
		httpclient.WithTracing(true),
		httpclient.WithRetryPolicy(policy),
		httpclient.WithCircuitBreaker(breaker),
		httpclient.WithRateLimit(limiter),
		httpclient.WithPillars(pillars),
	)
	if err != nil {
		panic(err)
	}

	// Outermost to innermost the client is now: observability, breaker, retry,
	// rate limit, tracing, transport. Every package that builds its client
	// through httpclient gets the same arrangement without writing any of it.
	fmt.Println(client.Timeout)

	// Output: 30s
}

// The identity provider's JWKS document changes a few times a year and is read
// on every token verification. It is also served with no caching headers
// whatsoever, which is the usual reason a TTL map grows in front of one.
func ExampleWithHTTPCache() {
	// Per-process, because a JWKS is small, public, and read constantly. A
	// cache/redis store would share the entries across the fleet instead; the
	// option is the same either way. The hour is retention, not freshness — an
	// entry kept past its freshness is one a 304 can confirm without a body.
	store, err := memory.NewInMemoryCache[httpclient.CachedResponse](time.Hour)
	if err != nil {
		panic(err)
	}

	defer func() {
		if closeErr := store.Close(); closeErr != nil {
			panic(closeErr)
		}
	}()

	limiter, err := ratelimiting.NewInMemoryRateLimiter(10, 20)
	if err != nil {
		panic(err)
	}
	defer limiter.Close()

	client, err := httpclient.NewHTTPClient(
		httpclient.WithHTTPCache(store,
			// Consulted only where the origin said nothing. An origin that
			// sends max-age still governs its own resource.
			httpclient.WithCacheTTL(5*time.Minute),
			httpclient.WithMaxCacheableBody(256<<10),
		),
		// The cache sits above this, and above a circuit breaker if one is
		// named. A hit spends no token and reports no outcome, because it
		// never became a request.
		httpclient.WithRateLimit(limiter),
	)
	if err != nil {
		panic(err)
	}

	fmt.Println(client.Timeout)

	// Output: 10s
}

// A service whose status codes do not mean what the registry says they mean is
// the normal case, not the exceptional one. Both classification decisions are
// overridable, and both compose with the default rather than replacing it.
func ExampleWithOutcomeClassifier() {
	// This provider reports its own overload as 400 with a header, and answers
	// 503 for tenants that are merely out of quota. Taking either at face value
	// would trip a circuit against a host that is working perfectly well.
	classifier := func(resp *http.Response, err error) httpclient.Outcome {
		if resp != nil {
			switch {
			case resp.StatusCode == http.StatusBadRequest && resp.Header.Get("X-Overloaded") != "":
				return httpclient.OutcomeFailure
			case resp.StatusCode == http.StatusServiceUnavailable && resp.Header.Get("X-Quota-Exceeded") != "":
				return httpclient.OutcomeIgnored
			}
		}

		return httpclient.DefaultOutcome(resp, err)
	}

	overloaded := &http.Response{StatusCode: http.StatusBadRequest, Header: http.Header{"X-Overloaded": {"1"}}}
	outOfQuota := &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"X-Quota-Exceeded": {"1"}}}
	genuine := &http.Response{StatusCode: http.StatusServiceUnavailable, Header: http.Header{}}

	fmt.Println(httpclient.DefaultOutcome(overloaded, nil), classifier(overloaded, nil))
	fmt.Println(httpclient.DefaultOutcome(outOfQuota, nil), classifier(outOfQuota, nil))
	fmt.Println(httpclient.DefaultOutcome(genuine, nil), classifier(genuine, nil))

	// Output:
	// success failure
	// failure ignored
	// failure failure
}

// claimRequest and claimResponse stand in for the typed bodies a service-to-
// service caller already has.
type claimRequest struct {
	Worker string `json:"worker"`
}

type claimResponse struct {
	ID    string `json:"id"`
	Count int    `json:"count"`
}

// The exchange every service-to-service caller was writing by hand: marshal,
// send, check the status, unmarshal. Named no content type, it speaks
// DefaultContentType.
func ExampleExchange() {
	server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
		res.Header().Set("Content-Type", "application/json")
		fmt.Fprint(res, `{"id":"claim-7","count":3}`)
	}))
	defer server.Close()

	client, err := httpclient.NewHTTPClient()
	if err != nil {
		panic(err)
	}

	claim, err := httpclient.Exchange[claimResponse](
		context.Background(),
		client,
		http.MethodPost,
		server.URL+"/v1/claim",
		&claimRequest{Worker: "worker-1"},
	)
	if err != nil {
		panic(err)
	}

	fmt.Println(claim.ID, claim.Count)

	// Output: claim-7 3
}

// Nothing about an exchange is written in terms of JSON. A service that speaks
// CBOR — smaller on the wire than JSON, and readable outside Go — is one option
// away, and so is every other encoding the encoding package implements.
func ExampleWithContentType() {
	cbor := encoding.NewClientEncoder(encoding.ContentTypeCBOR)

	server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		// One option set both directions: what the request body is, and what the
		// caller will accept back.
		fmt.Println(req.Header.Get("Content-Type"), req.Header.Get("Accept"))

		raw, err := cbor.Marshal(req.Context(), &claimResponse{ID: "claim-7", Count: 3})
		if err != nil {
			panic(err)
		}

		res.Header().Set("Content-Type", encoding.ContentTypeCBOR.String())
		_, _ = res.Write(raw)
	}))
	defer server.Close()

	client, err := httpclient.NewHTTPClient()
	if err != nil {
		panic(err)
	}

	claim, err := httpclient.Exchange[claimResponse](
		context.Background(),
		client,
		http.MethodPost,
		server.URL+"/v1/claim",
		&claimRequest{Worker: "worker-1"},
		httpclient.WithContentType(encoding.ContentTypeCBOR),
	)
	if err != nil {
		panic(err)
	}

	fmt.Println(claim.ID, claim.Count)

	// Output:
	// application/cbor application/cbor
	// claim-7 3
}

// A refused status is an error carrying what an operator needs and no more of
// the body than a log line can afford.
func ExampleStatusError() {
	// The proxy's four-megabyte HTML error page, which is the reason the limit
	// is on the read rather than on the string.
	server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
		res.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(res, "worker-1 is not registered for this area"+strings.Repeat(".", 4<<20))
	}))
	defer server.Close()

	client, err := httpclient.NewHTTPClient()
	if err != nil {
		panic(err)
	}

	_, err = httpclient.Exchange[claimResponse](
		context.Background(),
		client,
		http.MethodPost,
		server.URL+"/v1/claim",
		&claimRequest{Worker: "worker-1"},
		httpclient.WithErrorBodyLimit(40),
	)

	var status *httpclient.StatusError
	if errors.As(err, &status) {
		fmt.Println(status.StatusCode, status.Path, status.Truncated)
		fmt.Println(status.Body)
	}

	// A 400 is the server saying the request itself is wrong, so a caller's own
	// retry loop stops on it — the same rule the retry transport applies to a
	// response, read here from the error.
	fmt.Println(errors.Is(err, retry.ErrUnretryable))

	// Output:
	// 400 /v1/claim true
	// worker-1 is not registered for this area
	// true
}

// The other half every consumer rebuilds: a base URL, so call sites name a path.
func ExampleNewBaseURLClient() {
	server := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		res.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(res, `{"id":%q,"count":1}`, req.URL.Path)
	}))
	defer server.Close()

	client, err := httpclient.NewHTTPClient()
	if err != nil {
		panic(err)
	}

	// The trailing slash on the base and the leading slash on the path are both
	// fine, in any combination — which is the whole reason not to concatenate.
	leader, err := httpclient.NewBaseURLClient(client, server.URL+"/api/")
	if err != nil {
		panic(err)
	}

	claim, err := httpclient.Exchange[claimResponse](context.Background(), leader, http.MethodGet, "/v1/claim", nil)
	if err != nil {
		panic(err)
	}

	fmt.Println(claim.ID)

	// Output: /api/v1/claim
}

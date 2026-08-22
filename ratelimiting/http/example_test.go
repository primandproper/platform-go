package http_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"

	"github.com/primandproper/platform-go/v13/ratelimiting"
	ratelimitinghttp "github.com/primandproper/platform-go/v13/ratelimiting/http"
)

func ExampleNewMiddleware() {
	// One request per second, no burst, so the second request in a row is
	// refused and the limiter can say when to come back.
	limiter, err := ratelimiting.NewInMemoryRateLimiter(1, 1)
	if err != nil {
		panic(err)
	}
	defer limiter.Close()

	mw, err := ratelimitinghttp.NewMiddleware(limiter,
		ratelimitinghttp.FirstNonEmpty(
			ratelimitinghttp.KeyByHeader("X-API-Key"),
			ratelimitinghttp.KeyByRemoteAddr(),
		),
	)
	if err != nil {
		panic(err)
	}

	guarded := mw(http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
		res.WriteHeader(http.StatusOK)
	}))

	send := func() (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/things", http.NoBody)
		req.Header.Set("X-API-Key", "sk_example")

		rec := httptest.NewRecorder()
		guarded.ServeHTTP(rec, req)

		return rec.Code, rec.Header().Get(ratelimitinghttp.RetryAfterHeader)
	}

	status, _ := send()
	fmt.Println(status)

	status, retryAfter := send()
	fmt.Println(status, retryAfter)

	// Output:
	// 200
	// 429 1
}

package http_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	cachememory "github.com/primandproper/platform-go/v13/cache/memory"
	"github.com/primandproper/platform-go/v13/distributedlock"
	dlmemory "github.com/primandproper/platform-go/v13/distributedlock/memory"
	"github.com/primandproper/platform-go/v13/idempotency"
	idempotencyhttp "github.com/primandproper/platform-go/v13/idempotency/http"
)

func newMiddleware() (func(http.Handler) http.Handler, error) {
	store, err := cachememory.NewInMemoryCache[idempotency.Record[idempotencyhttp.Response]](0)
	if err != nil {
		return nil, err
	}

	locker, err := dlmemory.NewLocker()
	if err != nil {
		return nil, err
	}

	scoped, err := distributedlock.NewScopedLocker(locker)
	if err != nil {
		return nil, err
	}

	// NewManager rather than idempotency.NewManager: it applies the HTTP rule
	// that a 5xx is not recorded.
	manager, err := idempotencyhttp.NewManager(store, scoped)
	if err != nil {
		return nil, err
	}

	return idempotencyhttp.NewMiddleware(manager)
}

// Example shows the client and server halves together: one key, two requests,
// one execution.
func Example() {
	mw, err := newMiddleware()
	if err != nil {
		panic(err)
	}

	charges := 0
	srv := httptest.NewServer(mw(http.HandlerFunc(func(res http.ResponseWriter, _ *http.Request) {
		charges++
		res.Header().Set("Content-Type", "application/json")
		res.WriteHeader(http.StatusCreated)
		_, _ = res.Write([]byte(`{"id":"ch_1"}`))
	})))
	defer srv.Close()

	client := srv.Client()
	client.Transport = idempotencyhttp.NewTransport(client.Transport)

	// Minted once, outside the loop, so both attempts carry the same key.
	ctx, _ := idempotency.WithNewKey(context.Background())

	for range 2 {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/charges", strings.NewReader(`{"amount":10}`))
		if reqErr != nil {
			panic(reqErr)
		}

		res, doErr := client.Do(req)
		if doErr != nil {
			panic(doErr)
		}

		fmt.Println(res.StatusCode, "replayed:", res.Header.Get(idempotencyhttp.ReplayHeader) == "true")
		_ = res.Body.Close()
	}

	fmt.Println("charges:", charges)

	// Output:
	// 201 replayed: false
	// 201 replayed: true
	// charges: 1
}

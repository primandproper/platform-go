package http_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/primandproper/platform-go/v13/cache/memory"
	"github.com/primandproper/platform-go/v13/cookies"
	"github.com/primandproper/platform-go/v13/sessions"
	sessionscache "github.com/primandproper/platform-go/v13/sessions/cache"
	sessionshttp "github.com/primandproper/platform-go/v13/sessions/http"
)

// Principal is what a session carries.
type Principal struct {
	UserID string
}

// newManager wires a store to a cookie manager. Production reads the cookie
// keys out of secrets rather than spelling them out.
func newManager() *sessionshttp.Manager[Principal] {
	c, err := memory.NewInMemoryCache[sessions.Record[Principal]](time.Hour)
	if err != nil {
		panic(err)
	}

	backend, err := sessionscache.NewBackend(c)
	if err != nil {
		panic(err)
	}

	store, err := sessions.NewStore(backend)
	if err != nil {
		panic(err)
	}

	cookieManager, err := cookies.NewCookieManager(&cookies.Config{
		Base64EncodedHashKey:  base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef")),
		Base64EncodedBlockKey: base64.StdEncoding.EncodeToString([]byte("fedcba9876543210fedcba9876543210")),
		Lifetime:              24 * time.Hour,
	})
	if err != nil {
		panic(err)
	}

	manager, err := sessionshttp.NewManager(store, cookieManager)
	if err != nil {
		panic(err)
	}

	return manager
}

// The middleware attaches a session when there is one and decides nothing
// else — handlers ask, and answer for themselves.
func Example() {
	manager := newManager()

	handler := manager.Middleware()(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		session, ok := sessionshttp.SessionFromContext[Principal](req.Context())
		if !ok {
			res.WriteHeader(http.StatusUnauthorized)

			return
		}

		fmt.Fprintf(res, "hello %s", session.Data.UserID)
	}))

	// An anonymous request.
	anonymous := httptest.NewRecorder()
	handler.ServeHTTP(anonymous, httptest.NewRequest(http.MethodGet, "/", http.NoBody))
	fmt.Println("anonymous:", anonymous.Code)

	// Sign in, then repeat the request with the cookie the sign-in set.
	signIn := httptest.NewRecorder()
	if _, err := manager.Issue(context.Background(), signIn, &Principal{UserID: "u_123"}); err != nil {
		panic(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", http.NoBody)
	for _, cookie := range signIn.Result().Cookies() {
		req.AddCookie(cookie)
	}

	authenticated := httptest.NewRecorder()
	handler.ServeHTTP(authenticated, req)
	fmt.Println("authenticated:", authenticated.Body.String())

	// Output:
	// anonymous: 401
	// authenticated: hello u_123
}

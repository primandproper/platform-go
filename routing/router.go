package routing

import (
	"net/http"

	"github.com/primandproper/platform-go/v2/observability/logging"
)

// all interfaces HEAVILY inspired by github.com/go-chi/chi.

type (
	// Middleware  is a type alias for a middleware handler function.
	Middleware func(http.Handler) http.Handler

	// Router defines the contract between routing library and caller.
	Router interface {
		Routes() []*Route
		Handler() http.Handler
		Handle(pattern string, handler http.Handler)
		HandleFunc(pattern string, handler http.HandlerFunc)
		WithMiddleware(middleware ...Middleware) Router
		Route(pattern string, fn func(r Router)) Router
		Connect(pattern string, handler http.HandlerFunc)
		Delete(pattern string, handler http.HandlerFunc)
		Get(pattern string, handler http.HandlerFunc)
		Head(pattern string, handler http.HandlerFunc)
		Options(pattern string, handler http.HandlerFunc)
		Patch(pattern string, handler http.HandlerFunc)
		Post(pattern string, handler http.HandlerFunc)
		Put(pattern string, handler http.HandlerFunc)
		Trace(pattern string, handler http.HandlerFunc)
		AddRoute(method, path string, handler http.HandlerFunc, middleware ...Middleware) error
	}

	// RouteParamManager builds route param fetchers for a compatible router.
	RouteParamManager interface {
		BuildRouteParamIDFetcher(logger logging.Logger, key, logDescription string) func(req *http.Request) uint64
		BuildRouteParamStringIDFetcher(key string) func(req *http.Request) string
	}

	// Route represents a registered HTTP route with its method and path.
	Route struct {
		Method string
		Path   string
	}
)

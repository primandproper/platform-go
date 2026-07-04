package chi

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/primandproper/platform-go/v3/observability/logging"
	"github.com/primandproper/platform-go/v3/routing"

	"github.com/go-chi/chi/v5"
)

type chiRouteParamManager struct{}

// NewRouteParamManager provides a new RouteParamManager.
func NewRouteParamManager() routing.RouteParamManager {
	return &chiRouteParamManager{}
}

// buildRouteParamIDFetcher is the shared implementation behind both the router's
// and the route-param manager's BuildRouteParamIDFetcher. On a missing or
// non-numeric param it returns 0 — which the uint64-only signature can't
// distinguish from a real ID of 0 — so it always logs the parse failure rather
// than swallowing it when no description is provided.
func buildRouteParamIDFetcher(logger logging.Logger, key, logDescription string) func(req *http.Request) uint64 {
	log := logging.EnsureLogger(logger)
	return func(req *http.Request) uint64 {
		raw := chi.URLParam(req, key)
		u, err := strconv.ParseUint(raw, 10, 64)
		if err != nil {
			desc := logDescription
			if desc == "" {
				desc = key
			}
			log.Error(fmt.Sprintf("fetching %s ID from request (value %q)", desc, raw), err)
		}

		return u
	}
}

// BuildRouteParamIDFetcher builds a function that fetches a given key from a path with variables added by a router.
func (r chiRouteParamManager) BuildRouteParamIDFetcher(logger logging.Logger, key, logDescription string) func(req *http.Request) uint64 {
	return buildRouteParamIDFetcher(logger, key, logDescription)
}

// BuildRouteParamStringIDFetcher builds a function that fetches a given key from a path with variables added by a router.
func (r chiRouteParamManager) BuildRouteParamStringIDFetcher(key string) func(req *http.Request) string {
	return func(req *http.Request) string {
		return chi.URLParam(req, key)
	}
}

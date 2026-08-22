// Package httprouter provides a routing.Backend built on
// julienschmidt/httprouter, a fast radix-tree router. httprouter uses ":name"
// path parameters, so the "/users/{id}" patterns the routing layer produces are
// rewritten to "/users/:id" at registration; path values are read back from the
// request context httprouter populates. httprouter matches on the decoded
// request path and offers no way to change that, so the lookup for a request
// carrying a percent-escaped path value is taken here instead — see
// escapedPathDispatch. The shared observability, recovery,
// CORS, and OpenTelemetry middleware stack is applied around the router,
// matching the chi backend's behavior.
package httprouter

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/routing"
	"github.com/primandproper/platform-go/v13/routing/backends/internal/httpmw"
	"github.com/primandproper/platform-go/v13/routing/backends/internal/pathvalues"

	hr "github.com/julienschmidt/httprouter"
)

var _ routing.Backend = (*Backend)(nil)

// Backend is a julienschmidt/httprouter implementation of routing.Backend.
// Global middleware is composed around the router lazily in Handler; because the
// router is wrapped by reference, routes registered after the first Handler call
// are still served.
//
// It is exported, and returned by NewBackend, so a caller who has chosen
// httprouter can depend on that choice rather than on the seam every router
// backend shares.
type Backend struct {
	built    http.Handler
	router   *hr.Router
	standard []func(http.Handler) http.Handler
	user     []func(http.Handler) http.Handler
	once     sync.Once
	sealed   atomic.Bool
}

// NewBackend constructs an httprouter-backed routing.Backend with the standard
// middleware and OpenTelemetry stack installed. Pass it to routing.New. Panics
// in handlers propagate to the shared recovery middleware, so no httprouter
// PanicHandler is installed.
func NewBackend(cfg *Config, opts ...Option) *Backend {
	// A nil config is the zero config, not a panic. The config subpackage
	// dispatches on Provider and hands whichever sub-config happens to be set —
	// which is nil unless the deployment filled that provider's section in, so
	// every backend here got one on a perfectly ordinary configuration.
	if cfg == nil {
		cfg = &Config{}
	}

	o := newOptions(opts)
	tracerProvider := tracing.EnsureTracerProvider(o.tracerProvider)
	o11y := observability.NewObserver("router", logging.EnsureLogger(o.logger), tracerProvider)

	return &Backend{
		router: hr.New(),
		standard: httpmw.Standard(o11y, &httpmw.StackConfig{
			TracerProvider:         tracerProvider,
			MeterProvider:          metrics.EnsureMetricsProvider(o.metricsProvider).MeterProvider(),
			ServiceName:            cfg.ServiceName,
			ValidDomains:           cfg.ValidDomains,
			EnableCORSForLocalhost: cfg.EnableCORSForLocalhost,
			SilenceRouteLogging:    cfg.SilenceRouteLogging,
		}),
	}
}

// Use installs global middleware, applied to every route. It may be called at
// any time before Handler.
// Use appends middleware to the chain.
//
// It must be called before Handler(). The chain is composed once, on the first
// Handler() call, so middleware added afterwards was silently dropped — the
// server ran without the authentication or rate limiting the caller believed it
// had registered. That is now a panic: a middleware that does not run is not a
// condition a process should serve traffic in.
func (b *Backend) Use(middleware ...routing.Middleware) {
	if b.sealed.Load() {
		panic("routing: Use called after Handler; middleware must be registered before the handler is built")
	}

	b.user = append(b.user, httpmw.Convert(middleware...)...)
}

// Handle registers handler for method at pattern, rewriting the "{name}"
// placeholders to httprouter's ":name" form.
func (b *Backend) Handle(method, pattern string, handler http.Handler) {
	b.router.Handler(method, httpmw.ColonParams(pattern), handler)
}

// PathValue returns the named path parameter from the httprouter params stored
// on the request context, decoded. A match found by escapedPathDispatch captured
// an escaped segment; one found by httprouter itself has nothing left to decode.
func (b *Backend) PathValue(req *http.Request, name string) string {
	return pathvalues.Decode(hr.ParamsFromContext(req.Context()).ByName(name))
}

// probeMethods stands in for httprouter's unexported allowed(), which walks the
// method trees it holds privately. Looking a method up that was never registered
// costs a nil map read, so probing the fixed set is equivalent. OPTIONS is left
// out for the same reason httprouter leaves it out: it is appended to the Allow
// header rather than probed for.
var probeMethods = []string{
	http.MethodGet,
	http.MethodHead,
	http.MethodPost,
	http.MethodPut,
	http.MethodPatch,
	http.MethodDelete,
	http.MethodConnect,
	http.MethodTrace,
}

// escapedPathDispatch matches on the escaped path, so a percent-escaped
// separator stays inside its segment rather than splitting it. httprouter reads
// URL.Path — already decoded — and has no raw-path setting of any kind, so for a
// request carrying an escaped value the routing decision has to be taken here.
//
// It is only the decision that moves. Lookup neither reads nor writes the
// request, and a request with nothing escaped is handed straight to ServeHTTP,
// so the ordinary path is byte-for-byte what it was. The escaped path then has
// to answer for itself, because "no route" and "wrong method" are different
// answers and a caller is owed the right one: a match runs, a trailing-slash
// near-miss redirects, and a path that exists under another verb is a 405 with
// an Allow header, all decided on the escaped form.
//
// Anything left over falls through to ServeHTTP on the decoded path, which is
// what httprouter did before. That keeps RedirectFixedPath — its case-insensitive
// fixup reaches through an unexported tree method, so it cannot be mirrored here,
// and the shared middleware stack deliberately leaves path normalization to the
// router. The cost is one asymmetry: where the escaped path matches nothing at
// all, httprouter still gets to try the decoded one, so "/things/x%2Fy" can reach
// a registered "/things/{a}/{b}" that the other backends refuse.
func escapedPathDispatch(router *hr.Router) http.Handler {
	return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		// Equal whenever nothing needed escaping, which is the overwhelming
		// majority of requests and the case this must not perturb.
		escaped := req.URL.EscapedPath()
		if escaped == req.URL.Path {
			router.ServeHTTP(res, req)

			return
		}

		handle, params, tsr := router.Lookup(req.Method, escaped)
		if handle != nil {
			handle(res, req, params)

			return
		}

		if tsr && router.RedirectTrailingSlash && req.Method != http.MethodConnect && escaped != "/" {
			if redirectTrailingSlash(res, req, escaped) {
				return
			}
		}

		if allow := allowedMethods(router, escaped, req.Method); allow != "" {
			if req.Method == http.MethodOptions && router.HandleOPTIONS {
				res.Header().Set("Allow", allow)

				return
			}

			if req.Method != http.MethodOptions && router.HandleMethodNotAllowed {
				res.Header().Set("Allow", allow)
				http.Error(res, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)

				return
			}
		}

		router.ServeHTTP(res, req)
	})
}

// redirectTrailingSlash sends the caller to escaped with its trailing slash
// added or removed, reporting whether it wrote a response.
//
// httprouter builds this redirect by writing the path it routed on back into
// req.URL.Path and calling URL.String(), which would escape an already-escaped
// path a second time and send the caller to "%252F". Setting Path and RawPath
// together keeps the two consistent, so String emits the escaping that arrived.
func redirectTrailingSlash(res http.ResponseWriter, req *http.Request, escaped string) bool {
	target, hadSlash := strings.CutSuffix(escaped, "/")
	if !hadSlash {
		target = escaped + "/"
	}

	decoded, err := url.PathUnescape(target)
	if err != nil {
		return false
	}

	// Go has no 308, which is why httprouter uses 307 for the methods where a
	// 301 would let the client drop the body and the verb.
	code := http.StatusTemporaryRedirect
	if req.Method == http.MethodGet {
		code = http.StatusMovedPermanently
	}

	location := *req.URL
	location.Path, location.RawPath = decoded, target
	http.Redirect(res, req, location.String(), code)

	return true
}

// allowedMethods returns the Allow header for escaped, or "" if no other method
// serves it. It mirrors httprouter's allowed(): the requested method is skipped
// because it has already been tried, and OPTIONS is appended rather than probed.
func allowedMethods(router *hr.Router, escaped, reqMethod string) string {
	var allowed []string

	for _, method := range probeMethods {
		if method == reqMethod {
			continue
		}

		if handle, _, _ := router.Lookup(method, escaped); handle != nil {
			allowed = append(allowed, method)
		}
	}

	if len(allowed) == 0 {
		return ""
	}

	allowed = append(allowed, http.MethodOptions)
	slices.Sort(allowed)

	return strings.Join(allowed, ", ")
}

// Handler returns the composed http.Handler: the standard middleware stack and
// any user middleware wrapped around the router.
func (b *Backend) Handler() http.Handler {
	b.sealed.Store(true)

	b.once.Do(func() {
		all := make([]func(http.Handler) http.Handler, 0, len(b.standard)+len(b.user))
		all = append(all, b.standard...)
		all = append(all, b.user...)
		b.built = httpmw.Chain(escapedPathDispatch(b.router), all...)
	})

	return b.built
}

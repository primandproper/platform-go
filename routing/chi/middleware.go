package chi

import (
	"net/http"
	"time"

	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/tracing"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

// healthCheckPaths are request paths that should not be traced or logged (e.g. load balancer probes).
var healthCheckPaths = map[string]bool{
	"/_ops_/live":  true,
	"/_ops_/ready": true,
}

func isHealthCheck(path string) bool {
	return healthCheckPaths[path]
}

// buildLoggingMiddleware builds a logging middleware.
func buildLoggingMiddleware(logger logging.Logger, tracer tracing.Tracer, silenceRouteLogging bool) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			ctx := req.Context()
			var span tracing.Span
			if !isHealthCheck(req.URL.Path) {
				ctx, span = tracer.StartSpan(ctx)
				defer span.End()
			}

			ww := chimiddleware.NewWrapResponseWriter(res, req.ProtoMajor)
			start := time.Now()

			next.ServeHTTP(ww, req.WithContext(ctx))

			if !silenceRouteLogging && !isHealthCheck(req.URL.Path) {
				logger.WithRequest(req).WithSpan(span).WithValues(map[string]any{
					"status":  ww.Status(),
					"elapsed": time.Since(start).Milliseconds(),
					"written": ww.BytesWritten(),
				}).Info("response served")
			}
		})
	}
}

package chi

import (
	stderrors "errors"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/primandproper/platform-go/v4/errors"
	"github.com/primandproper/platform-go/v4/observability"

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

// buildRecoveryMiddleware builds a middleware that recovers from panics in downstream
// handlers, logs them with request context, and returns a 500 rather than letting the
// panic sever the connection.
func buildRecoveryMiddleware(o11y observability.Observer) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}

				err, ok := rec.(error)

				// don't recover http.ErrAbortHandler, so the response to the client is aborted.
				if ok && stderrors.Is(err, http.ErrAbortHandler) {
					panic(rec)
				}

				if !ok {
					err = errors.Newf("%v", rec)
				}

				o11y.Logger().
					WithRequest(req).
					WithValue("stack", string(debug.Stack())).
					Error("recovering from panic in HTTP handler", err)

				res.WriteHeader(http.StatusInternalServerError)
			}()

			next.ServeHTTP(res, req)
		})
	}
}

// buildLoggingMiddleware builds a logging middleware.
func buildLoggingMiddleware(o11y observability.Observer, silenceRouteLogging bool) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			ctx := req.Context()
			var op observability.Operation
			if !isHealthCheck(req.URL.Path) {
				ctx, op = o11y.Begin(ctx)
				defer op.End()
			}

			ww := chimiddleware.NewWrapResponseWriter(res, req.ProtoMajor)
			start := time.Now()

			next.ServeHTTP(ww, req.WithContext(ctx))

			if !silenceRouteLogging && !isHealthCheck(req.URL.Path) {
				op.Logger().WithRequest(req).WithValues(map[string]any{
					"status":  ww.Status(),
					"elapsed": time.Since(start).Milliseconds(),
					"written": ww.BytesWritten(),
				}).Info("response served")
			}
		})
	}
}

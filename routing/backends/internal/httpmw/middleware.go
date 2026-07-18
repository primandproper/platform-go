package httpmw

import (
	stderrors "errors"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/primandproper/platform-go/v5/errors"
	"github.com/primandproper/platform-go/v5/observability"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

// Recovery builds a middleware that recovers from panics in downstream handlers,
// logs them with request context, and returns a 500 rather than letting the
// panic sever the connection. http.ErrAbortHandler is re-panicked so the
// connection is still aborted as the standard library intends.
func Recovery(o11y observability.Observer) func(next http.Handler) http.Handler {
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

// Logging builds a middleware that opens an observability operation per request
// and logs the served response (status, elapsed, bytes written). Health-check
// paths are neither traced nor logged. When silenceRouteLogging is set, the
// per-response log line is suppressed while the operation span is still opened.
func Logging(o11y observability.Observer, silenceRouteLogging bool) func(next http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			ctx := req.Context()
			var op observability.Operation
			if !IsHealthCheck(req.URL.Path) {
				ctx, op = o11y.Begin(ctx)
				defer op.End()
			}

			ww := chimiddleware.NewWrapResponseWriter(res, req.ProtoMajor)
			start := time.Now()

			next.ServeHTTP(ww, req.WithContext(ctx))

			if !silenceRouteLogging && !IsHealthCheck(req.URL.Path) {
				op.Logger().WithRequest(req).WithValues(map[string]any{
					"status":  ww.Status(),
					"elapsed": time.Since(start).Milliseconds(),
					"written": ww.BytesWritten(),
				}).Info("response served")
			}
		})
	}
}

package httprouter

import (
	"net/http"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

// RequestIDFunc returns the request ID assigned to a request by the backend's
// request-ID middleware, or "" if none is present. It can be handed to
// logging.Logger.SetRequestIDFunc so log lines carry the request ID.
func RequestIDFunc(req *http.Request) string {
	if x, ok := req.Context().Value(chimiddleware.RequestIDKey).(string); ok {
		return x
	}

	return ""
}

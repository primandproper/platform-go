package chi

import (
	"net/http"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
)

// RequestIDFunc returns the request ID assigned to a request with Chi's middleware.
func RequestIDFunc(req *http.Request) string {
	if x, ok := req.Context().Value(chimiddleware.RequestIDKey).(string); ok {
		return x
	}

	return ""
}

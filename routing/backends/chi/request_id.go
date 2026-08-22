package chi

import (
	"net/http"

	"github.com/primandproper/platform-go/v13/routing/backends/internal/httpmw"
)

// RequestIDFunc returns the request ID assigned to a request by the backend's
// request-ID middleware, or "" if none is present. It can be handed to
// logging.Logger.SetRequestIDFunc so log lines carry the request ID.
func RequestIDFunc(req *http.Request) string {
	return httpmw.RequestID(req)
}

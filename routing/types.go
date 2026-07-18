package routing

import (
	"context"
	"net/http"
)

type (
	// Handler is a typed HTTP handler. It receives a decoded, validated input value
	// and returns a typed output or an error. The framework handles decoding In from
	// the request, encoding Out into the response, and mapping a returned error to an
	// HTTP status and error envelope.
	Handler[In, Out any] func(ctx context.Context, in In) (Out, error)

	// Empty is a placeholder type for routes that take no meaningful input or produce
	// no response body. A route whose Out is Empty writes only a status code (no body).
	Empty struct{}

	// Route is the descriptor returned by a registration call. It records the concrete
	// method and (annotation-stripped) path the route was registered under, plus the
	// resolved OpenAPI operation ID.
	Route struct {
		Method      string
		Path        string
		OperationID string
	}
)

// methodAllowsBody reports whether an HTTP method conventionally carries a request
// body that the layer should attempt to decode.
func methodAllowsBody(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

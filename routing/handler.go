package routing

import (
	"net/http"
	"reflect"

	httpx "github.com/primandproper/platform-go/v6/errors/http"
)

// buildHTTPHandler wraps a typed Handler in an http.HandlerFunc that runs the
// full request lifecycle: begin a per-operation span, bind and validate the
// input, invoke the handler, and encode the output (or error).
func buildHTTPHandler[In, Out any](r *Router, plan *bindPlan, rc *routeConfig, h Handler[In, Out]) http.HandlerFunc {
	enc := r.encoderFor(rc.contentType)
	noBody := isEmptyType[Out]()
	successStatus := rc.successStatus
	envelope := rc.envelope
	operationID := rc.operationID

	return func(res http.ResponseWriter, req *http.Request) {
		ctx, op := r.o11y.BeginCustom(req.Context(), operationID)
		defer op.End()

		var in In
		if err := plan.bind(ctx, r, req, reflect.ValueOf(&in).Elem()); err != nil {
			r.writeError(ctx, res, op, enc, err)

			return
		}

		out, err := h(ctx, in)
		if err != nil {
			r.writeError(ctx, res, op, enc, err)

			return
		}

		if noBody {
			res.WriteHeader(successStatus)

			return
		}

		if envelope {
			enc.EncodeResponseWithStatus(ctx, res, httpx.APIResponse[Out]{
				Data:    out,
				Details: detailsFromCtx(ctx),
			}, successStatus)

			return
		}

		enc.EncodeResponseWithStatus(ctx, res, out, successStatus)
	}
}

// isEmptyType reports whether T is the Empty sentinel.
func isEmptyType[T any]() bool {
	var t T
	_, ok := any(t).(Empty)

	return ok
}

// responseStructure returns the value whose type is reflected into the operation's
// success response body: nil for Empty (no body), APIResponse[Out] when enveloped,
// else Out.
func responseStructure[Out any](envelope bool) any {
	if isEmptyType[Out]() {
		return nil
	}

	if envelope {
		return new(httpx.APIResponse[Out])
	}

	return new(Out)
}

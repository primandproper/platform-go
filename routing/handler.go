package routing

import (
	"fmt"
	"net/http"
	"reflect"

	httpx "github.com/primandproper/platform-go/v13/errors/http"
)

// buildHTTPHandler wraps a typed Handler in an http.HandlerFunc that runs the
// full request lifecycle: begin a per-operation span, bind and validate the
// input, invoke the handler, and encode the output (or error).
func buildHTTPHandler[In, Out any](r *Router, plan *bindPlan, rc *routeConfig, h Handler[In, Out]) http.HandlerFunc {
	enc := r.encoderFor(rc.contentType)
	noBody := bodylessResponse[Out]()
	successStatus := rc.successStatus
	envelope := rc.envelope
	operationID := rc.operationID

	return func(res http.ResponseWriter, req *http.Request) {
		ctx, op := r.o11y.BeginCustom(req.Context(), operationID)
		defer op.End()

		var in In
		if err := plan.bind(ctx, r, res, req, reflect.ValueOf(&in).Elem()); err != nil {
			r.writeError(ctx, res, op, enc, err)

			return
		}

		out, err := h(ctx, in)
		if err != nil {
			r.writeError(ctx, res, op, enc, err)

			return
		}

		// A Result names the status of this one response; anything else is
		// answered at the status the route registered.
		status := successStatus

		result, wrapped := any(out).(anyResult)
		if wrapped {
			switch named := result.responseStatus(); {
			case named == 0:
				// The handler named nothing, so the registered status stands.
			case named < minHTTPStatus || named > maxHTTPStatus:
				r.writeError(ctx, res, op, enc, fmt.Errorf("%w: %d", ErrInvalidResponseStatus, named))

				return
			default:
				status = named
			}

			// Before anything is written: a header set after WriteHeader is a
			// header the client never sees, and writeError below still needs a
			// response it can write in full.
			if err = applyResponseHeader(res.Header(), result.responseHeader()); err != nil {
				r.writeError(ctx, res, op, enc, err)

				return
			}
		}

		if noBody {
			res.WriteHeader(status)

			return
		}

		// The Result is unwrapped before encoding, never encoded: what reaches
		// the client is what a handler returning the wrapped type would have
		// sent, at the status this one chose.
		switch {
		case wrapped && envelope:
			enc.EncodeResponseWithStatus(ctx, res, result.responseEnvelope(detailsFromCtx(ctx)), status)
		case wrapped:
			enc.EncodeResponseWithStatus(ctx, res, result.responseValue(), status)
		case envelope:
			enc.EncodeResponseWithStatus(ctx, res, httpx.APIResponse[Out]{
				Data:    out,
				Details: detailsFromCtx(ctx),
			}, status)
		default:
			enc.EncodeResponseWithStatus(ctx, res, out, status)
		}
	}
}

// isEmptyType reports whether T is the Empty sentinel.
func isEmptyType[T any]() bool {
	var t T
	_, ok := any(t).(Empty)

	return ok
}

// bodylessResponse reports whether a route returning Out writes a status and no
// body — Empty, or a Result wrapping one. It is a property of the type, so it is
// settled once at registration rather than per response.
func bodylessResponse[Out any]() bool {
	if isEmptyType[Out]() {
		return true
	}

	var zero Out
	if result, ok := any(zero).(anyResult); ok {
		return result.responseIsEmpty()
	}

	return false
}

// responseStructure returns the value whose type is reflected into the operation's
// success response body: nil for Empty (no body), APIResponse[Out] when enveloped,
// else Out.
//
// A Result reflects as what it wraps. The wrapper is a way to carry a status out
// of a handler, not something a client ever sees, and documenting it would put a
// "Value"/"Status" object in the spec that no response will ever contain.
func responseStructure[Out any](envelope bool) any {
	var zero Out
	if result, ok := any(zero).(anyResult); ok {
		return result.responseStructure(envelope)
	}

	if isEmptyType[Out]() {
		return nil
	}

	if envelope {
		return new(httpx.APIResponse[Out])
	}

	return new(Out)
}

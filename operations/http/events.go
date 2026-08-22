package http

import (
	"context"
	nethttp "net/http"

	httpx "github.com/primandproper/platform-go/v13/errors/http"
	"github.com/primandproper/platform-go/v13/eventstream"
	"github.com/primandproper/platform-go/v13/eventstream/sse"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/operations"
	"github.com/primandproper/platform-go/v13/routing"

	"github.com/swaggest/openapi-go/openapi3"
)

// Event types the stream emits.
const (
	// EventOperation carries a snapshot of the operation. Every frame but the
	// last is one of these, and so is the last.
	EventOperation = "operation"

	// EventError carries a failure of the stream itself — not of the operation.
	// An operation that fails arrives as an EventOperation whose state says so.
	EventError = "error"
)

// watchOperationID is the OpenAPI operation ID of the event stream. It is named
// once and used twice — in the document and in the Route MountEvents returns —
// because those two are the same endpoint and a client generated from one is
// pointed at it by the other.
const watchOperationID = "watch_operation_events"

// MountEvents registers the server-sent-events endpoint, if there is a Watcher
// to serve it, and returns nil if there is not.
//
// Without one the route is not registered at all. A subscription endpoint with
// nothing behind it would accept the connection, hold it open, and say nothing
// forever — which a client cannot distinguish from an operation that is taking a
// long time, and which is therefore worse than a 404.
//
// The Route comes back hand-built rather than from routing.Get, because this
// endpoint goes on the Backend directly — see stream for why — and the untyped
// registration has no operation to return.
func (h *Handlers) MountEvents(r *routing.Router) *routing.Route {
	if h.watcher == nil {
		return nil
	}

	pattern := h.eventsPattern()

	r.Handle(nethttp.MethodGet, pattern, nethttp.HandlerFunc(h.stream))

	h.describeStream(r)

	return &routing.Route{
		Method:      nethttp.MethodGet,
		Path:        pattern,
		OperationID: watchOperationID,
	}
}

// stream upgrades the connection and pushes a snapshot for every change until
// the operation finishes or the client goes away.
//
// It is registered on the Backend rather than through routing's typed
// registration because a typed handler is func(ctx, In) (Out, error) — it
// returns a value and the framework encodes it once, where this holds the
// response writer and writes a frame at a time. See the package documentation.
func (h *Handlers) stream(res nethttp.ResponseWriter, req *nethttp.Request) {
	ctx, span := h.o11y.Begin(req.Context())
	defer span.End()

	id := operationIDFromPath(req.URL.Path)

	span.Set(operationIDKey, id)

	// The same ownership check the polling endpoint makes, and made before the
	// upgrade so that a refusal is an ordinary HTTP status rather than a stream
	// that opens and immediately closes.
	if _, err := h.read(ctx, id); err != nil {
		status, body := httpx.ToAPIResponse(err)
		h.writeRefusal(ctx, res, span, status, body)

		return
	}

	// Subscribed before the upgrade, for the same reason: a subscription refused
	// for capacity should be a 429 the client can back off from, not a stream
	// that says nothing.
	snapshots, err := h.watcher.Watch(ctx, id)
	if err != nil {
		status, body := httpx.ToAPIResponse(err)
		h.writeRefusal(ctx, res, span, status, body)

		return
	}

	stream, err := sse.NewUpgrader().UpgradeToEventStream(res, req)
	if err != nil {
		span.Acknowledge(err, "upgrading operation event stream")

		return
	}

	defer func() {
		if closeErr := stream.Close(); closeErr != nil {
			span.Acknowledge(closeErr, "closing operation event stream")
		}
	}()

	h.pump(ctx, span, stream, snapshots)
}

// pump forwards snapshots to the client until the channel closes.
//
// The channel closes on the operation's terminal snapshot, so the loop ends by
// itself on the ordinary path and there is no "am I finished" check anywhere in
// it. A client that disconnects cancels the request context, which retires the
// subscription, which closes the channel — the same exit.
func (h *Handlers) pump(
	ctx context.Context,
	span observability.Operation,
	stream eventstream.EventStream,
	snapshots <-chan *operations.Operation,
) {
	sent := 0

	defer func() { span.Set(streamedKey, sent) }()

	for {
		select {
		case <-ctx.Done():
			return

		case <-stream.Done():
			return

		case op, ok := <-snapshots:
			if !ok {
				return
			}

			payload, err := h.codec.Marshal(ctx, op)
			if err != nil {
				// Reported to the client rather than only logged. A stream that
				// simply stopped would look to a client exactly like an
				// operation that is taking a while.
				h.sendError(ctx, span, stream, "could not encode the operation")

				return
			}

			if err = stream.Send(ctx, &eventstream.Event{Type: EventOperation, Payload: payload}); err != nil {
				// The client has gone, or the connection broke. Neither is this
				// process's fault and neither is worth an error-level line: the
				// operation carries on regardless, which is the whole point of
				// it being durable.
				span.Acknowledge(err, "sending operation snapshot")

				return
			}

			sent++
		}
	}
}

// sendError emits a stream-level failure frame. It is best-effort: the
// connection that could not carry a snapshot may not carry this either.
func (h *Handlers) sendError(
	ctx context.Context,
	span observability.Operation,
	stream eventstream.EventStream,
	message string,
) {
	payload, err := h.codec.Marshal(ctx, map[string]string{"message": message})
	if err != nil {
		return
	}

	if err = stream.Send(ctx, &eventstream.Event{Type: EventError, Payload: payload}); err != nil {
		span.Acknowledge(err, "sending operation stream error")
	}
}

// writeRefusal writes a pre-upgrade response — the refusals that happen before
// the connection becomes a stream, and the only responses from this endpoint
// that are ordinary HTTP rather than frames.
//
// The content type is read off the codec rather than written out, so the header
// cannot claim one encoding while the body is in another.
func (h *Handlers) writeRefusal(
	ctx context.Context,
	res nethttp.ResponseWriter,
	span observability.Operation,
	status int,
	body any,
) {
	encoded, err := h.codec.Marshal(ctx, body)
	if err != nil {
		nethttp.Error(res, "could not encode the response", nethttp.StatusInternalServerError)

		return
	}

	res.Header().Set("Content-Type", h.codec.ContentType())
	res.WriteHeader(status)

	// The body is the platform's own error envelope, encoded by the platform's
	// own codec and served under the content type that codec names. There is no
	// HTML context for it to escape into.
	//nolint:gosec // G705: see above.
	if _, err = res.Write(encoded); err != nil {
		span.Acknowledge(err, "writing operation stream refusal")
	}
}

// describeStream writes the event-stream operation into the OpenAPI document by
// hand.
//
// Routing's reflector builds an operation from a typed handler's input and
// output, and this endpoint has neither in the shape it understands. Left out
// entirely, the one endpoint a client most needs to be told about — because
// text/event-stream is not something a generated client guesses — would be the
// one endpoint the document does not mention. So it is described here: the path,
// the parameter, and a 200 whose content type says what actually arrives.
func (h *Handlers) describeStream(r *routing.Router) {
	spec := r.Spec()
	if spec == nil {
		return
	}

	description := "Subscribes to an operation over server-sent events. Each `operation` event " +
		"carries a complete snapshot of the operation as JSON, not a delta, so a client that " +
		"misses one loses nothing. The stream ends after the snapshot in which `done` is true."

	op := openapi3.Operation{
		Tags:        h.tags,
		ID:          new(watchOperationID),
		Summary:     new("Watch a long-running operation"),
		Description: &description,
	}

	op.Parameters = []openapi3.ParameterOrRef{{
		Parameter: &openapi3.Parameter{
			Name:     pathParam,
			In:       openapi3.ParameterInPath,
			Required: new(true),
			Schema: &openapi3.SchemaOrRef{
				Schema: &openapi3.Schema{Type: new(openapi3.SchemaTypeString)},
			},
		},
	}}

	op.Responses.MapOfResponseOrRefValues = map[string]openapi3.ResponseOrRef{
		"200": {
			Response: &openapi3.Response{
				Description: "A stream of operation snapshots.",
				Content: map[string]openapi3.MediaType{
					"text/event-stream": {},
				},
			},
		},
	}

	// The reflector's own error accumulator is not reachable from here, so a
	// failure to add this is logged rather than swallowed: the endpoint still
	// works, and the document is missing one entry, which is worth knowing about
	// but is not worth refusing to serve over.
	if err := spec.AddOperation(nethttp.MethodGet, h.eventsPattern(), op); err != nil {
		h.o11y.Logger().Error("describing the operation event stream in the OpenAPI document", err)
	}
}

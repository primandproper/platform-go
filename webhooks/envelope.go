package webhooks

import (
	"bytes"
	"encoding/json"
	"slices"

	"github.com/primandproper/platform-go/v15/searchsync"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// Envelope is what Emit enqueues for the broker: the event's identity around
// the body the subscriber receives bare. A queue consumer decodes this, routes
// on EventType, and decodes Payload into the type that event names — or calls
// Decode, which does all three.
//
// It exists because the broker has nowhere else to put the name. An outbox
// message is a topic, a key and a body, and the relay publishes the body
// verbatim; a payload type that serves several events (identity's UserEvent is
// the body of every user event) would otherwise arrive with nothing saying
// which one it is. Every emitted event is wrapped, including a payload that
// already carries a type of its own: one shape per topic, never two.
//
// A subscriber never sees it. A Delivery's body is Payload, byte for byte, and
// the event's name and ID travel in the delivery's headers instead.
type Envelope struct {
	// EventType is the event's name, the one Emit was handed.
	EventType EventType `json:"eventType"`
	// ID is the event's identifier, and the ID of the delivery the fan-out
	// produced when it produced one, so a consumer and a subscriber that both
	// saw an event can say so in one word.
	ID string `json:"id"`
	// Scope is the owner identifier of the scope the event was emitted in, so a
	// handler can act in the right tenancy without a second lookup. Empty, and
	// omitted, for the global scope.
	Scope string `json:"scope,omitempty"`
	// Payload is the event body exactly as the subscriber receives it.
	Payload json.RawMessage `json:"payload"`
}

// outboundEnvelope is the value Emit hands the outbox writer. It renders as the
// Envelope it points at, and it exists separately from it so that the writer's
// side effects can read an emitted event by type: an effect asserts its message
// to an interface, and an Envelope asserts to nothing an application declared.
//
// It answers searchsync.Change for every event, whatever the payload, because
// the two facts a Change states are split between the envelope and the payload
// and neither can state both alone. A payload type routinely serves several
// events — identity's UserEvent is the body of every user event — so it cannot
// say which one it is, and the envelope is the one thing that knows. The
// payload is what knows its IDs. Answering for every event costs a side effect
// nothing it did not already pay: a rule is matched on the event type, so an
// event no rule names derives nothing, exactly as a payload that was not a
// Change derived nothing before.
type outboundEnvelope struct {
	*Envelope

	// payload is the value Emit was handed, kept so that its own answers take
	// precedence over the envelope's.
	payload any
}

var _ searchsync.Change = outboundEnvelope{}

// newOutboundEnvelope wraps envelope for the writer, around the payload value
// it was rendered from.
func newOutboundEnvelope(envelope *Envelope, payload any) outboundEnvelope {
	return outboundEnvelope{Envelope: envelope, payload: payload}
}

// MarshalJSON renders the envelope alone. Stated rather than left to field
// promotion, so the stored body is Envelope's shape by construction and not by
// the happenstance that nothing else on the wrapping types is exported.
func (e outboundEnvelope) MarshalJSON() ([]byte, error) {
	return json.Marshal(e.Envelope)
}

// IndexEventType is the envelope's EventType, unless the payload is a full
// searchsync.Change, whose own answer is kept: a payload that already names its
// index event type goes on naming it.
func (e outboundEnvelope) IndexEventType() string {
	if change, ok := e.payload.(searchsync.Change); ok {
		return change.IndexEventType()
	}

	return e.EventType.String()
}

// IndexDocumentID asks the payload when it is a searchsync.DocumentIDs, and
// otherwise reads key as a top-level field of the rendered payload.
//
// The field is read from the JSON because that is what the payload already is
// by now, and a JSON field name is what a rule's IDKey is written in. Only a
// JSON string is an ID, and it is returned verbatim; a field that is absent,
// null, a number or anything else reports false, as does a payload that is not
// a JSON object. A payload whose IDs are not top-level strings implements
// searchsync.DocumentIDs and says where they are.
func (e outboundEnvelope) IndexDocumentID(key string) (string, bool) {
	if ids, ok := e.payload.(searchsync.DocumentIDs); ok {
		return ids.IndexDocumentID(key)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(e.Payload, &fields); err != nil {
		return "", false
	}

	// Checked for a string before decoding, because a JSON null decodes into a
	// string without error and would read as an empty ID that was found.
	raw := bytes.TrimSpace(fields[key])
	if len(raw) == 0 || raw[0] != '"' {
		return "", false
	}

	var id string
	if err := json.Unmarshal(raw, &id); err != nil {
		return "", false
	}

	return id, true
}

// Decode reads an envelope off the broker and unmarshals its payload into v
// when the envelope's EventType is one of types; it reports false and leaves v
// untouched for any other event. No types matches nothing.
//
//	var event identity.UserEvent
//	eventType, ok, err := webhooks.Decode(msg, &event, identity.EventUserRegistered)
//
// The event type is returned whether or not it matched, so a handler that
// routes on several payload types can decode the envelope once per candidate
// without unmarshaling a payload into the wrong one. A body that is not an
// envelope — malformed, or naming no event — is an error rather than a miss,
// because a topic carrying something Emit did not write is a wiring mistake a
// consumer should hear about rather than skip.
func Decode[T any](raw []byte, v *T, types ...EventType) (eventType EventType, matched bool, err error) {
	if v == nil {
		return "", false, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "decoding a webhooks envelope into nil")
	}

	var envelope Envelope
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return "", false, platformerrors.Wrap(err, "decoding a webhooks envelope")
	}

	if envelope.EventType == "" {
		return "", false, platformerrors.Wrap(ErrEmptyEventType, "decoding a webhooks envelope")
	}

	if !slices.Contains(types, envelope.EventType) {
		return envelope.EventType, false, nil
	}

	// Into a fresh value first, so a payload that fails to decode leaves v as
	// the caller handed it rather than half-written.
	var decoded T
	if err = json.Unmarshal(envelope.Payload, &decoded); err != nil {
		return envelope.EventType, false, platformerrors.Wrapf(err, "decoding the payload of a %q event", envelope.EventType)
	}

	*v = decoded

	return envelope.EventType, true, nil
}

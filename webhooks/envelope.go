package webhooks

import (
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
// Envelope it points at, and it exists separately from it only so that the
// writer's side effects still see the payload: they read a message by type
// assertion, and an Envelope asserts to nothing an application declared.
type outboundEnvelope struct {
	*Envelope
}

// MarshalJSON renders the envelope alone. Stated rather than left to field
// promotion, so the stored body is Envelope's shape by construction and not by
// the happenstance that nothing else on the wrapping types is exported.
func (e outboundEnvelope) MarshalJSON() ([]byte, error) {
	return json.Marshal(e.Envelope)
}

// changeEnvelope is an outboundEnvelope whose payload is a searchsync.Change,
// and answers for it by delegation. That is what keeps an emitted data change
// deriving its index events on the writer, as it did when the writer was handed
// the payload itself.
//
// Its own type rather than a method set on outboundEnvelope that answers "no
// change" for every other payload, because a side effect asks the question by
// type assertion: an envelope that always asserted to Change would be telling
// every effect that every event is one.
type changeEnvelope struct {
	outboundEnvelope
	change searchsync.Change
}

var _ searchsync.Change = changeEnvelope{}

func (e changeEnvelope) IndexEventType() string { return e.change.IndexEventType() }

func (e changeEnvelope) IndexDocumentID(key string) (string, bool) {
	return e.change.IndexDocumentID(key)
}

// newOutboundEnvelope wraps envelope for the writer, delegating to payload
// where payload is a searchsync.Change.
func newOutboundEnvelope(envelope *Envelope, payload any) any {
	if change, ok := payload.(searchsync.Change); ok {
		return changeEnvelope{outboundEnvelope: outboundEnvelope{envelope}, change: change}
	}

	return outboundEnvelope{envelope}
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

package webhooks

import (
	"encoding/json"
	"testing"

	"github.com/primandproper/platform-go/v15/searchsync"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestDecode(T *testing.T) {
	T.Parallel()

	const raw = `{"eventType":"order.created","id":"event-1","scope":"acct_1","payload":{"orderID":"order-1"}}`

	T.Run("decodes a payload whose event type is listed", func(t *testing.T) {
		t.Parallel()

		var v orderChange

		eventType, matched, err := Decode([]byte(raw), &v, orderDeleted, orderCreated)
		must.NoError(t, err)
		test.True(t, matched)
		test.EqOp(t, orderCreated, eventType)
		test.EqOp(t, "order-1", v.OrderID)
	})

	// The event type is reported on a miss, so a handler can route on it
	// without unmarshaling the payload into the wrong type.
	T.Run("leaves v untouched for an event it was not asked for", func(t *testing.T) {
		t.Parallel()

		v := orderChange{OrderID: "untouched"}

		eventType, matched, err := Decode([]byte(raw), &v, orderDeleted)
		must.NoError(t, err)
		test.False(t, matched)
		test.EqOp(t, orderCreated, eventType)
		test.EqOp(t, "untouched", v.OrderID)
	})

	T.Run("no types matches nothing", func(t *testing.T) {
		t.Parallel()

		var v orderChange

		eventType, matched, err := Decode([]byte(raw), &v)
		must.NoError(t, err)
		test.False(t, matched)
		test.EqOp(t, orderCreated, eventType)
	})

	T.Run("refuses a body that is not JSON", func(t *testing.T) {
		t.Parallel()

		var v orderChange

		_, matched, err := Decode([]byte(`not json`), &v, orderCreated)
		test.Error(t, err)
		test.False(t, matched)
	})

	// A bare payload on the topic is something Emit did not write, and reads
	// as a wiring mistake rather than as an event nobody asked about.
	T.Run("refuses a body that names no event", func(t *testing.T) {
		t.Parallel()

		var v orderChange

		_, matched, err := Decode([]byte(`{"orderID":"order-1"}`), &v, orderCreated)
		test.ErrorIs(t, err, ErrEmptyEventType)
		test.False(t, matched)
	})

	T.Run("leaves v untouched when the payload does not decode", func(t *testing.T) {
		t.Parallel()

		v := orderChange{OrderID: "untouched"}

		eventType, matched, err := Decode([]byte(`{"eventType":"order.created","id":"event-1","payload":{"orderID":7}}`), &v, orderCreated)
		test.Error(t, err)
		test.False(t, matched)
		test.EqOp(t, orderCreated, eventType)
		test.EqOp(t, "untouched", v.OrderID)
	})

	T.Run("refuses a nil target", func(t *testing.T) {
		t.Parallel()

		_, _, err := Decode[orderChange]([]byte(raw), nil, orderCreated)
		test.ErrorIs(t, err, platformerrors.ErrNilInputParameter)
	})
}

// orderIDs is a payload that answers for its own IDs and not for its
// event, the shape of a body several events share.
type orderIDs struct {
	OrderID string `json:"orderID"`
}

var _ searchsync.DocumentIDs = (*orderIDs)(nil)

func (*orderIDs) IndexDocumentID(key string) (string, bool) {
	if key != "order" {
		return "", false
	}

	return "from-document", true
}

func TestOutboundEnvelope_Change(T *testing.T) {
	T.Parallel()

	// envelopeAround builds what Emit would hand the writer for payload, with
	// rendered standing in for the bytes the writer's encoding produced. The
	// two disagree on purpose in the delegation cases, so a case can tell which
	// of them answered.
	envelopeAround := func(eventType EventType, rendered string, payload any) searchsync.Change {
		return newOutboundEnvelope(&Envelope{
			EventType: eventType,
			ID:        "event-1",
			Payload:   json.RawMessage(rendered),
		}, payload)
	}

	T.Run("a full change answers both for itself", func(t *testing.T) {
		t.Parallel()

		change := envelopeAround(orderDeleted, `{"orderID":"from-json"}`, &orderChange{OrderID: "order-1"})

		test.EqOp(t, orderCreated.String(), change.IndexEventType())

		documentID, found := change.IndexDocumentID("orderID")
		test.True(t, found)
		test.EqOp(t, "order-1", documentID)
	})

	T.Run("a payload of document IDs answers for them, and the envelope names the event", func(t *testing.T) {
		t.Parallel()

		change := envelopeAround(orderDeleted, `{"orderID":"from-json","order":"from-json"}`, &orderIDs{OrderID: "order-1"})

		test.EqOp(t, orderDeleted.String(), change.IndexEventType())

		documentID, found := change.IndexDocumentID("order")
		test.True(t, found)
		test.EqOp(t, "from-document", documentID)
	})

	// A payload that said where its IDs are is not second-guessed: a key it
	// does not answer is a miss even where the JSON has a field by that name.
	T.Run("its miss is not retried against the JSON", func(t *testing.T) {
		t.Parallel()

		change := envelopeAround(orderCreated, `{"orderID":"from-json"}`, &orderIDs{OrderID: "order-1"})

		documentID, found := change.IndexDocumentID("orderID")
		test.False(t, found)
		test.EqOp(t, "", documentID)
	})

	T.Run("any other payload names the envelope's event and reads a top-level string field", func(t *testing.T) {
		t.Parallel()

		change := envelopeAround(orderCreated, `{"orderID":"order-1","nested":{"orderID":"order-2"}}`, map[string]any{})

		test.EqOp(t, orderCreated.String(), change.IndexEventType())

		documentID, found := change.IndexDocumentID("orderID")
		test.True(t, found)
		test.EqOp(t, "order-1", documentID)
	})

	T.Run("a string field is returned verbatim, the empty string included", func(t *testing.T) {
		t.Parallel()

		change := envelopeAround(orderCreated, `{"orderID":" Order 1é ","empty":""}`, nil)

		documentID, found := change.IndexDocumentID("orderID")
		test.True(t, found)
		test.EqOp(t, " Order 1é ", documentID)

		documentID, found = change.IndexDocumentID("empty")
		test.True(t, found)
		test.EqOp(t, "", documentID)
	})

	T.Run("anything but a string field is a miss", func(t *testing.T) {
		t.Parallel()

		change := envelopeAround(orderCreated, `{"null":null,"number":7,"bool":true,"object":{"id":"order-1"},"array":["order-1"]}`, nil)

		for _, key := range []string{"absent", "null", "number", "bool", "object", "array", "object.id", ""} {
			documentID, found := change.IndexDocumentID(key)
			test.False(t, found, test.Sprintf("key %q", key))
			test.EqOp(t, "", documentID, test.Sprintf("key %q", key))
		}
	})

	T.Run("a payload that is not a JSON object carries no fields", func(t *testing.T) {
		t.Parallel()

		for _, rendered := range []string{`"order-1"`, `["order-1"]`, `null`, `7`} {
			documentID, found := envelopeAround(orderCreated, rendered, nil).IndexDocumentID("orderID")
			test.False(t, found, test.Sprintf("payload %s", rendered))
			test.EqOp(t, "", documentID, test.Sprintf("payload %s", rendered))
		}
	})
}

package webhooks

import (
	"testing"

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

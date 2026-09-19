package searchsync

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v14/outbox"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// stubChange is a data-change payload of the shape the Change interface is
// written for: an event type and a bag of the identifiers that change named.
//
// It carries an EventType field beside an IndexEventType method deliberately —
// that is exactly the collision the prefixed method names exist to avoid, and a
// payload that could not have both would be a payload this package had renamed.
type stubChange struct {
	IDs       map[string]string
	EventType string
}

var _ Change = stubChange{}

func (c stubChange) IndexEventType() string { return c.EventType }

func (c stubChange) IndexDocumentID(key string) (string, bool) {
	id, ok := c.IDs[key]

	return id, ok
}

// orderRules is the table most of these cases are driven through.
func orderRules() []Rule {
	return []Rule{
		{EventType: "order_written", Topic: "orders-index", IDKey: "orderID", Op: OpUpsert},
		{EventType: "order_archived", Topic: "orders-index", IDKey: "orderID", Op: OpDelete},
	}
}

// derive runs a side effect over one message and returns what it derived.
func derive(t *testing.T, rules []Rule, payload any) []outbox.Message {
	t.Helper()

	effect, err := NewSideEffect(rules)
	must.NoError(t, err)

	derived, err := effect(context.Background(), nil, []outbox.Message{{Topic: "data-changes", Payload: payload}})
	must.NoError(t, err)

	return derived
}

func TestNewSideEffect(T *testing.T) {
	T.Parallel()

	T.Run("builds from a table", func(t *testing.T) {
		t.Parallel()

		effect, err := NewSideEffect(orderRules())
		must.NoError(t, err)
		must.NotNil(t, effect)
	})

	T.Run("refuses an empty table", func(t *testing.T) {
		t.Parallel()

		_, err := NewSideEffect(nil)
		test.ErrorIs(t, err, ErrNoRules)
		test.ErrorIs(t, err, platformerrors.ErrEmptyInputParameter)
	})

	T.Run("refuses a rule with no event type", func(t *testing.T) {
		t.Parallel()

		_, err := NewSideEffect([]Rule{{Topic: "orders-index", IDKey: "orderID", Op: OpUpsert}})
		test.ErrorIs(t, err, ErrInvalidRule)
		test.StrContains(t, err.Error(), "no event type")
	})

	T.Run("refuses a rule with no topic", func(t *testing.T) {
		t.Parallel()

		_, err := NewSideEffect([]Rule{{EventType: "order_written", IDKey: "orderID", Op: OpUpsert}})
		test.ErrorIs(t, err, ErrInvalidRule)
		test.StrContains(t, err.Error(), "no topic")
	})

	T.Run("refuses a rule with no ID key", func(t *testing.T) {
		t.Parallel()

		_, err := NewSideEffect([]Rule{{EventType: "order_written", Topic: "orders-index", Op: OpUpsert}})
		test.ErrorIs(t, err, ErrInvalidRule)
		test.StrContains(t, err.Error(), "no ID key")
	})

	T.Run("refuses a rule with an unknown op", func(t *testing.T) {
		t.Parallel()

		_, err := NewSideEffect([]Rule{{EventType: "order_written", Topic: "orders-index", IDKey: "orderID", Op: Op("reindex")}})
		test.ErrorIs(t, err, ErrInvalidRule)
		test.StrContains(t, err.Error(), `unknown op "reindex"`)
	})

	T.Run("refuses two rules that derive the same event", func(t *testing.T) {
		t.Parallel()

		_, err := NewSideEffect([]Rule{
			{EventType: "order_written", Topic: "orders-index", IDKey: "orderID", Op: OpUpsert},
			{EventType: "order_written", Topic: "orders-index", IDKey: "orderID", Op: OpDelete},
		})
		test.ErrorIs(t, err, ErrDuplicateRule)
	})

	T.Run("admits one event type into two indexes", func(t *testing.T) {
		t.Parallel()

		_, err := NewSideEffect([]Rule{
			{EventType: "order_written", Topic: "orders-index", IDKey: "orderID", Op: OpUpsert},
			{EventType: "order_written", Topic: "customers-index", IDKey: "customerID", Op: OpUpsert},
		})
		must.NoError(t, err)
	})
}

func TestSideEffect(T *testing.T) {
	T.Parallel()

	T.Run("derives an upsert for a matching change", func(t *testing.T) {
		t.Parallel()

		derived := derive(t, orderRules(), stubChange{
			EventType: "order_written",
			IDs:       map[string]string{"orderID": "order-1"},
		})

		must.SliceLen(t, 1, derived)
		test.EqOp(t, "orders-index", derived[0].Topic)
		test.EqOp(t, "order-1", derived[0].Key)

		event, ok := derived[0].Payload.(Event)
		must.True(t, ok)
		test.EqOp(t, OpUpsert, event.Op)
		test.EqOp(t, "order-1", event.DocumentID)
		test.False(t, event.OccurredAt.IsZero())
	})

	T.Run("derives a delete for the rule that names one", func(t *testing.T) {
		t.Parallel()

		derived := derive(t, orderRules(), stubChange{
			EventType: "order_archived",
			IDs:       map[string]string{"orderID": "order-1"},
		})

		must.SliceLen(t, 1, derived)

		event, ok := derived[0].Payload.(Event)
		must.True(t, ok)
		test.EqOp(t, OpDelete, event.Op)
	})

	T.Run("derives one event per matching rule", func(t *testing.T) {
		t.Parallel()

		derived := derive(t, []Rule{
			{EventType: "order_written", Topic: "orders-index", IDKey: "orderID", Op: OpUpsert},
			{EventType: "order_written", Topic: "customers-index", IDKey: "customerID", Op: OpUpsert},
		}, stubChange{
			EventType: "order_written",
			IDs:       map[string]string{"orderID": "order-1", "customerID": "customer-9"},
		})

		must.SliceLen(t, 2, derived)
		test.EqOp(t, "orders-index", derived[0].Topic)
		test.EqOp(t, "order-1", derived[0].Key)
		test.EqOp(t, "customers-index", derived[1].Topic)
		test.EqOp(t, "customer-9", derived[1].Key)
	})

	T.Run("derives nothing from an event type no rule names", func(t *testing.T) {
		t.Parallel()

		test.SliceEmpty(t, derive(t, orderRules(), stubChange{
			EventType: "order_viewed",
			IDs:       map[string]string{"orderID": "order-1"},
		}))
	})

	T.Run("passes over a payload that is not a Change", func(t *testing.T) {
		t.Parallel()

		test.SliceEmpty(t, derive(t, orderRules(), testDoc{Name: "not a change"}))
	})

	T.Run("derives from every message it is handed", func(t *testing.T) {
		t.Parallel()

		effect, err := NewSideEffect(orderRules())
		must.NoError(t, err)

		derived, err := effect(context.Background(), nil, []outbox.Message{
			{Topic: "data-changes", Payload: stubChange{EventType: "order_written", IDs: map[string]string{"orderID": "order-1"}}},
			{Topic: "data-changes", Payload: testDoc{Name: "unrelated"}},
			{Topic: "data-changes", Payload: stubChange{EventType: "order_archived", IDs: map[string]string{"orderID": "order-2"}}},
		})
		must.NoError(t, err)

		must.SliceLen(t, 2, derived)
		test.EqOp(t, "order-1", derived[0].Key)
		test.EqOp(t, "order-2", derived[1].Key)
	})

	T.Run("fails a change whose rule key it does not carry", func(t *testing.T) {
		t.Parallel()

		effect, err := NewSideEffect(orderRules())
		must.NoError(t, err)

		_, err = effect(context.Background(), nil, []outbox.Message{
			{Topic: "data-changes", Payload: stubChange{EventType: "order_written", IDs: map[string]string{"customerID": "customer-9"}}},
		})
		test.ErrorIs(t, err, ErrMissingDocumentID)
		test.StrContains(t, err.Error(), `carries no "orderID"`)
	})

	T.Run("fails a change whose ID is empty", func(t *testing.T) {
		t.Parallel()

		effect, err := NewSideEffect(orderRules())
		must.NoError(t, err)

		_, err = effect(context.Background(), nil, []outbox.Message{
			{Topic: "data-changes", Payload: stubChange{EventType: "order_written", IDs: map[string]string{"orderID": ""}}},
		})
		test.ErrorIs(t, err, ErrMissingDocumentID)
	})
}

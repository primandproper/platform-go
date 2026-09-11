package grpc_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/webhooks"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// dispatch fans one event out in scope and returns the delivery it wrote.
//
// It goes through webhooks.Dispatcher.Dispatch inside a transaction the test
// opens, which is the only way this module lets a delivery be written — and is
// exactly the reason Enqueue is not one of the nine RPCs.
func dispatch(tb testing.TB, h *harness, scope tenancy.Scope) *webhooks.Delivery {
	tb.Helper()

	delivery := &webhooks.Delivery{
		EventType: orderCreated,
		Payload:   json.RawMessage(`{"id":"order_1"}`),
	}

	must.NoError(tb, h.db.WithTransaction(tb.Context(), func(tx database.Tx) error {
		return h.dispatcher.Dispatch(tb.Context(), tx, scope, delivery)
	}))

	must.NotEq(tb, "", delivery.ID)

	return delivery
}

// record appends one line to a delivery's log, the way the worker does: on the
// store's own handle, with no executor at all.
func record(tb testing.TB, h *harness, deliveryID, endpointID string, statusCode, count int) {
	tb.Helper()

	must.NoError(tb, h.store.RecordAttempt(tb.Context(), &webhooks.Attempt{
		CreatedAt:    time.Now().UTC(),
		DeliveryID:   deliveryID,
		EndpointID:   endpointID,
		Duration:     150 * time.Millisecond,
		StatusCode:   statusCode,
		AttemptCount: count,
	}))
}

func TestListAttempts(T *testing.T) {
	T.Parallel()

	T.Run("pages the log of one of the caller's deliveries", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		endpoint := h.seed(t, testScope)
		delivery := dispatch(t, h, testScope)

		record(t, h, delivery.ID, endpoint.ID, 500, 1)
		record(t, h, delivery.ID, endpoint.ID, 200, 2)

		res, err := h.server.ListAttempts(h.ctx(t), &webhookspb.ListAttemptsRequest{
			DeliveryId: delivery.ID,
		})
		must.NoError(t, err)

		must.SliceLen(t, 2, res.GetResults())
		test.NotNil(t, res.GetPagination())

		// The two things the screen this RPC exists for actually shows: did it
		// get through, and what came back.
		for _, attempt := range res.GetResults() {
			test.EqOp(t, delivery.ID, attempt.GetDeliveryId())
			test.EqOp(t, endpoint.ID, attempt.GetEndpointId())
			test.EqOp(t, int64(150), attempt.GetDuration().AsDuration().Milliseconds())
			test.False(t, attempt.GetCreatedAt().AsTime().IsZero())
		}
	})

	// The quieter answer, and it is the store's rather than this handler's: an
	// attempts page is a page, and a delivery in another tenant's scope reads as
	// one with no attempts, which is what it is from here.
	T.Run("another tenant's delivery reads as one with no attempts", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)
		endpoint := h.seed(t, testScope)
		delivery := dispatch(t, h, testScope)

		record(t, h, delivery.ID, endpoint.ID, 200, 1)

		res, err := h.server.ListAttempts(h.otherCtx(t), &webhookspb.ListAttemptsRequest{
			DeliveryId: delivery.ID,
		})
		must.NoError(t, err)

		test.SliceEmpty(t, res.GetResults())
	})

	T.Run("refuses a caller with no principal", func(t *testing.T) {
		t.Parallel()

		h := newHarness(t)

		_, err := h.server.ListAttempts(t.Context(), &webhookspb.ListAttemptsRequest{})
		must.Error(t, err)

		test.EqOp(t, codes.Unauthenticated, status.Code(err))
	})
}

// TestAFailedAttemptCarriesItsError is the other half of what the delivery log
// is for, and the reason it is behind a grant of its own: it carries the
// subscriber's own words.
func TestAFailedAttemptCarriesItsError(T *testing.T) {
	T.Parallel()

	h := newHarness(T)
	endpoint := h.seed(T, testScope)
	delivery := dispatch(T, h, testScope)

	must.NoError(T, h.store.RecordAttempt(T.Context(), &webhooks.Attempt{
		CreatedAt:    time.Now().UTC(),
		DeliveryID:   delivery.ID,
		EndpointID:   endpoint.ID,
		Error:        "dial tcp: connection refused",
		AttemptCount: 1,
	}))

	res, err := h.server.ListAttempts(h.ctx(T), &webhookspb.ListAttemptsRequest{
		DeliveryId: delivery.ID,
	})
	must.NoError(T, err)
	must.SliceLen(T, 1, res.GetResults())

	test.EqOp(T, "dial tcp: connection refused", res.GetResults()[0].GetError())
	test.EqOp(T, int32(0), res.GetResults()[0].GetStatusCode())
}

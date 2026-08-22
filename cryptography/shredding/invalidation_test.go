package shredding

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/primandproper/platform-go/v13/messagequeue"
	messagequeuemock "github.com/primandproper/platform-go/v13/messagequeue/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewQueueBroadcaster(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil publisher", func(t *testing.T) {
		t.Parallel()

		broadcaster, err := NewQueueBroadcaster(nil)
		test.Nil(t, broadcaster)
		test.ErrorIs(t, err, ErrNilPublisher)
	})

	T.Run("publishes the subject", func(t *testing.T) {
		t.Parallel()

		var published any

		publisher := &messagequeuemock.PublisherMock{
			PublishFunc: func(_ context.Context, data any, _ ...messagequeue.PublishOption) error {
				published = data

				return nil
			},
		}

		broadcaster, err := NewQueueBroadcaster(publisher)
		must.NoError(t, err)

		must.NoError(t, broadcaster.Broadcast(t.Context(), testSubject))
		test.Eq(t, any(testSubject), published)
	})

	T.Run("reports a publish failure", func(t *testing.T) {
		t.Parallel()

		sentinel := errors.New("bus is down")
		publisher := &messagequeuemock.PublisherMock{
			PublishFunc: func(context.Context, any, ...messagequeue.PublishOption) error { return sentinel },
		}

		broadcaster, err := NewQueueBroadcaster(publisher)
		must.NoError(t, err)

		test.ErrorIs(t, broadcaster.Broadcast(t.Context(), testSubject), sentinel)
	})

	T.Run("refuses a subject with no ID", func(t *testing.T) {
		t.Parallel()

		publisher := &messagequeuemock.PublisherMock{
			PublishFunc: func(context.Context, any, ...messagequeue.PublishOption) error { return nil },
		}

		broadcaster, err := NewQueueBroadcaster(publisher)
		must.NoError(t, err)

		test.ErrorIs(t, broadcaster.Broadcast(t.Context(), Subject{Type: "user"}), ErrEmptySubjectID)
	})
}

func TestNewInvalidationHandler(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil invalidator", func(t *testing.T) {
		t.Parallel()

		// A handler with nothing to invalidate acknowledges every announcement
		// and acts on none of them, which is indistinguishable from a working
		// subscriber right up until somebody audits an erasure.
		handler, err := NewInvalidationHandler(nil)
		test.Nil(t, handler)
		test.ErrorIs(t, err, ErrNilInvalidator)
	})

	T.Run("round-trips what a Broadcaster publishes", func(t *testing.T) {
		t.Parallel()

		var published any

		publisher := &messagequeuemock.PublisherMock{
			PublishFunc: func(_ context.Context, data any, _ ...messagequeue.PublishOption) error {
				published = data

				return nil
			},
		}

		broadcaster, err := NewQueueBroadcaster(publisher)
		must.NoError(t, err)
		must.NoError(t, broadcaster.Broadcast(t.Context(), testSubject))

		// The two halves have to agree about the wire shape or a shred is
		// announced to a fleet that silently cannot read it.
		encoded, err := json.Marshal(published)
		must.NoError(t, err)

		invalidator := &recordingInvalidator{}
		meter := newCountingMeter()

		handler, err := NewInvalidationHandler(invalidator, WithInvalidationMetricsProvider(meter))
		must.NoError(t, err)
		must.NoError(t, handler(t.Context(), encoded))

		test.SliceLen(t, 1, invalidator.seen())
		test.EqOp(t, testSubject, invalidator.seen()[0])

		// The pair the publisher's own count is read against: a topic wired at
		// one end only shows up as one of these staying at zero.
		test.EqOp(t, int64(1), meter.count(serviceName+"_invalidations_received"))
		test.EqOp(t, int64(0), meter.count(serviceName+"_invalidations_rejected"))
	})

	T.Run("rejects a message that is not a subject", func(t *testing.T) {
		t.Parallel()

		invalidator := &recordingInvalidator{}
		meter := newCountingMeter()

		handler, err := NewInvalidationHandler(invalidator, WithInvalidationMetricsProvider(meter))
		must.NoError(t, err)

		test.Error(t, handler(t.Context(), []byte("{")))
		test.SliceEmpty(t, invalidator.seen())

		// Counted rather than only returned. A rolling deploy that changes the
		// wire format leaves both halves running and every shred completing on
		// the TTL, and this is the number that says so.
		test.EqOp(t, int64(1), meter.count(serviceName+"_invalidations_received"))
		test.EqOp(t, int64(1), meter.count(serviceName+"_invalidations_rejected"))
	})

	T.Run("rejects a subject with no ID", func(t *testing.T) {
		t.Parallel()

		invalidator := &recordingInvalidator{}
		meter := newCountingMeter()

		handler, err := NewInvalidationHandler(invalidator, WithInvalidationMetricsProvider(meter))
		must.NoError(t, err)

		test.ErrorIs(t, handler(t.Context(), []byte(`{"type":"user"}`)), ErrEmptySubjectID)
		test.SliceEmpty(t, invalidator.seen())
		test.EqOp(t, int64(1), meter.count(serviceName+"_invalidations_rejected"))
	})

	T.Run("ignores nil options", func(t *testing.T) {
		t.Parallel()

		handler, err := NewInvalidationHandler(&recordingInvalidator{}, nil)
		must.NoError(t, err)
		test.NotNil(t, handler)
	})
}

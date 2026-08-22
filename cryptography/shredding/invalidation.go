package shredding

import (
	"context"

	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/messagequeue"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// DefaultInvalidationTopic is the topic a shred is announced on when a
// deployment has no opinion about topic naming.
const DefaultInvalidationTopic = "shredding_invalidations"

var (
	// ErrNilPublisher indicates a nil messagequeue.Publisher.
	ErrNilPublisher = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil shredding invalidation publisher")

	// ErrNilInvalidator indicates a nil Invalidator. There is nothing sensible
	// to substitute: a handler with nothing to invalidate is a subscriber that
	// acknowledges every shred announcement and acts on none of them, which
	// looks exactly like a working one.
	ErrNilInvalidator = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil shredding invalidator")
)

var _ Broadcaster = (*QueueBroadcaster)(nil)

// QueueBroadcaster announces shreds over a message queue. It is exported, and
// returned by NewQueueBroadcaster, so a caller can depend on the broadcaster it
// built rather than on the Broadcaster seam.
type QueueBroadcaster struct {
	publisher messagequeue.Publisher
}

// NewQueueBroadcaster builds a Broadcaster over a messagequeue topic.
//
// What travels is a subject type and a subject ID, in the clear, on whatever bus
// the publisher is wired to. That is an identifier rather than any of the data
// the key protects — but it is still a statement that this person was erased,
// arriving on a topic every replica subscribes to, so a deployment whose bus is
// less trusted than its database should know it is being sent.
//
// Delivery semantics are the provider's. The redis provider is at-most-once, so
// a replica that was restarting simply misses the message and falls back to the
// TTL, which is the bound this is an improvement on rather than a replacement
// for. The at-least-once providers may deliver twice, which is harmless:
// dropping a key that is already gone is what Invalidate does anyway.
func NewQueueBroadcaster(publisher messagequeue.Publisher) (*QueueBroadcaster, error) {
	if publisher == nil {
		return nil, ErrNilPublisher
	}

	return &QueueBroadcaster{publisher: publisher}, nil
}

// Broadcast implements Broadcaster.
func (b *QueueBroadcaster) Broadcast(ctx context.Context, subject Subject) error {
	if err := subject.validate(); err != nil {
		return err
	}

	return b.publisher.Publish(ctx, subject)
}

// invalidationHandler is the subscribing half of the broadcast: it decodes a
// subject off the bus and hands it to an Invalidator.
type invalidationHandler struct {
	invalidator Invalidator
	o11y        observability.Observer
	codec       encoding.Unmarshaler

	receivedCounter metrics.Int64Counter
	rejectedCounter metrics.Int64Counter

	// What the options wrote, kept only until the observer is built from it.
	// Read h.o11y.Logger() for the logger this handler actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
}

// NewInvalidationHandler adapts an Invalidator to a messagequeue consumer, for
// the subscribing half of the same topic:
//
//	handler, err := shredding.NewInvalidationHandler(keys, shredding.WithInvalidationLogger(logger))
//	// ...
//	consumer, err := consumers.NewConsumer(ctx, shredding.DefaultInvalidationTopic, handler)
//
// A replica that does not run this consumer is not broken; its cached keys
// expire on the TTL like they would with no broadcaster at all. It is the
// deployment that runs the publisher and forgets the subscriber that gets the
// worst of both: keys broadcast to nobody, and erasure quietly completing on the
// TTL everywhere while the publisher's counter says otherwise.
//
// Which is why this end is instrumented rather than being the two lines of
// decoding it otherwise is. It counts every message that arrives
// (shredding_invalidations_received) and every one it could not turn into a
// subject (shredding_invalidations_rejected), and the Invalidator counts what it
// then did (shredding_invalidations_applied). Read against the publisher's
// shredding_invalidations_broadcast, those say whether the topic is wired at
// both ends, whether a rolling deploy has left the two halves disagreeing about
// the wire format, and whether the broadcast is reaching replicas before their
// TTL does the job anyway.
//
// Observability is optional and defaults to nothing, as everywhere else here. A
// deployment that names none of it gets a working handler and no way to tell
// that it is working.
func NewInvalidationHandler(invalidator Invalidator, opts ...InvalidationOption) (messagequeue.ConsumerFunc, error) {
	if invalidator == nil {
		return nil, ErrNilInvalidator
	}

	h := &invalidationHandler{invalidator: invalidator}
	for _, opt := range opts {
		if opt != nil {
			opt(h)
		}
	}

	h.o11y = observability.NewObserver(serviceName, h.logger, h.tracerProvider)

	// The publishing half of this topic hands the Subject to a
	// messagequeue.Publisher, and every provider's publisher encodes with a JSON
	// encoding.ClientEncoder. Decoding through the same seam is what keeps the two
	// halves one round trip: a change to how this module renders a value on the
	// wire reaches the subscriber that has to read it.
	h.codec = encoding.NewClientEncoder(
		encoding.ContentTypeJSON,
		encoding.WithLogger(h.logger),
		encoding.WithTracerProvider(h.tracerProvider),
	)

	mp := metrics.EnsureMetricsProvider(h.metricsProvider)

	var err error

	if h.receivedCounter, err = mp.NewInt64Counter(serviceName + "_invalidations_received"); err != nil {
		return nil, platformerrors.Wrap(err, "creating shredding invalidations received counter")
	}

	// Separate from the errors the consumer already logs, because this one is a
	// statement about the topic rather than about a message: anything above zero
	// means something is publishing to it that this build cannot read, and every
	// one of those is a shred that will now complete on the TTL instead.
	if h.rejectedCounter, err = mp.NewInt64Counter(serviceName + "_invalidations_rejected"); err != nil {
		return nil, platformerrors.Wrap(err, "creating shredding invalidations rejected counter")
	}

	return h.handle, nil
}

// handle is the ConsumerFunc.
func (h *invalidationHandler) handle(ctx context.Context, data []byte) error {
	ctx, op := h.o11y.BeginCustom(ctx, "handle_shredding_invalidation")
	defer op.End()

	h.receivedCounter.Add(ctx, 1)

	var subject Subject
	if err := h.codec.Unmarshal(ctx, data, &subject); err != nil {
		h.rejectedCounter.Add(ctx, 1)

		return op.Error(err, "decoding shredding invalidation")
	}

	if err := subject.validate(); err != nil {
		h.rejectedCounter.Add(ctx, 1)

		return op.Error(err, "decoding shredding invalidation")
	}

	op.Set(subjectIDKey, subject.ID).Set(subjectTypeKey, subject.Type)

	h.invalidator.Invalidate(ctx, subject)

	return nil
}

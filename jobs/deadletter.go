package jobs

import (
	"context"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/messagequeue"
)

// DeadLetter is the envelope a Pool hands to its DeadLetterFunc once a message
// has exhausted its attempts. It carries why the message died as well as the
// message itself: a bare payload on a dead-letter topic tells an operator that
// something failed but not what, and the failure is the only reason the record
// exists.
type DeadLetter struct {
	// FailedAt is when the final attempt returned, per the Pool's clock.
	FailedAt time.Time `json:"failedAt"`
	// Topic is the topic the message was consumed from, not the dead-letter
	// topic it is being written to.
	Topic string `json:"topic"`
	// Error is the last attempt's error, rendered. It is a string rather than
	// an error because this envelope is serialized onto a queue and read by a
	// human.
	Error string `json:"error"`
	// Payload is the message exactly as it was consumed. It is []byte rather
	// than json.RawMessage so that a payload which is not valid JSON — or is
	// empty — cannot render the whole envelope unparseable; the cost is that
	// JSON encodes it as base64, so replaying means decoding this field rather
	// than reading it off the topic.
	Payload []byte `json:"payload"`
	// Attempts is how many times the handler ran before the Pool gave up.
	Attempts uint `json:"attempts"`
}

// DeadLetterFunc disposes of a message the Pool will not retry again. It is the
// terminal step: whatever it does with the envelope, the Pool does not see the
// message again.
//
// Returning an error is reported and counted (jobs_pool_dead_letter_failures)
// but does not resurrect the message — there is nowhere left to put it. Alert
// on that counter, since every increment is a lost message.
type DeadLetterFunc func(ctx context.Context, msg DeadLetter) error

// NewTopicDeadLetter publishes exhausted messages to a queue topic, which is
// the usual destination: the same broker the Pool consumes from, a topic nobody
// consumes, and a human with a queue browser.
//
// The envelope is published as a whole, so the dead-letter topic does not carry
// the same wire shape as the source topic. A replay tool reads DeadLetter,
// base64-decodes Payload, and publishes those bytes back to Topic.
func NewTopicDeadLetter(ctx context.Context, provider messagequeue.PublisherProvider, topic string) (DeadLetterFunc, error) {
	if provider == nil {
		return nil, ErrNilPublisherProvider
	}

	if topic == "" {
		return nil, messagequeue.ErrEmptyTopicName
	}

	publisher, err := provider.NewPublisher(ctx, topic)
	if err != nil {
		return nil, platformerrors.Wrapf(err, "building dead-letter publisher for topic %q", topic)
	}

	return func(ctx context.Context, msg DeadLetter) error {
		return publisher.Publish(ctx, msg)
	}, nil
}

package outbox

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/ddl"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// tableFor renders the outbox table name under a namespace. It is the one
// place the component segment is spelled, so the Writer, the Relay, and the
// DDL cannot disagree about it.
func tableFor(prefix string) string {
	return ddl.Qualify(prefix) + "outbox_messages"
}

// DefaultTablePrefix is the namespace the outbox table carries when none is
// configured, which is none — rendering outbox_messages.
//
// The outbox_ segment is the schema's, not the caller's: a table always says
// which package created it. Setting a namespace of "ddb" renders
// ddb_outbox_messages, for a database shared between applications.
const DefaultTablePrefix = ""

// Message is one event awaiting publication.
type Message struct {
	// Payload is marshaled to JSON at enqueue and republished verbatim, so the
	// broker sees exactly what a direct Publish of this value would have sent.
	Payload any
	// Topic names the destination; the Relay resolves one Publisher per topic.
	Topic string
	// Key groups messages that must be published in order relative to one
	// another. Empty means unordered. At most one message per key is ever in
	// flight, so per-key order holds even with several relays running.
	Key string
}

// SideEffect derives further work from the messages a caller enqueued. It runs
// inside Enqueue, on the caller's executor, so rows it writes commit with the
// row change that prompted them and messages it returns are enqueued in the
// same statement as the caller's own. Returning an error aborts the enqueue,
// which leaves the caller's transaction to roll back with it.
//
// msgs holds what the caller passed to Enqueue and never what another side
// effect derived. Registration order therefore fixes the order the effects run
// in and the order their messages land in, and nothing else: an effect that
// could read another's output would make the registration list an evaluation
// order to reason about, and would let derived events derive events.
//
// The slice is this effect's own copy, so editing it changes neither what is
// written nor what the next effect sees.
type SideEffect func(ctx context.Context, q database.SQLQueryExecutor, msgs []Message) ([]Message, error)

// sideEffect is one registration: an effect and the name it is reported under
// on spans and in errors.
type sideEffect struct {
	effect SideEffect
	name   string
}

// Writer enqueues messages into the outbox table. It holds no database handle:
// every Enqueue takes the caller's executor, so one Writer serves every
// transaction in the process.
type Writer struct {
	clock clock.Clock
	o11y  observability.Observer

	// marshaler is pinned to JSON rather than configurable. The Relay hands
	// these bytes to the publisher inside a json.RawMessage, so any other
	// encoding would be spliced verbatim into a JSON message rather than
	// encoded into one. Held as the narrow encoding.Marshaler because bytes,
	// not a transport, are all this needs.
	marshaler encoding.Marshaler

	enqueuedCounter metrics.Int64Counter
	fanoutHist      metrics.Float64Histogram

	// What the options wrote, kept only until the observer is built from it.
	// Read w.o11y.Logger() for the logger this writer actually uses; this one may
	// be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	dialect         dialect.Dialect
	table           string

	// notifyChannel is empty unless WithWriterNotifyChannel was given one, and
	// an empty one emits nothing: an outbox that has not asked for wakeups runs
	// exactly the SQL it always did inside its callers' transactions.
	notifyChannel string

	// sideEffects are the derived writes registered at construction, run in
	// this order inside every Enqueue. Empty unless WithWriterSideEffect was
	// used, and an empty one costs an Enqueue nothing at all.
	sideEffects []sideEffect
}

// NewWriter builds a Writer for the given dialect.
func NewWriter(d dialect.Dialect, opts ...WriterOption) (*Writer, error) {
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "outbox dialect %q", d)
	}

	w := &Writer{
		dialect: d,
		table:   tableFor(DefaultTablePrefix),
		clock:   clock.NewClock(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(w)
		}
	}

	if !dialect.ValidIdentifier(w.table) {
		return nil, platformerrors.Wrapf(dialect.ErrInvalidIdentifier, "outbox table %q", w.table)
	}

	if w.notifyChannel != "" {
		// Refused rather than ignored. A channel configured against MySQL is a
		// deployment that believes it has millisecond wakeups and has silently
		// been running on the poll interval — which is exactly the
		// working-looking noop this module's constructors exist to prevent.
		if !w.dialect.SupportsNotify() {
			return nil, platformerrors.Wrapf(ErrNotifyUnsupported, "outbox dialect %q", w.dialect)
		}

		if !dialect.ValidIdentifier(w.notifyChannel) {
			return nil, platformerrors.Wrapf(dialect.ErrInvalidIdentifier, "outbox notify channel %q", w.notifyChannel)
		}
	}

	// Registrations are refused rather than dropped. A side effect exists
	// because a call site cannot be relied on to remember the event; one
	// silently discarded at construction reproduces exactly the failure it was
	// added to prevent, a level up and with even less to see it by.
	seen := make(map[string]struct{}, len(w.sideEffects))
	for _, se := range w.sideEffects {
		if se.name == "" {
			return nil, ErrUnnamedSideEffect
		}

		if se.effect == nil {
			return nil, platformerrors.Wrapf(ErrNilSideEffect, "outbox side effect %q", se.name)
		}

		if _, ok := seen[se.name]; ok {
			return nil, platformerrors.Wrapf(ErrDuplicateSideEffect, "outbox side effect %q", se.name)
		}

		seen[se.name] = struct{}{}
	}

	w.o11y = observability.NewObserver(serviceName, w.logger, w.tracerProvider)
	w.marshaler = encoding.NewClientEncoder(encoding.ContentTypeJSON, encoding.WithLogger(w.o11y.Logger()), encoding.WithTracerProvider(w.tracerProvider))

	mp := metrics.EnsureMetricsProvider(w.metricsProvider)

	var err error
	if w.enqueuedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_messages_enqueued", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating messages enqueued counter")
	}

	if w.fanoutHist, err = mp.NewFloat64Histogram(fmt.Sprintf("%s_enqueue_fanout", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating enqueue fanout histogram")
	}

	return w, nil
}

// Enqueue writes messages into the outbox using the caller's executor, so they
// commit or roll back with whatever else that transaction did. Passing several
// messages costs one round trip.
//
// Enqueue is deliberately not variadic-only sugar over a loop: a transaction
// that emits three events should not pay three round trips inside a lock.
//
// Registered side effects run first, on the same executor, and whatever they
// return is written by the same statement as the caller's messages — so an
// enqueue that owes a derived event still costs one round trip. An Enqueue
// with no messages runs none of them: a side effect derives from what the
// caller asked for, and a caller that asked for nothing changed nothing.
func (w *Writer) Enqueue(ctx context.Context, q database.SQLQueryExecutor, msgs ...Message) error {
	ctx, op := w.o11y.Begin(ctx)
	defer op.End()

	if q == nil {
		return op.Error(ErrNilExecutor, "enqueuing outbox messages")
	}

	if len(msgs) == 0 {
		return nil
	}

	all, sideEffectErr := w.withSideEffects(ctx, op, q, msgs)
	if sideEffectErr != nil {
		return sideEffectErr
	}

	op.Set(messageCountKey, len(all))

	now := w.clock.Now().UTC()

	// Topics are recorded on the span so a transaction that fans out to several
	// destinations is legible from the trace alone, without joining against the
	// counter.
	topics := make([]string, 0, len(all))

	rows := make([]enqueueRow, 0, len(all))
	for i := range all {
		msg := all[i]

		if msg.Topic == "" {
			return op.Error(ErrEmptyTopic, "enqueuing outbox messages")
		}
		if msg.Payload == nil {
			return op.Error(platformerrors.Wrapf(ErrNilPayload, "topic %q", msg.Topic), "enqueuing outbox messages")
		}

		payload, err := w.marshaler.Marshal(ctx, msg.Payload)
		if err != nil {
			return op.Error(err, "marshaling outbox payload for topic %q", msg.Topic)
		}

		rows = append(rows, enqueueRow{
			id:        identifiers.New(),
			topic:     msg.Topic,
			key:       msg.Key,
			payload:   payload,
			createdAt: now,
		})
		topics = append(topics, msg.Topic)
	}

	op.Set(keys.TopicKey, topics)

	query, args := buildInsert(w.dialect, w.table, rows)

	if _, err := q.ExecContext(ctx, query, args...); err != nil {
		return op.Error(err, "inserting outbox messages")
	}

	// On the caller's executor, so the notification is transactional: Postgres
	// delivers it at commit, which means a woken relay cannot look for the rows
	// before they are visible. That exactness is why this rides the same
	// transaction rather than firing after it.
	//
	// The error is returned rather than swallowed. A failed statement has
	// already aborted the caller's transaction, so there is no "carry on
	// without the wakeup" branch to take.
	if w.notifyChannel != "" {
		op.Set(notifyChannelKey, w.notifyChannel)

		if _, err := q.ExecContext(ctx, dialect.PostgresNotifyStatement, w.notifyChannel); err != nil {
			return op.Error(err, "notifying outbox channel %q", w.notifyChannel)
		}
	}

	// Counted after the statement succeeds, but the transaction can still roll
	// back afterwards — so this counts intent to publish, not committed rows.
	// The gap is exactly the rollback rate, and comparing this against
	// outbox_messages_published is how you see it.
	//
	// The fan-out is the same number the span carries, recorded once per
	// distinct topic in the enqueue rather than once per message: what it
	// answers is whether a write of this kind still owes what it used to, so a
	// call site that stopped emitting its index event shows up as the
	// data-change topic's distribution shifting down by one. Per message the
	// sample count would scale with the fan-out being measured, and a topic
	// that vanished entirely would take its samples with it — which is the
	// quiet-period ambiguity a rate already has.
	fanout := float64(len(all))

	counted := make(map[string]struct{}, len(topics))
	for _, topic := range topics {
		w.enqueuedCounter.Add(ctx, 1, topicAttr(topic))

		if _, ok := counted[topic]; ok {
			continue
		}

		counted[topic] = struct{}{}

		w.fanoutHist.Record(ctx, fanout, topicAttr(topic))
	}

	return nil
}

// withSideEffects runs the registered side effects in order and returns the
// caller's messages followed by whatever they derived. With none registered the
// caller's own slice comes back untouched, so an outbox nobody has registered
// against allocates nothing and runs exactly the statements it always did.
func (w *Writer) withSideEffects(
	ctx context.Context,
	op observability.Operation,
	q database.SQLQueryExecutor,
	msgs []Message,
) ([]Message, error) {
	if len(w.sideEffects) == 0 {
		return msgs, nil
	}

	// Named on the span however the enqueue ends: the effect that failed is
	// one that ran, and a trace omitting it describes a different enqueue than
	// the one that happened.
	ran := make([]string, 0, len(w.sideEffects))
	defer func() { op.Set(sideEffectsKey, ran) }()

	// Cloned rather than appended to, because the variadic slice may be the
	// caller's own and growing it in place would write into their array.
	all := slices.Clone(msgs)

	for _, se := range w.sideEffects {
		ran = append(ran, se.name)

		derived, err := se.effect(ctx, q, slices.Clone(msgs))
		if err != nil {
			return nil, op.Error(err, "running outbox side effect %q", se.name)
		}

		all = append(all, derived...)
	}

	return all, nil
}

// enqueueRow is one row's worth of bound parameters.
type enqueueRow struct {
	createdAt time.Time
	id        string
	topic     string
	key       string
	payload   []byte
}

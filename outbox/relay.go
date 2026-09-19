package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/primandproper/platform-go/v14/outbox/internal/outboxdb"

	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/identifiers"
	"github.com/primandproper/primitives-go/v2/messagequeue"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/keys"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	retrycfg "github.com/primandproper/primitives-go/v2/retry/config"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Observability keys for this package's spans and log fields. Declared once so
// that a field set on a span and the same field logged alongside it cannot
// drift apart, and so the outbox. prefix is applied uniformly — an un-namespaced
// attribute name collides with every other component writing to the same trace.
//
// Keys that are not outbox-specific come from observability/keys instead;
// keys.TopicKey is the one in use here.
const (
	messageIDKey       = "outbox.message_id"
	messageCountKey    = "outbox.message_count"
	partitionKeyKey    = "outbox.partition_key"
	attemptsKey        = "outbox.attempts"
	selectedKey        = "outbox.selected"
	claimedKey         = "outbox.claimed"
	claimTokenKey      = "outbox.claim_token"
	retiredKey         = "outbox.retired"
	claimModeKey       = "outbox.claim_mode"
	batchSizeKey       = "outbox.batch_size"
	backlogDepthKey    = "outbox.backlog_depth"
	backlogAgeKey      = "outbox.backlog_age_seconds"
	retentionCutoffKey = "outbox.retention_cutoff"
	reapedKey          = "outbox.reaped"
	lastErrorKey       = "outbox.last_error"
	limitKey           = "outbox.limit"
	releasedKey        = "outbox.released"
	notifyChannelKey   = "outbox.notify_channel"
	sideEffectsKey     = "outbox.side_effects"
)

// claimedMessage is one row the relay has taken ownership of.
type claimedMessage struct {
	id    string
	topic string
	key   string
	// claimToken is the name the claim that produced this message stamped on
	// the row, carried through the publish so the write that reports the
	// outcome can present it again. Every message from one claim carries the
	// same one: a claim is the unit that took the rows, and it is the unit that
	// gets to say what became of them.
	claimToken string
	payload    []byte
	attempts   int
}

// QuarantinedMessage is one message the relay has given up on, as an operator
// reading the quarantine sees it.
//
// It is a separate type from Message rather than the same one in another state,
// because they answer different questions. A Message is something to publish
// and carries the payload that will be published; this is something to decide
// about, and what it carries is why the decision is needed — the error that
// abandoned it, the topic and key it was holding up, and how long it has been
// sitting there. Release takes the ids.
type QuarantinedMessage struct {
	// CreatedAt is when the transaction that emitted the event chose, which is
	// the age the backlog gauges would have been reporting until this message
	// left the claimable set.
	CreatedAt time.Time
	// QuarantinedAt is when the fleet gave up, and the instant
	// RelayConfig.QuarantineRetention is measured from — so the difference
	// between it and now is how much of the window is left.
	QuarantinedAt time.Time
	// ID is the handle Release takes.
	ID string
	// Topic is where the message was going. A quarantine that is all one topic
	// is a broken publisher rather than a broken message.
	Topic string
	// Key is the partition key, empty for an unordered message. A keyed message
	// that quarantines was holding up every later message for its key until it
	// did, which is the one thing here that was affecting other traffic.
	Key string
	// LastError is the truncated reason the final attempt failed. It is the
	// column this whole read exists for.
	LastError string
	// Attempts is how many claims the message consumed before it was abandoned,
	// which is the count Release deliberately does not reset.
	Attempts int
}

// Relay moves committed outbox rows onto the broker. It owns a goroutine
// started by Run and stopped by Close.
type Relay struct {
	client   database.Client
	provider messagequeue.PublisherProvider
	// dialect is read from client at construction rather than configured, so
	// the SQL this relay emits cannot disagree with the database it runs on.
	dialect dialect.Dialect
	clock   clock.Clock
	o11y    observability.Observer

	// q is the generated querier, built once the prefix is settled and the
	// dialect is known — the only two things the generated statements do not
	// already carry. What a cycle executes is what sqlc analyzed, with one
	// marker substitution; see outbox/internal/outboxdb.
	q outboxdb.Querier

	publishers map[string]messagequeue.Publisher

	// wakeup is nil unless WithRelayWakeup supplied one. A nil channel blocks
	// forever in a select, so the loop below needs no branch for its absence —
	// a relay without one behaves exactly as it did before the option existed.
	wakeup <-chan struct{}

	stop chan struct{}
	done chan struct{}

	publishedCounter   metrics.Int64Counter
	failedCounter      metrics.Int64Counter
	quarantinedCounter metrics.Int64Counter
	fencedCounter      metrics.Int64Counter
	reapedCounter      metrics.Int64Counter
	// quarantineReapedCounter is separate from reapedCounter because the two
	// count opposite things. A published row reaped is housekeeping; a
	// quarantined one reaped is an event discarded for good, and summed into
	// one number it would be invisible beside the millions of the other.
	quarantineReapedCounter metrics.Int64Counter
	claimErrCounter         metrics.Int64Counter
	backlogGauge            metrics.Int64Gauge
	backlogAgeGauge         metrics.Int64Gauge
	batchHist               metrics.Float64Histogram
	cycleHist               metrics.Float64Histogram
	publishHist             metrics.Float64Histogram

	// What the options wrote, kept only until the observer is built from it.
	// Read r.o11y.Logger() for the logger this relay actually uses; this one may
	// be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	cfg RelayConfig

	publishersMu sync.Mutex
	stopOnce     sync.Once

	// started records that Run was entered, so Close can tell a loop it must
	// wait for from one that was never started. Without it a process that
	// builds a relay, fails a later wiring step and closes what it has in
	// cleanup waits out its whole shutdown budget on a done channel nothing
	// will ever close.
	//
	// The narrow race it leaves is not one: Close closes stop before it reads
	// this, so a Run entered afterwards returns on its first pass.
	started atomic.Bool
}

// NewRelay builds a Relay. It does not start it; call Run.
//
// ctx is used to validate the config and is not retained — Run takes its own.
func NewRelay(ctx context.Context, cfg *RelayConfig, client database.Client, provider messagequeue.PublisherProvider, opts ...RelayOption) (*Relay, error) {
	if cfg == nil {
		return nil, platformerrors.New("nil outbox relay config provided")
	}
	if client == nil {
		return nil, ErrNilDatabaseClient
	}
	if provider == nil {
		return nil, ErrNilPublisherProvider
	}

	cfg.EnsureDefaults()

	d := client.Dialect()
	if err := cfg.resolveForDialect(d); err != nil {
		return nil, err
	}

	if !dialect.ValidIdentifier(cfg.table) {
		return nil, platformerrors.Wrapf(dialect.ErrInvalidIdentifier, "outbox table %q", cfg.table)
	}

	q, querierErr := querierFor(d, cfg.TablePrefix)
	if querierErr != nil {
		return nil, querierErr
	}

	r := &Relay{
		q:          q,
		cfg:        *cfg,
		dialect:    d,
		client:     client,
		provider:   provider,
		clock:      clock.NewClock(),
		publishers: map[string]messagequeue.Publisher{},
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}

	if err := r.cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating outbox relay config")
	}

	r.o11y = observability.NewObserver(serviceName, r.logger, r.tracerProvider)

	mp := metrics.EnsureMetricsProvider(r.metricsProvider)

	var err error
	if r.publishedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_messages_published", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating messages published counter")
	}
	if r.failedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_messages_failed", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating messages failed counter")
	}
	if r.quarantinedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_messages_quarantined", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating messages quarantined counter")
	}
	if r.fencedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_messages_fenced", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating messages fenced counter")
	}
	if r.reapedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_messages_reaped", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating messages reaped counter")
	}
	if r.quarantineReapedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_quarantined_messages_reaped", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating quarantined messages reaped counter")
	}
	if r.claimErrCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_claim_errors", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating claim error counter")
	}
	if r.backlogGauge, err = mp.NewInt64Gauge(fmt.Sprintf("%s_backlog_depth", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating backlog depth gauge")
	}
	if r.backlogAgeGauge, err = mp.NewInt64Gauge(fmt.Sprintf("%s_backlog_age_seconds", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating backlog age gauge")
	}
	if r.publishHist, err = mp.NewFloat64Histogram(fmt.Sprintf("%s_publish_latency_ms", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating publish latency histogram")
	}
	if r.batchHist, err = mp.NewFloat64Histogram(fmt.Sprintf("%s_claimed_batch_size", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating claimed batch size histogram")
	}
	if r.cycleHist, err = mp.NewFloat64Histogram(fmt.Sprintf("%s_cycle_latency_ms", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating cycle latency histogram")
	}

	return r, nil
}

// Run is the relay loop. Like eventcapture.Recorder.Run it takes no context:
// tied to a server context it would stop draining while requests were still
// committing outbox rows. The owner calls Close after the server has shut
// down.
//
// A wakeup supplied by WithRelayWakeup cycles the relay beside the poll ticker.
// The two are not alternatives: the ticker is the backstop that makes the
// wakeup safe to lose, which it is — the signal is at-most-once, and a
// reconnecting listener misses whatever arrived while it was away.
//
// Run returns only after Close.
func (r *Relay) Run() {
	defer close(r.done)

	r.started.Store(true)

	ctx := context.Background()

	pollTicker := r.clock.NewTicker(r.cfg.PollInterval)
	defer pollTicker.Stop()

	reapTicker := r.clock.NewTicker(r.cfg.ReapInterval)
	defer reapTicker.Stop()

	// lastCycle anchors the wake floor. It starts at the zero time so the first
	// wake — the catch-up a listener fires as soon as it connects — is served
	// immediately.
	var (
		lastCycle   time.Time
		wakePending bool
		wakeFloor   <-chan time.Time
	)

	// The floor is a ticker rather than a timer because clock.Clock offers no
	// timer, and giving it one would mean adding a method to an exported
	// interface. It exists only when a wakeup does, and it costs a timer tick
	// rather than a query: an idle relay with a wakeup issues strictly fewer
	// statements than one without, which is the point.
	if r.wakeup != nil {
		floorTicker := r.clock.NewTicker(r.cfg.MinWakeInterval)
		defer floorTicker.Stop()

		wakeFloor = floorTicker.Chan()
	}

	cycle := func() {
		lastCycle = r.clock.Now()
		r.cycle(ctx)
	}

	for {
		select {
		case <-r.stop:
			return
		case <-pollTicker.Chan():
			cycle()
		case <-r.wakeup:
			// A burst of enqueues is one notification per commit, and without
			// this a busy table would drive a claim transaction per commit —
			// more queries under load than polling, which is the opposite of
			// what a wakeup is for. Deferring instead of dropping is what keeps
			// the last enqueue of a burst from waiting out the poll interval.
			if r.clock.Since(lastCycle) < r.cfg.MinWakeInterval {
				wakePending = true

				continue
			}

			cycle()
		case <-wakeFloor:
			if wakePending {
				wakePending = false

				cycle()
			}
		case <-reapTicker.Chan():
			r.reap(ctx)
			// Sampled on the reap tick rather than every poll: it is an
			// aggregate over the unpublished rows, and at poll cadence it would
			// cost more than the work it reports on.
			r.sampleBacklog(ctx)
		}
	}
}

// Close stops the relay, waits for the in-flight cycle to finish, and releases
// the publishers. Safe to call more than once, and on a relay that was never
// started — there is no goroutine to wait for, so it returns as soon as it has
// released the publishers.
//
// There is no final cycle on the way out, for the reason saga.Worker.Close
// gives: a cycle claims a fresh batch of up to BatchSize messages and publishes
// them one round trip at a time, which is work the process has just been told
// it has no time left for, and every row it takes is leased away from the
// replica still running. Rows committed just before shutdown stay committed and
// the next cycle anywhere picks them up.
func (r *Relay) Close(ctx context.Context) error {
	_, op := r.o11y.Begin(ctx)
	defer op.End()

	r.stopOnce.Do(func() { close(r.stop) })

	if r.started.Load() {
		select {
		case <-r.done:
		case <-ctx.Done():
			return op.Error(ctx.Err(), "waiting for outbox relay to drain")
		}
	}

	r.publishersMu.Lock()
	defer r.publishersMu.Unlock()

	for _, p := range r.publishers {
		p.Stop()
	}
	r.publishers = map[string]messagequeue.Publisher{}

	return nil
}

// cycle claims one batch and publishes it. Errors are logged and counted
// rather than returned: there is no caller to hand them to, and the next cycle
// retries.
func (r *Relay) cycle(ctx context.Context) {
	msgs, err := r.claim(ctx)
	if err != nil {
		r.claimErrCounter.Add(ctx, 1)
		r.o11y.Logger().Error("claiming outbox messages", err)

		return
	}

	if len(msgs) == 0 {
		return
	}

	r.batchHist.Record(ctx, float64(len(msgs)))

	ctx, op := r.o11y.Begin(ctx, observability.WithValue(claimedKey, len(msgs)))
	defer op.End()

	defer op.Time(ctx, r.clock, r.cycleHist)()

	// Published serially, in created_at order. The claim predicate admits at
	// most one message per partition key per batch, so a failure here can never
	// strand a later message for the same key.
	published := make([]string, 0, len(msgs))
	for i := range msgs {
		if err = r.publish(ctx, &msgs[i]); err != nil {
			r.recordFailure(ctx, &msgs[i], err)

			continue
		}

		published = append(published, msgs[i].id)
		r.publishedCounter.Add(ctx, 1, topicAttr(msgs[i].topic))
	}

	if len(published) == 0 {
		return
	}

	// Every message in a batch came from one claim, so they all carry the same
	// name and the retirement presents it once.
	if err = r.markPublished(ctx, msgs[0].claimToken, published); err != nil {
		// The messages are on the broker but still look unpublished. The next
		// cycle republishes them — this is precisely the at-least-once window
		// the package documentation describes.
		op.Acknowledge(err, "marking outbox messages published")
	}
}

// publish sends one message to its topic. The payload is republished as
// json.RawMessage so the broker receives exactly the bytes a direct Publish of
// the original value would have produced.
//
// It carries its own span: the broker round trip is where a cycle spends its
// time, and a single span over the whole batch cannot say which topic is slow.
func (r *Relay) publish(ctx context.Context, msg *claimedMessage) error {
	ctx, op := r.o11y.Begin(ctx,
		observability.WithValue(keys.TopicKey, msg.topic),
		observability.WithValue(messageIDKey, msg.id),
		observability.WithValue(attemptsKey, msg.attempts),
	)
	defer op.End()

	if msg.key != "" {
		op.Set(partitionKeyKey, msg.key)
	}

	defer op.Time(ctx, r.clock, r.publishHist, topicAttr(msg.topic))()

	publisher, err := r.publisherFor(ctx, msg.topic)
	if err != nil {
		return op.Error(err, "resolving publisher")
	}

	if err = publisher.Publish(ctx, json.RawMessage(msg.payload)); err != nil {
		return op.Error(err, "publishing outbox message")
	}

	return nil
}

// publisherFor resolves and caches one Publisher per topic.
func (r *Relay) publisherFor(ctx context.Context, topic string) (messagequeue.Publisher, error) {
	r.publishersMu.Lock()
	defer r.publishersMu.Unlock()

	if p, ok := r.publishers[topic]; ok {
		return p, nil
	}

	p, err := r.provider.NewPublisher(ctx, topic)
	if err != nil {
		return nil, platformerrors.Wrapf(err, "building publisher for topic %q", topic)
	}

	r.publishers[topic] = p

	return p, nil
}

// claim selects a batch, takes what of it is still free, and reads back the
// rows it took — all in one transaction.
//
// The batch the select returns is a request rather than a holding. In the lease
// mode the select takes no lock, so two relays ordinarily read the same ids;
// what divides them is the guarded UPDATE, which leases a row only while it is
// still free and stamps this claim's name on the ones it wins. The read-back
// then asks for that name rather than for the ids, so a relay that lost some or
// all of its batch publishes only what it actually holds. See
// outbox/internal/queries.
//
// The name is minted here, once per claim, rather than carried on the Relay: it
// identifies the claim and not the relay, so a second cycle cannot read back a
// first cycle's rows. It goes onto the span as well as into the row, which is
// what lets an operator join a stuck lease to the cycle that took it.
func (r *Relay) claim(ctx context.Context) ([]claimedMessage, error) {
	claimToken := identifiers.New()

	ctx, op := r.o11y.Begin(ctx, observability.WithValues(map[string]any{
		claimModeKey:  string(r.cfg.ClaimMode),
		batchSizeKey:  r.cfg.BatchSize,
		claimTokenKey: claimToken,
		"db.system":   string(r.dialect),
	}))
	defer op.End()

	var claimed []claimedMessage

	err := r.client.WithTransaction(ctx, func(q database.Tx) error {
		now := r.clock.Now().UTC()

		ids, err := r.selectClaimable(ctx, q, now)
		if err != nil {
			return platformerrors.Wrap(err, "selecting claimable outbox messages")
		}

		op.Set(selectedKey, len(ids))

		if len(ids) == 0 {
			return nil
		}

		leaseUntil := now.Add(r.cfg.LeaseDuration)

		// The horizon this claim writes and the horizon it tests against are
		// the two ends of one lease, bound from the one clock read above: a
		// row is free when its lease has reached now, and held until now plus
		// the duration.
		if err = r.q.ClaimOutboxMessages(ctx, q, outboxdb.ClaimOutboxMessagesParams{
			ClaimedUntil:   &leaseUntil,
			ClaimedBy:      &claimToken,
			Now:            now,
			LeaseExpiredBy: &now,
			IDs:            ids,
		}); err != nil {
			return platformerrors.Wrap(err, "claiming outbox messages")
		}

		rows, err := r.q.FetchClaimedOutboxMessages(ctx, q, outboxdb.FetchClaimedOutboxMessagesParams{
			ClaimedBy: &claimToken,
			IDs:       ids,
		})
		if err != nil {
			return platformerrors.Wrap(err, "reading claimed outbox messages")
		}

		claimed = make([]claimedMessage, 0, len(rows))
		for i := range rows {
			claimed = append(claimed, claimedMessage{
				id:         rows[i].ID,
				topic:      rows[i].Topic,
				key:        rows[i].PartitionKey,
				claimToken: claimToken,
				payload:    rows[i].Payload,
				attempts:   int(rows[i].Attempts),
			})
		}

		return nil
	})
	if err != nil {
		return nil, op.Error(err, "claiming outbox batch")
	}

	op.Set(claimedKey, len(claimed))

	return claimed, nil
}

// markPublished retires the rows that made it to the broker, under the name the
// claim that took them stamped.
//
// A short count is the lease being overrun: this relay was slow, its lease
// lapsed, and another relay has since taken some of these rows and is
// publishing them itself. The rows it took stay unretired here and are retired
// by whoever holds them, so the fact is a duplicate publish rather than a lost
// one — the at-least-once window the package documentation describes, observed
// at the one statement that can see it. It is counted and said out loud rather
// than returned, because there is nothing the caller could do differently and
// the next cycle is already correct.
func (r *Relay) markPublished(ctx context.Context, claimToken string, ids []string) error {
	at := r.clock.Now().UTC()

	retired, err := r.q.MarkOutboxMessagesPublished(ctx, r.client.Writer(), outboxdb.MarkOutboxMessagesPublishedParams{
		PublishedAt: &at,
		HeldBy:      &claimToken,
		IDs:         ids,
	})
	if err != nil {
		return platformerrors.Wrap(err, "marking outbox messages published")
	}

	if fenced := int64(len(ids)) - retired; fenced > 0 {
		r.fencedCounter.Add(ctx, fenced)
		r.o11y.Logger().WithValues(map[string]any{
			claimTokenKey:   claimToken,
			messageCountKey: len(ids),
			retiredKey:      retired,
		}).Info("outbox lease lapsed mid-publish; another relay holds the rest")
	}

	return nil
}

// recordFailure releases the lease, schedules the retry, and quarantines the
// message once it has exhausted its attempts. A quarantined message is skipped
// by every future claim, so one permanently broken message cannot block the
// queue behind it.
func (r *Relay) recordFailure(ctx context.Context, msg *claimedMessage, cause error) {
	r.failedCounter.Add(ctx, 1, topicAttr(msg.topic))

	now := r.clock.Now().UTC()

	nextAttempt := now.Add(retrycfg.ScheduledDelayFor(r.cfg.Backoff, msg.attempts))

	// The stamp is the instant the fleet gave up, and it is what the quarantine
	// reap measures its window from. A message still inside its attempts leaves
	// the column alone, which is what binding nil does: the write assigns it on
	// every pass, so a row that is not being abandoned has to be told so.
	var quarantinedAt *time.Time
	if uint(msg.attempts) >= r.cfg.Backoff.MaxAttempts {
		quarantinedAt = &now
	}

	lastErr := truncateError(cause)

	// The partition key matters here more than anywhere else: a keyed message
	// that is failing is also holding up every later message for that key, so
	// the log has to say which key is stalled.
	logger := r.o11y.Logger().WithValues(map[string]any{
		messageIDKey:    msg.id,
		keys.TopicKey:   msg.topic,
		partitionKeyKey: msg.key,
		attemptsKey:     msg.attempts,
	})

	// The lease is released by binding it to nothing rather than by leaving it
	// out of the statement: a message whose publish failed must be reclaimable
	// before its lease would have lapsed on its own. The name on the lease goes
	// with it, so a free row never reads as one somebody is still holding.
	//
	// The same name is bound again as the guard, under HeldByArg. A relay whose
	// lease lapsed while it was slow must not schedule a retry for, release the
	// lease of, or quarantine a message a second relay has since taken and may
	// be about to publish successfully.
	affected, err := r.q.RecordOutboxMessageFailure(ctx, r.client.Writer(), outboxdb.RecordOutboxMessageFailureParams{
		ID:            msg.id,
		ClaimedUntil:  nil,
		ClaimedBy:     nil,
		HeldBy:        &msg.claimToken,
		NextAttempt:   nextAttempt,
		LastError:     &lastErr,
		QuarantinedAt: quarantinedAt,
	})
	if err != nil {
		// The lease still expires on its own, so the message is retried
		// regardless — just later than intended.
		logger.Error("recording outbox publish failure", err)

		return
	}

	if affected == 0 {
		// Somebody else holds this row. Their outcome is the one that counts,
		// and the failure this relay saw is theirs to rediscover if it is real.
		r.fencedCounter.Add(ctx, 1, topicAttr(msg.topic))
		logger.WithValue(claimTokenKey, msg.claimToken).
			Info("outbox lease lapsed before the failure could be recorded; another relay holds the message")

		return
	}

	if quarantinedAt != nil {
		r.quarantinedCounter.Add(ctx, 1, topicAttr(msg.topic))
		logger.Error("quarantining outbox message after exhausting attempts", cause)

		return
	}

	logger.WithValue("next_attempt", nextAttempt).Info("outbox publish failed, retry scheduled")
}

// sampleBacklog records how far behind the relay is. These two gauges are the
// package's primary health signal: every other instrument is a rate or a
// latency, and none of them can distinguish "publishing steadily" from
// "publishing steadily while falling further behind".
func (r *Relay) sampleBacklog(ctx context.Context) {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	depth, age, err := r.backlog(ctx)
	if err != nil {
		op.Acknowledge(err, "sampling outbox backlog")

		return
	}

	ageSeconds := int64(age.Seconds())

	r.backlogGauge.Record(ctx, depth)
	r.backlogAgeGauge.Record(ctx, ageSeconds)

	op.SetValues(map[string]any{
		backlogDepthKey: depth,
		backlogAgeKey:   ageSeconds,
	})
}

// backlog reads how many messages are waiting and how old the oldest is. Split
// out from sampleBacklog because this is the part with something to get wrong:
// the oldest instant comes back through three different drivers, and an empty
// queue comes back as no row at all.
//
// An empty backlog reports an age of zero rather than no age at all, so a
// drained queue actively resets the gauge instead of leaving a stale reading
// on the dashboard.
func (r *Relay) backlog(ctx context.Context) (depth int64, age time.Duration, err error) {
	row, err := r.q.OutboxBacklog(ctx, r.client.Reader())

	switch {
	case errors.Is(err, sql.ErrNoRows):
		// An empty queue is no row rather than a row of zeroes — see
		// outbox/internal/queries on why the statement is grouped — so the
		// driver's empty result is the answer here rather than a failure.
		return 0, 0, nil
	case err != nil:
		return 0, 0, platformerrors.Wrap(err, "reading outbox backlog")
	}

	if age = r.clock.Since(row.Oldest.UTC()); age < 0 {
		age = 0
	}

	return row.Depth, age, nil
}

// reap collects what has aged out, in two passes: the published rows past
// Retention, and the quarantined ones past QuarantineRetention.
//
// They are two passes rather than one because they are keeping different things
// for different reasons, and a single horizon would make the longer window
// govern both — see RelayConfig.QuarantineRetention. Each reports its own
// errors and neither stops the other; a quarantine the operator cannot reach is
// no reason to stop collecting delivered rows.
func (r *Relay) reap(ctx context.Context) {
	r.reapPublished(ctx)
	r.reapQuarantined(ctx)
}

// reapPublished deletes published rows past the retention window.
func (r *Relay) reapPublished(ctx context.Context) {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	before := r.clock.Now().UTC().Add(-r.cfg.Retention)

	op.Set(retentionCutoffKey, before)

	affected, err := r.q.ReapPublishedOutboxMessages(ctx, r.client.Writer(), outboxdb.ReapPublishedOutboxMessagesParams{
		Before:      &before,
		ResultLimit: int64(r.cfg.ReapBatchSize),
	})
	if err != nil {
		op.Acknowledge(err, "reaping published outbox messages")

		return
	}

	op.Set(reapedKey, affected)

	if affected > 0 {
		r.reapedCounter.Add(ctx, affected)
		op.Logger().Debug("reaped published outbox messages")
	}
}

// reapQuarantined deletes quarantined rows past the quarantine retention
// window, and says out loud what each of them was.
//
// It reads before it deletes, which the published reap does not, because the
// two passes are destroying different things. A published row that goes was
// delivered and the count is the whole story; a quarantined one that goes is an
// event nobody ever received, and the Warn line naming its id and its last
// error is the last record that it existed at all. There is nothing to read it
// back from afterwards, which is why the read comes first.
//
// Both statements run on the writer rather than the reader, for that same
// reason: a replica a few seconds behind would hand this pass a set of rows
// that is not the set the delete takes, and the row missing from the log is
// exactly the one the log was for.
//
// The log follows the delete, so no line claims an event was discarded that is
// in fact still there. What it does not claim is that this relay was the one
// that discarded it: two reapers can name the same batch, in which case the
// second's delete takes fewer rows than it read and the line appears twice.
// Saying a dropped event was dropped twice is the safe direction for a record
// that is the only one there will be.
func (r *Relay) reapQuarantined(ctx context.Context) {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	before := r.clock.Now().UTC().Add(-r.cfg.QuarantineRetention)
	limit := int64(r.cfg.ReapBatchSize)

	op.Set(retentionCutoffKey, before)

	doomed, err := r.q.SelectReapableQuarantinedOutboxMessages(ctx, r.client.Writer(), outboxdb.SelectReapableQuarantinedOutboxMessagesParams{
		Before:      &before,
		ResultLimit: limit,
	})
	if err != nil {
		op.Acknowledge(err, "reading reapable quarantined outbox messages")

		return
	}

	if len(doomed) == 0 {
		return
	}

	affected, err := r.q.ReapQuarantinedOutboxMessages(ctx, r.client.Writer(), outboxdb.ReapQuarantinedOutboxMessagesParams{
		Before:      &before,
		ResultLimit: limit,
	})
	if err != nil {
		op.Acknowledge(err, "reaping quarantined outbox messages")

		return
	}

	op.Set(reapedKey, affected)
	r.quarantineReapedCounter.Add(ctx, affected)

	for i := range doomed {
		r.o11y.Logger().WithValues(map[string]any{
			messageIDKey: doomed[i].ID,
			lastErrorKey: stringValue(doomed[i].LastError),
		}).Warn("reaped a quarantined outbox message; the event is permanently discarded")
	}
}

// Quarantined reads the messages the relay has given up on, the ones abandoned
// longest ago first, with the error that abandoned each of them.
//
// It is the surface the quarantine metric sends an operator to.
// outbox_messages_quarantined is an alarm on any increase, and an alarm whose
// answer is a SQL client is an alarm nobody can act on from where they were
// paged; this is what the increase was about, and Release is what to do with
// it. A limit of zero or less is DefaultQuarantineLimit.
//
// The payload is deliberately not among the fields — see the corpus's
// QuarantinedColumns. What replays a message is its id.
//
// It reads through the writer rather than a replica: the two calls an operator
// makes are this one and Release, and a list read from a lagging replica offers
// ids that the write then reports as no longer quarantined.
func (r *Relay) Quarantined(ctx context.Context, limit int) ([]QuarantinedMessage, error) {
	if limit <= 0 {
		limit = DefaultQuarantineLimit
	}

	ctx, op := r.o11y.Begin(ctx, observability.WithValue(limitKey, limit))
	defer op.End()

	rows, err := r.q.SelectQuarantinedOutboxMessages(ctx, r.client.Writer(), outboxdb.SelectQuarantinedOutboxMessagesParams{
		ResultLimit: int64(limit),
	})
	if err != nil {
		return nil, op.Error(err, "reading quarantined outbox messages")
	}

	quarantined := make([]QuarantinedMessage, 0, len(rows))
	for i := range rows {
		quarantined = append(quarantined, QuarantinedMessage{
			ID:            rows[i].ID,
			Topic:         rows[i].Topic,
			Key:           rows[i].PartitionKey,
			CreatedAt:     rows[i].CreatedAt.UTC(),
			QuarantinedAt: timeValue(rows[i].QuarantinedAt),
			Attempts:      int(rows[i].Attempts),
			LastError:     stringValue(rows[i].LastError),
		})
	}

	op.Set(messageCountKey, len(quarantined))

	return quarantined, nil
}

// Release returns quarantined messages to the claimable set, due immediately,
// and reports how many of the named ids were actually in the quarantine.
//
// It is the other half of Quarantined, and the half that does something: an
// operator who has fixed the broken topic, the missing subscription or the
// consumer that was rejecting a payload releases the messages that failed
// because of it, and the next cycle publishes them.
//
// A short count is an id that was not quarantined — already released, already
// published, or never in this table — rather than a failure. That is the one
// fact a later Quarantined cannot recover, because a message released and
// published in between is absent from both answers.
//
// The attempt count is left where it was, so a released message gets one more
// publish and returns to the quarantine if that one fails too. It is not a
// reset: the count is the record that this message has already exhausted a
// budget, and an operator who releases a message that is still broken should
// see it come straight back rather than watch it work through its attempts
// again.
func (r *Relay) Release(ctx context.Context, ids ...string) (int64, error) {
	ctx, op := r.o11y.Begin(ctx, observability.WithValue(messageCountKey, len(ids)))
	defer op.End()

	// No ids is no work rather than a statement: a set predicate over an empty
	// set is a marker list with nothing in it on the two dialects that expand
	// one, which is a syntax error rather than a write that matches nothing.
	if len(ids) == 0 {
		return 0, nil
	}

	released, err := r.q.ReleaseQuarantinedOutboxMessages(ctx, r.client.Writer(), outboxdb.ReleaseQuarantinedOutboxMessagesParams{
		NextAttempt: r.clock.Now().UTC(),
		IDs:         ids,
	})
	if err != nil {
		return 0, op.Error(err, "releasing quarantined outbox messages")
	}

	op.Set(releasedKey, released)

	return released, nil
}

// topicAttr labels a measurement with its topic. One Relay serves every topic,
// so without this the counters collapse into a single number and a topic whose
// publisher is broken is invisible beside the ones that are fine. Topics are
// low-cardinality by nature, which is what makes this safe as a metric
// dimension.
func topicAttr(topic string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String(keys.TopicKey, topic))
}

// maxStoredErrorLength bounds what goes into last_error, so a pathological
// driver error cannot bloat the row.
const maxStoredErrorLength = 1024

// truncateError renders a cause for the last_error column, bounded.
func truncateError(err error) string {
	return platformerrors.TruncateError(err, maxStoredErrorLength)
}

// stringValue reads a nullable text column. The columns it is used on are
// last_error, which is absent on a message that has never failed and present on
// every message the quarantine read returns — so the absence is the zero value
// rather than anything a caller has to branch on.
func stringValue(s *string) string {
	if s == nil {
		return ""
	}

	return *s
}

// timeValue reads a nullable instant into the UTC value this package reports.
//
// The one column it is used on is quarantined_at, which the read that projects
// it has already required to be NOT NULL — the pointer is the schema's
// nullability rather than a value that can be missing here, and the zero time
// is what a row that somehow had none would report.
func timeValue(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}

	return t.UTC()
}

// selectClaimable picks the batch of ids this cycle will lease, through
// whichever of the two claim statements the configured mode names.
//
// The lock clause is statement text rather than a bound value, so the mode is a
// choice between two generated methods — the way a paged read chooses between
// its two directions — rather than a clause appended to one. See
// outbox/internal/queries.
//
// The two comparisons are one instant and two arguments: next_attempt is NOT
// NULL and claimed_until is not, and no analyzer gives one argument two
// nullabilities. Both are bound from this cycle's single clock read, which is
// what keeps them the same moment in fact.
func (r *Relay) selectClaimable(ctx context.Context, q database.Tx, now time.Time) ([]string, error) {
	limit := int64(r.cfg.BatchSize)

	if r.cfg.ClaimMode == ClaimSkipLocked {
		rows, err := r.q.SelectClaimableOutboxMessagesSkipLocked(ctx, q, outboxdb.SelectClaimableOutboxMessagesSkipLockedParams{
			Now:            now,
			LeaseExpiredBy: &now,
			ResultLimit:    limit,
		})
		if err != nil {
			return nil, err
		}

		ids := make([]string, 0, len(rows))
		for i := range rows {
			ids = append(ids, rows[i].ID)
		}

		return ids, nil
	}

	rows, err := r.q.SelectClaimableOutboxMessages(ctx, q, outboxdb.SelectClaimableOutboxMessagesParams{
		Now:            now,
		LeaseExpiredBy: &now,
		ResultLimit:    limit,
	})
	if err != nil {
		return nil, err
	}

	ids := make([]string, 0, len(rows))
	for i := range rows {
		ids = append(ids, rows[i].ID)
	}

	return ids, nil
}

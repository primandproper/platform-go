package outbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/messagequeue"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	retrycfg "github.com/primandproper/platform-go/v13/retry/config"

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
	claimedKey         = "outbox.claimed"
	claimModeKey       = "outbox.claim_mode"
	batchSizeKey       = "outbox.batch_size"
	backlogDepthKey    = "outbox.backlog_depth"
	backlogAgeKey      = "outbox.backlog_age_seconds"
	retentionCutoffKey = "outbox.retention_cutoff"
	reapedKey          = "outbox.reaped"
	notifyChannelKey   = "outbox.notify_channel"
	sideEffectsKey     = "outbox.side_effects"
)

// claimedMessage is one row the relay has taken ownership of.
type claimedMessage struct {
	id       string
	topic    string
	key      string
	payload  []byte
	attempts int
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
	reapedCounter      metrics.Int64Counter
	claimErrCounter    metrics.Int64Counter
	backlogGauge       metrics.Int64Gauge
	backlogAgeGauge    metrics.Int64Gauge
	batchHist          metrics.Float64Histogram
	cycleHist          metrics.Float64Histogram
	publishHist        metrics.Float64Histogram

	// What the options wrote, kept only until the observer is built from it.
	// Read r.o11y.Logger() for the logger this relay actually uses; this one may
	// be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	cfg RelayConfig

	publishersMu sync.Mutex
	stopOnce     sync.Once
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

	r := &Relay{
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
	if r.reapedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_messages_reaped", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating messages reaped counter")
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
			// One last cycle, so rows committed just before shutdown are not
			// left sitting until the next process starts.
			r.cycle(ctx)

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
// the publishers. Safe to call more than once.
func (r *Relay) Close(ctx context.Context) error {
	_, op := r.o11y.Begin(ctx)
	defer op.End()

	r.stopOnce.Do(func() { close(r.stop) })

	select {
	case <-r.done:
	case <-ctx.Done():
		return op.Error(ctx.Err(), "waiting for outbox relay to drain")
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

	if err = r.markPublished(ctx, published); err != nil {
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

// claim selects a batch, leases it, and reads it back — all in one
// transaction, so two relays cannot lease the same rows.
func (r *Relay) claim(ctx context.Context) ([]claimedMessage, error) {
	ctx, op := r.o11y.Begin(ctx, observability.WithValues(map[string]any{
		claimModeKey: string(r.cfg.ClaimMode),
		batchSizeKey: r.cfg.BatchSize,
		"db.system":  string(r.dialect),
	}))
	defer op.End()

	var claimed []claimedMessage

	err := r.client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		now := r.clock.Now().UTC()

		selectQuery, selectArgs := buildSelectClaimable(
			r.dialect, r.cfg.table, now, r.cfg.BatchSize, r.cfg.ClaimMode == ClaimSkipLocked,
		)

		ids, err := scanIDs(ctx, q, selectQuery, selectArgs)
		if err != nil {
			return platformerrors.Wrap(err, "selecting claimable outbox messages")
		}

		if len(ids) == 0 {
			return nil
		}

		claimQuery, claimArgs := buildClaim(r.dialect, r.cfg.table, ids, now.Add(r.cfg.LeaseDuration))
		if _, err = q.ExecContext(ctx, claimQuery, claimArgs...); err != nil {
			return platformerrors.Wrap(err, "claiming outbox messages")
		}

		fetchQuery, fetchArgs := buildFetch(r.dialect, r.cfg.table, ids)

		claimed, err = scanMessages(ctx, q, fetchQuery, fetchArgs)
		if err != nil {
			return platformerrors.Wrap(err, "reading claimed outbox messages")
		}

		return nil
	})
	if err != nil {
		return nil, op.Error(err, "claiming outbox batch")
	}

	op.Set(claimedKey, len(claimed))

	return claimed, nil
}

// markPublished retires the rows that made it to the broker.
func (r *Relay) markPublished(ctx context.Context, ids []string) error {
	query, args := buildMarkPublished(r.dialect, r.cfg.table, ids, r.clock.Now().UTC())

	if _, err := r.client.Writer().ExecContext(ctx, query, args...); err != nil {
		return platformerrors.Wrap(err, "marking outbox messages published")
	}

	return nil
}

// recordFailure releases the lease, schedules the retry, and quarantines the
// message once it has exhausted its attempts. A quarantined message is skipped
// by every future claim, so one permanently broken message cannot block the
// queue behind it.
func (r *Relay) recordFailure(ctx context.Context, msg *claimedMessage, cause error) {
	r.failedCounter.Add(ctx, 1, topicAttr(msg.topic))

	quarantine := uint(msg.attempts) >= r.cfg.Backoff.MaxAttempts

	nextAttempt := r.clock.Now().UTC().Add(retrycfg.ScheduledDelayFor(r.cfg.Backoff, msg.attempts))

	query, args := buildRecordFailure(
		r.dialect, r.cfg.table, msg.id, nextAttempt, truncateError(cause), quarantine,
	)

	// The partition key matters here more than anywhere else: a keyed message
	// that is failing is also holding up every later message for that key, so
	// the log has to say which key is stalled.
	logger := r.o11y.Logger().WithValues(map[string]any{
		messageIDKey:    msg.id,
		keys.TopicKey:   msg.topic,
		partitionKeyKey: msg.key,
		attemptsKey:     msg.attempts,
	})

	if _, err := r.client.Writer().ExecContext(ctx, query, args...); err != nil {
		// The lease still expires on its own, so the message is retried
		// regardless — just later than intended.
		logger.Error("recording outbox publish failure", err)

		return
	}

	if quarantine {
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
// MIN over a timestamp column comes back through three different drivers.
//
// An empty backlog reports an age of zero rather than no age at all, so a
// drained queue actively resets the gauge instead of leaving a stale reading
// on the dashboard.
func (r *Relay) backlog(ctx context.Context) (depth int64, age time.Duration, err error) {
	var oldest any
	if err = r.client.Reader().
		QueryRowContext(ctx, buildBacklog(r.cfg.table)).
		Scan(&depth, &oldest); err != nil {
		return 0, 0, platformerrors.Wrap(err, "reading outbox backlog")
	}

	created, ok := database.CoerceTime(oldest)
	if !ok {
		return depth, 0, nil
	}

	if age = r.clock.Since(created.UTC()); age < 0 {
		age = 0
	}

	return depth, age, nil
}

// reap deletes published rows past the retention window.
func (r *Relay) reap(ctx context.Context) {
	ctx, op := r.o11y.Begin(ctx)
	defer op.End()

	before := r.clock.Now().UTC().Add(-r.cfg.Retention)

	op.Set(retentionCutoffKey, before)

	query, args := buildReap(r.dialect, r.cfg.table, before, r.cfg.ReapBatchSize)

	res, err := r.client.Writer().ExecContext(ctx, query, args...)
	if err != nil {
		op.Acknowledge(err, "reaping published outbox messages")

		return
	}

	affected, err := res.RowsAffected()
	if err != nil {
		op.Acknowledge(err, "counting reaped outbox messages")

		return
	}

	op.Set(reapedKey, affected)

	if affected > 0 {
		r.reapedCounter.Add(ctx, affected)
		op.Logger().Debug("reaped published outbox messages")
	}
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

// scanIDs runs a single-column query and collects the results. A close failure
// is surfaced only when nothing worse already went wrong, so the real cause is
// never masked by the cleanup.
func scanIDs(ctx context.Context, q database.SQLQueryExecutor, query string, args []any) ([]string, error) {
	return database.ScanStrings(ctx, q, "outbox id", query, args)
}

// scanMessages projects claimed rows. The column list comes from
// messageColumns so the query and this scan cannot drift.
func scanMessages(ctx context.Context, q database.SQLQueryExecutor, query string, args []any) ([]claimedMessage, error) {
	return database.ScanAll(ctx, q, "outbox message", query, args, func(scanner database.Scanner) (claimedMessage, error) {
		var (
			msg claimedMessage
			key sql.NullString
		)

		if err := scanner.Scan(&msg.id, &msg.topic, &key, &msg.payload, &msg.attempts); err != nil {
			return claimedMessage{}, err
		}

		msg.key = key.String

		return msg, nil
	})
}

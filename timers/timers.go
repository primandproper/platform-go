package timers

import (
	"context"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/pgretry"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Observability keys for this package's spans and log fields. Declared once so
// that a field set on a span and the same field logged alongside it cannot
// drift apart, and so the timers. prefix is applied uniformly — an
// un-namespaced attribute name collides with every other component writing to
// the same trace.
const (
	setNameKey       = "timers.set"
	timerCountKey    = "timers.timer_count"
	claimedKey       = "timers.claimed"
	reclaimedKey     = "timers.reclaimed"
	claimLimitKey    = "timers.claim_limit"
	leaseKey         = "timers.lease"
	attemptKey       = "timers.attempt"
	outstandingKey   = "timers.outstanding"
	dueKey           = "timers.due"
	oldestDueKey     = "timers.oldest_due_lateness_seconds"
	cancelledKey     = "timers.cancelled"
	reapedKey        = "timers.reaped"
	nextDueKey       = "timers.next_due"
	notifyChannelKey = "timers.notify_channel"
)

// microsPerMilli converts the microsecond-resolution latency this package
// measures into the milliseconds every other histogram in the module reports.
const microsPerMilli = 1000.0

// Timer is one durable one-shot schedule.
type Timer[K comparable] struct {
	// RunAt is when the timer is meant to fire, as an absolute instant. It is
	// required; the zero time is rejected rather than read as "now".
	//
	// An absolute instant, rather than the offset a work queue takes, is the
	// whole distinction this package draws. "Three days from now" evaluated in
	// one process and stored as an offset is anchored to that process's clock at
	// that moment; "2026-08-10T09:00:00Z" means the same thing to every process
	// that reads it, forever, including the ones that restart in between.
	//
	// Whether that instant has arrived is Postgres's to decide, always. The
	// caller's clock chooses the instant; it never gets a vote on when the
	// instant is reached.
	RunAt time.Time

	// Key names the timer. It is the row's identity: scheduling the same key
	// twice moves one timer rather than creating two.
	Key K

	// Payload is handed back to whoever fires the timer, unchanged, and is
	// optional.
	//
	// A work queue has no payload column on the reasoning that the consumer
	// already knows how to turn a key into work. A timer is different often
	// enough to earn one: the thing it fires about frequently has no durable row
	// to key into — an abandoned checkout, an unaccepted invitation, an
	// escalation that exists only as a decision somebody made — so without a
	// payload the caller has to invent a table to hold the context, which is the
	// table this package is supposed to be.
	//
	// It is opaque bytes rather than a generic type parameter. The set is
	// already generic over its key, and making it generic over the payload too
	// would put a second encoding contract in the primary key's schema for the
	// benefit of callers who can reach encoding directly.
	//
	// Nil and empty are stored distinctly and come back as they went in.
	// MaxPayloadSize bounds it.
	Payload []byte
}

// Due is one leased firing: a timer that has come due and been handed to this
// caller for the length of a lease.
type Due[K comparable] struct {
	// RunAt is the instant this firing is for, read back from the row.
	//
	// It is not decoration. Complete and Release match on it as well as on the
	// key, so a timer rescheduled while it was being fired is not marked fired
	// against a schedule it no longer has. Pass the Due value back rather than
	// its key, and that fence applies without anybody having to think about it.
	RunAt time.Time

	// Key names the timer, decoded back through the set's codec.
	Key K

	// Payload is what was scheduled with the timer, unchanged. Nil if none was.
	Payload []byte

	// Late is how far past RunAt the claim happened, measured on the database's
	// clock. It is the number that says whether the fleet is keeping up.
	//
	// It is never negative: a timer is not claimable before its instant.
	Late time.Duration

	// Attempts counts claims of this timer, including this one. It is 1 on a
	// first claim, so a handler can tell a fresh firing from a retried one.
	Attempts int

	// Reclaimed reports that this claim took over a lease that lapsed rather
	// than one that was released or completed.
	//
	// It is the only visible trace of the package's failure-recovery mechanism.
	// A steady trickle is healthy — workers do die. A rate that tracks the claim
	// rate means leases are shorter than the work, and every timer is firing at
	// least twice.
	Reclaimed bool
}

// Stats is the set's shape, read in one round trip.
type Stats struct {
	// OldestDueLateness is how far past its instant the oldest claimable timer
	// already is, measured on the database's clock. Zero when nothing is
	// claimable.
	//
	// This is the number to alert on. Every other field is a level, and no level
	// distinguishes a set that is large because a lot is scheduled from one that
	// is large because nothing is firing.
	OldestDueLateness time.Duration
	// Outstanding counts timers that have not fired, whether or not their
	// instant has arrived.
	Outstanding int64
	// Due counts timers a Claim would hand out right now.
	Due int64
	// Leased counts timers currently being fired.
	Leased int64
	// Stalled counts unfired timers that have exhausted Config.MaxAttempts and
	// will never be claimed again. Always zero when MaxAttempts is unlimited.
	Stalled int64
	// Fired counts finished timers still inside the retention window.
	Fired int64
}

// Timers is a durable one-shot scheduler over one Postgres table: run this once
// at instant T, exactly once across the fleet, surviving restarts.
//
// It is safe for concurrent use, and is meant to be shared: one Timers per
// process per logical set, handed to every goroutine that schedules or fires.
//
// It owns no goroutine and needs no Close. The loop that fires timers is the
// caller's — Worker is the one this package supplies.
type Timers[K comparable] struct {
	clock  clock.Clock
	client database.Client
	codec  KeyCodec[K]
	o11y   observability.Observer

	scheduledCounter metrics.Int64Counter
	claimedCounter   metrics.Int64Counter
	reclaimedCounter metrics.Int64Counter
	firedCounter     metrics.Int64Counter
	releasedCounter  metrics.Int64Counter
	cancelledCounter metrics.Int64Counter
	reapedCounter    metrics.Int64Counter
	retryCounter     metrics.Int64Counter

	// retrier re-runs a write that Postgres asked to have re-run. See
	// internal/pgretry for why these retries exist in a table this package
	// otherwise locks in a fixed order.
	retrier pgretry.Retrier

	outstandingGauge metrics.Int64Gauge
	dueGauge         metrics.Int64Gauge
	stalledGauge     metrics.Int64Gauge
	oldestDueGauge   metrics.Int64Gauge

	claimBatchHist   metrics.Float64Histogram
	claimLatencyHist metrics.Float64Histogram
	latenessHist     metrics.Float64Histogram

	// attrs labels every measurement with the set name. One process commonly
	// runs several sets against one table, and without this their counters
	// collapse into a single number in which a set that has stopped firing is
	// invisible beside the ones that are fine.
	attrs metric.MeasurementOption

	// wakeup is nil unless WithWakeup supplied one. A nil channel blocks forever
	// in a select, so Wait needs no branch for its absence.
	wakeup <-chan struct{}

	cfg Config
}

// New builds a timer set over client, which must speak Postgres and must be the
// database holding the timer table.
//
// ctx is used to validate the config and is not retained; every method takes its
// own.
func New[K comparable](
	ctx context.Context,
	cfg *Config,
	client database.Client,
	opts ...Option,
) (*Timers[K], error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}
	if client == nil {
		return nil, ErrNilDatabaseClient
	}

	if err := dialect.RequirePostgres("timers", client.Dialect()); err != nil {
		return nil, err
	}

	cfg.EnsureDefaults()

	if cfg.Name == "" {
		return nil, ErrEmptySetName
	}

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating timers config")
	}

	if !dialect.ValidIdentifier(cfg.resolvedTable()) {
		return nil, platformerrors.Wrapf(dialect.ErrInvalidIdentifier, "timer table %q", cfg.resolvedTable())
	}

	// The channel is bound as text by the statement this package emits, but the
	// listener on the other end has to render it into a LISTEN, which takes no
	// parameters. Vetting it here is what keeps that end from having to.
	if cfg.NotifyChannel != "" && !dialect.ValidIdentifier(cfg.NotifyChannel) {
		return nil, platformerrors.Wrapf(dialect.ErrInvalidIdentifier, "timer notify channel %q", cfg.NotifyChannel)
	}

	o := newTimerOptions(opts)

	t := &Timers[K]{
		cfg:    *cfg,
		client: client,
		clock:  clock.NewClock(),
		codec:  DefaultKeyCodec[K](),
		wakeup: o.wakeup,
		attrs:  metric.WithAttributes(attribute.String(setNameKey, cfg.Name)),
	}

	if o.clock != nil {
		t.clock = o.clock
	}

	// Asserted rather than assumed: Option cannot name K, so this is where a
	// codec built for another key type is caught. Failing here means it is
	// caught at construction, before a single key has been written to the table
	// under a rendering nothing will ever decode.
	if o.keyCodec != nil {
		codec, ok := o.keyCodec.(KeyCodec[K])
		if !ok {
			return nil, platformerrors.Wrapf(ErrKeyCodecTypeMismatch,
				"codec is %T, want KeyCodec[%T]", o.keyCodec, *new(K))
		}

		t.codec = codec
	}

	// Every operation this set performs is about this one set, so the name is
	// stated once here instead of at each Begin below.
	t.o11y = observability.NewObserverWithValues(serviceName, o.logger, o.tracerProvider,
		map[string]any{setNameKey: cfg.Name})

	if err := t.buildInstruments(o.metricsProvider); err != nil {
		return nil, err
	}

	t.retrier = pgretry.Retrier{
		Logger:     t.o11y.Logger(),
		Counter:    t.retryCounter,
		AddOptions: []metric.AddOption{t.attrs},
		AttemptKey: attemptKey,
		Subject:    "timer",
		Attempts:   t.cfg.WriteAttempts,
	}

	return t, nil
}

// buildInstruments creates every metric the set records. Split out of New
// because it is a wall of near-identical error handling that says nothing about
// how a Timers is assembled.
func (t *Timers[K]) buildInstruments(metricsProvider metrics.Provider) error {
	mp := metrics.EnsureMetricsProvider(metricsProvider)

	counters := []struct {
		into *metrics.Int64Counter
		name string
	}{
		{&t.scheduledCounter, "timers_scheduled"},
		{&t.claimedCounter, "timers_claimed"},
		{&t.reclaimedCounter, "leases_expired"},
		{&t.firedCounter, "timers_fired"},
		{&t.releasedCounter, "timers_released"},
		{&t.cancelledCounter, "timers_cancelled"},
		{&t.reapedCounter, "timers_reaped"},
		{&t.retryCounter, "write_retries"},
	}
	for _, c := range counters {
		instrument, err := mp.NewInt64Counter(fmt.Sprintf("%s_%s", serviceName, c.name))
		if err != nil {
			return platformerrors.Wrapf(err, "creating %s counter", c.name)
		}

		*c.into = instrument
	}

	gauges := []struct {
		into *metrics.Int64Gauge
		name string
	}{
		{&t.outstandingGauge, "outstanding"},
		{&t.dueGauge, "due"},
		{&t.stalledGauge, "stalled"},
		{&t.oldestDueGauge, "oldest_due_lateness_seconds"},
	}
	for _, g := range gauges {
		instrument, err := mp.NewInt64Gauge(fmt.Sprintf("%s_%s", serviceName, g.name))
		if err != nil {
			return platformerrors.Wrapf(err, "creating %s gauge", g.name)
		}

		*g.into = instrument
	}

	histograms := []struct {
		into *metrics.Float64Histogram
		name string
	}{
		{&t.claimBatchHist, "claimed_batch_size"},
		{&t.claimLatencyHist, "claim_latency_ms"},
		{&t.latenessHist, "firing_lateness_ms"},
	}
	for _, h := range histograms {
		instrument, err := mp.NewFloat64Histogram(fmt.Sprintf("%s_%s", serviceName, h.name))
		if err != nil {
			return platformerrors.Wrapf(err, "creating %s histogram", h.name)
		}

		*h.into = instrument
	}

	return nil
}

// Name reports the logical timer set this Timers reads and writes.
func (t *Timers[K]) Name() string {
	return t.cfg.Name
}

// Clock reports the clock this set reads, for a caller assembling the instants
// it schedules against — the one this set was given, so a synctest bubble does
// not have to be threaded to two places.
func (t *Timers[K]) Clock() clock.Clock {
	return t.clock
}

// NextDue reports how long until the nearest outstanding timer becomes
// claimable, and whether there is one at all.
//
// It is the read that makes a timer poller cheap. A work queue polls because it
// cannot know when work will be enqueued; a timer set knows exactly when its
// next firing is owed, so it can sleep until then and issue no queries at all in
// between. That is the difference between a poll interval tuned as a compromise
// between latency and load, and one that is purely a backstop.
//
// The duration is non-positive when something is claimable now. The instant it
// measures to is when the row next becomes claimable rather than when it was
// meant to run, so a timer whose holder has died is counted at its lease expiry
// rather than at its long-past instant.
//
// It reads the writer, not a replica: a poller acting on replication-lagged
// scheduling data would sleep through timers that are already durable.
func (t *Timers[K]) NextDue(ctx context.Context) (time.Duration, bool, error) {
	ctx, op := t.o11y.Begin(ctx)
	defer op.End()

	var (
		outstanding int64
		micros      int64
	)

	if err := t.client.Writer().
		QueryRowContext(ctx, buildNextDue(t.cfg.resolvedTable()), t.cfg.Name, t.cfg.attemptCeiling()).
		Scan(&outstanding, &micros); err != nil {
		return 0, false, op.Error(err, "reading the next due timer")
	}

	if outstanding == 0 {
		return 0, false, nil
	}

	next := time.Duration(micros) * time.Microsecond

	op.SetValues(map[string]any{outstandingKey: outstanding, nextDueKey: next.String()})

	return next, true, nil
}

// Wait paces a firing loop: it blocks until the next timer is due, until poll
// elapses, until a wakeup arrives, or until ctx is done, whichever comes first.
//
// It is the one piece of the loop this package supplies to callers writing their
// own, and it exists because the loop is otherwise theirs:
//
//	for {
//		due, err := set.Claim(ctx, 20, time.Minute)
//		// ...
//		if len(due) == 0 {
//			if err = set.Wait(ctx, time.Minute); err != nil {
//				return err
//			}
//		}
//	}
//
// Call it only when the last claim came back empty. Wait's floor exists to stop
// a spin, and applying it between full batches would pace a drain at one batch
// per floor instead of as fast as the handlers run.
//
// poll must be positive. It is the backstop that makes both the next-due read
// and the wakeup safe to lose, and losing wakes is normal: the signal is
// at-most-once, and a listener that reconnects misses whatever arrived while it
// was away. A loop with no backstop would sleep through every timer scheduled
// during a reconnect.
//
// Config.MinWakeInterval floors the sleep. It bounds a burst of wakes to one
// extra pass apiece, and — the case a timer set has and a work queue does not —
// it bounds the spin when the next-due read says something is claimable but the
// claim does not get it, because a fleet-mate took it first or because it has
// stalled out of attempts.
//
// Call it from one loop per Timers. A wake goes to a single receiver, so several
// loops sharing one would divide the wakes between them arbitrarily.
//
// Wait is not traced. A span per sleep would be a root span per idle tick, which
// is the same noise Claim declines to emit when it leases nothing. The next-due
// read inside it is.
func (t *Timers[K]) Wait(ctx context.Context, poll time.Duration) error {
	if poll <= 0 {
		return ErrInvalidPollInterval
	}

	return t.sleep(ctx, t.sleepFor(ctx, poll))
}

// sleepFor decides how long Wait parks: until the next timer is owed, but never
// longer than the poll backstop and never shorter than the wake floor.
func (t *Timers[K]) sleepFor(ctx context.Context, poll time.Duration) time.Duration {
	sleep := poll

	// A failed read is not fatal: the poll interval is exactly the fallback the
	// backstop exists to be, and a set that cannot be read right now is a set
	// whose Claim is about to report the same thing with a caller to tell.
	next, found, err := t.NextDue(ctx)
	if err != nil {
		t.o11y.Logger().Error("reading the next due timer, sleeping for the poll interval instead", err)
	} else if found {
		sleep = min(sleep, next)
	}

	return max(sleep, t.cfg.MinWakeInterval)
}

// sleep parks for d, returning early on a wakeup or a cancelled context.
func (t *Timers[K]) sleep(ctx context.Context, d time.Duration) error {
	// A Ticker rather than clock.Sleep, which blocks and so could not be raced
	// against the wakeup without a goroutine per pass. Only the first tick is
	// ever read.
	ticker := t.clock.NewTicker(d)
	defer ticker.Stop()

	select {
	case <-ctx.Done():
		return platformerrors.Wrap(ctx.Err(), "waiting for the next timer")
	case <-ticker.Chan():
		return nil
	case <-t.wakeup:
		return nil
	}
}

// Claim leases up to limit of the set's due timers for the given lease duration,
// in one statement: nothing is selected without also being leased, so two
// claimants can never see the same firing.
//
// Due means unfired, unleased, past its instant, and — when Config.MaxAttempts
// is set, which it is by default — not yet out of attempts. The oldest debt goes
// first; there is no priority, because a timer already said what it wanted by
// naming an instant.
//
// The limit counts timers actually leased, so a full batch comes back whenever
// that many are due — a row another claimant holds is skipped and replaced, not
// subtracted. A short batch therefore means the set is nearly drained, and an
// empty one means nothing is due.
//
// The lease is what the caller promises to finish inside. There is no heartbeat
// and no way to extend one — a lease that lapses mid-firing hands the timer to
// somebody else, and the original worker's eventual Complete lands on a firing
// that is already done. That is a duplicate firing, not a lost one, which is why
// a handler has to be idempotent; if firing twice is not the same as firing
// once, this package is the wrong tool.
func (t *Timers[K]) Claim(ctx context.Context, limit int, lease time.Duration) ([]Due[K], error) {
	ctx, op := t.o11y.Begin(ctx, observability.WithValues(map[string]any{
		claimLimitKey: limit,
		leaseKey:      lease.String(),
	}))
	defer op.End()

	if lease <= 0 {
		return nil, op.Error(ErrInvalidLease, "claiming due timers")
	}

	if limit <= 0 || limit > t.cfg.MaxClaimBatch {
		limit = t.cfg.MaxClaimBatch
	}

	// Through the injected clock, not time.Now: this package hands one to the
	// ticker its Run loop paces on, and a histogram that read the wall clock
	// anyway was the one measurement a test driving that loop could not control.
	// Not op.Time, because the record below is deliberately on the success path
	// only — a claim that failed took no time worth a latency reading.
	startTime := t.clock.Now()

	due, err := t.claim(ctx, limit, lease)
	if err != nil {
		return nil, op.Error(err, "claiming due timers")
	}

	t.claimLatencyHist.Record(ctx, float64(t.clock.Since(startTime).Microseconds())/microsPerMilli, t.attrs)

	if len(due) == 0 {
		return nil, nil
	}

	var reclaimed int64
	for i := range due {
		if due[i].Reclaimed {
			reclaimed++
		}

		t.latenessHist.Record(ctx, float64(due[i].Late.Microseconds())/microsPerMilli, t.attrs)
	}

	t.claimedCounter.Add(ctx, int64(len(due)), t.attrs)
	t.claimBatchHist.Record(ctx, float64(len(due)), t.attrs)

	if reclaimed > 0 {
		t.reclaimedCounter.Add(ctx, reclaimed, t.attrs)
	}

	op.SetValues(map[string]any{claimedKey: len(due), reclaimedKey: reclaimed})

	return due, nil
}

// claim runs the statement and decodes the result. Separated from Claim so that
// the retry wrapper has a single unit to re-run: a partially scanned result set
// from a deadlocked statement must be discarded, not merged with the retry's.
func (t *Timers[K]) claim(ctx context.Context, limit int, lease time.Duration) ([]Due[K], error) {
	var due []Due[K]

	err := t.retrier.Do(ctx, "claim", func() error {
		var claimErr error

		due, claimErr = t.claimOnce(ctx, limit, lease)

		return claimErr
	})

	return due, err
}

// claimOnce is one attempt of claim.
func (t *Timers[K]) claimOnce(ctx context.Context, limit int, lease time.Duration) ([]Due[K], error) {
	// The writer, not the reader: this is an UPDATE that happens to return rows,
	// and a read replica would both fail it and lose every lease it handed out.
	due, err := database.ScanAll(ctx, t.client.Writer(), "claimed timer",
		buildClaim(t.cfg.resolvedTable()),
		[]any{t.cfg.Name, t.cfg.attemptCeiling(), limit, lease.Microseconds()},
		func(scanner database.Scanner) (Due[K], error) {
			var (
				encoded    string
				lateMicros int64
				fired      Due[K]
			)

			if scanErr := scanner.Scan(&encoded, &fired.Payload, &fired.RunAt,
				&lateMicros, &fired.Attempts, &fired.Reclaimed); scanErr != nil {
				return fired, platformerrors.Wrap(scanErr, "scanning claimed timer")
			}

			fired.Late = max(time.Duration(lateMicros)*time.Microsecond, 0)

			// A key that will not decode is the one failure here a caller cannot act
			// on and must not be hidden: it means the table holds rows written under
			// a different key type or codec, and every claim will keep leasing them.
			// Failing the whole batch is the loud version of that, and the lease
			// lapses on its own.
			var decodeErr error
			if fired.Key, decodeErr = t.codec.DecodeKey(encoded); decodeErr != nil {
				return fired, platformerrors.Wrapf(decodeErr, "decoding claimed timer key %q", encoded)
			}

			return fired, nil
		})
	if err != nil {
		return nil, platformerrors.Wrap(err, "leasing due timers")
	}

	return due, nil
}

// Complete retires firings that have been handled: the lease is dropped and the
// timer stops being claimable, forever. Rows are marked rather than deleted so
// that "did the expiry run, and when" stays answerable; Reap removes them once
// they age past Config.Retention.
//
// It takes the Due values Claim handed out rather than bare keys, because a
// firing is identified by its key and its instant together. A timer rescheduled
// while it was being fired no longer matches, so this marks nothing and the new
// schedule survives — the same "matches nothing" outcome a lapsed lease already
// produces. Cancelled and already-fired timers are ignored for the same reason:
// a straggler has nothing useful to do with an error.
//
// Completing is idempotent, and scheduling a completed key again restarts it.
func (t *Timers[K]) Complete(ctx context.Context, fired ...Due[K]) error {
	ctx, op := t.o11y.Begin(ctx, observability.WithValue(timerCountKey, len(fired)))
	defer op.End()

	affected, err := t.writeFirings(ctx, "complete", fired, func(rows []firingRef) (string, []any) {
		args := make([]any, 0, (len(rows)*2)+1)
		args = append(args, t.cfg.Name)

		for i := range rows {
			args = append(args, rows[i].key, rows[i].runAt)
		}

		return buildComplete(t.cfg.resolvedTable(), len(rows)), args
	})
	if err != nil {
		return op.Error(err, "completing fired timers")
	}

	t.firedCounter.Add(ctx, affected, t.attrs)

	return nil
}

// Release hands firings back before their leases lapse, pushing each one out by
// delay and recording cause as its last error.
//
// A zero delay and a nil cause is the plain hand-back — "I am not going to get
// to this" — and needs no ceremony. A non-zero delay is how a caller backs off a
// failing timer without a scheduler of its own; retry/config's DelayFor computes
// the same schedule the rest of this module retries on.
//
// Releasing is optional. An unreleased lease lapses and the timer returns
// anyway, just later, which is why nothing here treats a failed Release as
// fatal. What it buys is the delay and the recorded reason: without it, a failing
// timer comes straight back and spins against whatever it failed on until it
// exhausts Config.MaxAttempts.
//
// The delay moves the timer's instant rather than holding it behind a separate
// column, so a released timer is genuinely rescheduled — and, as with Complete,
// a release whose instant no longer matches the row does nothing, so a
// reschedule that landed in the meantime is not dragged backwards.
func (t *Timers[K]) Release(ctx context.Context, delay time.Duration, cause error, fired ...Due[K]) error {
	ctx, op := t.o11y.Begin(ctx, observability.WithValue(timerCountKey, len(fired)))
	defer op.End()

	delay = max(delay, 0)

	affected, err := t.writeFirings(ctx, "release", fired, func(rows []firingRef) (string, []any) {
		args := make([]any, 0, (len(rows)*2)+3)
		args = append(args, t.cfg.Name, delay.Microseconds(), pgretry.TruncateError(cause))

		for i := range rows {
			args = append(args, rows[i].key, rows[i].runAt)
		}

		return buildRelease(t.cfg.resolvedTable(), len(rows)), args
	})
	if err != nil {
		return op.Error(err, "releasing timers")
	}

	t.releasedCounter.Add(ctx, affected, t.attrs)

	return nil
}

// Cancel removes timers outright, whatever their schedule and whether or not
// they have already fired, and reports how many rows it deleted.
//
// The count is the answer to the question a cancel actually asks. "Stop the
// trial-expiry email" is only satisfied if the row was still there to delete: a
// zero means the timer had already fired, or had already been cancelled, or was
// never scheduled — and the caller usually has to do something different in the
// first of those cases. Cancelling a timer somebody currently holds a lease on
// is allowed and reports one, but their firing is already in flight; the cancel
// stops the row coming back, not the handler that is running.
//
// It deletes rather than marking. A cancelled timer has no history worth
// keeping, and keeping it would mean Schedule had to distinguish "reschedule a
// cancelled timer" from "reschedule a fired one".
func (t *Timers[K]) Cancel(ctx context.Context, keys ...K) (int64, error) {
	ctx, op := t.o11y.Begin(ctx, observability.WithValue(timerCountKey, len(keys)))
	defer op.End()

	if len(keys) == 0 {
		return 0, nil
	}

	encoded := make([]string, 0, len(keys))

	for i := range keys {
		one, err := encodeKey(t.codec, keys[i])
		if err != nil {
			return 0, op.Error(err, "encoding timer key")
		}

		encoded = append(encoded, one)
	}

	encoded = sortAndDedupe(encoded)

	args := make([]any, 0, len(encoded)+1)
	args = append(args, t.cfg.Name)

	for _, key := range encoded {
		args = append(args, key)
	}

	var affected int64

	err := t.retrier.Do(ctx, "cancel", func() error {
		res, execErr := t.client.Writer().
			ExecContext(ctx, buildCancel(t.cfg.resolvedTable(), len(encoded)), args...)
		if execErr != nil {
			return execErr
		}

		affected, execErr = res.RowsAffected()

		return execErr
	})
	if err != nil {
		return 0, op.Error(err, "cancelling timers")
	}

	op.Set(cancelledKey, affected)

	t.cancelledCounter.Add(ctx, affected, t.attrs)

	return affected, nil
}

// Reap deletes fired timers that have aged past Config.Retention, up to
// Config.ReapBatchSize of them, and reports how many it removed.
//
// It is a method rather than a loop this package runs, because a consumer
// already has a scheduler — see the jobs package — and a component that starts
// its own timers is a component that has to be told when to stop. Call it on a
// period comfortably shorter than the time it takes ReapBatchSize firings to
// accumulate; a return value equal to the batch size means retention is falling
// behind and the period is too long.
func (t *Timers[K]) Reap(ctx context.Context) (int64, error) {
	ctx, op := t.o11y.Begin(ctx)
	defer op.End()

	var affected int64

	err := t.retrier.Do(ctx, "reap", func() error {
		res, execErr := t.client.Writer().ExecContext(ctx, buildReap(t.cfg.resolvedTable()),
			t.cfg.Name, t.cfg.Retention.Microseconds(), t.cfg.ReapBatchSize)
		if execErr != nil {
			return platformerrors.Wrap(execErr, "reaping fired timers")
		}

		affected, execErr = res.RowsAffected()

		return execErr
	})
	if err != nil {
		return 0, op.Error(err, "reaping fired timers")
	}

	op.Set(reapedKey, affected)

	if affected > 0 {
		t.reapedCounter.Add(ctx, affected, t.attrs)
	}

	return affected, nil
}

// Stats reads the set's shape and records it to the gauges.
//
// It is the health signal: nothing in this package fails loudly, so a set whose
// fleet has stopped firing looks exactly like an idle one until somebody counts
// what is overdue. Sample it on a timer rather than per claim — every field is
// an aggregate over the set, and at claim cadence the read costs more than the
// firing it reports on.
func (t *Timers[K]) Stats(ctx context.Context) (Stats, error) {
	ctx, op := t.o11y.Begin(ctx)
	defer op.End()

	var (
		stats     Stats
		lateMicro int64
	)

	if err := t.client.Reader().
		QueryRowContext(ctx, buildStats(t.cfg.resolvedTable()), t.cfg.Name, t.cfg.attemptCeiling()).
		Scan(&stats.Outstanding, &stats.Due, &stats.Leased, &stats.Stalled, &stats.Fired, &lateMicro); err != nil {
		return Stats{}, op.Error(err, "reading timer stats")
	}

	if lateMicro > 0 {
		stats.OldestDueLateness = time.Duration(lateMicro) * time.Microsecond
	}

	lateSeconds := int64(stats.OldestDueLateness.Seconds())

	t.outstandingGauge.Record(ctx, stats.Outstanding, t.attrs)
	t.dueGauge.Record(ctx, stats.Due, t.attrs)
	t.stalledGauge.Record(ctx, stats.Stalled, t.attrs)
	t.oldestDueGauge.Record(ctx, lateSeconds, t.attrs)

	op.SetValues(map[string]any{
		outstandingKey: stats.Outstanding,
		dueKey:         stats.Due,
		oldestDueKey:   lateSeconds,
	})

	return stats, nil
}

// firingRef is one firing reduced to what the statements bind: the encoded key
// and the instant that fences it.
type firingRef struct {
	runAt time.Time
	key   string
}

// writeFirings is the shape Complete and Release share: encode the firings, hand
// the batch to build, run it with the retry wrapper, and report how many rows it
// touched.
//
// The firings are sorted by key before the statement is built. That is the
// lock-ordering discipline, applied at the one place both writers pass through,
// so a third added later inherits it rather than having to remember it.
func (t *Timers[K]) writeFirings(
	ctx context.Context,
	label string,
	fired []Due[K],
	build func(rows []firingRef) (query string, args []any),
) (int64, error) {
	if len(fired) == 0 {
		return 0, nil
	}

	rows := make([]firingRef, 0, len(fired))

	for i := range fired {
		key, err := encodeKey(t.codec, fired[i].Key)
		if err != nil {
			return 0, err
		}

		rows = append(rows, firingRef{key: key, runAt: fired[i].RunAt})
	}

	rows = sortAndDedupeFirings(rows)

	query, args := build(rows)

	var affected int64

	err := t.retrier.Do(ctx, label, func() error {
		res, execErr := t.client.Writer().ExecContext(ctx, query, args...)
		if execErr != nil {
			return execErr
		}

		affected, execErr = res.RowsAffected()

		return execErr
	})

	return affected, err
}

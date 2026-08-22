package metering

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"math/rand/v2"
	"strconv"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/primandproper/platform-go/v13/capitalism"
	"github.com/primandproper/platform-go/v13/clock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/jobs"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	retrycfg "github.com/primandproper/platform-go/v13/retry/config"
)

const (
	// DefaultFlushJobName is the name the Flusher's jobs.Job carries.
	//
	// It is a constant because a job's name is its lock key: two replicas that
	// disagree about it both run the flush, and the flush spends money.
	DefaultFlushJobName = "metering-flush"

	// idempotencyKeyPrefix namespaces the keys sent to the billing provider, so a
	// metering post cannot collide with an idempotency key the application uses
	// for something else against the same account.
	idempotencyKeyPrefix = "mtr_"

	// maxStoredErrorLength bounds a stored error rendering. A provider error can
	// carry the request body back, and the request body is a customer's usage —
	// which does not belong in a column somebody reads over a shoulder.
	maxStoredErrorLength = 1024
)

// ErrFlusherPanicked wraps the value recovered from a provider post that
// panicked. The Flusher contains the panic rather than letting it unwind the
// goroutine, which would stop the flush — and only the flush — silently for the
// life of the process, while usage kept accumulating.
var ErrFlusherPanicked = platformerrors.New("metering flush panicked")

// FlushResult is what one pass did.
type FlushResult struct {
	// Claimed is how many totals the pass leased.
	Claimed int

	// Flushed is how many were posted to the provider and settled.
	Flushed int

	// Skipped is how many had no provider ref and were settled without a post
	// — a subject on a plan that does not bill for that meter. Not a failure; see
	// ErrNoProviderRef.
	Skipped int

	// Failed is how many could not be posted and were returned for retry.
	Failed int

	// Quantity is the total usage posted, summed across every successful flush.
	Quantity int64

	// EventsReaped is how many ledger rows the retention pass deleted.
	EventsReaped int64
}

// Flusher pushes accumulated usage to the billing provider.
//
// It is the part of this package where a mistake costs money rather than
// accuracy, and its whole shape follows from one requirement: a post that is
// retried must not be a second charge. Three things together give that.
//
// The delta, not the total. Providers aggregate the records inside a billing
// period, so each post carries only what has accumulated since the last one.
//
// The sequence, not the clock. Every successful post increments a counter stored
// beside the total, and that counter is what varies in the idempotency key — so a
// retry of the same post reuses the same key and is a no-op at the provider,
// while a genuinely new post gets a fresh one.
//
// The settle is guarded on the sequence it read. A flusher whose lease lapsed
// mid-post cannot advance a sequence somebody else has already moved, which is
// the one race that would put the same delta on the wire under two different
// keys.
type Flusher struct {
	store    Store
	mapper   ProviderMapper
	reporter capitalism.UsageReporter
	clock    clock.Clock
	o11y     observability.Observer

	flushedCounter  metrics.Int64Counter
	skippedCounter  metrics.Int64Counter
	failedCounter   metrics.Int64Counter
	quantityCounter metrics.Int64Counter
	abandonCounter  metrics.Int64Counter
	reapedCounter   metrics.Int64Counter
	backlogGauge    metrics.Int64Gauge
	postHist        metrics.Float64Histogram
	passHist        metrics.Float64Histogram

	// What the options wrote, kept only until the observer is built from it.
	// Read f.o11y.Logger() for the logger this flusher actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	cfg FlusherConfig
}

// NewFlusher builds a Flusher. It does not schedule it; see Job.
//
// ctx is used to validate the config and is not retained.
func NewFlusher(
	ctx context.Context,
	cfg *FlusherConfig,
	store Store,
	mapper ProviderMapper,
	reporter capitalism.UsageReporter,
	opts ...FlusherOption,
) (*Flusher, error) {
	if cfg == nil {
		return nil, platformerrors.New("nil metering flusher config provided")
	}

	if store == nil {
		return nil, ErrNilStore
	}

	if mapper == nil {
		return nil, ErrNilProviderMapper
	}

	if reporter == nil {
		// No implicit noop. A flusher that silently posted nowhere would mark
		// usage flushed and advance the sequence, so the usage would never be
		// posted again once a real reporter was wired in — a month of revenue
		// discarded by an omission in the wiring. capitalism/noop.NewUsageReporter
		// exists for the deployment that means it.
		return nil, ErrNilUsageReporter
	}

	cfg.EnsureDefaults()

	f := &Flusher{
		cfg:      *cfg,
		store:    store,
		mapper:   mapper,
		reporter: reporter,
		clock:    clock.NewClock(),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(f)
		}
	}

	if err := f.cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating metering flusher config")
	}

	f.o11y = observability.NewObserver(serviceName, f.logger, f.tracerProvider)

	if err := f.initInstruments(); err != nil {
		return nil, err
	}

	return f, nil
}

// initInstruments builds the flusher's meters.
func (f *Flusher) initInstruments() error {
	mp := metrics.EnsureMetricsProvider(f.metricsProvider)

	var err error
	if f.flushedCounter, err = mp.NewInt64Counter(serviceName + "_flushes"); err != nil {
		return platformerrors.Wrap(err, "creating flush counter")
	}
	if f.skippedCounter, err = mp.NewInt64Counter(serviceName + "_flushes_skipped"); err != nil {
		return platformerrors.Wrap(err, "creating skipped flush counter")
	}
	if f.failedCounter, err = mp.NewInt64Counter(serviceName + "_flush_failures"); err != nil {
		return platformerrors.Wrap(err, "creating flush failure counter")
	}
	if f.quantityCounter, err = mp.NewInt64Counter(serviceName + "_flushed_quantity"); err != nil {
		return platformerrors.Wrap(err, "creating flushed quantity counter")
	}
	if f.abandonCounter, err = mp.NewInt64Counter(serviceName + "_flushes_abandoned"); err != nil {
		return platformerrors.Wrap(err, "creating abandoned flush counter")
	}
	if f.reapedCounter, err = mp.NewInt64Counter(serviceName + "_events_reaped"); err != nil {
		return platformerrors.Wrap(err, "creating events reaped counter")
	}
	if f.backlogGauge, err = mp.NewInt64Gauge(serviceName + "_flush_backlog"); err != nil {
		return platformerrors.Wrap(err, "creating flush backlog gauge")
	}
	if f.postHist, err = mp.NewFloat64Histogram(serviceName + "_provider_post_latency_ms"); err != nil {
		return platformerrors.Wrap(err, "creating provider post latency histogram")
	}
	if f.passHist, err = mp.NewFloat64Histogram(serviceName + "_flush_pass_latency_ms"); err != nil {
		return platformerrors.Wrap(err, "creating flush pass latency histogram")
	}

	return nil
}

// Job renders the Flusher as a jobs.Job, for registration with a jobs.Scheduler.
//
// Scheduling it there rather than running a ticker of its own is what makes the
// flush run once across a fleet instead of once per replica. Ten replicas each
// claiming the same totals is survivable — the claim is guarded — but it is ten
// times the contention on the one table an outage here stops billing from.
//
// LeaseTTL must comfortably exceed one pass. The scheduler does not renew a lease
// while a job runs, so a flush that outlives its lease loses exclusivity halfway
// through — see jobs.Job.LeaseTTL.
func (f *Flusher) Job(schedule jobs.Schedule, leaseTTL time.Duration) jobs.Job {
	return jobs.Job{
		Name:     DefaultFlushJobName,
		Schedule: schedule,
		LeaseTTL: leaseTTL,
		Run: func(ctx context.Context) error {
			_, err := f.Flush(ctx)

			return err
		},
	}
}

// Flush runs one pass: claim what owes the provider, post it, and reap the ledger
// rows whose periods have settled.
//
// The reap runs whether or not the posts succeeded. They are unrelated chores
// sharing a schedule, and a provider being unreachable is not a reason to let the
// event table grow unbounded — the reap's own predicate already refuses to touch
// anything a failed post still needs.
func (f *Flusher) Flush(ctx context.Context) (*FlushResult, error) {
	ctx, op := f.o11y.Begin(ctx)
	defer op.End()

	defer op.Time(ctx, f.clock, f.passHist)()

	now := f.clock.Now().UTC()

	result := &FlushResult{}

	var errs []error

	claimed, err := f.store.ClaimFlushable(ctx, now, f.cfg.BatchSize, f.cfg.MaxAttempts, now.Add(f.cfg.LeaseDuration))
	if err != nil {
		errs = append(errs, platformerrors.Wrap(err, "claiming flushable metering totals"))
	} else {
		result.Claimed = len(claimed)
		f.post(ctx, claimed, result)
	}

	// Sampled every pass, including the ones that claimed nothing — which is
	// exactly when nobody would otherwise look. A backlog that is not draining is
	// revenue that is not being invoiced, and no counter of successful flushes can
	// distinguish "flushing steadily" from "flushing steadily while a queue builds
	// behind it".
	f.backlogGauge.Record(ctx, int64(result.Claimed-result.Flushed-result.Skipped))

	if !f.cfg.DisableReap {
		reaped, reapErr := f.store.ReapEvents(ctx, now.Add(-f.cfg.EventRetention), f.cfg.ReapBatchSize)
		if reapErr != nil {
			errs = append(errs, platformerrors.Wrap(reapErr, "reaping metering usage events"))
		} else if reaped > 0 {
			result.EventsReaped = reaped
			f.reapedCounter.Add(ctx, reaped)
		}
	}

	op.SetValues(map[string]any{
		resultCountKey: result.Claimed,
		flushedKey:     result.Flushed,
		reapedKey:      result.EventsReaped,
	})

	if len(errs) > 0 {
		return result, op.Error(platformerrors.Join(errs...), "flushing metering usage")
	}

	return result, nil
}

// post pushes each claimed total to the provider, bounded by the configured
// concurrency.
func (f *Flusher) post(ctx context.Context, claimed []*Total, result *FlushResult) {
	if len(claimed) == 0 {
		return
	}

	var (
		mu sync.Mutex
		wg sync.WaitGroup
	)

	sem := make(chan struct{}, f.cfg.Concurrency)

	for _, total := range claimed {
		sem <- struct{}{}

		wg.Go(func() {
			defer func() { <-sem }()

			outcome, quantity := f.flushOne(ctx, total)

			mu.Lock()
			defer mu.Unlock()

			switch outcome {
			case outcomeFlushed:
				result.Flushed++
				result.Quantity += quantity
			case outcomeSkipped:
				result.Skipped++
			case outcomeFailed:
				result.Failed++
			}
		})
	}

	wg.Wait()
}

// flushOutcome is what happened to one total.
type flushOutcome uint8

const (
	outcomeFailed flushOutcome = iota
	outcomeFlushed
	outcomeSkipped
)

// flushOne posts one total and settles it.
func (f *Flusher) flushOne(ctx context.Context, total *Total) (outcome flushOutcome, flushed int64) {
	ctx, op := f.o11y.Begin(ctx)
	defer op.End()

	delta := total.Delta()

	op.SetValues(map[string]any{
		subjectKey:     total.Subject,
		meterKey:       total.Meter,
		periodStartKey: total.PeriodStart,
		periodEndKey:   total.PeriodEnd,
		aggregationKey: string(total.Aggregation),
		sequenceKey:    total.FlushSequence,
		deltaKey:       delta,
		attemptsKey:    total.FlushAttempts,
	})

	if delta <= 0 {
		// Claimed on a predicate that said otherwise, so something settled it
		// between the select and the read. Settling it again would advance the
		// sequence for a post that never happened, which would make the next
		// genuine post's key distinct from the one a retry would use.
		return outcomeSkipped, 0
	}

	ref, err := f.mapper.ProviderRefFor(ctx, total.Subject, total.Meter)
	if err != nil {
		if errors.Is(err, ErrNoProviderRef) {
			// Not billable, and not a failure. Settled rather than retried, so the
			// pass does not re-claim it every interval forever — a free-plan
			// subject on a metered endpoint would otherwise be the permanent head
			// of the flush queue.
			return f.settle(ctx, op, total, delta, true), 0
		}

		f.fail(ctx, op, total, err)

		return outcomeFailed, 0
	}

	// A wholly zero ref is read as the ErrNoProviderRef the mapper did not bother
	// to return: nothing to post, settled rather than retried.
	//
	// A half-filled one is not. A customer with no meter, or a meter with no
	// customer, is a mapper bug rather than a free plan, and letting it through to
	// the reporter's own validation makes it a visible failure instead of usage
	// that quietly stops being billed.
	if ref == (ProviderRef{}) {
		return f.settle(ctx, op, total, delta, true), 0
	}

	if err = f.report(ctx, total, ref, delta); err != nil {
		f.fail(ctx, op, total, err)

		return outcomeFailed, 0
	}

	return f.settle(ctx, op, total, delta, false), delta
}

// report posts one delta to the provider under a deterministic idempotency key.
func (f *Flusher) report(ctx context.Context, total *Total, ref ProviderRef, delta int64) (err error) {
	ctx, op := f.o11y.Begin(ctx, observability.WithValue(meterKey, total.Meter))
	defer op.End()

	// Bounded so a provider that hangs cannot hold the lease past its expiry and
	// let a second flusher start posting the same delta. The config validation
	// enforces LeaseDuration > FlushTimeout for exactly this reason.
	ctx, cancel := context.WithTimeout(ctx, f.cfg.FlushTimeout)
	defer cancel()

	defer op.Time(ctx, f.clock, f.postHist, meterAttr(total.Meter))()

	// A provider SDK is third-party code on the money path. A panic in it would
	// otherwise take down the goroutine, and with it every other total in the
	// batch — turning one malformed customer record into a stalled flush for the
	// whole fleet.
	defer func() {
		if recovered := recover(); recovered != nil {
			err = platformerrors.Wrapf(ErrFlusherPanicked, "%v", recovered)
		}
	}()

	return f.reporter.ReportUsage(ctx, &capitalism.UsageReportInput{
		CustomerID: ref.CustomerID,
		MeterName:  ref.MeterName,
		Quantity:   delta,
		// The period's own end, not the wall clock. A pass that runs a minute
		// after a period closed still belongs to that period, and providers place
		// a usage record by its timestamp.
		OccurredAt:     f.reportTimestamp(total),
		IdempotencyKey: FlushIdempotencyKey(total),
		Metadata: map[string]string{
			"metering_meter":        total.Meter,
			"metering_subject":      total.Subject,
			"metering_period_start": total.PeriodStart.UTC().Format(time.RFC3339),
			"metering_sequence":     strconv.Itoa(total.FlushSequence),
		},
	})
}

// reportTimestamp is the instant a post is dated.
//
// Inside the period, and never in the future. A provider rejects a usage record
// dated ahead of now, and one dated after the period has closed lands on the next
// invoice — so a period still running is stamped now, and a period that has ended
// is stamped just inside its own final instant.
func (f *Flusher) reportTimestamp(total *Total) time.Time {
	now := f.clock.Now().UTC()
	if now.Before(total.PeriodEnd) {
		return now
	}

	return total.PeriodEnd.Add(-time.Second)
}

// settle records a successful post — or a deliberate skip — against the total.
func (f *Flusher) settle(
	ctx context.Context,
	op observability.Operation,
	total *Total,
	delta int64,
	skipped bool,
) flushOutcome {
	if err := f.store.MarkFlushed(ctx, total, total.Quantity, f.clock.Now().UTC()); err != nil {
		// The provider has the usage and the row does not say so. The next pass
		// re-claims the total, posts the same delta under the same sequence — the
		// sequence did not advance, because this is what failed — and the provider
		// deduplicates it. That is the whole reason the key is derived from the
		// sequence rather than from the attempt.
		f.failedCounter.Add(ctx, 1, meterAttr(total.Meter))
		op.Acknowledge(err, "settling metering flush")

		return outcomeFailed
	}

	if skipped {
		f.skippedCounter.Add(ctx, 1, meterAttr(total.Meter))

		return outcomeSkipped
	}

	f.flushedCounter.Add(ctx, 1, meterAttr(total.Meter))
	f.quantityCounter.Add(ctx, delta, meterAttr(total.Meter))

	return outcomeFlushed
}

// fail returns a total to the flushable set, or abandons it once it has spent its
// attempts.
func (f *Flusher) fail(ctx context.Context, op observability.Operation, total *Total, cause error) {
	f.failedCounter.Add(ctx, 1, meterAttr(total.Meter))

	terminal := total.FlushAttempts >= f.cfg.MaxAttempts

	op.Set(terminalKey, terminal)

	if terminal {
		// Left where it is rather than marked flushed. Marking it would discard
		// usage nobody has been billed for; leaving it keeps the row in the
		// backlog gauge and the money recoverable by hand once whatever broke is
		// fixed. The claim predicate excludes it from now on, so it stops costing
		// a provider call every interval.
		f.abandonCounter.Add(ctx, 1, meterAttr(total.Meter))
		f.o11y.Logger().WithValues(map[string]any{
			subjectKey:     total.Subject,
			meterKey:       total.Meter,
			periodStartKey: total.PeriodStart,
			periodEndKey:   total.PeriodEnd,
			aggregationKey: string(total.Aggregation),
			attemptsKey:    total.FlushAttempts,
		}).Error("abandoning metering flush after exhausting attempts; usage is recorded but unbilled", cause)
	}

	nextFlush := f.clock.Now().UTC().Add(f.backoff(total.FlushAttempts))

	if err := f.store.ReleaseFlush(ctx, total, truncateError(cause), nextFlush); err != nil {
		// The lease simply expires instead. Slower than an explicit release, and
		// the total is picked up again either way.
		op.Acknowledge(err, "releasing metering flush lease")
	}
}

// backoff is how long a failed total waits, with full jitter.
//
// Full jitter rather than the equal jitter retry.Execute sleeps with, because
// this schedule is written into a row and read by a fleet. Without spreading, a
// provider outage synchronizes every replica's retries onto the same instant, and
// the flush that recovers is the one that arrives as a thundering herd.
func (f *Flusher) backoff(attempts int) time.Duration {
	delay := retrycfg.DelayFor(f.cfg.Backoff, uint(max(1, attempts)))

	//nolint:gosec // jitter, not cryptography
	return time.Duration(rand.Int64N(int64(delay)) + 1)
}

// FlushIdempotencyKey is the key one post carries to the provider.
//
// It is derived from the subject, meter, period, and sequence, so it is exactly
// as stable as the post it identifies: a retry of the same post computes the same
// key and the provider ignores the duplicate, while the next post — after the
// sequence advances — computes a different one and is accepted.
//
// It is hashed rather than concatenated because a subject ID is an application's
// own identifier and may be anything: long, non-ASCII, or an email address.
// Providers cap the key at 255 bytes and one that overflowed would be truncated
// into a collision with a different subject's — which is to say, one customer's
// usage silently discarded as a duplicate of another's.
//
// Exported so that an operator reconciling an invoice by hand can compute the key
// a given post would have used, which is otherwise a question with no answer.
func FlushIdempotencyKey(total *Total) string {
	if total == nil {
		return ""
	}

	sum := sha256.Sum256([]byte(
		total.Subject + "\x00" + total.Meter + "\x00" +
			strconv.FormatInt(total.PeriodStart.UTC().Unix(), 10) + "\x00" +
			strconv.Itoa(total.FlushSequence),
	))

	return idempotencyKeyPrefix + base64.RawURLEncoding.EncodeToString(sum[:])
}

// truncateError renders an error for storage, bounded.
//
// Truncated on a rune boundary, not a byte one. The stored string goes into a
// text column and out again through a JSON encoder, and half a multi-byte rune is
// invalid UTF-8 that some encoders refuse and others silently replace.
func truncateError(err error) string {
	if err == nil {
		return ""
	}

	rendered := err.Error()
	if len(rendered) <= maxStoredErrorLength {
		return rendered
	}

	cut := maxStoredErrorLength
	for cut > 0 && !utf8.RuneStart(rendered[cut]) {
		cut--
	}

	return rendered[:cut]
}

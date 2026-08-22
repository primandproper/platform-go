package webhooks

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"sync"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	cbnoop "github.com/primandproper/platform-go/v13/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/cryptography/requestsigning"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/httpclient"
	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/retry"
	retrycfg "github.com/primandproper/platform-go/v13/retry/config"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// ErrCircuitOpen indicates a delivery skipped because the endpoint's circuit
// breaker is open. It is a failure for retry purposes but is deliberately not
// counted against the attempt budget — see deliver.
var ErrCircuitOpen = platformerrors.Wrap(circuitbreaking.ErrCircuitBroken, "webhook endpoint circuit is open")

// ErrNonSuccessStatus indicates a subscriber that answered with something other
// than 2xx.
var ErrNonSuccessStatus = platformerrors.New("webhook endpoint returned a non-success status")

// maxResponseBodyBytes bounds how much of a subscriber's response body is read.
//
// The body is read at all only so the connection can be reused — an unread body
// leaves the connection unusable for keep-alive, which would defeat the shared
// client this package exists partly to introduce. Nothing in the response is
// interpreted, so the cap can be small; what it prevents is a hostile
// subscriber streaming gigabytes into a worker that only wanted to hang up.
const maxResponseBodyBytes = 8 * 1024

// CircuitBreakerFactory builds the circuit breaker guarding one endpoint. It is
// called at most once per endpoint per worker, lazily, and the result is
// retained.
//
// It is a factory rather than a fixed map because endpoints are registered at
// runtime — the set is not known when the worker is constructed, so
// partitioned.KeyedCircuitBreaker's operator-chosen key list does not fit.
// Cardinality is bounded by the endpoints table, which is bounded by however
// many subscribers an operator has accepted.
type CircuitBreakerFactory func(endpointID string) (circuitbreaking.CircuitBreaker, error)

// Worker delivers claimed dispatches. It owns a goroutine started by Run and
// stopped by Close.
type Worker struct {
	store    Store
	client   *http.Client
	clock    clock.Clock
	o11y     observability.Observer
	breaker  CircuitBreakerFactory
	checkURL URLChecker

	breakers map[string]circuitbreaking.CircuitBreaker

	stop chan struct{}
	done chan struct{}

	sentCounter       metrics.Int64Counter
	failedCounter     metrics.Int64Counter
	deadCounter       metrics.Int64Counter
	shortCircuitedCtr metrics.Int64Counter
	claimErrCounter   metrics.Int64Counter
	reapedCounter     metrics.Int64Counter
	backlogGauge      metrics.Int64Gauge
	backlogAgeGauge   metrics.Int64Gauge
	deliveryHist      metrics.Float64Histogram
	cycleHist         metrics.Float64Histogram
	batchHist         metrics.Float64Histogram

	// What the options wrote, kept only until the observer is built from it.
	// Read w.o11y.Logger() for the logger this worker actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	cfg WorkerConfig

	breakersMu sync.Mutex
	stopOnce   sync.Once
}

// NewWorker builds a Worker. It does not start it; call Run.
//
// ctx is used to validate the config and is not retained — Run takes its own.
func NewWorker(ctx context.Context, cfg *WorkerConfig, store Store, opts ...WorkerOption) (*Worker, error) {
	if cfg == nil {
		return nil, platformerrors.New("nil webhooks worker config provided")
	}

	if store == nil {
		return nil, ErrNilStore
	}

	cfg.EnsureDefaults()

	w := &Worker{
		cfg:      *cfg,
		store:    store,
		clock:    clock.NewClock(),
		breakers: map[string]circuitbreaking.CircuitBreaker{},
		checkURL: CheckEndpointURL,
		breaker: func(string) (circuitbreaking.CircuitBreaker, error) {
			return cbnoop.NewCircuitBreaker(), nil
		},
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(w)
		}
	}

	if err := w.cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating webhooks worker config")
	}

	w.o11y = observability.NewObserver(serviceName, w.logger, w.tracerProvider)

	if w.client == nil {
		client, err := httpclient.NewHTTPClient(
			httpclient.WithTimeout(w.cfg.RequestTimeout),
			httpclient.WithTracing(true),
			httpclient.WithLogger(w.o11y.Logger()),
			httpclient.WithTracerProvider(w.tracerProvider),
			httpclient.WithMetricsProvider(w.metricsProvider),
		)
		if err != nil {
			return nil, platformerrors.Wrap(err, "building the delivery HTTP client")
		}

		w.client = client
	}

	// Applied to a supplied client as well as a built one: refusing redirects is
	// a security property of this package, not a default a caller opts into.
	w.client.CheckRedirect = refuseRedirects

	mp := metrics.EnsureMetricsProvider(w.metricsProvider)

	var err error
	if w.sentCounter, err = mp.NewInt64Counter(serviceName + "_deliveries_sent"); err != nil {
		return nil, platformerrors.Wrap(err, "creating deliveries sent counter")
	}
	if w.failedCounter, err = mp.NewInt64Counter(serviceName + "_deliveries_failed"); err != nil {
		return nil, platformerrors.Wrap(err, "creating deliveries failed counter")
	}
	if w.deadCounter, err = mp.NewInt64Counter(serviceName + "_deliveries_dead"); err != nil {
		return nil, platformerrors.Wrap(err, "creating deliveries dead counter")
	}
	if w.shortCircuitedCtr, err = mp.NewInt64Counter(serviceName + "_deliveries_short_circuited"); err != nil {
		return nil, platformerrors.Wrap(err, "creating deliveries short circuited counter")
	}
	if w.claimErrCounter, err = mp.NewInt64Counter(serviceName + "_claim_errors"); err != nil {
		return nil, platformerrors.Wrap(err, "creating claim error counter")
	}
	if w.reapedCounter, err = mp.NewInt64Counter(serviceName + "_dispatches_reaped"); err != nil {
		return nil, platformerrors.Wrap(err, "creating dispatches reaped counter")
	}
	if w.backlogGauge, err = mp.NewInt64Gauge(serviceName + "_backlog_depth"); err != nil {
		return nil, platformerrors.Wrap(err, "creating backlog depth gauge")
	}
	if w.backlogAgeGauge, err = mp.NewInt64Gauge(serviceName + "_backlog_age_seconds"); err != nil {
		return nil, platformerrors.Wrap(err, "creating backlog age gauge")
	}
	if w.deliveryHist, err = mp.NewFloat64Histogram(serviceName + "_delivery_latency_ms"); err != nil {
		return nil, platformerrors.Wrap(err, "creating delivery latency histogram")
	}
	if w.cycleHist, err = mp.NewFloat64Histogram(serviceName + "_cycle_latency_ms"); err != nil {
		return nil, platformerrors.Wrap(err, "creating cycle latency histogram")
	}
	if w.batchHist, err = mp.NewFloat64Histogram(serviceName + "_claimed_batch_size"); err != nil {
		return nil, platformerrors.Wrap(err, "creating claimed batch size histogram")
	}

	return w, nil
}

// refuseRedirects stops the client from following a redirect.
//
// A 3xx from a subscriber is treated as a failed delivery rather than as a
// destination. Following it would send a signed payload — and the headers that
// authenticate it — to a host that was never registered and never passed
// CheckEndpointURL, so an open redirect on any subscriber's domain would become
// a way to point this worker at anything.
func refuseRedirects(*http.Request, []*http.Request) error {
	return http.ErrUseLastResponse
}

// Run is the worker loop. Like outbox.Relay.Run it takes no context: tied to a
// server context it would stop delivering while requests were still committing
// dispatch rows. The owner calls Close after the server has shut down.
//
// Run returns only after Close.
func (w *Worker) Run() {
	defer close(w.done)

	ctx := context.Background()

	pollTicker := w.clock.NewTicker(w.cfg.PollInterval)
	defer pollTicker.Stop()

	reapTicker := w.clock.NewTicker(w.cfg.ReapInterval)
	defer reapTicker.Stop()

	for {
		select {
		case <-w.stop:
			// One last cycle, so dispatches committed just before shutdown are
			// not left sitting until the next process starts.
			w.cycle(ctx)

			return
		case <-pollTicker.Chan():
			w.cycle(ctx)
		case <-reapTicker.Chan():
			w.reap(ctx)
			// Sampled on the reap tick rather than every poll: it is an
			// aggregate over undelivered rows, and at poll cadence it would cost
			// more than the work it reports on.
			w.sampleBacklog(ctx)
		}
	}
}

// Close stops the worker and waits for the in-flight cycle to finish. Safe to
// call more than once.
func (w *Worker) Close(ctx context.Context) error {
	_, op := w.o11y.Begin(ctx)
	defer op.End()

	w.stopOnce.Do(func() { close(w.stop) })

	select {
	case <-w.done:
	case <-ctx.Done():
		return op.Error(ctx.Err(), "waiting for webhooks worker to drain")
	}

	w.client.CloseIdleConnections()

	return nil
}

// cycle claims one batch and delivers it. Errors are logged and counted rather
// than returned: there is no caller to hand them to, and the next cycle retries.
func (w *Worker) cycle(ctx context.Context) {
	// Through the clock, like every other time this package reads. Not through
	// op.Time, because the window it measures opens before the span does: a
	// cycle that claims nothing records no duration at all, so the Begin below
	// cannot come first without giving every idle poll a span.
	startTime := w.clock.Now()

	now := w.clock.Now().UTC()

	claimed, err := w.store.Claim(ctx, now, w.cfg.BatchSize, now.Add(w.cfg.LeaseDuration))
	if err != nil {
		w.claimErrCounter.Add(ctx, 1)
		w.o11y.Logger().Error("claiming webhook dispatches", err)

		return
	}

	if len(claimed) == 0 {
		return
	}

	w.batchHist.Record(ctx, float64(len(claimed)))
	defer func() {
		w.cycleHist.Record(ctx, float64(w.clock.Since(startTime).Milliseconds()))
	}()

	ctx, op := w.o11y.Begin(ctx, observability.WithValue(claimedKey, len(claimed)))
	defer op.End()

	// Deliveries run concurrently up to Concurrency, because a batch is
	// dominated by network round trips to unrelated hosts and running it
	// serially would make one slow subscriber set the pace for the whole batch.
	//
	// The claim predicate admits at most one dispatch per (endpoint, ordering
	// key) per batch, so nothing in a batch is ordered relative to anything else
	// in it and running them concurrently cannot reorder a key.
	sem := make(chan struct{}, w.cfg.Concurrency)

	var wg sync.WaitGroup

	for i := range claimed {
		sem <- struct{}{}

		wg.Go(func() {
			defer func() { <-sem }()

			w.handle(ctx, &claimed[i])
		})
	}

	wg.Wait()
}

// handle delivers one dispatch and records the outcome.
func (w *Worker) handle(ctx context.Context, dispatch *ClaimedDispatch) {
	attempt, err := w.deliver(ctx, dispatch)

	// The attempt is recorded whatever happened, including a short circuit —
	// the log is what an operator reads to explain a gap, and "we did not try,
	// because the circuit was open" is the most confusing gap to encounter
	// without a record of it.
	if attempt != nil {
		if recordErr := w.store.RecordAttempt(ctx, attempt); recordErr != nil {
			w.o11y.Logger().WithValue(deliveryIDKey, dispatch.DeliveryID).
				Error("recording webhook delivery attempt", recordErr)
		}
	}

	if err == nil {
		w.sentCounter.Add(ctx, 1, eventTypeAttr(dispatch.EventType))

		if markErr := w.store.MarkDelivered(ctx, dispatch.ID, w.clock.Now().UTC()); markErr != nil {
			// The subscriber has the payload but the row still looks pending.
			// The next cycle redelivers it — this is precisely the at-least-once
			// window the package documentation describes.
			w.o11y.Logger().WithValue(dispatchIDKey, dispatch.ID).
				Error("marking webhook dispatch delivered", markErr)
		}

		return
	}

	w.recordFailure(ctx, dispatch, err)
}

// deliver issues one request.
//
// It carries its own span: the subscriber round trip is where a cycle spends its
// time, and a single span over the whole batch cannot say which endpoint is
// slow.
func (w *Worker) deliver(ctx context.Context, dispatch *ClaimedDispatch) (*Attempt, error) {
	ctx, op := w.o11y.Begin(ctx,
		observability.WithValue(dispatchIDKey, dispatch.ID),
		observability.WithValue(deliveryIDKey, dispatch.DeliveryID),
		observability.WithValue(endpointIDKey, dispatch.EndpointID),
		observability.WithValue(scopeKey, dispatch.Scope.String()),
		observability.WithValue(eventTypeKey, dispatch.EventType.String()),
		observability.WithValue(attemptsKey, dispatch.Attempts),
		observability.WithSpanValue(endpointURLKey, dispatch.Endpoint.URL),
	)
	defer op.End()

	if dispatch.OrderingKey != "" {
		op.Set(orderingKeyKey, dispatch.OrderingKey)
	}

	breaker, err := w.breakerFor(dispatch.EndpointID)
	if err != nil {
		return nil, op.Error(err, "resolving circuit breaker")
	}

	attempt := &Attempt{
		ID:           identifiers.New(),
		DeliveryID:   dispatch.DeliveryID,
		EndpointID:   dispatch.EndpointID,
		AttemptCount: dispatch.Attempts,
		AttemptedAt:  w.clock.Now().UTC(),
	}

	if breaker.CannotProceed() {
		op.Set(circuitOpenKey, true)
		w.shortCircuitedCtr.Add(ctx, 1, endpointAttr(dispatch.EndpointID))

		attempt.Error = ErrCircuitOpen.Error()

		return attempt, ErrCircuitOpen
	}

	// Re-checked at delivery, not only at registration. DNS is mutable: a name
	// that resolved publicly when it was registered can resolve to 127.0.0.1 by
	// the time the worker dials it.
	if err = w.checkURL(ctx, dispatch.Endpoint.URL); err != nil {
		// Terminal, not retryable. A URL that is no longer a legal target will
		// not become one by waiting, and continuing to resolve it every backoff
		// interval is a slow scan of the internal network.
		attempt.Error = truncateError(err)

		return attempt, platformerrors.Wrap(retry.Unretryable(err), "endpoint URL is no longer deliverable")
	}

	req, err := w.buildRequest(ctx, dispatch)
	if err != nil {
		attempt.Error = truncateError(err)

		// Also terminal: a request that cannot be built — an endpoint with no
		// secret, an unparseable URL — fails identically every time.
		return attempt, retry.Unretryable(err)
	}

	startTime := w.clock.Now()

	// G704: the URL is user-supplied by design — that is what a webhook endpoint
	// is. w.checkURL ran immediately above and the client refuses redirects, so
	// the target has been vetted as far as this package can vet it; see
	// CheckEndpointURL for what that does and does not cover.
	res, err := w.client.Do(req) //nolint:gosec // G704: endpoint URL is vetted by w.checkURL above

	attempt.Duration = w.clock.Since(startTime)
	w.deliveryHist.Record(ctx, float64(attempt.Duration.Milliseconds()), endpointAttr(dispatch.EndpointID))

	if err != nil {
		breaker.Failed()
		attempt.Error = truncateError(err)

		return attempt, op.Error(err, "delivering webhook")
	}

	defer func() {
		// Drained before closing so the connection can go back to the pool; an
		// unread body makes it unusable for keep-alive.
		// Drained for connection reuse alone, so a failure to drain is not worth
		// reporting: the body is discarded either way and the only cost is that
		// this connection does not go back to the pool.
		if _, copyErr := io.Copy(io.Discard, io.LimitReader(res.Body, maxResponseBodyBytes)); copyErr != nil {
			op.Acknowledge(copyErr, "draining webhook response body")
		}

		if closeErr := res.Body.Close(); closeErr != nil {
			op.Acknowledge(closeErr, "closing webhook response body")
		}
	}()

	attempt.StatusCode = res.StatusCode
	op.Set(statusCodeKey, res.StatusCode)

	if !successfulStatus(res.StatusCode) {
		breaker.Failed()

		statusErr := platformerrors.Wrapf(ErrNonSuccessStatus, "status %d from %s", res.StatusCode, dispatch.Endpoint.URL)
		attempt.Error = truncateError(statusErr)

		// A 4xx other than 408 or 429 says the subscriber understood the request
		// and rejected it. Retrying an unauthorized or malformed delivery
		// twenty-five times changes nothing and spends the budget that a
		// genuinely transient failure would have needed.
		if terminalStatus(res.StatusCode) {
			return attempt, retry.Unretryable(statusErr)
		}

		return attempt, statusErr
	}

	breaker.Succeeded()

	return attempt, nil
}

// buildRequest renders the signed HTTP request for one dispatch.
func (w *Worker) buildRequest(ctx context.Context, dispatch *ClaimedDispatch) (*http.Request, error) {
	endpoint := dispatch.Endpoint

	signedAt := w.clock.Now().UTC()

	// Signed over exactly the bytes that become the body. The payload is stored
	// and carried as raw bytes precisely so that nothing between dispatch and
	// delivery re-serializes it — a signature over a re-marshaled body covers
	// something the subscriber never received.
	signature, err := requestsigning.Sign(endpoint.Secret, dispatch.Payload, signedAt)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.URL, bytes.NewReader(dispatch.Payload))
	if err != nil {
		return nil, platformerrors.Wrapf(err, "building request for webhook endpoint %q", endpoint.ID)
	}

	// Static headers first, so nothing below can be overwritten by them even if
	// a Store handed back an endpoint this package never validated.
	endpoint.applyHeaders(req.Header)

	contentType := endpoint.ContentType
	if contentType == "" {
		contentType = DefaultContentType
	}

	req.Header.Set("Content-Type", contentType)
	req.Header.Set(requestsigning.SignatureHeader, signature)
	req.Header.Set(requestsigning.TimestampHeader, strconv.FormatInt(signedAt.Unix(), 10))
	req.Header.Set(EventTypeHeader, dispatch.EventType.String())
	req.Header.Set(DeliveryIDHeader, dispatch.DeliveryID)
	req.Header.Set(AttemptHeader, strconv.Itoa(dispatch.Attempts))
	req.Header.Set("User-Agent", w.cfg.UserAgent)

	return req, nil
}

// terminalStatus reports whether a status means "do not bother trying again".
//
// 4xx is the subscriber saying it understood and refused. The exceptions are
// 408 Request Timeout and 429 Too Many Requests, which both explicitly invite a
// later attempt.
func terminalStatus(code int) bool {
	if code == http.StatusRequestTimeout || code == http.StatusTooManyRequests {
		return false
	}

	return code >= 400 && code < 500
}

// recordFailure releases the lease, schedules the retry, and marks the dispatch
// dead once it has exhausted its attempts. A dead dispatch is skipped by every
// future claim, so one permanently broken subscriber cannot block the ordering
// key behind it forever.
func (w *Worker) recordFailure(ctx context.Context, dispatch *ClaimedDispatch, cause error) {
	w.failedCounter.Add(ctx, 1, endpointAttr(dispatch.EndpointID))

	// A short circuit is not the dispatch's fault and must not consume its
	// budget: an endpoint that is down for an hour would otherwise kill every
	// delivery queued behind it, and they would all need replaying by hand once
	// it recovered. The attempt count is rolled back to what it was before this
	// claim incremented it — persisted, not merely used for the backoff — and
	// the row simply waits for the breaker.
	shortCircuited := errors.Is(cause, ErrCircuitOpen)

	attempts := dispatch.Attempts
	if shortCircuited {
		attempts--
	}

	dead := !shortCircuited &&
		(errors.Is(cause, retry.ErrUnretryable) || uint(dispatch.Attempts) >= w.cfg.Backoff.MaxAttempts)

	nextAttempt := w.clock.Now().UTC().Add(retrycfg.ScheduledDelayFor(w.cfg.Backoff, attempts))
	if shortCircuited {
		// Retried on the breaker's own timescale rather than the delivery's:
		// backing off exponentially against an open circuit means the first
		// delivery after recovery can wait far longer than the outage did.
		nextAttempt = w.clock.Now().UTC().Add(w.cfg.CircuitOpenRetryDelay)
	}

	logger := w.o11y.Logger().WithValues(map[string]any{
		dispatchIDKey:  dispatch.ID,
		deliveryIDKey:  dispatch.DeliveryID,
		endpointIDKey:  dispatch.EndpointID,
		scopeKey:       dispatch.Scope.String(),
		eventTypeKey:   dispatch.EventType.String(),
		orderingKeyKey: dispatch.OrderingKey,
		attemptsKey:    dispatch.Attempts,
	})

	if err := w.store.RecordFailure(ctx, dispatch.ID, attempts, nextAttempt, truncateError(cause), dead); err != nil {
		// The lease still expires on its own, so the dispatch is retried
		// regardless — just later than intended.
		logger.Error("recording webhook delivery failure", err)

		return
	}

	if dead {
		w.deadCounter.Add(ctx, 1, endpointAttr(dispatch.EndpointID))
		logger.Error("webhook dispatch is dead after exhausting attempts", cause)

		return
	}

	logger.WithValue("next_attempt", nextAttempt).Info("webhook delivery failed, retry scheduled")
}

// breakerFor resolves and caches one circuit breaker per endpoint.
func (w *Worker) breakerFor(endpointID string) (circuitbreaking.CircuitBreaker, error) {
	w.breakersMu.Lock()
	defer w.breakersMu.Unlock()

	if breaker, ok := w.breakers[endpointID]; ok {
		return breaker, nil
	}

	breaker, err := w.breaker(endpointID)
	if err != nil {
		return nil, platformerrors.Wrapf(err, "building circuit breaker for webhook endpoint %q", endpointID)
	}

	if breaker == nil {
		breaker = cbnoop.NewCircuitBreaker()
	}

	w.breakers[endpointID] = breaker

	return breaker, nil
}

// sampleBacklog records how far behind the worker is. These two gauges are the
// package's primary health signal: every other instrument is a rate or a
// latency, and none of them can distinguish "delivering steadily" from
// "delivering steadily while falling further behind".
func (w *Worker) sampleBacklog(ctx context.Context) {
	ctx, op := w.o11y.Begin(ctx)
	defer op.End()

	depth, oldest, err := w.store.Backlog(ctx)
	if err != nil {
		op.Acknowledge(err, "sampling webhook backlog")

		return
	}

	// An empty backlog reports an age of zero rather than no age at all, so a
	// drained queue actively resets the gauge instead of leaving a stale reading
	// on the dashboard.
	var ageSeconds int64
	if !oldest.IsZero() {
		if age := w.clock.Since(oldest); age > 0 {
			ageSeconds = int64(age.Seconds())
		}
	}

	w.backlogGauge.Record(ctx, depth)
	w.backlogAgeGauge.Record(ctx, ageSeconds)

	op.SetValues(map[string]any{
		backlogDepthKey: depth,
		backlogAgeKey:   ageSeconds,
	})
}

// reap deletes delivered dispatches past the retention window.
func (w *Worker) reap(ctx context.Context) {
	ctx, op := w.o11y.Begin(ctx)
	defer op.End()

	before := w.clock.Now().UTC().Add(-w.cfg.Retention)

	reaped, err := w.store.Reap(ctx, before, w.cfg.ReapBatchSize)
	if err != nil {
		op.Acknowledge(err, "reaping delivered webhook dispatches")

		return
	}

	op.Set(reapedKey, reaped)

	if reaped > 0 {
		w.reapedCounter.Add(ctx, reaped)
		op.Logger().Debug("reaped delivered webhook dispatches")
	}
}

// eventTypeAttr labels a measurement with its event type. One dispatcher serves
// every event, so without this the counters collapse into a single number and
// an event whose subscribers are all failing is invisible in the total. Event
// types come from the Catalog and are therefore low-cardinality by construction,
// which is what makes this safe as a metric dimension.
func eventTypeAttr(eventType EventType) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String(eventTypeKey, eventType.String()))
}

// EndpointAttributeKey is the attribute name this package labels per-endpoint
// measurements with.
//
// It is exported so that instruments configured alongside a Worker — a circuit
// breaker's own counters, most obviously — can tag themselves identically. An
// endpoint that keeps tripping its breaker and an endpoint whose deliveries
// keep failing are the same endpoint, and a dashboard can only join those two
// series if both agree on the label.
const EndpointAttributeKey = endpointIDKey

// endpointAttr labels a measurement with its endpoint.
//
// Endpoint cardinality is bounded by the endpoints table rather than by the
// Catalog, so this is the one dimension here that can grow with tenants. It is
// still worth carrying: the questions these counters exist to answer — which
// subscriber is failing, which one is being short-circuited — cannot be asked
// without it. An operator with enough endpoints to care should drop the
// attribute in their collector rather than lose the distinction at the source.
func endpointAttr(endpointID string) metric.MeasurementOption {
	return metric.WithAttributes(attribute.String(endpointIDKey, endpointID))
}

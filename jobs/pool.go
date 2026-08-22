package jobs

import (
	"context"
	"fmt"
	"sync"

	"github.com/primandproper/platform-go/v13/clock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/messagequeue"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/panicking"
	"github.com/primandproper/platform-go/v13/retry"
	retrycfg "github.com/primandproper/platform-go/v13/retry/config"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const (
	// poolServiceName names the Pool's spans, logger, and metrics.
	poolServiceName = "jobs_pool"

	// DefaultConcurrency is how many messages a Pool handles at once when
	// Concurrency is not set.
	DefaultConcurrency = 8
)

// Observability keys for the Pool's spans and log fields, namespaced so they
// cannot collide with another component writing to the same trace. Keys that
// are not specific to this package — the topic — come from observability/keys.
const (
	attemptsKey    = "jobs.attempts"
	payloadSizeKey = "jobs.payload_bytes"
	concurrencyKey = "jobs.concurrency"
	panicStackKey  = "jobs.panic_stack"
)

// message is one payload in flight between the consumer and a worker, together
// with the span context of the consume that produced it.
//
// It travels by pointer: a trace.SpanContext is 80 bytes on its own, so copying
// the envelope through the channel and again into each callee would cost more
// than the single allocation submit already makes for the payload.
type message struct {
	payload []byte
	origin  trace.SpanContext
}

// linkToOrigin renders the consume span as a span link, or nothing at all when
// the consumer was not tracing. An invalid link is not inert — it produces a
// span with a dangling reference — so it is omitted rather than attached.
func (m *message) linkToOrigin() []trace.SpanStartOption {
	if !m.origin.IsValid() {
		return nil
	}

	return []trace.SpanStartOption{trace.WithLinks(trace.Link{SpanContext: m.origin})}
}

// Handler processes one consumed message. A returned error means the message
// will be retried, up to the Pool's configured attempts, after which it is
// dead-lettered.
//
// Wrap an error with retry.Unretryable to skip the remaining attempts and go
// straight to the dead-letter path — a payload that fails to parse will fail to
// parse three more times, and each of those attempts is latency a healthy
// message spends waiting behind it.
type Handler func(ctx context.Context, payload []byte) error

// Pool binds a messagequeue consumer to a handler with a bounded set of
// workers, per-message retry, and a dead-letter path. It owns the goroutines
// started by Run and stopped by Close.
type Pool struct {
	consumer messagequeue.Consumer
	handler  Handler
	deadLtr  DeadLetterFunc
	policy   retry.Policy
	clock    clock.Clock
	o11y     observability.Observer

	// workerCtx is the context every handler attempt derives from. It is not
	// the consumer's context and not a request context: it is canceled only
	// when Close gives up waiting for the drain, at which point telling the
	// in-flight handlers to stop is the only lever left.
	workerCtx    context.Context
	cancelWorker context.CancelFunc

	// work has no buffer, so a consumed message is handed straight to a free
	// worker. Buffering it would let the Pool acknowledge more messages than it
	// can currently process, and those extra messages are lost on a crash.
	//
	// It is never closed. Closing it would be the obvious way to retire the
	// workers, but a send on a closed channel panics even from inside a select
	// that also offers a ready alternative — so a consumer that calls its
	// handler once more after Consume has returned would take the process down.
	// noMoreWork retires the workers instead, and submit's select on stop is
	// what makes a late message an error rather than a crash.
	work       chan *message
	noMoreWork chan struct{}
	stop       chan struct{}
	done       chan struct{}

	receivedCounter    metrics.Int64Counter
	processedCounter   metrics.Int64Counter
	failedCounter      metrics.Int64Counter
	deadLetteredCount  metrics.Int64Counter
	droppedCounter     metrics.Int64Counter
	panicCounter       metrics.Int64Counter
	deadLetterFailures metrics.Int64Counter
	consumerErrCounter metrics.Int64Counter
	inFlightGauge      metrics.Int64UpDownCounter
	handlerHist        metrics.Float64Histogram
	messageHist        metrics.Float64Histogram
	waitHist           metrics.Float64Histogram

	// What the options wrote, kept only until the observer is built from it.
	// Read p.o11y.Logger() for the logger this pool actually uses; this one
	// may be nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	topicAttr metric.MeasurementOption

	cfg PoolConfig

	wg       sync.WaitGroup
	stopOnce sync.Once
}

// NewPool subscribes to cfg.Topic and returns a Pool bound to handler. It does
// not start consuming; call Run.
//
// ctx is used to establish the subscription and is not retained — the workers
// run on their own context so that a canceled setup context cannot silently
// stop the Pool later.
func NewPool(ctx context.Context, cfg *PoolConfig, provider messagequeue.ConsumerProvider, handler Handler, opts ...PoolOption) (*Pool, error) {
	if cfg == nil {
		return nil, platformerrors.New("nil job pool config provided")
	}
	if provider == nil {
		return nil, ErrNilConsumerProvider
	}
	if handler == nil {
		return nil, ErrNilHandler
	}

	cfg.EnsureDefaults()

	p := &Pool{
		cfg:        *cfg,
		handler:    handler,
		clock:      clock.NewClock(),
		work:       make(chan *message),
		noMoreWork: make(chan struct{}),
		stop:       make(chan struct{}),
		done:       make(chan struct{}),
		topicAttr:  metric.WithAttributes(attribute.String(keys.TopicKey, cfg.Topic)),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(p)
		}
	}

	if err := p.cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating job pool config")
	}

	p.workerCtx, p.cancelWorker = context.WithCancel(context.WithoutCancel(ctx))

	// A pool consumes exactly one topic, so the topic is stated here rather than
	// at each call site. Seeding the observer rather than only the logger is
	// what puts it on the pool's spans as well as its log lines.
	p.o11y = observability.NewObserverWithValues(poolServiceName, p.logger, p.tracerProvider,
		map[string]any{keys.TopicKey: p.cfg.Topic})

	if p.policy == nil {
		policy, err := retrycfg.NewExponentialBackoffPolicy(p.cfg.Retry,
			retrycfg.WithName(poolServiceName),
			retrycfg.WithLogger(p.logger),
			retrycfg.WithTracerProvider(p.tracerProvider),
			retrycfg.WithMetricsProvider(p.metricsProvider),
			retrycfg.WithClock(p.clock),
		)
		if err != nil {
			p.cancelWorker()

			return nil, err
		}

		p.policy = policy
	}

	if err := p.buildInstruments(); err != nil {
		p.cancelWorker()

		return nil, err
	}

	consumer, err := provider.NewConsumer(ctx, p.cfg.Topic, messagequeue.ConsumerFunc(p.submit))
	if err != nil {
		p.cancelWorker()

		return nil, platformerrors.Wrapf(err, "building consumer for topic %q", p.cfg.Topic)
	}
	p.consumer = consumer

	return p, nil
}

// buildInstruments creates the Pool's metrics up front, so a misconfigured
// meter fails the constructor rather than the first message.
func (p *Pool) buildInstruments() error {
	mp := metrics.EnsureMetricsProvider(p.metricsProvider)

	var err error
	if p.receivedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_messages_received", poolServiceName)); err != nil {
		return platformerrors.Wrap(err, "creating messages received counter")
	}
	if p.processedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_messages_processed", poolServiceName)); err != nil {
		return platformerrors.Wrap(err, "creating messages processed counter")
	}
	if p.failedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_attempts_failed", poolServiceName)); err != nil {
		return platformerrors.Wrap(err, "creating attempts failed counter")
	}
	if p.deadLetteredCount, err = mp.NewInt64Counter(fmt.Sprintf("%s_messages_dead_lettered", poolServiceName)); err != nil {
		return platformerrors.Wrap(err, "creating messages dead lettered counter")
	}
	if p.droppedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_messages_dropped", poolServiceName)); err != nil {
		return platformerrors.Wrap(err, "creating messages dropped counter")
	}
	if p.panicCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_handler_panics", poolServiceName)); err != nil {
		return platformerrors.Wrap(err, "creating handler panics counter")
	}
	if p.deadLetterFailures, err = mp.NewInt64Counter(fmt.Sprintf("%s_dead_letter_failures", poolServiceName)); err != nil {
		return platformerrors.Wrap(err, "creating dead letter failures counter")
	}
	if p.consumerErrCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_consumer_errors", poolServiceName)); err != nil {
		return platformerrors.Wrap(err, "creating consumer errors counter")
	}
	if p.inFlightGauge, err = mp.NewInt64UpDownCounter(fmt.Sprintf("%s_in_flight", poolServiceName)); err != nil {
		return platformerrors.Wrap(err, "creating in flight up down counter")
	}
	if p.handlerHist, err = mp.NewFloat64Histogram(fmt.Sprintf("%s_handler_latency_ms", poolServiceName)); err != nil {
		return platformerrors.Wrap(err, "creating handler latency histogram")
	}
	if p.messageHist, err = mp.NewFloat64Histogram(fmt.Sprintf("%s_message_latency_ms", poolServiceName)); err != nil {
		return platformerrors.Wrap(err, "creating message latency histogram")
	}
	if p.waitHist, err = mp.NewFloat64Histogram(fmt.Sprintf("%s_queue_wait_ms", poolServiceName)); err != nil {
		return platformerrors.Wrap(err, "creating queue wait histogram")
	}

	return nil
}

// Run starts the workers and the consumer, and blocks until Close.
//
// Like outbox.Relay.Run it takes no context. A background consumer tied to a
// request-scoped or server-scoped context stops mid-message when that context
// is canceled; the owner ends it explicitly with Close instead, which drains
// first.
func (p *Pool) Run() {
	defer close(p.done)

	p.wg.Add(p.cfg.Concurrency)
	for range p.cfg.Concurrency {
		go p.worker()
	}

	p.o11y.Logger().WithValue(concurrencyKey, p.cfg.Concurrency).Info("job pool started")

	errs := make(chan error, p.cfg.Concurrency)

	// The consumer gets its own cancellable context, derived from workerCtx
	// rather than being workerCtx: stopping the consumer must not cancel the
	// handlers still running, which is the whole point of the ordering below.
	consumeCtx, stopConsuming := context.WithCancel(p.workerCtx)
	defer stopConsuming()

	consumeDone := make(chan struct{})
	go func() {
		defer close(consumeDone)
		p.consumer.Consume(consumeCtx, errs)
	}()

	// The consumer reports transport-level failures here; a handler error never
	// reaches this channel, because submit hands the message off and returns nil
	// before the handler has run.
	//
	// The drain runs on its own goroutine, and keeps running through shutdown,
	// because a consumer whose error channel has backed up blocks inside its own
	// read loop — and would then never see the stop signal it is being asked to
	// act on. It ends on consumeDone rather than on a close of errs: errs was
	// handed to code this package does not own, and closing a channel out from
	// under a sender is how that turns into a panic.
	drained := make(chan struct{})
	go func() {
		defer close(drained)

		for {
			select {
			case err := <-errs:
				p.reportConsumerError(err)
			case <-consumeDone:
				// Consume has returned, so no further error can arrive and
				// whatever is buffered is the last of them.
				for {
					select {
					case err := <-errs:
						p.reportConsumerError(err)
					default:
						return
					}
				}
			}
		}
	}()

	<-p.stop

	// The order here is the drain. Stopping the consumer first is what makes it
	// safe to retire the workers at all: once Consume has returned there is no
	// sender left, so a worker that sees noMoreWork knows every message has
	// already been handed out — and the one it may still be holding is finished
	// before it exits.
	stopConsuming()
	<-consumeDone
	<-drained
	close(p.noMoreWork)
	p.wg.Wait()
}

// reportConsumerError records a transport-level failure the consumer surfaced.
func (p *Pool) reportConsumerError(err error) {
	p.consumerErrCounter.Add(p.workerCtx, 1, p.topicAttr)
	p.o11y.Logger().Error("consuming messages", err)
}

// Close stops the consumer, waits for the in-flight messages to finish, and
// returns. Safe to call more than once, and only meaningful after Run — there
// is nothing to drain before it, so Close would wait out its whole context.
//
// If ctx expires first, the worker context is canceled so that in-flight
// handlers and their backoff sleeps are told to stop, and the error is
// returned — but the goroutines are not waited on, so a handler that ignores
// its context may still be running when Close returns.
func (p *Pool) Close(ctx context.Context) error {
	_, op := p.o11y.Begin(ctx)
	defer op.End()

	p.stopOnce.Do(func() { close(p.stop) })

	select {
	case <-p.done:
		p.cancelWorker()

		return nil
	case <-ctx.Done():
		p.cancelWorker()

		return op.Error(ctx.Err(), "waiting for job pool to drain")
	}
}

// submit is the messagequeue handler. It hands the payload to a worker and
// returns nil without waiting for the result, which is the entire reason the
// Pool can run more than one message at a time: the consumer calls its handler
// serially, so anything that blocks here bounds throughput at one.
//
// The consequence is that the Pool, not the transport, owns the message from
// here on. For a transport that acknowledges when its handler returns, the
// message is acknowledged before it has been processed — so a crash loses
// whatever is in flight, and redelivery is not the safety net. The retry and
// dead-letter paths below are what replaces it.
//
// It returns an error only when the Pool is shutting down and the message
// cannot be placed, so that the transport does not treat it as handled.
func (p *Pool) submit(ctx context.Context, payload []byte) error {
	p.receivedCounter.Add(ctx, 1, p.topicAttr)

	// Copied because the payload belongs to the consumer, which is free to reuse
	// its buffer once its handler returns — and this one returns immediately,
	// while a worker still holds the slice.
	msg := &message{
		payload: make([]byte, len(payload)),
		// Carried so the handler's trace can point back at the consume that
		// produced it. Taken here because this is the last place the consumer's
		// context is in scope: the worker runs on the Pool's own context, and
		// without this the two halves of a message's life are unrelated traces.
		origin: trace.SpanContextFromContext(ctx),
	}
	copy(msg.payload, payload)

	queuedAt := p.clock.Now()

	select {
	case p.work <- msg:
		p.waitHist.Record(ctx, float64(p.clock.Since(queuedAt).Milliseconds()), p.topicAttr)

		return nil
	case <-p.stop:
		return platformerrors.New("job pool is shutting down")
	}
}

// worker handles messages until Run retires it, which happens only after the
// consumer has stopped and can therefore hand out no more.
func (p *Pool) worker() {
	defer p.wg.Done()

	for {
		select {
		case msg := <-p.work:
			p.process(msg)
		case <-p.noMoreWork:
			return
		}
	}
}

// process runs one message to a terminal state: handled, dead-lettered, or
// dropped. It never returns an error, because there is nobody to return one to.
//
// The consume span is attached as a link rather than as a parent. A parent
// would be wrong on its own terms: the consumer ends its span the moment submit
// returns, which is before the handler has started, so the child would outlive
// it. A link says what is actually true — this work was caused by that consume
// — and leaves both spans with honest durations.
func (p *Pool) process(msg *message) {
	ctx, op := p.o11y.BeginCustom(p.workerCtx, "handle_message", msg.linkToOrigin()...)
	defer op.End()

	op.Set(payloadSizeKey, len(msg.payload))

	p.inFlightGauge.Add(ctx, 1, p.topicAttr)
	defer p.inFlightGauge.Add(ctx, -1, p.topicAttr)

	startTime := p.clock.Now()

	var attempts uint
	err := p.policy.Execute(ctx, func(ctx context.Context) error {
		attempts++

		attemptErr := p.invoke(ctx, msg.payload, attempts)
		if attemptErr != nil {
			p.failedCounter.Add(ctx, 1, p.topicAttr)
		}

		return attemptErr
	})

	// Message latency, not handler latency: this includes every retry and the
	// backoff between them. The per-attempt cost is jobs_pool_handler_latency_ms,
	// recorded by invoke — a single histogram cannot be both, and conflating
	// them makes a healthy handler look slow the moment anything starts
	// retrying.
	p.messageHist.Record(ctx, float64(p.clock.Since(startTime).Milliseconds()), p.topicAttr)
	op.Set(attemptsKey, attempts)

	if err == nil {
		p.processedCounter.Add(ctx, 1, p.topicAttr)

		return
	}

	p.retire(ctx, op, msg.payload, attempts, err)
}

// invoke runs the handler once, containing a panic rather than letting it
// unwind the worker and take the process with it.
//
// A recovered panic is returned as an ordinary error, so it is retried like any
// other failure and ends at the same dead-letter path. Treating it as terminal
// instead would be defensible — a handler that panics is usually deterministic
// about it — but a nil dereference on a dependency that was briefly unavailable
// is not, and the attempt count bounds the cost of being wrong either way.
func (p *Pool) invoke(ctx context.Context, payload []byte, attempt uint) (err error) {
	// Its own span, so a slow or failing attempt is attributable to that
	// attempt rather than averaged into the message. Without it the enclosing
	// span carries only the last error and a duration that includes the backoff.
	ctx, op := p.o11y.BeginCustom(ctx, "handle_message_attempt")
	defer op.End()

	op.Set(attemptsKey, attempt)

	startTime := p.clock.Now()

	// One deferred function rather than several, because the order matters and
	// LIFO makes it easy to get wrong: the panic has to become this package's
	// error before anything can observe that there was one.
	defer func() {
		err = containedPanic(ctx, op, err, p.panicCounter, p.topicAttr, ErrHandlerPanicked)

		p.handlerHist.Record(ctx, float64(p.clock.Since(startTime).Milliseconds()), p.topicAttr)

		if err != nil {
			op.Acknowledge(err, "handling message")
		}
	}()

	if p.cfg.HandlerTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, p.cfg.HandlerTimeout)
		defer cancel()
	}

	return panicking.Contain(func() error { return p.handler(ctx, payload) })
}

// retire disposes of a message that will not be retried again. Every exit from
// here is an error-level event with the original cause attached, because each
// one is a message that nobody handled.
//
// The dead-letter call gets its own span and its own context. The span because
// it is a broker round trip, and a failure here is indistinguishable from a
// failure to handle unless the trace separates them. The context because this
// is the last chance to record the message anywhere, so it deliberately cannot
// be canceled — losing it because the pool happens to be shutting down is
// exactly the loss the dead-letter path exists to prevent.
func (p *Pool) retire(ctx context.Context, op observability.Operation, payload []byte, attempts uint, cause error) {
	if p.deadLtr == nil {
		p.droppedCounter.Add(ctx, 1, p.topicAttr)
		op.Acknowledge(cause, "dropping message after %d attempt(s): no dead-letter destination configured", attempts)

		return
	}

	msg := DeadLetter{
		Topic:    p.cfg.Topic,
		Payload:  payload,
		Attempts: attempts,
		Error:    cause.Error(),
		FailedAt: p.clock.Now().UTC(),
	}

	dlCtx, dlOp := p.o11y.BeginCustom(ctx, "dead_letter_message")
	err := p.deadLtr(context.WithoutCancel(dlCtx), msg)
	dlOp.End()

	if err != nil {
		p.deadLetterFailures.Add(ctx, 1, p.topicAttr)
		op.Acknowledge(err, "dead-lettering message after %d attempt(s), caused by: %s", attempts, cause)

		return
	}

	p.deadLetteredCount.Add(ctx, 1, p.topicAttr)
	op.Acknowledge(cause, "dead-lettered message after %d attempt(s)", attempts)
}

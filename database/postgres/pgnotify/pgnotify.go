package pgnotify

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/retry"

	"github.com/jackc/pgx/v5"
)

// serviceName names the loggers, spans, and metrics this package emits.
const serviceName = "postgres_listener"

// Observability keys for this package's spans and log fields, namespaced so
// they cannot collide with another component writing to the same trace.
const (
	channelKey = "pgnotify.channel"
	backoffKey = "pgnotify.reconnect_backoff"
)

// closeTimeout bounds the teardown of a session's connection. Close is
// best-effort — the session is already over — and a server that has stopped
// answering must not be able to hold shutdown open.
const closeTimeout = 5 * time.Second

// Listener holds one dedicated Postgres connection, keeps a LISTEN standing on
// it across reconnects, and turns every notification into a wake on a
// coalescing channel.
//
// It owns a goroutine started by Run and stopped by Close.
type Listener struct {
	// o11y carries the channel, seeded at construction, so every span and every
	// log line this listener emits says which channel it is about without any
	// of them having to remember to.
	o11y observability.Observer

	// signal has capacity one and is only ever sent to non-blockingly, which is
	// what makes a burst collapse into a single pending wake — and what keeps
	// the listener goroutine from ever parking on a slow consumer. See the
	// package documentation for why that second property is the important one.
	signal chan struct{}

	stop chan struct{}
	done chan struct{}

	receivedCounter   metrics.Int64Counter
	coalescedCounter  metrics.Int64Counter
	reconnectCounter  metrics.Int64Counter
	connectErrCounter metrics.Int64Counter

	// jitter spreads reconnect backoff across the upper half of its interval,
	// so that a fleet of listeners knocked off by one failover does not
	// reconnect in lockstep and knock the recovering primary over again. Equal
	// rather than full jitter: the lower bound is what keeps the delay from
	// collapsing toward zero, which is the case this is defending against.
	jitter retry.Jitter

	// listen is the statement issued on every new session, rendered once at
	// construction. The channel name is quoted rather than bound: LISTEN is a
	// utility statement and takes no parameters.
	listen string

	cfg Config

	// everConnected distinguishes the first connect from a reconnect. It is
	// touched only by the Run goroutine.
	everConnected bool

	started  atomic.Bool
	stopOnce sync.Once
}

// NewListener builds a Listener. It does not connect and does not start it;
// call Run.
//
// ctx is used to validate the config and is not retained — Run takes its own,
// because a listener tied to a request or startup context would stop listening
// the moment that context was cancelled.
func NewListener(ctx context.Context, cfg *Config, opts ...Option) (*Listener, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating postgres listener config")
	}

	// The identifier check and the quoting below are two halves of one
	// requirement. The check is what keeps an arbitrary string out of statement
	// text; the quoting is what makes the name match the producer's, which
	// binds it as text and compares byte for byte.
	if !dialect.ValidIdentifier(cfg.Channel) {
		return nil, platformerrors.Wrapf(dialect.ErrInvalidIdentifier, "postgres notification channel %q", cfg.Channel)
	}
	if len(cfg.Channel) > MaxChannelLength {
		return nil, platformerrors.Wrapf(ErrChannelTooLong, "postgres notification channel %q", cfg.Channel)
	}

	o := newOptions(opts)

	l := &Listener{
		cfg:    *cfg,
		listen: "LISTEN " + pgx.Identifier{cfg.Channel}.Sanitize(),
		signal: make(chan struct{}, 1),
		stop:   make(chan struct{}),
		done:   make(chan struct{}),
		jitter: retry.Equal(o.rand),
	}

	// The channel is what every line and every span here is about, and it never
	// changes, so it is stated once rather than at each of the sites below.
	l.o11y = observability.NewObserverWithValues(serviceName, o.logger, o.tracerProvider,
		map[string]any{channelKey: cfg.Channel})

	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

	var err error
	if l.receivedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_notifications_received", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating notifications received counter")
	}
	if l.coalescedCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_wakes_coalesced", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating coalesced wakes counter")
	}
	if l.reconnectCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_reconnects", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating reconnects counter")
	}
	if l.connectErrCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_connect_errors", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating connect errors counter")
	}

	return l, nil
}

// Signal is the wake channel, and is safe to read before Run has started.
//
// It is edge-triggered and level-collapsed: a send never blocks, and a pending
// wake absorbs every notification that arrives before it is read. A reader
// therefore learns only that something happened, never how much — which is all
// a poller needs, since it re-reads the table on waking.
//
// Read it from exactly one loop. A wake is delivered to one receiver, so
// several loops sharing a Listener would each see an arbitrary subset; give
// each its own Listener, or fan the signal out yourself.
func (l *Listener) Signal() <-chan struct{} {
	return l.signal
}

// Run is the listener loop: connect, LISTEN, wake on every notification, and
// reconnect with backoff when the session drops.
//
// Like outbox.Relay.Run it takes no context. A listener exists to shorten the
// latency of a loop that outlives any single request, and Close is how it
// stops.
//
// Run returns only after Close.
func (l *Listener) Run() {
	defer close(l.done)

	l.started.Store(true)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Close has no other way to interrupt a connection parked in
	// WaitForNotification. The goroutine exits with Run either way.
	go func() {
		select {
		case <-l.stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	backoff := l.cfg.MinReconnectBackoff

	for {
		if l.stopping() {
			return
		}

		connected, err := l.session(ctx)
		if l.stopping() {
			return
		}

		// A session that got as far as LISTEN was healthy, however briefly, so
		// the next failure starts its backoff from the floor again. Without
		// this, a server that drops a connection every few minutes would have
		// the listener waiting the maximum delay by the end of the day.
		if connected {
			backoff = l.cfg.MinReconnectBackoff
		}

		l.o11y.Logger().WithValue(backoffKey, backoff).Error("postgres listener session ended, reconnecting", err)

		if !l.sleep(ctx, l.jitter(backoff)) {
			return
		}

		backoff = min(backoff*2, l.cfg.MaxReconnectBackoff)
	}
}

// Close stops the listener and waits for its connection to be released. Safe to
// call more than once, and safe to call on a Listener that was never run.
func (l *Listener) Close(ctx context.Context) error {
	_, op := l.o11y.Begin(ctx)
	defer op.End()

	l.stopOnce.Do(func() { close(l.stop) })

	// A Listener that was never started has nothing to wait for, and waiting
	// would hang until the caller's context expired. Run checks the same flag
	// before it connects, so a Run racing this Close returns without dialing.
	if !l.started.Load() {
		return nil
	}

	select {
	case <-l.done:
	case <-ctx.Done():
		return op.Error(ctx.Err(), "waiting for postgres listener to stop")
	}

	return nil
}

// session holds one connection for as long as it lasts. It reports whether the
// session was ever established, which is what decides if the reconnect backoff
// resets.
func (l *Listener) session(ctx context.Context) (bool, error) {
	conn, err := l.connect(ctx)
	if err != nil {
		l.connectErrCounter.Add(ctx, 1)

		return false, err
	}

	defer func() {
		// Closed on a context of its own: by the time this runs, ctx is usually
		// the cancelled one that ended the session, and a cancelled context
		// would skip the terminate message and leave the server to notice the
		// dropped socket itself.
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cancel()

		if closeErr := conn.Close(closeCtx); closeErr != nil {
			l.o11y.Logger().Error("closing postgres listener connection", closeErr)
		}
	}()

	// Every session begins with a gap. Notifications sent while this listener
	// was not connected are gone and there is no way to ask what they were, so
	// the consumer is woken unconditionally and its own query establishes the
	// truth.
	l.wake(ctx)

	for {
		if _, err = conn.WaitForNotification(ctx); err != nil {
			return true, platformerrors.Wrap(err, "waiting for postgres notification")
		}

		// The payload is deliberately not read. It is always empty, and a
		// listener that read one would be the first step toward a system that
		// depended on the contents of an at-most-once signal.
		l.receivedCounter.Add(ctx, 1)
		l.wake(ctx)
	}
}

// connect dials and issues the LISTEN, carrying the span for the attempt.
//
// The span covers connecting only, not the session it opens: a span held for
// the life of a connection would be a trace that never ends.
func (l *Listener) connect(ctx context.Context) (*pgx.Conn, error) {
	ctx, op := l.o11y.Begin(ctx)
	defer op.End()

	conn, err := pgx.Connect(ctx, l.cfg.ConnectionString)
	if err != nil {
		return nil, op.Error(err, "connecting to postgres for LISTEN")
	}

	if _, err = conn.Exec(ctx, l.listen); err != nil {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer cancel()

		if closeErr := conn.Close(closeCtx); closeErr != nil {
			op.Acknowledge(closeErr, "closing postgres listener connection after a failed LISTEN")
		}

		return nil, op.Error(err, "issuing LISTEN on channel %q", l.cfg.Channel)
	}

	if l.everConnected {
		l.reconnectCounter.Add(ctx, 1)
		op.Logger().Debug("postgres listener reconnected")
	}
	l.everConnected = true

	return conn, nil
}

// wake signals the consumer without ever blocking. A wake that finds one
// already pending is discarded, which is the coalescing: the consumer re-reads
// the table when it wakes, so one pending wake and fifty are the same
// instruction.
func (l *Listener) wake(ctx context.Context) {
	select {
	case l.signal <- struct{}{}:
	default:
		l.coalescedCounter.Add(ctx, 1)
	}
}

// stopping reports whether Close has been called.
func (l *Listener) stopping() bool {
	select {
	case <-l.stop:
		return true
	default:
		return false
	}
}

// sleep waits out a reconnect delay, reporting false if the listener was closed
// while it waited.
func (l *Listener) sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

package jobs

import (
	"context"
	"sync"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/messagequeue"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

const (
	// poolGroupServiceName names the PoolGroup's spans and logger.
	poolGroupServiceName = "jobs_pool_group"

	// DefaultDrainTimeout bounds the teardown of the pools that did start when a
	// later one failed to build. Start is about to return an error and the
	// process is about to exit, so this only has to be long enough for the
	// in-flight handlers to finish, not for a graceful drain.
	DefaultDrainTimeout = 15 * time.Second
)

// Observability keys for the PoolGroup's spans and log fields, namespaced for
// the same reason the Pool's are. A group spans every topic it was given, so it
// names them as a set rather than reusing keys.TopicKey, which is one topic.
const (
	poolCountKey = "jobs.pool_count"
	topicsKey    = "jobs.topics"
)

// PoolSpec is one topic's worth of a PoolGroup: what to consume, how, and with
// what.
type PoolSpec struct {
	// Config carries the pool's knobs — concurrency, retry, handler timeout. It
	// is copied rather than retained, so one config value may back several
	// specs and the caller's copy is never written to.
	//
	// Nil means the package defaults, which is the whole config for a topic
	// whose only requirement is that something drains it.
	Config *PoolConfig

	// Handler processes one message off Topic. Required.
	Handler Handler

	// Topic is the queue topic to consume. It overrides Config.Topic, and is
	// the field to use when the topic names live somewhere other than the pool
	// knobs — a queues config the publishers on the other side already read, or
	// a per-index topic computed at startup. Empty means Config.Topic, which is
	// where a wholly env-driven pool already carries it.
	Topic string

	// Options are applied to this pool alone, after the group-wide ones, so a
	// single topic may take a dead-letter destination, a retry policy, or a
	// clock of its own without the rest of the group changing.
	Options []PoolOption
}

// PoolGroup runs one Pool per topic and starts them all or none.
//
// A worker process almost always drains several topics, and supervising several
// pools has a failure mode that running one does not: partial start. Building
// the third pool fails while the first two are already consuming, and unless
// something takes those two back down, a process that is on its way out is
// still pulling messages off topics nothing is going to finish handling.
//
// Start is therefore all-or-nothing. A build failure drains whatever came up,
// under a bounded timeout rather than the operator-controlled one Close uses —
// the caller is about to see an error and exit, so the only thing worth waiting
// for is the handlers already in flight.
//
// A group is single-use: one Start, one Close. Start after either reports
// ErrPoolGroupStarted rather than rebuilding, because a Pool cannot be restarted
// either — its stop channel is closed for good, so a restarted group would hand
// back pools that decline every message they are given.
//
// It is not a service.Runner, and cannot be: Runner.Run reports nothing, which
// is exactly what a group's start has to be able to do. So a group is started
// before the service and closed after it, as a deferred Close or as a step in
// whatever the application's own shutdown is.
type PoolGroup struct {
	provider messagequeue.ConsumerProvider
	o11y     observability.Observer

	// What the options wrote, kept for the pools built at Start. Read
	// g.o11y.Logger() for the logger the group itself uses; this one may be
	// nil, because supplying none is how a caller asks for no logging.
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider
	deadLtr         DeadLetterFunc

	specs []resolvedSpec
	pools []*Pool

	drainTimeout time.Duration

	// started is a one-way latch rather than a "currently running" flag: Close
	// sets it too, so a group that has been taken down stays down instead of
	// rebuilding pools whose stop channels are already closed.
	mu      sync.Mutex
	started bool
}

// resolvedSpec is a PoolSpec with its config copied, defaulted, and validated,
// which is what makes the copy load-bearing: the loop below hands each pool a
// config carrying its own topic, and specs sharing one *PoolConfig would
// otherwise all end up consuming whichever topic was written last.
type resolvedSpec struct {
	handler Handler
	opts    []PoolOption
	cfg     PoolConfig
}

// NewPoolGroup validates specs and returns a group that will build a Pool for
// each of them. It builds nothing and consumes nothing; call Start.
//
// Every config is copied, defaulted, and validated here, so the most common
// reason a pool fails to build is settled before any of them is consuming —
// which is the partial start this type exists to survive, avoided outright.
// What is left for Start is the broker: a subscription that cannot be
// established, or a meter that will not make an instrument.
//
// ctx is used for validation and is not retained.
func NewPoolGroup(ctx context.Context, specs []PoolSpec, provider messagequeue.ConsumerProvider, opts ...PoolGroupOption) (*PoolGroup, error) {
	if provider == nil {
		return nil, ErrNilConsumerProvider
	}
	if len(specs) == 0 {
		return nil, ErrNoPoolSpecs
	}

	g := &PoolGroup{
		provider:     provider,
		drainTimeout: DefaultDrainTimeout,
		specs:        make([]resolvedSpec, 0, len(specs)),
		pools:        make([]*Pool, 0, len(specs)),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(g)
		}
	}

	g.o11y = observability.NewObserver(poolGroupServiceName, g.logger, g.tracerProvider)

	seen := make(map[string]struct{}, len(specs))
	for i := range specs {
		resolved, err := g.resolve(ctx, &specs[i])
		if err != nil {
			return nil, platformerrors.Wrapf(err, "pool spec %d", i)
		}

		// Caught here rather than left to the provider, which reports
		// ErrConsumerAlreadyRegistered from the middle of a start that has
		// already brought other pools up — a partial start over a mistake that
		// was visible before anything consumed anything.
		if _, taken := seen[resolved.cfg.Topic]; taken {
			return nil, platformerrors.Wrapf(ErrDuplicateTopic, "topic %q", resolved.cfg.Topic)
		}
		seen[resolved.cfg.Topic] = struct{}{}

		g.specs = append(g.specs, resolved)
	}

	return g, nil
}

// resolve turns one spec into the config and handler a Pool will be built from.
// The spec is taken by pointer to avoid the copy, and read from only: the
// caller still owns it, and what comes back is this package's own copy.
func (g *PoolGroup) resolve(ctx context.Context, spec *PoolSpec) (resolvedSpec, error) {
	if spec.Handler == nil {
		return resolvedSpec{}, ErrNilHandler
	}

	var cfg PoolConfig
	if spec.Config != nil {
		cfg = *spec.Config
	}

	if spec.Topic != "" {
		cfg.Topic = spec.Topic
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return resolvedSpec{}, platformerrors.Wrap(err, "validating job pool config")
	}

	return resolvedSpec{cfg: cfg, handler: spec.Handler, opts: spec.Options}, nil
}

// Start builds a Pool per spec and begins consuming. Either every pool is
// running when it returns nil, or none is when it returns an error.
//
// A pool's Run starts as soon as that pool is built rather than after the whole
// set is, because Pool.Close waits on the goroutine Run owns: a pool that was
// built and never run has nothing to end that wait, so closing it would block
// until its context expired — which on the error path is the timeout the drain
// is trying to stay inside of.
//
// ctx establishes the subscriptions and is not retained. The pools run on
// contexts of their own, so a canceled setup context cannot stop them later.
func (g *PoolGroup) Start(ctx context.Context) error {
	ctx, op := g.o11y.Begin(ctx)
	defer op.End()

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.started {
		return op.Error(ErrPoolGroupStarted, "starting job pools")
	}
	g.started = true

	for i := range g.specs {
		spec := &g.specs[i]

		pool, err := NewPool(ctx, &spec.cfg, g.provider, spec.handler, g.poolOptions(spec)...)
		if err != nil {
			g.drainPartialStart(ctx)

			return op.Error(err, "building job pool for topic %q", spec.cfg.Topic)
		}

		g.pools = append(g.pools, pool)

		//nolint:contextcheck // Pool.Run takes no context by design: tied to the start context it would stop mid-message the instant that context was canceled. Close is the stop signal.
		go pool.Run()
	}

	g.o11y.Logger().
		WithValue(poolCountKey, len(g.pools)).
		WithValue(topicsKey, g.Topics()).
		Info("job pools started")

	return nil
}

// poolOptions assembles the options one pool is built with: the group's, then
// the spec's, so a spec can override anything the group set.
func (g *PoolGroup) poolOptions(spec *resolvedSpec) []PoolOption {
	opts := make([]PoolOption, 0, 4+len(spec.opts))

	if g.logger != nil {
		opts = append(opts, WithPoolLogger(g.logger))
	}
	if g.tracerProvider != nil {
		opts = append(opts, WithPoolTracerProvider(g.tracerProvider))
	}
	if g.metricsProvider != nil {
		opts = append(opts, WithPoolMetricsProvider(g.metricsProvider))
	}
	if g.deadLtr != nil {
		opts = append(opts, WithPoolDeadLetter(g.deadLtr))
	}

	return append(opts, spec.opts...)
}

// drainPartialStart takes down the pools that did start when a later one failed
// to build. Its failure is logged rather than returned: the caller is owed the
// reason the group would not start, and a drain that overran on the way out is
// an operator's problem rather than a second thing to branch on.
//
// The context is stripped of the caller's cancellation before the bound is
// applied. A start that failed because its context was canceled would otherwise
// drain on an already-expired deadline, which is not a drain at all.
func (g *PoolGroup) drainPartialStart(ctx context.Context) {
	if len(g.pools) == 0 {
		return
	}

	g.o11y.Logger().WithValue(poolCountKey, len(g.pools)).Info("draining partially started job pools")

	drainCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), g.drainTimeout)
	defer cancel()

	if err := g.close(drainCtx); err != nil {
		g.o11y.Logger().Error("draining partially started job pools", err)
	}
}

// Close stops every pool and waits for the in-flight handlers to drain,
// reporting every pool that did not stop cleanly rather than the first.
//
// Safe to call more than once, and safe before Start — a group that never
// started has nothing to drain and returns immediately, rather than waiting out
// its whole context the way a single unstarted Pool would. Either way it is
// final: the group is spent afterwards and Start reports ErrPoolGroupStarted.
//
// The context bounds the whole group, not each pool in turn: the pools are
// closed concurrently because they are independent and share one budget.
func (g *PoolGroup) Close(ctx context.Context) error {
	_, op := g.o11y.Begin(ctx)
	defer op.End()

	g.mu.Lock()
	defer g.mu.Unlock()

	g.started = true

	return g.close(ctx)
}

// close is Close without the lock or the span, so that the partial-start drain
// can reuse it from inside Start.
func (g *PoolGroup) close(ctx context.Context) error {
	errs := make([]error, len(g.pools))

	var wg sync.WaitGroup

	for idx, pool := range g.pools {
		wg.Go(func() {
			if err := pool.Close(ctx); err != nil {
				errs[idx] = platformerrors.Wrapf(err, "closing job pool for topic %q", g.specs[idx].cfg.Topic)
			}
		})
	}

	wg.Wait()

	// There is no second wait on the Run goroutines, because Pool.Close already
	// is one: it returns nil only once the pool's done channel is closed, and
	// that closes as Run returns. A Close that reports an error is the case a
	// wait here would have to survive, and waiting through it is precisely what
	// the caller's deadline said not to do.

	// Cleared so a second Close is the no-op it claims to be rather than a
	// second drain of pools that are already down.
	g.pools = nil

	return platformerrors.Join(errs...)
}

// Topics reports the topics the group drains, in spec order. It is what a
// health check or a startup log names, and the only reader of the resolution
// between PoolSpec.Topic and PoolConfig.Topic.
func (g *PoolGroup) Topics() []string {
	topics := make([]string, len(g.specs))
	for i := range g.specs {
		topics[i] = g.specs[i].cfg.Topic
	}

	return topics
}

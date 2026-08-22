package jobs

import (
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/retry"
)

// SchedulerOption configures a Scheduler.
type SchedulerOption func(*Scheduler)

// WithSchedulerClock swaps the clock driving the tickers. Tests generally do
// not need it: inside a testing/synctest bubble the default clock already reads
// bubble time, so a daily job fires instantly and deterministically.
func WithSchedulerClock(c clock.Clock) SchedulerOption {
	return func(s *Scheduler) {
		if c != nil {
			s.clock = c
		}
	}
}

// WithSchedulerLogger attaches a logger. A job's error goes nowhere else —
// there is no caller to return it to — so without one a job that has been
// failing for a week is visible only in metrics.
func WithSchedulerLogger(logger logging.Logger) SchedulerOption {
	return func(s *Scheduler) {
		s.logger = logger
	}
}

// WithSchedulerTracerProvider attaches a tracer provider. A tick that is skipped
// because another replica holds the lease is still traced: "did this replica
// decline, or did nobody run it" is the question a missed job actually raises.
func WithSchedulerTracerProvider(tracerProvider tracing.Provider) SchedulerOption {
	return func(s *Scheduler) {
		s.tracerProvider = tracerProvider
	}
}

// WithSchedulerMetricsProvider attaches a metrics provider.
func WithSchedulerMetricsProvider(metricsProvider metrics.Provider) SchedulerOption {
	return func(s *Scheduler) {
		s.metricsProvider = metricsProvider
	}
}

// PoolOption configures a Pool.
type PoolOption func(*Pool)

// WithPoolDeadLetter sets where messages go once they have exhausted their
// attempts. Without one the Pool has no terminal destination and drops them,
// logging at error level and incrementing jobs_pool_messages_dropped — which is
// a defensible choice for a topic whose messages are individually worthless,
// and a silent data-loss bug for every other topic.
//
// NewTopicDeadLetter builds the usual implementation.
func WithPoolDeadLetter(fn DeadLetterFunc) PoolOption {
	return func(p *Pool) {
		if fn != nil {
			p.deadLtr = fn
		}
	}
}

// WithPoolClock swaps the clock used to stamp dead-letter envelopes and measure
// handler latency.
func WithPoolClock(c clock.Clock) PoolOption {
	return func(p *Pool) {
		if c != nil {
			p.clock = c
		}
	}
}

// WithPoolLogger attaches a logger. Nothing in the Pool's steady state is
// surfaced to a caller — there is no caller — so without a logger a handler
// that fails every message is visible only in metrics.
func WithPoolLogger(logger logging.Logger) PoolOption {
	return func(p *Pool) {
		p.logger = logger
	}
}

// WithPoolTracerProvider attaches a tracer provider. Each message gets a root
// span covering all of its attempts.
func WithPoolTracerProvider(tracerProvider tracing.Provider) PoolOption {
	return func(p *Pool) {
		p.tracerProvider = tracerProvider
	}
}

// WithPoolMetricsProvider attaches a metrics provider.
func WithPoolMetricsProvider(metricsProvider metrics.Provider) PoolOption {
	return func(p *Pool) {
		p.metricsProvider = metricsProvider
	}
}

// WithPoolRetryPolicy replaces the retry policy built from PoolConfig.Retry.
// The policy still governs how many times the handler runs, so a policy whose
// attempt count disagrees with PoolConfig.Retry.MaxAttempts changes when a
// message is dead-lettered.
func WithPoolRetryPolicy(policy retry.Policy) PoolOption {
	return func(p *Pool) {
		if policy != nil {
			p.policy = policy
		}
	}
}

// PoolGroupOption configures a PoolGroup.
//
// Everything here that a Pool also takes is applied to every pool in the group;
// PoolSpec.Options is where one topic departs from the rest, and runs after
// these so it can override them.
type PoolGroupOption func(*PoolGroup)

// WithPoolGroupDeadLetter sets where messages go once they have exhausted their
// attempts, for every pool in the group. Read WithPoolDeadLetter for what
// happens to a pool that has none.
//
// One destination for the whole group is the usual arrangement — a DeadLetter
// carries the topic it came from, so a single dead-letter topic or table stays
// attributable. A topic that wants its own sets one through PoolSpec.Options.
func WithPoolGroupDeadLetter(fn DeadLetterFunc) PoolGroupOption {
	return func(g *PoolGroup) {
		if fn != nil {
			g.deadLtr = fn
		}
	}
}

// WithPoolGroupDrainTimeout bounds the teardown of the pools that did start
// when a later one failed to build. Zero or negative leaves DefaultDrainTimeout
// in place.
//
// It is not the bound on Close, which is the caller's context: this one applies
// only on the error path out of Start, where the process is about to exit and
// the only thing worth waiting for is the handlers already in flight.
func WithPoolGroupDrainTimeout(d time.Duration) PoolGroupOption {
	return func(g *PoolGroup) {
		if d > 0 {
			g.drainTimeout = d
		}
	}
}

// WithPoolGroupLogger attaches a logger to the group and to every pool in it.
// Without one, a group that failed to start reports why to its caller and a
// group that dropped a message reports it nowhere.
func WithPoolGroupLogger(logger logging.Logger) PoolGroupOption {
	return func(g *PoolGroup) {
		g.logger = logger
	}
}

// WithPoolGroupTracerProvider attaches a tracer provider to the group and to
// every pool in it. The group's own spans cover the start and the shutdown,
// which is where a partial start is visible as one event rather than as a pool
// that never logged again.
func WithPoolGroupTracerProvider(tracerProvider tracing.Provider) PoolGroupOption {
	return func(g *PoolGroup) {
		g.tracerProvider = tracerProvider
	}
}

// WithPoolGroupMetricsProvider attaches a metrics provider to every pool in the
// group. The group adds no instruments of its own — every message it handles is
// handled by one of its pools, which already counts it, carrying the topic
// attribute that tells them apart.
func WithPoolGroupMetricsProvider(metricsProvider metrics.Provider) PoolGroupOption {
	return func(g *PoolGroup) {
		g.metricsProvider = metricsProvider
	}
}

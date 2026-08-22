package retention

import (
	"github.com/primandproper/platform-go/v13/audit"
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// SweeperOption configures a Sweeper.
type SweeperOption func(*Sweeper)

// WithSweeperClock swaps the clock the retention cutoff is computed from, and
// that the pause between batches is taken against.
//
// It is what makes expiry deterministic in a test: a policy with a thirty-day
// age is otherwise only observable by waiting thirty days or by writing rows
// with fabricated timestamps, and the second is the same assertion made against
// a fixture instead of against the policy.
func WithSweeperClock(c clock.Clock) SweeperOption {
	return func(s *Sweeper) {
		if c != nil {
			s.clock = c
		}
	}
}

// WithSweeperLogger attaches a logger.
func WithSweeperLogger(logger logging.Logger) SweeperOption {
	return func(s *Sweeper) {
		s.logger = logger
	}
}

// WithSweeperTracerProvider attaches a tracer provider.
func WithSweeperTracerProvider(tracerProvider tracing.Provider) SweeperOption {
	return func(s *Sweeper) {
		s.tracerProvider = tracerProvider
	}
}

// WithSweeperMetricsProvider attaches a metrics provider, enabling the backlog
// gauge — which is the one instrument here worth alerting on. Rows removed is a
// rate and tells you the sweep is working; backlog is the number that says the
// sweep is not keeping up, and it is the only one that distinguishes a policy
// deleting nothing because the table is clean from a policy deleting nothing
// because it is stuck.
func WithSweeperMetricsProvider(metricsProvider metrics.Provider) SweeperOption {
	return func(s *Sweeper) {
		s.metricsProvider = metricsProvider
	}
}

// WithSweeperAuditRecorder attaches the audit log sweeps are accounted for in.
//
// Worth attaching. Without it this package deletes data on a schedule and
// leaves behind a counter, which is the same evidence the ad-hoc script it
// replaces left behind. The entry is what turns "we delete expired tokens
// daily" from a claim about a cron job into a record with a cutoff, a row
// count, and a stated basis against it.
func WithSweeperAuditRecorder(recorder audit.Recorder) SweeperOption {
	return func(s *Sweeper) {
		if recorder != nil {
			s.recorder = recorder
		}
	}
}

// WithSweeperActor sets the principal recorded against the sweep's audit
// entries. It defaults to DefaultAuditActorID as an audit.ActorSystem.
//
// Set it where one database is swept by more than one deployment, so the entry
// says which one — the default is the same string in every process, and an
// audit record whose actor cannot be resolved to a running thing is only half
// an answer.
func WithSweeperActor(actor audit.Actor) SweeperOption {
	return func(s *Sweeper) {
		if actor.ID != "" {
			s.actor = actor
		}
	}
}

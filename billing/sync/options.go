package sync

import (
	"github.com/primandproper/platform-go/v14/billing/standing"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
)

// Option configures a Syncer.
//
// The observability dependencies are options rather than parameters because
// every one of them is genuinely optional: an absent logger logs nowhere, an
// absent tracer provider traces nowhere, and an absent metrics provider records
// nothing. The standing writer is an option for a different reason — see
// [WithStanding].
type Option func(*Syncer)

// WithStanding records the account's coarse standing on the same transaction as
// the subscription row.
//
// The writer is [identity.BillingWriter], which identity.Store satisfies, and
// classify is the deployment's reading of what a processor status means —
// [github.com/primandproper/platform-go/v14/billing/standing.Strict] is the one
// most of them want and none of them have to take.
//
// Both or neither. A writer with no reading, or a reading with nowhere to write
// it, is refused by [New] rather than silently doing half the job.
//
// Without it a Syncer writes the subscription row and stops, which is the right
// shape for a deployment that gates through billing/plans and stores no coarse
// standing of its own. What it costs to add is one write in a transaction that
// was already open, and what it buys is that an account's standing and the
// agreement it was derived from can never disagree — the hand-written version
// writes them in two transactions and the second one is the one that fails.
func WithStanding(accounts identity.BillingWriter, classify standing.Classify) Option {
	return func(s *Syncer) {
		s.accounts = accounts
		s.classify = classify
	}
}

// WithLogger attaches a logger. An absent logger logs nowhere.
func WithLogger(logger logging.Logger) Option {
	return func(s *Syncer) { s.logger = logger }
}

// WithTracerProvider attaches a tracer provider, enabling a span per delivery.
// An absent provider traces nowhere.
//
// It takes a provider rather than a ready-made tracer so that the spans this
// package emits carry this package's instrumentation scope. A caller-supplied
// tracer would attribute them to whoever built it.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(s *Syncer) { s.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider. An absent provider records
// nothing.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(s *Syncer) { s.metricsProvider = metricsProvider }
}

// WithPillars attaches a logger, tracer provider and metrics provider in one go.
// A nil Pillars attaches nothing.
//
// Options apply in order, so a caller can hand over its pillars and then
// override one of them.
func WithPillars(p *observability.Pillars) Option {
	return func(s *Syncer) { s.logger, s.tracerProvider, s.metricsProvider = p.Deps() }
}

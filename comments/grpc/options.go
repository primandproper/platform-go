package grpc

import (
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
)

// Option configures a [Server] at construction.
type Option func(*Server)

// WithAuthorAuthorizer replaces the rule this surface applies when a caller
// reaches for somebody else's words.
//
// The default, [OwnCommentsOnly], refuses every author but the caller — see
// [AuthorAuthorizer] for why a closed default is the right one and what a
// consumer with moderators supplies instead.
func WithAuthorAuthorizer(authorizer AuthorAuthorizer) Option {
	return func(s *Server) {
		if authorizer != nil {
			s.authors = authorizer
		}
	}
}

// WithLogger sets the server's logger.
func WithLogger(logger logging.Logger) Option {
	return func(s *Server) { s.logger = logger }
}

// WithTracerProvider sets the server's tracer provider.
//
// A provider rather than a ready-made tracer, so the instrumentation scope is
// this package's rather than the caller's.
func WithTracerProvider(provider tracing.Provider) Option {
	return func(s *Server) { s.tracerProvider = provider }
}

// WithMetricsProvider sets the server's metrics provider.
func WithMetricsProvider(provider metrics.Provider) Option {
	return func(s *Server) { s.metricsProvider = provider }
}

// WithPillars supplies logger, tracer provider and metrics provider at once.
//
// Options apply in order, so WithPillars(p) followed by WithMetricsProvider(nil)
// leaves this server unmetered.
func WithPillars(pillars *observability.Pillars) Option {
	return func(s *Server) {
		if pillars == nil {
			return
		}

		s.logger = pillars.Logger
		s.tracerProvider = pillars.TracerProvider
		s.metricsProvider = pillars.MetricsProvider
	}
}

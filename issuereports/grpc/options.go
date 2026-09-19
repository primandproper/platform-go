package grpc

import (
	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
)

// Option configures a [Server] at construction.
type Option func(*Server)

// WithGrantsExtractor supplies what the caller may do, which this surface reads
// for exactly one decision: whether a paged read's include_archived is honored
// or cleared.
//
// It is the same authorization.GrantsExtractor a consumer already hands
// primitives-go's authorization/grpc enforcer — the interceptor decides whether
// a method may be called at all, and this decides which rows the answer may
// contain, off the same authority so the two cannot disagree.
//
// It is an option rather than a parameter because its absence has a coherent
// answer and a safe one: a server built without it clears the field on every
// read, so a deployment that has not wired it serves live rows to everybody
// rather than archived ones to anybody. See archived.go.
func WithGrantsExtractor(grants authorization.GrantsExtractor) Option {
	return func(s *Server) { s.grants = grants }
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

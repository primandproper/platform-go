package grpc

import (
	"context"
	"testing"

	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	loggingnoop "github.com/primandproper/primitives-go/v2/observability/logging/noop"
	metricsnoop "github.com/primandproper/primitives-go/v2/observability/metrics/noop"
	tracingnoop "github.com/primandproper/primitives-go/v2/observability/tracing/noop"

	"github.com/shoenig/test"
)

// apply builds the server the options would have configured, without a database
// or a store. Every option here is a field write; what the server does with the
// fields is the suite in grpc_test's business.
func apply(opts ...Option) *Server {
	s := &Server{}
	for _, opt := range opts {
		opt(s)
	}

	return s
}

func TestOptions(T *testing.T) {
	T.Parallel()

	T.Run("each option sets the field it names", func(t *testing.T) {
		t.Parallel()

		var logger logging.Logger = loggingnoop.NewLogger()
		tracerProvider := tracingnoop.NewTracerProvider()
		metricsProvider := metricsnoop.NewMetricsProvider()

		s := apply(
			WithLogger(logger),
			WithTracerProvider(tracerProvider),
			WithMetricsProvider(metricsProvider),
		)

		test.Eq(t, logger, s.logger)
		test.Eq(t, tracerProvider, s.tracerProvider)
		test.Eq(t, metricsProvider, s.metricsProvider)
	})

	T.Run("naming none of them leaves all three absent", func(t *testing.T) {
		t.Parallel()

		// A caller wanting no observability names none, which is why these are
		// options rather than parameters. NewServer resolves the absences
		// through the Ensure* helpers.
		s := apply()

		test.Nil(t, s.logger)
		test.Nil(t, s.tracerProvider)
		test.Nil(t, s.metricsProvider)
	})

	T.Run("WithPillars supplies all three at once", func(t *testing.T) {
		t.Parallel()

		pillars := &observability.Pillars{
			Logger:          loggingnoop.NewLogger(),
			TracerProvider:  tracingnoop.NewTracerProvider(),
			MetricsProvider: metricsnoop.NewMetricsProvider(),
		}

		s := apply(WithPillars(pillars))

		test.Eq(t, pillars.Logger, s.logger)
		test.Eq(t, pillars.TracerProvider, s.tracerProvider)
		test.Eq(t, pillars.MetricsProvider, s.metricsProvider)
	})

	T.Run("a nil Pillars attaches nothing", func(t *testing.T) {
		t.Parallel()

		// A consumer that built no pillars hands over nil rather than branching
		// at the wiring site.
		s := apply(WithPillars(nil))

		test.Nil(t, s.logger)
		test.Nil(t, s.tracerProvider)
		test.Nil(t, s.metricsProvider)
	})

	T.Run("a later option overrides what the pillars supplied", func(t *testing.T) {
		t.Parallel()

		// Options apply in order, which is what lets a caller hand over their
		// pillars and then leave this one surface unmetered.
		s := apply(
			WithPillars(&observability.Pillars{
				Logger:          loggingnoop.NewLogger(),
				TracerProvider:  tracingnoop.NewTracerProvider(),
				MetricsProvider: metricsnoop.NewMetricsProvider(),
			}),
			WithMetricsProvider(nil),
		)

		test.NotNil(t, s.logger)
		test.NotNil(t, s.tracerProvider)
		test.Nil(t, s.metricsProvider)
	})

	// The authorizer is the one option whose absence is not "do nothing", so a
	// nil handed to it must not clear the default the constructor installed —
	// that would turn a conditional wiring site into a surface where anybody may
	// rewrite anybody's words.
	T.Run("WithAuthorAuthorizer ignores a nil rule", func(t *testing.T) {
		t.Parallel()

		s := &Server{authors: OwnCommentsOnly{}}
		WithAuthorAuthorizer(nil)(s)

		test.NotNil(t, s.authors)
	})

	T.Run("WithAuthorAuthorizer replaces the rule", func(t *testing.T) {
		t.Parallel()

		rule := AuthorAuthorizerFunc(func(context.Context, Principal, string) error { return nil })

		s := apply(WithAuthorAuthorizer(rule))

		test.NotNil(t, s.authors)
	})
}

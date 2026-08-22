package oauth2servercfg

import (
	"context"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	oauth2database "github.com/primandproper/platform-go/v13/authentication/oauth2server/database"
	oauth2memory "github.com/primandproper/platform-go/v13/authentication/oauth2server/memory"
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/observability"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// frozenClock reads one instant forever and never ticks.
//
// The ticker matters: a store built through this package starts a sweeper on the
// configured interval, so a clock with no NewTicker is one the sweeper panics
// on before the case gets to what it is about.
type frozenClock struct{ at time.Time }

var _ clock.Clock = (*frozenClock)(nil)

func (c *frozenClock) Now() time.Time                                   { return c.at }
func (c *frozenClock) Since(t time.Time) time.Duration                  { return c.at.Sub(t) }
func (c *frozenClock) Sleep(ctx context.Context, _ time.Duration) error { return ctx.Err() }
func (c *frozenClock) NewTicker(_ time.Duration) clock.Ticker           { return silentTicker{} }

// silentTicker delivers nothing, on a channel nothing closes.
type silentTicker struct{}

func (silentTicker) Chan() <-chan time.Time { return make(chan time.Time) }
func (silentTicker) Stop()                  {}

func TestOptions(T *testing.T) {
	T.Parallel()

	T.Run("absent means nothing rather than a noop nobody asked for", func(t *testing.T) {
		t.Parallel()

		o := newOptions(nil)

		// Every constructor below this resolves an absent pillar through
		// EnsureLogger and friends, so nothing here has to supply one.
		test.Nil(t, o.logger)
		test.Nil(t, o.tracerProvider)
		test.Nil(t, o.metricsProvider)
		test.SliceEmpty(t, o.server)
		test.SliceEmpty(t, o.memoryStore)
		test.SliceEmpty(t, o.databaseStore)
	})

	T.Run("a nil option is ignored rather than dereferenced", func(t *testing.T) {
		t.Parallel()

		test.NotNil(t, newOptions([]Option{nil, WithLogger(loggingnoop.NewLogger())}))
	})

	T.Run("the three pillars are set one at a time", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithLogger(loggingnoop.NewLogger()),
			WithTracerProvider(tracingnoop.NewTracerProvider()),
			WithMetricsProvider(metricsnoop.NewMetricsProvider()),
		})

		test.NotNil(t, o.logger)
		test.NotNil(t, o.tracerProvider)
		test.NotNil(t, o.metricsProvider)
	})

	T.Run("WithPillars sets all three at once", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithPillars(&observability.Pillars{
			Logger:          loggingnoop.NewLogger(),
			TracerProvider:  tracingnoop.NewTracerProvider(),
			MetricsProvider: metricsnoop.NewMetricsProvider(),
		})})

		test.NotNil(t, o.logger)
		test.NotNil(t, o.tracerProvider)
		test.NotNil(t, o.metricsProvider)
	})

	T.Run("a later option overrides what WithPillars supplied", func(t *testing.T) {
		t.Parallel()

		// Options apply in order, which is what lets a caller hand over its
		// pillars and then leave one component unmetered.
		o := newOptions([]Option{
			WithPillars(&observability.Pillars{
				Logger:          loggingnoop.NewLogger(),
				MetricsProvider: metricsnoop.NewMetricsProvider(),
			}),
			WithMetricsProvider(nil),
		})

		test.NotNil(t, o.logger)
		test.Nil(t, o.metricsProvider)
	})

	T.Run("the pass-through slices accumulate", func(t *testing.T) {
		t.Parallel()

		// Go allows one variadic per function and that slot belongs to this
		// package's own Option, so anything bound for a component this builds
		// arrives through one of these.
		o := newOptions([]Option{
			WithServerOptions(oauth2server.WithScopes("read")),
			WithServerOptions(oauth2server.WithScopes("write")),
			WithMemoryStoreOptions(oauth2memory.WithLogger(loggingnoop.NewLogger())),
			WithDatabaseStoreOptions(oauth2database.WithClock(&frozenClock{at: time.Now().UTC()})),
		})

		test.SliceLen(t, 2, o.server)
		test.SliceLen(t, 1, o.memoryStore)
		test.SliceLen(t, 1, o.databaseStore)
	})
}

// The pass-through options are wiring rather than bookkeeping: what they collect
// has to reach the thing being built.
func TestOptions_ReachTheStore(T *testing.T) {
	T.Parallel()

	T.Run("memory store options reach the memory store", func(t *testing.T) {
		t.Parallel()

		frozen := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)

		store, err := NewStore(t.Context(), &Config{Provider: ProviderMemory}, nil,
			WithMemoryStoreOptions(oauth2memory.WithClock(&frozenClock{at: frozen})))
		must.NoError(t, err)

		// A code written a minute before the frozen instant is expired against
		// that clock and live against the wall one, so the store answering
		// ErrExpired is the option having arrived.
		must.NoError(t, store.CreateAuthorizationCode(t.Context(), &oauth2server.AuthorizationCode{
			IssuedAt:  frozen.Add(-2 * time.Minute),
			ExpiresAt: frozen.Add(-time.Minute),
			Hash:      oauth2server.Hash("expired"),
			ClientID:  "client",
		}))

		_, err = store.ConsumeAuthorizationCode(t.Context(), oauth2server.Hash("expired"))
		test.ErrorIs(t, err, oauth2server.ErrExpired)
	})
}

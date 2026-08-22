package database

import (
	"testing"
	"testing/synctest"
	"time"

	"github.com/primandproper/platform-go/v13/authentication/oauth2server"
	clockmock "github.com/primandproper/platform-go/v13/clock/mock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/logging"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

func TestOptions(T *testing.T) {
	T.Parallel()

	T.Run("a nil option is ignored rather than dereferenced", func(t *testing.T) {
		t.Parallel()

		store, err := NewStore(&Config{}, newTestClient(t), nil, WithLogger(loggingnoop.NewLogger()))
		must.NoError(t, err)
		test.NotNil(t, store)
	})

	T.Run("the pillars default to noops rather than to nil", func(t *testing.T) {
		t.Parallel()

		// A store built with no observability at all still has to work: absent
		// means noop everywhere in this module, and a nil logger here would be
		// a panic on the first sweep that failed.
		o := newOptions(nil)
		test.Nil(t, o.logger)
		test.Nil(t, o.tracerProvider)
		test.Nil(t, o.metricsProvider)
		test.NotNil(t, o.clock)

		store, err := NewStore(&Config{}, newTestClient(t))
		must.NoError(t, err)
		test.NoError(t, store.CreateClient(t.Context(), &oauth2server.Client{
			CreatedAt: time.Now().UTC(), ID: "no_observability",
		}))
	})

	T.Run("the pillars are what they were given", func(t *testing.T) {
		t.Parallel()

		var (
			logger   logging.Logger = loggingnoop.NewLogger()
			provider                = metricsnoop.NewMetricsProvider()
		)

		o := newOptions([]Option{
			WithLogger(logger),
			WithTracerProvider(tracingnoop.NewTracerProvider()),
			WithMetricsProvider(provider),
		})

		test.NotNil(t, o.logger)
		test.NotNil(t, o.tracerProvider)
		test.NotNil(t, o.metricsProvider)
	})

	T.Run("a nil clock leaves the wall clock in place", func(t *testing.T) {
		t.Parallel()

		// Absent is not a request for a store with no clock; every deadline
		// here is evaluated against one.
		o := newOptions([]Option{WithClock(nil)})
		test.NotNil(t, o.clock)
		test.False(t, o.clock.Now().IsZero())
	})

	T.Run("a supplied clock is what every deadline is read from", func(t *testing.T) {
		t.Parallel()

		frozen := time.Date(2026, time.August, 15, 12, 0, 0, 0, time.UTC)

		o := newOptions([]Option{WithClock(&clockmock.ClockMock{
			NowFunc: func() time.Time { return frozen },
		})})

		// The store's own clock rather than the database server's, so that
		// "issued for fifteen minutes" and "expired" measure the same fifteen
		// minutes as the Server that stamped the deadline.
		test.EqOp(t, frozen, o.clock.Now())
	})

	T.Run("a sweeper needs both a context and an interval", func(t *testing.T) {
		t.Parallel()

		// Half a sweeper is worse than none: a goroutine ticking on a zero
		// interval panics, and one with no context never stops.
		//nolint:staticcheck // the nil context is the case under test.
		test.Nil(t, newOptions([]Option{WithSweeper(nil, time.Minute)}).sweepCtx)
		test.Nil(t, newOptions([]Option{WithSweeper(t.Context(), 0)}).sweepCtx)
		test.Nil(t, newOptions([]Option{WithSweeper(t.Context(), -time.Minute)}).sweepCtx)

		configured := newOptions([]Option{WithSweeper(t.Context(), time.Minute)})
		test.NotNil(t, configured.sweepCtx)
		test.EqOp(t, time.Minute, configured.sweepInterval)
	})
}

// The sweeper is what keeps four tables from growing with every login attempt
// and every anonymous registration, and nothing else in the package calls it.
func TestWithSweeper(T *testing.T) {
	T.Parallel()

	// The wall clock is deliberate rather than an injected fake: inside a
	// synctest bubble clock.NewClock reads the bubble's time, so the sweeper's
	// ticker advances with time.Sleep and needs no test double.
	T.Run("removes what nothing else would discover", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			client := newTestClient(t)

			store, err := NewStore(&Config{}, client, WithSweeper(t.Context(), 10*time.Second))
			must.NoError(t, err)

			now := time.Now().UTC()

			// The code nobody redeemed, which is the row no Consume will ever
			// reach and no read will ever delete.
			must.NoError(t, store.CreateAuthorizationCode(t.Context(), &oauth2server.AuthorizationCode{
				IssuedAt:  now,
				ExpiresAt: now.Add(time.Minute),
				Hash:      oauth2server.Hash("abandoned"),
				ClientID:  "x",
			}))

			time.Sleep(time.Minute + 10*time.Second)
			synctest.Wait()

			_, err = store.ConsumeAuthorizationCode(t.Context(), oauth2server.Hash("abandoned"))
			test.ErrorIs(t, err, oauth2server.ErrNotFound)
		})
	})

	T.Run("leaves live records alone", func(t *testing.T) {
		t.Parallel()

		synctest.Test(t, func(t *testing.T) {
			client := newTestClient(t)

			store, err := NewStore(&Config{}, client, WithSweeper(t.Context(), 10*time.Second))
			must.NoError(t, err)

			now := time.Now().UTC()

			must.NoError(t, store.CreateAuthorizationCode(t.Context(), &oauth2server.AuthorizationCode{
				IssuedAt:  now,
				ExpiresAt: now.Add(time.Hour),
				Hash:      oauth2server.Hash("live"),
				ClientID:  "x",
			}))

			time.Sleep(time.Minute)
			synctest.Wait()

			got, err := store.ConsumeAuthorizationCode(t.Context(), oauth2server.Hash("live"))
			must.NoError(t, err)
			test.NotNil(t, got)
		})
	})
}

func TestNewStore_instrumentFailures(T *testing.T) {
	T.Parallel()

	// The two counters the sweeper owns, with the message NewStore wraps each
	// failure in.
	for _, tc := range []struct {
		instrument string
		wantErr    string
	}{
		{"rows_swept", "creating swept oauth2 rows counter"},
		{"sweep_errors", "creating oauth2 sweep errors counter"},
	} {
		T.Run(tc.instrument, func(t *testing.T) {
			t.Parallel()

			store, err := NewStore(&Config{}, newTestClient(t),
				WithMetricsProvider(failingMetricsProvider(serviceName+"_"+tc.instrument)))

			// Nil rather than a store with no counters. A sweeper that could
			// not report how much it removed is a garbage collector nobody can
			// tell has stopped.
			test.Nil(t, store)
			must.Error(t, err)
			test.StrContains(t, err.Error(), tc.wantErr)
		})
	}
}

// failingMetricsProvider serves the noop provider's instruments for every name
// except failOn, which reports an error.
func failingMetricsProvider(failOn string) metrics.Provider {
	base := metricsnoop.NewMetricsProvider()
	boom := platformerrors.New("instrument unavailable")

	return &metricsmock.ProviderMock{
		NewInt64CounterFunc: func(name string, opts ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
			if name == failOn {
				return nil, boom
			}

			return base.NewInt64Counter(name, opts...)
		},
	}
}

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("accepts an empty prefix and a legal one", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, (&Config{}).ValidateWithContext(t.Context()))
		test.NoError(t, (&Config{TablePrefix: "tenant"}).ValidateWithContext(t.Context()))
	})

	T.Run("refuses a prefix the schema cannot render", func(t *testing.T) {
		t.Parallel()

		// The separator is supplied by database/ddl, so a prefix carrying its
		// own would render a double underscore — and a prefix that is a legal
		// identifier on its own can still push an index name past what the
		// supported engines accept.
		test.Error(t, (&Config{TablePrefix: "trailing_"}).ValidateWithContext(t.Context()))
		test.Error(t, (&Config{TablePrefix: "9leading"}).ValidateWithContext(t.Context()))
	})
}

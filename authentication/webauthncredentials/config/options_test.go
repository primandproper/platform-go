package webauthncredentialscfg

import (
	"testing"

	"github.com/primandproper/platform-go/v14/authentication/webauthncredentials"

	"github.com/primandproper/primitives-go/authentication/webauthn"
	webauthncache "github.com/primandproper/primitives-go/authentication/webauthn/cache"
	webauthncfg "github.com/primandproper/primitives-go/authentication/webauthn/config"
	"github.com/primandproper/primitives-go/observability"
	loggingnoop "github.com/primandproper/primitives-go/observability/logging/noop"
	metricsnoop "github.com/primandproper/primitives-go/observability/metrics/noop"
	tracingnoop "github.com/primandproper/primitives-go/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewOptions(T *testing.T) {
	T.Parallel()

	T.Run("wants nothing", func(t *testing.T) {
		t.Parallel()

		o := newOptions(nil)

		must.Nil(t, o.logger)
		must.Nil(t, o.tracerProvider)
		must.Nil(t, o.metricsProvider)
	})

	T.Run("ignores a nil option", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{nil, WithLogger(loggingnoop.NewLogger())})
		must.NotNil(t, o.logger)
	})

	T.Run("takes what it is given", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithLogger(loggingnoop.NewLogger()),
			WithTracerProvider(tracingnoop.NewTracerProvider()),
			WithMetricsProvider(metricsnoop.NewMetricsProvider()),
			WithDatabaseStoreOptions(webauthncredentials.WithCodec(nil)),
			// The primitive half's options arrive nested rather than mirrored
			// under names of their own, which is what keeps the two Option sets
			// from having to grow in step.
			WithRelyingPartyOptions(
				webauthncfg.WithRelyingPartyOptions(webauthn.WithLogger(nil)),
				webauthncfg.WithCacheStoreOptions(webauthncache.WithLogger(nil)),
			),
		})

		must.NotNil(t, o.logger)
		must.NotNil(t, o.tracerProvider)
		must.NotNil(t, o.metricsProvider)
		test.SliceLen(t, 2, o.relyingParty)
		test.SliceLen(t, 1, o.databaseStore)
	})

	// What is rendered for the half this package delegates to carries its own
	// observability first and the caller's passthroughs last, so the caller
	// wins.
	T.Run("renders the delegated options with the caller's last", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{
			WithLogger(loggingnoop.NewLogger()),
			WithRelyingPartyOptions(webauthncfg.WithRelyingPartyOptions()),
		})

		test.SliceLen(t, 4, o.relyingPartyOptions())
	})
}

func TestWithPillars(T *testing.T) {
	T.Parallel()

	T.Run("attaches all three at once", func(t *testing.T) {
		t.Parallel()

		pillars := &observability.Pillars{
			Logger:          loggingnoop.NewLogger(),
			TracerProvider:  tracingnoop.NewTracerProvider(),
			MetricsProvider: metricsnoop.NewMetricsProvider(),
		}

		o := newOptions([]Option{WithPillars(pillars)})

		must.NotNil(t, o.logger)
		must.NotNil(t, o.tracerProvider)
		must.NotNil(t, o.metricsProvider)
	})

	// Applied in order with the individual options, so a caller can hand over
	// its pillars and then take one back.
	T.Run("loses to a later individual option", func(t *testing.T) {
		t.Parallel()

		pillars := &observability.Pillars{
			Logger:          loggingnoop.NewLogger(),
			TracerProvider:  tracingnoop.NewTracerProvider(),
			MetricsProvider: metricsnoop.NewMetricsProvider(),
		}

		o := newOptions([]Option{WithPillars(pillars), WithMetricsProvider(nil)})

		must.NotNil(t, o.logger)
		must.Nil(t, o.metricsProvider)
	})

	T.Run("attaches nothing from nil pillars", func(t *testing.T) {
		t.Parallel()

		o := newOptions([]Option{WithPillars(nil)})

		must.Nil(t, o.logger)
		must.Nil(t, o.tracerProvider)
		must.Nil(t, o.metricsProvider)
	})
}

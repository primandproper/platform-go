package webauthncfg

import (
	"testing"

	"github.com/primandproper/platform-go/v13/authentication/webauthn"
	webauthncache "github.com/primandproper/platform-go/v13/authentication/webauthn/cache"
	webauthndatabase "github.com/primandproper/platform-go/v13/authentication/webauthn/database"
	"github.com/primandproper/platform-go/v13/observability"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

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
			WithRelyingPartyOptions(webauthn.WithLogger(nil)),
			WithDatabaseStoreOptions(webauthndatabase.WithCodec(nil)),
			WithCacheStoreOptions(webauthncache.WithLogger(nil)),
		})

		must.NotNil(t, o.logger)
		must.NotNil(t, o.tracerProvider)
		must.NotNil(t, o.metricsProvider)
		test.SliceLen(t, 1, o.relyingParty)
		test.SliceLen(t, 1, o.databaseStore)
		test.SliceLen(t, 1, o.cacheStore)
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

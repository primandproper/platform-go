package emailcfg

import (
	"net/http"
	"testing"

	"github.com/primandproper/platform-go/v6/email"
	"github.com/primandproper/platform-go/v6/email/sendgrid"
	loggingnoop "github.com/primandproper/platform-go/v6/observability/logging/noop"
	"github.com/primandproper/platform-go/v6/observability/metrics"
	metricsnoop "github.com/primandproper/platform-go/v6/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v6/observability/tracing/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterEmailer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderSendgrid,
			Sendgrid: &sendgrid.Config{APIToken: t.Name()},
		}
		cfg.CircuitBreaker.Name = t.Name()

		i := do.New()
		do.ProvideValue(i, t.Context())
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue[metrics.Provider](i, metricsnoop.NewMetricsProvider())
		do.ProvideValue(i, &http.Client{})
		do.ProvideValue(i, cfg)

		RegisterEmailer(i)

		emailer, err := do.Invoke[email.Emailer](i)
		must.NoError(t, err)
		test.NotNil(t, emailer)
	})
}

package emailcfg

import (
	"fmt"
	"net/http"
	"testing"

	circuitbreakingcfg "github.com/primandproper/platform-go/v13/circuitbreaking/config"
	cbnoop "github.com/primandproper/platform-go/v13/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v13/email/mailgun"
	"github.com/primandproper/platform-go/v13/email/mailjet"
	"github.com/primandproper/platform-go/v13/email/postmark"
	"github.com/primandproper/platform-go/v13/email/resend"
	"github.com/primandproper/platform-go/v13/email/sendgrid"
	"github.com/primandproper/platform-go/v13/email/ses"
	"github.com/primandproper/platform-go/v13/errors"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	metricsmock "github.com/primandproper/platform-go/v13/observability/metrics/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
	"go.opentelemetry.io/otel/metric"
)

func TestConfig_ValidateWithContext(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: ProviderSendgrid,
			Sendgrid: &sendgrid.Config{APIToken: t.Name()},
		}

		must.NoError(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("with invalid token", func(t *testing.T) {
		t.Parallel()

		ctx := t.Context()
		cfg := &Config{
			Provider: "sendgrid",
		}

		must.Error(t, cfg.ValidateWithContext(ctx))
	})

	T.Run("mailgun provider requires config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderMailgun}
		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("mailjet provider requires config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderMailjet}
		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("resend provider requires config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderResend}
		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("postmark provider requires config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderPostmark}
		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("ses provider requires config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderSES}
		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("unknown provider fails validation", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: "sendgird"}
		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("empty provider is rejected", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ""}
		test.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("the noop provider is a valid choice", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderNoop}
		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})
}

func TestConfig_EnsureDefaults(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.EnsureDefaults()
		test.NotEq(t, "", cfg.CircuitBreaker.Name)
	})
}

func TestConfig_NewEmailer(T *testing.T) {
	T.Parallel()

	allProviders := []string{
		ProviderSendgrid,
		ProviderMailgun,
		ProviderMailjet,
		ProviderResend,
		ProviderPostmark,
	}

	for _, provider := range allProviders {
		T.Run(fmt.Sprintf("with %s", provider), func(t *testing.T) {
			t.Parallel()

			logger := loggingnoop.NewLogger()
			cfg := &Config{
				Provider: provider,
				Sendgrid: &sendgrid.Config{APIToken: t.Name()},
				Mailgun:  &mailgun.Config{PrivateAPIKey: t.Name(), Domain: t.Name()},
				Mailjet:  &mailjet.Config{APIKey: t.Name(), SecretKey: t.Name()},
				Resend:   &resend.Config{APIToken: t.Name()},
				Postmark: &postmark.Config{ServerToken: t.Name()},
			}

			actual, err := cfg.NewEmailer(t.Context(), &http.Client{}, cbnoop.NewCircuitBreaker(), nil, WithLogger(logger))
			test.NotNil(t, actual)
			test.NoError(t, err)
		})
	}

	T.Run("with ses provider", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()
		cfg := &Config{
			Provider: ProviderSES,
			SES:      &ses.Config{Region: "us-east-1"},
		}

		actual, err := cfg.NewEmailer(t.Context(), &http.Client{}, cbnoop.NewCircuitBreaker(), nil, WithLogger(logger))
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	// Outbound mail disappearing because PROVIDER was unset is the failure this
	// guards; noop is still reachable, but only by name.
	T.Run("with an empty provider", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()
		cfg := &Config{Provider: ""}

		actual, err := cfg.NewEmailer(t.Context(), &http.Client{}, cbnoop.NewCircuitBreaker(), nil, WithLogger(logger))
		test.Error(t, err)
		test.Nil(t, actual)
	})

	T.Run("with an unknown provider", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()
		cfg := &Config{Provider: "smtp"}

		actual, err := cfg.NewEmailer(t.Context(), &http.Client{}, cbnoop.NewCircuitBreaker(), nil, WithLogger(logger))
		test.ErrorIs(t, err, errors.ErrUnknownProvider)
		test.Nil(t, actual)
	})

	T.Run("with the noop provider", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()
		cfg := &Config{Provider: ProviderNoop}

		actual, err := cfg.NewEmailer(t.Context(), &http.Client{}, cbnoop.NewCircuitBreaker(), nil, WithLogger(logger))
		test.NoError(t, err)
		test.NotNil(t, actual)
	})
}

func TestNewEmailer(T *testing.T) {
	T.Parallel()

	T.Run("with the noop provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderNoop}
		cfg.CircuitBreaker.Name = t.Name()

		emailer, err := NewEmailer(
			t.Context(),
			cfg,
			&http.Client{},
		)
		must.NoError(t, err)
		test.NotNil(t, emailer)
	})

	T.Run("with sendgrid provider", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{
			Provider: ProviderSendgrid,
			Sendgrid: &sendgrid.Config{APIToken: t.Name()},
		}
		cfg.CircuitBreaker.Name = t.Name()

		emailer, err := NewEmailer(
			t.Context(),
			cfg,
			&http.Client{},
		)
		must.NoError(t, err)
		test.NotNil(t, emailer)
	})

	T.Run("circuit breaker init failure", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderNoop}
		cfg.CircuitBreaker.Name = "email-breaker"
		cfg.CircuitBreaker.ErrorRate = 50
		cfg.CircuitBreaker.MinimumSampleThreshold = 10

		mp := &metricsmock.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, circuitbreakingcfg.TrippedCounterName, counterName)
				return &metricsmock.Int64CounterMock{}, fmt.Errorf("counter init failure")
			},
		}

		emailer, err := NewEmailer(
			t.Context(),
			cfg,
			&http.Client{},
			WithMetricsProvider(mp),
		)
		must.Error(t, err)
		test.Nil(t, emailer)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})
}

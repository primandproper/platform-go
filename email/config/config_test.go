package emailcfg

import (
	"fmt"
	"net/http"
	"testing"

	cbnoop "github.com/primandproper/platform-go/v2/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v2/email"
	"github.com/primandproper/platform-go/v2/email/mailgun"
	"github.com/primandproper/platform-go/v2/email/mailjet"
	"github.com/primandproper/platform-go/v2/email/postmark"
	"github.com/primandproper/platform-go/v2/email/resend"
	"github.com/primandproper/platform-go/v2/email/sendgrid"
	"github.com/primandproper/platform-go/v2/email/ses"
	loggingnoop "github.com/primandproper/platform-go/v2/observability/logging/noop"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	mockmetrics "github.com/primandproper/platform-go/v2/observability/metrics/mock"
	metricsnoop "github.com/primandproper/platform-go/v2/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v2/observability/tracing/noop"

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
}

func TestConfig_BuildHermes(T *testing.T) {
	T.Parallel()

	T.Run("with branding", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{BaseURL: "https://example.com"}
		h := cfg.BuildHermes(&email.EmailBranding{
			CompanyName: "Acme",
			LogoURL:     "https://example.com/logo.png",
		})
		must.NotNil(t, h)
		test.EqOp(t, "Acme", h.Product.Name)
		test.EqOp(t, "https://example.com/logo.png", h.Product.Logo)
		test.EqOp(t, "https://example.com", h.Product.Link)
		test.StrContains(t, h.Product.Copyright, "Acme")
	})

	T.Run("without branding", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{BaseURL: "https://example.com"}
		h := cfg.BuildHermes(nil)
		must.NotNil(t, h)
		test.EqOp(t, "", h.Product.Name)
		test.EqOp(t, "", h.Product.Logo)
		test.EqOp(t, "", h.Product.Copyright)
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

func TestConfig_ProvideEmailer(T *testing.T) {
	T.Parallel()

	providers := []string{
		ProviderSendgrid,
		ProviderMailgun,
		ProviderMailjet,
		ProviderResend,
		ProviderPostmark,
	}

	for _, provider := range providers {
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

			actual, err := cfg.ProvideEmailer(t.Context(), logger, tracingnoop.NewTracerProvider(), &http.Client{}, cbnoop.NewCircuitBreaker(), nil)
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

		actual, err := cfg.ProvideEmailer(t.Context(), logger, tracingnoop.NewTracerProvider(), &http.Client{}, cbnoop.NewCircuitBreaker(), nil)
		test.NotNil(t, actual)
		test.NoError(t, err)
	})

	T.Run("with invalid provider", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()
		cfg := &Config{
			Provider: "",
		}

		actual, err := cfg.ProvideEmailer(t.Context(), logger, tracingnoop.NewTracerProvider(), &http.Client{}, cbnoop.NewCircuitBreaker(), nil)
		test.NotNil(t, actual)
		test.NoError(t, err)
	})
}

func TestProvideEmailer(T *testing.T) {
	T.Parallel()

	T.Run("standard falls back to noop", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.CircuitBreaker.Name = t.Name()

		emailer, err := ProvideEmailer(
			t.Context(),
			cfg,
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			metricsnoop.NewMetricsProvider(),
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

		emailer, err := ProvideEmailer(
			t.Context(),
			cfg,
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			metricsnoop.NewMetricsProvider(),
			&http.Client{},
		)
		must.NoError(t, err)
		test.NotNil(t, emailer)
	})

	T.Run("circuit breaker init failure", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		cfg.CircuitBreaker.Name = "email-breaker"
		cfg.CircuitBreaker.ErrorRate = 50
		cfg.CircuitBreaker.MinimumSampleThreshold = 10

		mp := &mockmetrics.ProviderMock{
			NewInt64CounterFunc: func(counterName string, _ ...metric.Int64CounterOption) (metrics.Int64Counter, error) {
				test.EqOp(t, "email-breaker_circuit_breaker_tripped", counterName)
				return &mockmetrics.Int64CounterMock{}, fmt.Errorf("counter init failure")
			},
		}

		emailer, err := ProvideEmailer(
			t.Context(),
			cfg,
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			mp,
			&http.Client{},
		)
		must.Error(t, err)
		test.Nil(t, emailer)

		test.SliceLen(t, 1, mp.NewInt64CounterCalls())
	})
}

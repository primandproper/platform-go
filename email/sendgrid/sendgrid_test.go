package sendgrid

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cbnoop "github.com/primandproper/platform-go/circuitbreaking/noop"
	"github.com/primandproper/platform-go/email"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"

	"github.com/sendgrid/sendgrid-go"
	"github.com/sendgrid/sendgrid-go/helpers/mail"
	"github.com/shoenig/test/must"
)

func TestNewSendGridEmailer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		client, err := NewSendGridEmailer(&Config{APIToken: t.Name()}, logger, tracingnoop.NewTracerProvider(), &http.Client{}, cbnoop.NewCircuitBreaker(), nil)
		must.NotNil(t, client)
		must.NoError(t, err)
	})
}

func TestSendGridEmailer_SendEmail(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			res.WriteHeader(http.StatusAccepted)
		}))

		c, err := NewSendGridEmailer(&Config{APIToken: t.Name()}, logger, tracingnoop.NewTracerProvider(), ts.Client(), cbnoop.NewCircuitBreaker(), nil)
		must.NotNil(t, c)
		must.NoError(t, err)

		c.client.BaseURL = ts.URL

		ctx := t.Context()
		details := &email.OutboundEmailMessage{
			ToAddress:   t.Name(),
			ToName:      t.Name(),
			FromAddress: t.Name(),
			FromName:    t.Name(),
			Subject:     t.Name(),
			HTMLContent: t.Name(),
		}

		must.NoError(t, c.SendEmail(ctx, details))
	})

	T.Run("with error executing request", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			time.Sleep(time.Hour)
		}))
		client := ts.Client()
		client.Timeout = time.Millisecond

		c, err := NewSendGridEmailer(&Config{APIToken: t.Name()}, logger, tracingnoop.NewTracerProvider(), client, cbnoop.NewCircuitBreaker(), nil)
		must.NotNil(t, c)
		must.NoError(t, err)

		c.client.BaseURL = ts.URL

		ctx := t.Context()
		details := &email.OutboundEmailMessage{
			ToAddress:   t.Name(),
			ToName:      t.Name(),
			FromAddress: t.Name(),
			FromName:    t.Name(),
			Subject:     t.Name(),
			HTMLContent: t.Name(),
		}

		err = c.SendEmail(ctx, details)
		must.Error(t, err)
	})

	T.Run("with invalid response code", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			res.WriteHeader(http.StatusInternalServerError)
		}))

		c, err := NewSendGridEmailer(&Config{APIToken: t.Name()}, logger, tracingnoop.NewTracerProvider(), ts.Client(), cbnoop.NewCircuitBreaker(), nil)
		must.NotNil(t, c)
		must.NoError(t, err)

		c.client.BaseURL = ts.URL

		ctx := t.Context()
		details := &email.OutboundEmailMessage{
			ToAddress:   t.Name(),
			ToName:      t.Name(),
			FromAddress: t.Name(),
			FromName:    t.Name(),
			Subject:     t.Name(),
			HTMLContent: t.Name(),
		}

		err = c.SendEmail(ctx, details)
		must.Error(t, err)
	})
}

func TestSendGridEmailer_sendDynamicTemplateEmail(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			res.WriteHeader(http.StatusAccepted)
		}))

		c, err := NewSendGridEmailer(&Config{APIToken: t.Name()}, logger, tracingnoop.NewTracerProvider(), ts.Client(), cbnoop.NewCircuitBreaker(), nil)
		must.NotNil(t, c)
		must.NoError(t, err)

		c.client.BaseURL = ts.URL

		ctx := t.Context()
		to := mail.NewEmail("sender", "sender@fake.com")
		from := mail.NewEmail("sender", "sender@fake.com")

		request := sendgrid.GetRequest(c.config.APIToken, "/v3/mail/send", ts.URL)

		must.NoError(t, c.sendDynamicTemplateEmail(ctx, to, from, t.Name(), map[string]any{"things": "stuff"}, request))
	})
}

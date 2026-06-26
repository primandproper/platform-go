package resend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	cbnoop "github.com/primandproper/platform-go/circuitbreaking/noop"
	"github.com/primandproper/platform-go/email"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"

	"github.com/shoenig/test/must"
)

type sendEmailResponse struct {
	Id string `json:"id"`
}

func TestNewResendEmailer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		config := &Config{APIToken: t.Name()}

		client, err := NewResendEmailer(config, logger, tracingnoop.NewTracerProvider(), &http.Client{}, cbnoop.NewCircuitBreaker(), nil)
		must.NotNil(t, client)
		must.NoError(t, err)
	})

	T.Run("with missing config", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		client, err := NewResendEmailer(nil, logger, tracingnoop.NewTracerProvider(), &http.Client{}, cbnoop.NewCircuitBreaker(), nil)
		must.Nil(t, client)
		must.Error(t, err)
	})

	T.Run("with missing config API token", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		config := &Config{}

		client, err := NewResendEmailer(config, logger, tracingnoop.NewTracerProvider(), &http.Client{}, cbnoop.NewCircuitBreaker(), nil)
		must.Nil(t, client)
		must.Error(t, err)
	})

	T.Run("with missing HTTP client", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		config := &Config{APIToken: t.Name()}

		client, err := NewResendEmailer(config, logger, tracingnoop.NewTracerProvider(), nil, cbnoop.NewCircuitBreaker(), nil)
		must.Nil(t, client)
		must.Error(t, err)
	})
}

func TestResendEmailer_SendEmail(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			json.NewEncoder(res).Encode(sendEmailResponse{Id: t.Name()})
		}))

		cfg := &Config{APIToken: t.Name()}

		c, err := NewResendEmailer(cfg, logger, tracingnoop.NewTracerProvider(), ts.Client(), cbnoop.NewCircuitBreaker(), nil)
		must.NotNil(t, c)
		must.NoError(t, err)

		baseURL, err := url.Parse(ts.URL + "/")
		must.NoError(t, err)
		c.client.BaseURL = baseURL

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

		cfg := &Config{APIToken: t.Name()}

		c, err := NewResendEmailer(cfg, logger, tracingnoop.NewTracerProvider(), client, cbnoop.NewCircuitBreaker(), nil)
		must.NotNil(t, c)
		must.NoError(t, err)

		baseURL, err := url.Parse(ts.URL + "/")
		must.NoError(t, err)
		c.client.BaseURL = baseURL

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

		cfg := &Config{APIToken: t.Name()}

		c, err := NewResendEmailer(cfg, logger, tracingnoop.NewTracerProvider(), ts.Client(), cbnoop.NewCircuitBreaker(), nil)
		must.NotNil(t, c)
		must.NoError(t, err)

		baseURL, err := url.Parse(ts.URL + "/")
		must.NoError(t, err)
		c.client.BaseURL = baseURL

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

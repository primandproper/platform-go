package mailgun

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cbnoop "github.com/primandproper/platform-go/circuitbreaking/noop"
	"github.com/primandproper/platform-go/email"
	loggingnoop "github.com/primandproper/platform-go/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/observability/tracing/noop"

	"github.com/shoenig/test/must"
)

const (
	exampleDomain = "whatever.gov"
)

type sendMessageResponse struct {
	Message string `json:"message"`
	Id      string `json:"id"`
}

func TestNewMailgunEmailer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		config := &Config{Domain: exampleDomain, PrivateAPIKey: t.Name()}

		client, err := NewMailgunEmailer(config, logger, tracingnoop.NewTracerProvider(), &http.Client{}, cbnoop.NewCircuitBreaker(), nil)
		must.NotNil(t, client)
		must.NoError(t, err)
	})

	T.Run("with missing config", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		client, err := NewMailgunEmailer(nil, logger, tracingnoop.NewTracerProvider(), &http.Client{}, cbnoop.NewCircuitBreaker(), nil)
		must.Nil(t, client)
		must.Error(t, err)
	})

	T.Run("with missing config domain", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		config := &Config{PrivateAPIKey: t.Name()}

		client, err := NewMailgunEmailer(config, logger, tracingnoop.NewTracerProvider(), &http.Client{}, cbnoop.NewCircuitBreaker(), nil)
		must.Nil(t, client)
		must.Error(t, err)
	})

	T.Run("with missing config private key", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		config := &Config{Domain: exampleDomain}

		client, err := NewMailgunEmailer(config, logger, tracingnoop.NewTracerProvider(), &http.Client{}, cbnoop.NewCircuitBreaker(), nil)
		must.Nil(t, client)
		must.Error(t, err)
	})

	T.Run("with missing HTTP client", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		config := &Config{Domain: exampleDomain, PrivateAPIKey: t.Name()}

		client, err := NewMailgunEmailer(config, logger, tracingnoop.NewTracerProvider(), nil, cbnoop.NewCircuitBreaker(), nil)
		must.Nil(t, client)
		must.Error(t, err)
	})
}

func TestMailgunEmailer_SendEmail(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			json.NewEncoder(res).Encode(sendMessageResponse{
				Message: "Queued. Thank you.",
				Id:      t.Name(),
			})
		}))

		cfg := &Config{Domain: exampleDomain, PrivateAPIKey: t.Name()}

		c, err := NewMailgunEmailer(cfg, logger, tracingnoop.NewTracerProvider(), ts.Client(), cbnoop.NewCircuitBreaker(), nil)
		must.NotNil(t, c)
		must.NoError(t, err)

		c.client.SetAPIBase(ts.URL + "/v4")

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

		cfg := &Config{Domain: exampleDomain, PrivateAPIKey: t.Name()}

		c, err := NewMailgunEmailer(cfg, logger, tracingnoop.NewTracerProvider(), client, cbnoop.NewCircuitBreaker(), nil)
		must.NotNil(t, c)
		must.NoError(t, err)
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

		cfg := &Config{Domain: exampleDomain, PrivateAPIKey: t.Name()}

		c, err := NewMailgunEmailer(cfg, logger, tracingnoop.NewTracerProvider(), ts.Client(), cbnoop.NewCircuitBreaker(), nil)
		must.NotNil(t, c)
		must.NoError(t, err)

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

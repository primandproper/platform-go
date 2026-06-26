package mailjet

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

	"github.com/mailjet/mailjet-apiv3-go/v4"
	"github.com/shoenig/test/must"
)

func TestNewMailjetEmailer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		config := &Config{SecretKey: t.Name(), APIKey: t.Name()}

		client, err := NewMailjetEmailer(config, logger, tracingnoop.NewTracerProvider(), &http.Client{}, cbnoop.NewCircuitBreaker(), nil)
		must.NotNil(t, client)
		must.NoError(t, err)
	})

	T.Run("with missing config", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		client, err := NewMailjetEmailer(nil, logger, tracingnoop.NewTracerProvider(), &http.Client{}, cbnoop.NewCircuitBreaker(), nil)
		must.Nil(t, client)
		must.Error(t, err)
	})

	T.Run("with missing config secret key", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		config := &Config{APIKey: t.Name()}

		client, err := NewMailjetEmailer(config, logger, tracingnoop.NewTracerProvider(), &http.Client{}, cbnoop.NewCircuitBreaker(), nil)
		must.Nil(t, client)
		must.Error(t, err)
	})

	T.Run("with missing config public key", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		config := &Config{SecretKey: t.Name()}

		client, err := NewMailjetEmailer(config, logger, tracingnoop.NewTracerProvider(), &http.Client{}, cbnoop.NewCircuitBreaker(), nil)
		must.Nil(t, client)
		must.Error(t, err)
	})

	T.Run("with missing HTTP client", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		config := &Config{SecretKey: t.Name(), APIKey: t.Name()}

		client, err := NewMailjetEmailer(config, logger, tracingnoop.NewTracerProvider(), nil, cbnoop.NewCircuitBreaker(), nil)
		must.Nil(t, client)
		must.Error(t, err)
	})
}

func TestMailjetEmailer_SendEmail(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		logger := loggingnoop.NewLogger()

		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			json.NewEncoder(res).Encode(&mailjet.ResultsV31{})
		}))

		config := &Config{SecretKey: t.Name(), APIKey: t.Name()}

		c, err := NewMailjetEmailer(config, logger, tracingnoop.NewTracerProvider(), ts.Client(), cbnoop.NewCircuitBreaker(), nil)
		must.NotNil(t, c)
		must.NoError(t, err)

		c.client.(*mailjet.Client).SetBaseURL(ts.URL + "/")

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

		config := &Config{SecretKey: t.Name(), APIKey: t.Name()}
		client := ts.Client()

		c, err := NewMailjetEmailer(config, logger, tracingnoop.NewTracerProvider(), client, cbnoop.NewCircuitBreaker(), nil)
		must.NotNil(t, c)
		must.NoError(t, err)

		c.client.(*mailjet.Client).SetBaseURL(ts.URL + "/")
		client.Timeout = time.Millisecond

		ctx := t.Context()
		details := &email.OutboundEmailMessage{
			ToAddress:   t.Name(),
			ToName:      t.Name(),
			FromAddress: t.Name(),
			FromName:    t.Name(),
			Subject:     t.Name(),
			HTMLContent: t.Name(),
		}

		must.Error(t, c.SendEmail(ctx, details))
	})
}

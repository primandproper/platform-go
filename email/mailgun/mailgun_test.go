package mailgun

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cbnoop "github.com/primandproper/platform-go/v2/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v2/email"
	"github.com/primandproper/platform-go/v2/observability"
	"github.com/primandproper/platform-go/v2/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/v2/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v2/observability/tracing/noop"

	"github.com/shoenig/test/must"
)

const (
	exampleDomain = "whatever.gov"
)

type sendMessageResponse struct {
	Message string `json:"message"`
	Id      string `json:"id"`
}

// newRecordingEmailer builds an Emailer with a RecordingObserver swapped in, so a
// test can both drive SendEmail and assert which fields it observed.
func newRecordingEmailer(t *testing.T, cfg *Config, client *http.Client) (*Emailer, *observability.RecordingObserver) {
	t.Helper()

	c, err := NewMailgunEmailer(cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), client, cbnoop.NewCircuitBreaker(), nil)
	must.NotNil(t, c)
	must.NoError(t, err)

	obs := observability.NewRecordingObserver()
	c.o11y = obs

	return c, obs
}

func testEmailMessage(t *testing.T) *email.OutboundEmailMessage {
	t.Helper()

	return &email.OutboundEmailMessage{
		ToAddress:   t.Name(),
		ToName:      t.Name(),
		FromAddress: t.Name(),
		FromName:    t.Name(),
		Subject:     t.Name(),
		HTMLContent: t.Name(),
	}
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

		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			must.NoError(t, json.NewEncoder(res).Encode(sendMessageResponse{
				Message: "Queued. Thank you.",
				Id:      t.Name(),
			}))
		}))

		cfg := &Config{Domain: exampleDomain, PrivateAPIKey: t.Name()}

		c, obs := newRecordingEmailer(t, cfg, ts.Client())

		c.client.SetAPIBase(ts.URL + "/v4")

		details := testEmailMessage(t)
		must.NoError(t, c.SendEmail(t.Context(), details))

		obs.ObservedOperationWithData(t, map[string]any{
			keys.EmailSubjectKey:   details.Subject,
			keys.EmailToAddressKey: details.ToAddress,
		})
	})

	T.Run("with error executing request", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			time.Sleep(time.Hour)
		}))

		client := ts.Client()
		client.Timeout = time.Millisecond

		cfg := &Config{Domain: exampleDomain, PrivateAPIKey: t.Name()}

		c, obs := newRecordingEmailer(t, cfg, client)

		details := testEmailMessage(t)
		must.Error(t, c.SendEmail(t.Context(), details))

		// Even though the send failed, the values must still have been observed,
		// and the failure itself recorded on the operation.
		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.EmailSubjectKey:   details.Subject,
			keys.EmailToAddressKey: details.ToAddress,
		})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("with invalid response code", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			res.WriteHeader(http.StatusInternalServerError)
		}))

		cfg := &Config{Domain: exampleDomain, PrivateAPIKey: t.Name()}

		c, obs := newRecordingEmailer(t, cfg, ts.Client())

		c.client.SetAPIBase(ts.URL + "/v4")

		details := testEmailMessage(t)
		must.Error(t, c.SendEmail(t.Context(), details))

		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.EmailSubjectKey:   details.Subject,
			keys.EmailToAddressKey: details.ToAddress,
		})
		must.SliceLen(t, 1, op.Errors)
	})
}

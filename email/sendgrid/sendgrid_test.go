package sendgrid

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cbnoop "github.com/primandproper/platform-go/v5/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v5/email"
	"github.com/primandproper/platform-go/v5/observability"
	"github.com/primandproper/platform-go/v5/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/v5/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v5/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// Minimal mirror of the SendGrid v3 mail-send payload, enough to assert the
// outbound request carried the fields SendEmail was told to send.
type (
	sgAddress struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}
	sgPersonalization struct {
		Subject string      `json:"subject"`
		To      []sgAddress `json:"to"`
	}
	sgContent struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	}
	sgPayload struct {
		From             sgAddress           `json:"from"`
		Subject          string              `json:"subject"`
		Personalizations []sgPersonalization `json:"personalizations"`
		Content          []sgContent         `json:"content"`
	}
)

// newRecordingEmailer builds an Emailer with a RecordingObserver swapped in, so a
// test can both drive SendEmail and assert which fields it observed.
func newRecordingEmailer(t *testing.T, cfg *Config, client *http.Client) (*Emailer, *observability.RecordingObserver) {
	t.Helper()

	c, err := NewSendGridEmailer(cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), client, cbnoop.NewCircuitBreaker(), nil)
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

		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			res.WriteHeader(http.StatusAccepted)
		}))

		c, obs := newRecordingEmailer(t, &Config{APIToken: t.Name()}, ts.Client())
		c.client.BaseURL = ts.URL

		details := testEmailMessage(t)
		must.NoError(t, c.SendEmail(t.Context(), details))

		obs.ObservedOperationWithData(t, map[string]any{
			keys.EmailToAddressKey: details.ToAddress,
		})
	})

	T.Run("sends the correct request shape", func(t *testing.T) {
		t.Parallel()

		// Distinct values per field so a from/to or subject/body swap (the shape
		// of the C-08 bug) fails this test rather than sliding through.
		var gotBody sgPayload
		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			must.NoError(t, json.NewDecoder(req.Body).Decode(&gotBody))
			res.WriteHeader(http.StatusAccepted)
		}))

		c, _ := newRecordingEmailer(t, &Config{APIToken: t.Name()}, ts.Client())
		c.client.BaseURL = ts.URL

		details := &email.OutboundEmailMessage{
			ToAddress:   "recipient@example.com",
			ToName:      "Recipient Name",
			FromAddress: "sender@example.com",
			FromName:    "Sender Name",
			Subject:     "the subject line",
			HTMLContent: "<p>the html body</p>",
		}
		must.NoError(t, c.SendEmail(t.Context(), details))

		test.EqOp(t, details.FromName, gotBody.From.Name)
		test.EqOp(t, details.FromAddress, gotBody.From.Email)
		test.EqOp(t, details.Subject, gotBody.Subject)

		must.SliceLen(t, 1, gotBody.Personalizations)
		must.SliceLen(t, 1, gotBody.Personalizations[0].To)
		test.EqOp(t, details.ToName, gotBody.Personalizations[0].To[0].Name)
		test.EqOp(t, details.ToAddress, gotBody.Personalizations[0].To[0].Email)

		var html string
		for _, ct := range gotBody.Content {
			if ct.Type == "text/html" {
				html = ct.Value
			}
		}
		test.EqOp(t, details.HTMLContent, html)
	})

	T.Run("with error executing request", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			time.Sleep(time.Hour)
		}))
		client := ts.Client()
		client.Timeout = time.Millisecond

		c, obs := newRecordingEmailer(t, &Config{APIToken: t.Name()}, client)
		c.client.BaseURL = ts.URL

		details := testEmailMessage(t)
		must.Error(t, c.SendEmail(t.Context(), details))

		// Even though the send failed, the values must still have been observed.
		// This package records failures span-only via observability.PrepareError,
		// so op.Errors stays empty by design.
		obs.ObservedOperationWithData(t, map[string]any{
			keys.EmailToAddressKey: details.ToAddress,
		})
	})

	T.Run("with invalid response code", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			res.WriteHeader(http.StatusInternalServerError)
		}))

		c, obs := newRecordingEmailer(t, &Config{APIToken: t.Name()}, ts.Client())
		c.client.BaseURL = ts.URL

		details := testEmailMessage(t)
		must.Error(t, c.SendEmail(t.Context(), details))

		obs.ObservedOperationWithData(t, map[string]any{
			keys.EmailToAddressKey: details.ToAddress,
		})
	})
}

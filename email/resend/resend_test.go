package resend

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"net/url"
	"testing"
	"time"

	cbnoop "github.com/primandproper/platform-go/v4/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v4/email"
	"github.com/primandproper/platform-go/v4/observability"
	"github.com/primandproper/platform-go/v4/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/v4/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v4/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestFormatAddress(T *testing.T) {
	T.Parallel()

	T.Run("bare address when name is empty", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "real@example.com", formatAddress("", "real@example.com"))
	})

	T.Run("quotes hostile name to prevent recipient injection", func(t *testing.T) {
		t.Parallel()

		got := formatAddress(`x <a@attacker.com>,`, "real@example.com")

		parsed, err := mail.ParseAddress(got)
		must.NoError(t, err)
		test.EqOp(t, "real@example.com", parsed.Address)

		list, err := mail.ParseAddressList(got)
		must.NoError(t, err)
		test.SliceLen(t, 1, list)
	})
}

type sendEmailResponse struct {
	Id string `json:"id"`
}

// resendPayload mirrors the JSON body Resend's SDK posts to /emails, enough to
// assert the outbound request carried the fields SendEmail was told to send.
type resendPayload struct {
	From    string   `json:"from"`
	Subject string   `json:"subject"`
	Html    string   `json:"html"`
	To      []string `json:"to"`
}

// newRecordingEmailer builds an Emailer with a RecordingObserver swapped in, so a
// test can both drive SendEmail and assert which fields it observed.
func newRecordingEmailer(t *testing.T, cfg *Config, client *http.Client, baseURL string) (*Emailer, *observability.RecordingObserver) {
	t.Helper()

	c, err := NewResendEmailer(cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), client, cbnoop.NewCircuitBreaker(), nil)
	must.NotNil(t, c)
	must.NoError(t, err)

	parsed, err := url.Parse(baseURL + "/")
	must.NoError(t, err)
	c.client.BaseURL = parsed

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

		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			must.NoError(t, json.NewEncoder(res).Encode(sendEmailResponse{Id: t.Name()}))
		}))

		c, obs := newRecordingEmailer(t, &Config{APIToken: t.Name()}, ts.Client(), ts.URL)

		details := testEmailMessage(t)
		must.NoError(t, c.SendEmail(t.Context(), details))

		obs.ObservedOperationWithData(t, map[string]any{
			keys.EmailSubjectKey:   details.Subject,
			keys.EmailToAddressKey: details.ToAddress,
		})
	})

	T.Run("sends the correct request shape", func(t *testing.T) {
		t.Parallel()

		// Distinct values per field so a from/to or subject/body swap (the shape
		// of the C-08 bug) fails this test rather than sliding through.
		var gotBody resendPayload
		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			must.NoError(t, json.NewDecoder(req.Body).Decode(&gotBody))
			must.NoError(t, json.NewEncoder(res).Encode(sendEmailResponse{Id: t.Name()}))
		}))

		c, _ := newRecordingEmailer(t, &Config{APIToken: t.Name()}, ts.Client(), ts.URL)

		details := &email.OutboundEmailMessage{
			ToAddress:   "recipient@example.com",
			ToName:      "Recipient Name",
			FromAddress: "sender@example.com",
			FromName:    "Sender Name",
			Subject:     "the subject line",
			HTMLContent: "<p>the html body</p>",
		}
		must.NoError(t, c.SendEmail(t.Context(), details))

		test.EqOp(t, formatAddress(details.FromName, details.FromAddress), gotBody.From)
		must.SliceLen(t, 1, gotBody.To)
		test.EqOp(t, formatAddress(details.ToName, details.ToAddress), gotBody.To[0])
		test.EqOp(t, details.Subject, gotBody.Subject)
		test.EqOp(t, details.HTMLContent, gotBody.Html)
	})

	T.Run("with error executing request", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			time.Sleep(time.Hour)
		}))

		client := ts.Client()
		client.Timeout = time.Millisecond

		c, obs := newRecordingEmailer(t, &Config{APIToken: t.Name()}, client, ts.URL)

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

		c, obs := newRecordingEmailer(t, &Config{APIToken: t.Name()}, ts.Client(), ts.URL)

		details := testEmailMessage(t)
		must.Error(t, c.SendEmail(t.Context(), details))

		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.EmailSubjectKey:   details.Subject,
			keys.EmailToAddressKey: details.ToAddress,
		})
		must.SliceLen(t, 1, op.Errors)
	})
}

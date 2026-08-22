package mailgun

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	cbnoop "github.com/primandproper/platform-go/v13/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v13/email"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"

	"github.com/shoenig/test"
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

	c, err := NewMailgunEmailer(cfg, client, cbnoop.NewCircuitBreaker())
	must.NotNil(t, c)
	must.NoError(t, err)

	obs := observability.NewRecordingObserver()
	c.o11y = obs

	return c, obs
}

func testEmailMessage(t *testing.T) *email.OutboundEmailMessage {
	t.Helper()

	return &email.OutboundEmailMessage{
		ToAddress:   "recipient@example.com",
		ToName:      "Recipient Name",
		FromAddress: "sender@example.com",
		FromName:    "Sender Name",
		Subject:     "the subject line",
		HTMLContent: "<p>the html body</p>",
	}
}

func TestNewMailgunEmailer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		config := &Config{Domain: exampleDomain, PrivateAPIKey: t.Name()}

		client, err := NewMailgunEmailer(config, &http.Client{}, cbnoop.NewCircuitBreaker())
		must.NotNil(t, client)
		must.NoError(t, err)
	})

	T.Run("with missing config", func(t *testing.T) {
		t.Parallel()

		client, err := NewMailgunEmailer(nil, &http.Client{}, cbnoop.NewCircuitBreaker())
		must.Nil(t, client)
		must.Error(t, err)
	})

	T.Run("with missing config domain", func(t *testing.T) {
		t.Parallel()

		config := &Config{PrivateAPIKey: t.Name()}

		client, err := NewMailgunEmailer(config, &http.Client{}, cbnoop.NewCircuitBreaker())
		must.Nil(t, client)
		must.Error(t, err)
	})

	T.Run("with missing config private key", func(t *testing.T) {
		t.Parallel()

		config := &Config{Domain: exampleDomain}

		client, err := NewMailgunEmailer(config, &http.Client{}, cbnoop.NewCircuitBreaker())
		must.Nil(t, client)
		must.Error(t, err)
	})

	T.Run("with missing HTTP client", func(t *testing.T) {
		t.Parallel()

		config := &Config{Domain: exampleDomain, PrivateAPIKey: t.Name()}

		client, err := NewMailgunEmailer(config, nil, cbnoop.NewCircuitBreaker())
		must.Nil(t, client)
		must.Error(t, err)
	})
}

func TestMailgunEmailer_SendEmail(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		var gotForm url.Values
		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			must.NoError(t, req.ParseMultipartForm(1<<20))
			gotForm = req.Form
			must.NoError(t, json.NewEncoder(res).Encode(sendMessageResponse{
				Message: "Queued. Thank you.",
				Id:      t.Name(),
			}))
		}))

		cfg := &Config{Domain: exampleDomain, PrivateAPIKey: t.Name(), BaseURL: ts.URL + "/v4"}

		c, obs := newRecordingEmailer(t, cfg, ts.Client())

		details := testEmailMessage(t)
		must.NoError(t, c.SendEmail(t.Context(), details))

		// Assert the outbound request carried the right fields — not just that no error
		// came back. This is the effect C-08 corrupted: sender/recipient swapped and HTML
		// smuggled into the plain-text body.
		// Display names are quoted, matching every other provider in this package:
		// they go through mail.Address, so a comma in a name cannot become a
		// second recipient.
		test.EqOp(t, `"Sender Name" <sender@example.com>`, gotForm.Get("from"))
		test.EqOp(t, `"Recipient Name" <recipient@example.com>`, gotForm.Get("to"))
		test.EqOp(t, details.Subject, gotForm.Get("subject"))
		test.EqOp(t, details.HTMLContent, gotForm.Get("html"))
		test.EqOp(t, "", gotForm.Get("text"))

		// The ID Mailgun assigned is what ties this send to the provider's own
		// logs, and it goes on the span under the key the postmark, resend, and
		// ses siblings use.
		obs.ObservedOperationWithData(t, map[string]any{
			keys.EmailSubjectKey:   details.Subject,
			keys.EmailToAddressKey: details.ToAddress,
			"email.message_id":     t.Name(),
		})
	})

	T.Run("honors the configured API base", func(t *testing.T) {
		t.Parallel()

		// Without this the SDK's default host is where mail goes, which is the
		// US region — the whole reason an EU account was unreachable.
		var gotPath string
		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			must.NoError(t, req.ParseMultipartForm(1<<20))
			gotPath = req.URL.Path
			must.NoError(t, json.NewEncoder(res).Encode(sendMessageResponse{
				Message: "Queued. Thank you.",
				Id:      t.Name(),
			}))
		}))
		t.Cleanup(ts.Close)

		// The trailing slash is deliberate: the SDK joins the base to each path
		// with a separator of its own, so it must be trimmed off.
		cfg := &Config{Domain: exampleDomain, PrivateAPIKey: t.Name(), BaseURL: ts.URL + "/v4/"}

		c, _ := newRecordingEmailer(t, cfg, ts.Client())

		must.NoError(t, c.SendEmail(t.Context(), testEmailMessage(t)))
		test.EqOp(t, fmt.Sprintf("/v4/%s/messages", exampleDomain), gotPath)
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

		cfg := &Config{Domain: exampleDomain, PrivateAPIKey: t.Name(), BaseURL: ts.URL + "/v4"}

		c, obs := newRecordingEmailer(t, cfg, ts.Client())

		details := testEmailMessage(t)
		must.Error(t, c.SendEmail(t.Context(), details))

		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.EmailSubjectKey:   details.Subject,
			keys.EmailToAddressKey: details.ToAddress,
		})
		must.SliceLen(t, 1, op.Errors)
	})
}

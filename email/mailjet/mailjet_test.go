package mailjet

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cbnoop "github.com/primandproper/platform-go/v6/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v6/email"
	"github.com/primandproper/platform-go/v6/observability"
	"github.com/primandproper/platform-go/v6/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/v6/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v6/observability/tracing/noop"

	"github.com/mailjet/mailjet-apiv3-go/v4"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// recordingMailjetClient is a fake mailjetClient that captures the messages
// SendEmail hands off, so a test can assert the outbound request shape directly
// without an HTTP round trip.
type recordingMailjetClient struct {
	got    *mailjet.MessagesV31
	result *mailjet.ResultsV31
	err    error
}

func (r *recordingMailjetClient) SendMailV31(data *mailjet.MessagesV31, _ ...mailjet.RequestOptions) (*mailjet.ResultsV31, error) {
	r.got = data
	return r.result, r.err
}

// newRecordingEmailer builds an Emailer with a RecordingObserver swapped in, so a
// test can both drive SendEmail and assert which fields it observed. The Mailjet
// client's base URL is pointed at the provided test server.
func newRecordingEmailer(t *testing.T, cfg *Config, client *http.Client, baseURL string) (*Emailer, *observability.RecordingObserver) {
	t.Helper()

	c, err := NewMailjetEmailer(cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), client, cbnoop.NewCircuitBreaker(), nil)
	must.NotNil(t, c)
	must.NoError(t, err)

	c.client.(*mailjet.Client).SetBaseURL(baseURL + "/")

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

		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			must.NoError(t, json.NewEncoder(res).Encode(&mailjet.ResultsV31{}))
		}))

		config := &Config{SecretKey: t.Name(), APIKey: t.Name()}

		c, obs := newRecordingEmailer(t, config, ts.Client(), ts.URL)

		details := testEmailMessage(t)
		must.NoError(t, c.SendEmail(t.Context(), details))

		obs.ObservedOperationWithData(t, map[string]any{
			keys.EmailSubjectKey:   details.Subject,
			keys.EmailToAddressKey: details.ToAddress,
		})
	})

	T.Run("sends the correct request shape", func(t *testing.T) {
		t.Parallel()

		config := &Config{SecretKey: t.Name(), APIKey: t.Name()}

		c, err := NewMailjetEmailer(config, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), &http.Client{}, cbnoop.NewCircuitBreaker(), nil)
		must.NoError(t, err)

		fake := &recordingMailjetClient{result: &mailjet.ResultsV31{}}
		c.client = fake

		// Distinct values per field so a from/to or subject/body swap (the shape
		// of the C-08 bug) fails this test rather than sliding through.
		details := &email.OutboundEmailMessage{
			ToAddress:   "recipient@example.com",
			ToName:      "Recipient Name",
			FromAddress: "sender@example.com",
			FromName:    "Sender Name",
			Subject:     "the subject line",
			HTMLContent: "<p>the html body</p>",
		}
		must.NoError(t, c.SendEmail(t.Context(), details))

		must.NotNil(t, fake.got)
		must.SliceLen(t, 1, fake.got.Info)

		msg := fake.got.Info[0]
		must.NotNil(t, msg.From)
		test.EqOp(t, details.FromName, msg.From.Name)
		test.EqOp(t, details.FromAddress, msg.From.Email)

		must.NotNil(t, msg.To)
		must.SliceLen(t, 1, *msg.To)
		test.EqOp(t, details.ToName, (*msg.To)[0].Name)
		test.EqOp(t, details.ToAddress, (*msg.To)[0].Email)

		test.EqOp(t, details.Subject, msg.Subject)
		test.EqOp(t, details.HTMLContent, msg.HTMLPart)
	})

	T.Run("with error executing request", func(t *testing.T) {
		t.Parallel()

		ts := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
			time.Sleep(time.Hour)
		}))

		config := &Config{SecretKey: t.Name(), APIKey: t.Name()}
		client := ts.Client()

		c, obs := newRecordingEmailer(t, config, client, ts.URL)

		client.Timeout = time.Millisecond

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
}

package ses

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"testing"

	cbnoop "github.com/primandproper/platform-go/v6/circuitbreaking/noop"
	"github.com/primandproper/platform-go/v6/email"
	"github.com/primandproper/platform-go/v6/observability"
	"github.com/primandproper/platform-go/v6/observability/keys"
	loggingnoop "github.com/primandproper/platform-go/v6/observability/logging/noop"
	tracingnoop "github.com/primandproper/platform-go/v6/observability/tracing/noop"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
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

type mockSESClient struct {
	output *sesv2.SendEmailOutput
	err    error
	input  *sesv2.SendEmailInput
}

func (m *mockSESClient) SendEmail(_ context.Context, input *sesv2.SendEmailInput, _ ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error) {
	m.input = input
	return m.output, m.err
}

// newRecordingEmailer builds an Emailer with a RecordingObserver swapped in, so a
// test can both drive SendEmail and assert which fields it observed.
func newRecordingEmailer(t *testing.T, cfg *Config, sesClient SendEmailAPI) (*Emailer, *observability.RecordingObserver) {
	t.Helper()

	e, err := NewSESEmailer(t.Context(), cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, cbnoop.NewCircuitBreaker(), nil, sesClient)
	must.NoError(t, err)
	must.NotNil(t, e)

	obs := observability.NewRecordingObserver()
	e.o11y = obs

	return e, obs
}

func TestNewSESEmailer(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Region: "us-east-1"}
		mock := &mockSESClient{}

		client, err := NewSESEmailer(t.Context(), cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, cbnoop.NewCircuitBreaker(), nil, mock)
		must.NoError(t, err)
		must.NotNil(t, client)
	})

	T.Run("with nil config", func(t *testing.T) {
		t.Parallel()

		client, err := NewSESEmailer(t.Context(), nil, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, cbnoop.NewCircuitBreaker(), nil, &mockSESClient{})
		must.Error(t, err)
		test.Nil(t, client)
		test.ErrorIs(t, err, ErrNilConfig)
	})

	T.Run("with empty region", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}

		client, err := NewSESEmailer(t.Context(), cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, cbnoop.NewCircuitBreaker(), nil, &mockSESClient{})
		must.Error(t, err)
		test.Nil(t, client)
		test.ErrorIs(t, err, ErrEmptyRegion)
	})

	T.Run("with nil HTTP client and nil SES client", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Region: "us-east-1"}

		client, err := NewSESEmailer(t.Context(), cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, cbnoop.NewCircuitBreaker(), nil, nil)
		must.Error(t, err)
		test.Nil(t, client)
		test.ErrorIs(t, err, ErrNilHTTPClient)
	})

	T.Run("with HTTP client and nil SES client", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Region: "us-east-1"}

		client, err := NewSESEmailer(t.Context(), cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), &http.Client{}, cbnoop.NewCircuitBreaker(), nil, nil)
		must.NoError(t, err)
		must.NotNil(t, client)
	})
}

func TestEmailer_SendEmail(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		mock := &mockSESClient{output: &sesv2.SendEmailOutput{}}
		cfg := &Config{Region: "us-east-1"}

		e, obs := newRecordingEmailer(t, cfg, mock)

		details := &email.OutboundEmailMessage{
			ToAddress:   "to@example.com",
			ToName:      t.Name(),
			FromAddress: "from@example.com",
			FromName:    t.Name(),
			Subject:     t.Name(),
			HTMLContent: t.Name(),
		}

		must.NoError(t, e.SendEmail(t.Context(), details))

		obs.ObservedOperationWithData(t, map[string]any{
			keys.EmailSubjectKey:   details.Subject,
			keys.EmailToAddressKey: details.ToAddress,
		})
	})

	T.Run("sends the correct request shape", func(t *testing.T) {
		t.Parallel()

		mock := &mockSESClient{output: &sesv2.SendEmailOutput{}}
		cfg := &Config{Region: "us-east-1"}

		e, _ := newRecordingEmailer(t, cfg, mock)

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
		must.NoError(t, e.SendEmail(t.Context(), details))

		must.NotNil(t, mock.input)
		test.EqOp(t, formatAddress(details.FromName, details.FromAddress), aws.ToString(mock.input.FromEmailAddress))

		must.NotNil(t, mock.input.Destination)
		must.SliceLen(t, 1, mock.input.Destination.ToAddresses)
		test.EqOp(t, formatAddress(details.ToName, details.ToAddress), mock.input.Destination.ToAddresses[0])

		must.NotNil(t, mock.input.Content)
		must.NotNil(t, mock.input.Content.Simple)
		must.NotNil(t, mock.input.Content.Simple.Subject)
		test.EqOp(t, details.Subject, aws.ToString(mock.input.Content.Simple.Subject.Data))
		must.NotNil(t, mock.input.Content.Simple.Body)
		must.NotNil(t, mock.input.Content.Simple.Body.Html)
		test.EqOp(t, details.HTMLContent, aws.ToString(mock.input.Content.Simple.Body.Html.Data))
	})

	T.Run("without names", func(t *testing.T) {
		t.Parallel()

		mock := &mockSESClient{output: &sesv2.SendEmailOutput{}}
		cfg := &Config{Region: "us-east-1"}

		e, err := NewSESEmailer(t.Context(), cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, cbnoop.NewCircuitBreaker(), nil, mock)
		must.NoError(t, err)

		details := &email.OutboundEmailMessage{
			ToAddress:   "to@example.com",
			FromAddress: "from@example.com",
			Subject:     t.Name(),
			HTMLContent: t.Name(),
		}

		must.NoError(t, e.SendEmail(t.Context(), details))
	})

	T.Run("with error from SES", func(t *testing.T) {
		t.Parallel()

		mock := &mockSESClient{err: errors.New("ses send error")}
		cfg := &Config{Region: "us-east-1"}

		e, obs := newRecordingEmailer(t, cfg, mock)

		details := &email.OutboundEmailMessage{
			ToAddress:   "to@example.com",
			ToName:      t.Name(),
			FromAddress: "from@example.com",
			FromName:    t.Name(),
			Subject:     t.Name(),
			HTMLContent: t.Name(),
		}

		must.Error(t, e.SendEmail(t.Context(), details))

		// Even though the send failed, the values must still have been observed,
		// and the failure itself recorded on the operation.
		op := obs.ObservedOperationWithData(t, map[string]any{
			keys.EmailSubjectKey:   details.Subject,
			keys.EmailToAddressKey: details.ToAddress,
		})
		must.SliceLen(t, 1, op.Errors)
	})

	T.Run("with broken circuit breaker", func(t *testing.T) {
		t.Parallel()

		mock := &mockSESClient{output: &sesv2.SendEmailOutput{}}
		cfg := &Config{Region: "us-east-1"}

		e, err := NewSESEmailer(t.Context(), cfg, loggingnoop.NewLogger(), tracingnoop.NewTracerProvider(), nil, cbnoop.NewCircuitBreaker(), nil, mock)
		must.NoError(t, err)

		e.circuitBreaker = &brokenCircuitBreaker{}

		details := &email.OutboundEmailMessage{
			ToAddress:   "to@example.com",
			ToName:      t.Name(),
			FromAddress: "from@example.com",
			FromName:    t.Name(),
			Subject:     t.Name(),
			HTMLContent: t.Name(),
		}

		err = e.SendEmail(t.Context(), details)
		must.Error(t, err)
	})
}

type brokenCircuitBreaker struct{}

func (*brokenCircuitBreaker) Failed()             {}
func (*brokenCircuitBreaker) Succeeded()          {}
func (*brokenCircuitBreaker) CanProceed() bool    { return false }
func (*brokenCircuitBreaker) CannotProceed() bool { return true }

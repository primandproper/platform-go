package resend

import (
	"context"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v6/circuitbreaking"
	"github.com/primandproper/platform-go/v6/email"
	platformerrors "github.com/primandproper/platform-go/v6/errors"
	"github.com/primandproper/platform-go/v6/observability"
	"github.com/primandproper/platform-go/v6/observability/keys"
	"github.com/primandproper/platform-go/v6/observability/logging"
	"github.com/primandproper/platform-go/v6/observability/metrics"
	"github.com/primandproper/platform-go/v6/observability/tracing"

	"github.com/resend/resend-go/v3"
)

const (
	name = "resend_emailer"
)

var (
	_ email.Emailer = (*Emailer)(nil)

	// ErrNilConfig indicates a nil config was provided.
	ErrNilConfig = platformerrors.New("resend config is nil")
	// ErrEmptyAPIToken indicates an empty API token was provided.
	ErrEmptyAPIToken = platformerrors.New("empty Resend API token")
	// ErrNilHTTPClient indicates a nil HTTP client was provided.
	ErrNilHTTPClient = platformerrors.New("nil HTTP client")
)

type (
	// Emailer uses Resend to send email.
	Emailer struct {
		o11y           observability.Observer
		sendCounter    metrics.Int64Counter
		errorCounter   metrics.Int64Counter
		latencyHist    metrics.Float64Histogram
		client         *resend.Client
		circuitBreaker circuitbreaking.CircuitBreaker
	}
)

// NewResendEmailer returns a new Resend-backed Emailer.
func NewResendEmailer(cfg *Config, logger logging.Logger, tracerProvider tracing.TracerProvider, client *http.Client, circuitBreaker circuitbreaking.CircuitBreaker, metricsProvider metrics.Provider) (*Emailer, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}

	if cfg.APIToken == "" {
		return nil, ErrEmptyAPIToken
	}

	if client == nil {
		return nil, ErrNilHTTPClient
	}

	mp := metrics.EnsureMetricsProvider(metricsProvider)

	sendCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_sends", name))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating send counter")
	}

	errorCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_errors", name))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating error counter")
	}

	latencyHist, err := mp.NewFloat64Histogram(fmt.Sprintf("%s_latency_ms", name))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating latency histogram")
	}

	e := &Emailer{
		o11y:           observability.NewObserver(name, logger, tracerProvider),
		sendCounter:    sendCounter,
		errorCounter:   errorCounter,
		latencyHist:    latencyHist,
		client:         resend.NewCustomClient(client, cfg.APIToken),
		circuitBreaker: circuitBreaker,
	}

	return e, nil
}

func formatAddress(name, address string) string {
	if strings.TrimSpace(name) == "" {
		return address
	}
	return (&mail.Address{Name: name, Address: address}).String()
}

// SendEmail sends an email.
func (e *Emailer) SendEmail(ctx context.Context, details *email.OutboundEmailMessage) error {
	ctx, op := e.o11y.Begin(ctx)
	defer op.End()

	startTime := time.Now()
	defer func() {
		e.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	op.Set(keys.EmailSubjectKey, details.Subject).Set(keys.EmailToAddressKey, details.ToAddress).Set(keys.EmailFromAddressKey, details.FromAddress)

	if e.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	from := formatAddress(details.FromName, details.FromAddress)
	to := formatAddress(details.ToName, details.ToAddress)

	params := &resend.SendEmailRequest{
		From:    from,
		To:      []string{to},
		Subject: details.Subject,
		Html:    details.HTMLContent,
	}

	resp, err := e.client.Emails.SendWithContext(ctx, params)
	if err != nil {
		e.circuitBreaker.Failed()
		e.errorCounter.Add(ctx, 1)
		return op.Error(err, "sending email")
	}

	op.Set("email.message_id", resp.Id)

	e.circuitBreaker.Succeeded()
	e.sendCounter.Add(ctx, 1)
	return nil
}

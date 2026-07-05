package postmark

import (
	"context"
	"fmt"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v4/circuitbreaking"
	"github.com/primandproper/platform-go/v4/email"
	platformerrors "github.com/primandproper/platform-go/v4/errors"
	"github.com/primandproper/platform-go/v4/observability"
	"github.com/primandproper/platform-go/v4/observability/keys"
	"github.com/primandproper/platform-go/v4/observability/logging"
	"github.com/primandproper/platform-go/v4/observability/metrics"
	"github.com/primandproper/platform-go/v4/observability/tracing"

	"github.com/keighl/postmark"
)

const (
	name = "postmark_emailer"
)

var (
	_ email.Emailer = (*Emailer)(nil)

	// ErrNilConfig indicates a nil config was provided.
	ErrNilConfig = platformerrors.New("postmark config is nil")
	// ErrEmptyServerToken indicates an empty server token was provided.
	ErrEmptyServerToken = platformerrors.New("empty Postmark server token")
	// ErrNilHTTPClient indicates a nil HTTP client was provided.
	ErrNilHTTPClient = platformerrors.New("nil HTTP client")
)

type (
	// Emailer uses Postmark to send email.
	Emailer struct {
		o11y           observability.Observer
		sendCounter    metrics.Int64Counter
		errorCounter   metrics.Int64Counter
		latencyHist    metrics.Float64Histogram
		client         *postmark.Client
		circuitBreaker circuitbreaking.CircuitBreaker
	}
)

// NewPostmarkEmailer returns a new Postmark-backed Emailer.
func NewPostmarkEmailer(cfg *Config, logger logging.Logger, tracerProvider tracing.TracerProvider, client *http.Client, circuitBreaker circuitbreaking.CircuitBreaker, metricsProvider metrics.Provider) (*Emailer, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}

	if strings.TrimSpace(cfg.ServerToken) == "" {
		return nil, ErrEmptyServerToken
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

	pm := postmark.NewClient(cfg.ServerToken, "")
	pm.HTTPClient = client
	if cfg.BaseURL != "" {
		pm.BaseURL = strings.TrimSuffix(cfg.BaseURL, "/")
	}

	e := &Emailer{
		o11y:           observability.NewObserver(name, logger, tracerProvider),
		sendCounter:    sendCounter,
		errorCounter:   errorCounter,
		latencyHist:    latencyHist,
		client:         pm,
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

	if details == nil {
		return platformerrors.New("nil outbound email message")
	}

	startTime := time.Now()
	defer func() {
		e.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	op.Set(keys.EmailSubjectKey, details.Subject).Set(keys.EmailToAddressKey, details.ToAddress).Set(keys.EmailFromAddressKey, details.FromAddress)

	if e.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	pmEmail := postmark.Email{
		From:     formatAddress(details.FromName, details.FromAddress),
		To:       formatAddress(details.ToName, details.ToAddress),
		Subject:  details.Subject,
		HtmlBody: details.HTMLContent,
	}

	resp, err := e.client.SendEmail(pmEmail)
	if err != nil {
		e.circuitBreaker.Failed()
		e.errorCounter.Add(ctx, 1)
		return op.Error(err, "sending email")
	}

	op.Set("email.message_id", resp.MessageID)

	e.circuitBreaker.Succeeded()
	e.sendCounter.Add(ctx, 1)
	return nil
}

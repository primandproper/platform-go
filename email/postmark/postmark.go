package postmark

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/primandproper/platform-go/circuitbreaking"
	"github.com/primandproper/platform-go/email"
	platformerrors "github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"

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
		logger         logging.Logger
		tracer         tracing.Tracer
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
		logger:         logging.NewNamedLogger(logger, name),
		tracer:         tracing.NewNamedTracer(tracerProvider, name),
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
	return fmt.Sprintf("%s <%s>", name, address)
}

// SendEmail sends an email.
func (e *Emailer) SendEmail(ctx context.Context, details *email.OutboundEmailMessage) error {
	_, span := e.tracer.StartSpan(ctx)
	defer span.End()

	startTime := time.Now()
	defer func() {
		e.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	logger := e.logger.WithValue("email.subject", details.Subject).WithValue("email.to_address", details.ToAddress)
	tracing.AttachToSpan(span, "to_email", details.ToAddress)

	if e.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	pmEmail := postmark.Email{
		From:     formatAddress(details.FromName, details.FromAddress),
		To:       formatAddress(details.ToName, details.ToAddress),
		Subject:  details.Subject,
		HtmlBody: details.HTMLContent,
	}

	if _, err := e.client.SendEmail(pmEmail); err != nil {
		e.circuitBreaker.Failed()
		e.errorCounter.Add(ctx, 1)
		return observability.PrepareAndLogError(err, logger, span, "sending email")
	}

	e.circuitBreaker.Succeeded()
	e.sendCounter.Add(ctx, 1)
	return nil
}

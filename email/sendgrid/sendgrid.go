package sendgrid

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/primandproper/platform-go/v2/circuitbreaking"
	"github.com/primandproper/platform-go/v2/email"
	platformerrors "github.com/primandproper/platform-go/v2/errors"
	"github.com/primandproper/platform-go/v2/observability"
	"github.com/primandproper/platform-go/v2/observability/keys"
	"github.com/primandproper/platform-go/v2/observability/logging"
	"github.com/primandproper/platform-go/v2/observability/metrics"
	"github.com/primandproper/platform-go/v2/observability/tracing"

	"github.com/sendgrid/rest"
	"github.com/sendgrid/sendgrid-go"
	"github.com/sendgrid/sendgrid-go/helpers/mail"
)

const (
	name = "sendgrid_emailer"
)

var (
	_ email.Emailer = (*Emailer)(nil)
	// ErrNilConfig indicates a nil config was provided.
	ErrNilConfig = platformerrors.New("SendGrid config is nil")
	// ErrEmptyAPIToken indicates an empty API token was provided.
	ErrEmptyAPIToken = platformerrors.New("empty Sendgrid API token")
	// ErrNilHTTPClient indicates a nil HTTP client was provided.
	ErrNilHTTPClient = platformerrors.New("nil HTTP client")
)

type (
	// Emailer uses SendGrid to send email.
	Emailer struct {
		o11y           observability.Observer
		sendCounter    metrics.Int64Counter
		errorCounter   metrics.Int64Counter
		latencyHist    metrics.Float64Histogram
		circuitBreaker circuitbreaking.CircuitBreaker
		client         *sendgrid.Client
		restClient     *rest.Client
		config         Config
	}
)

// NewSendGridEmailer returns a new SendGrid-backed Emailer.
func NewSendGridEmailer(cfg *Config, logger logging.Logger, tracerProvider tracing.TracerProvider, client *http.Client, circuitBreaker circuitbreaking.CircuitBreaker, metricsProvider metrics.Provider) (*Emailer, error) {
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
		client:         sendgrid.NewSendClient(cfg.APIToken),
		restClient:     &rest.Client{HTTPClient: client},
		config:         *cfg,
		circuitBreaker: circuitBreaker,
	}

	return e, nil
}

// ErrSendgridAPIResponse indicates an error occurred in SendGrid.
var ErrSendgridAPIResponse = platformerrors.New("sendgrid request error")

// SendEmail sends an email.
func (e *Emailer) SendEmail(ctx context.Context, details *email.OutboundEmailMessage) error {
	ctx, op := e.o11y.Begin(ctx)
	defer op.End()

	startTime := time.Now()
	defer func() {
		e.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	op.Set(keys.EmailToAddressKey, details.ToAddress).Set(keys.EmailFromAddressKey, details.FromAddress).Set(keys.EmailSubjectKey, details.Subject)

	if e.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	to := mail.NewEmail(details.ToName, details.ToAddress)
	from := mail.NewEmail(details.FromName, details.FromAddress)
	message := mail.NewSingleEmail(from, details.Subject, to, "", details.HTMLContent)

	req := e.client.Request
	req.Body = mail.GetRequestBody(message)
	res, err := e.restClient.SendWithContext(ctx, req)
	if err != nil {
		e.errorCounter.Add(ctx, 1)
		return observability.PrepareError(err, op.Span(), "sending email")
	}

	// Fun fact: if your account is limited and not able to send an email, there is
	// no distinguishing feature of the response to let you know. Thanks, SendGrid!
	if res.StatusCode != http.StatusAccepted {
		op.Logger().Info("sending email yielded an invalid response")
		e.circuitBreaker.Failed()
		e.errorCounter.Add(ctx, 1)
		return observability.PrepareError(ErrSendgridAPIResponse, op.Span(), "sending email yielded a %d response", res.StatusCode)
	}

	e.circuitBreaker.Succeeded()
	e.sendCounter.Add(ctx, 1)
	return nil
}

func (e *Emailer) preparePersonalization(to *mail.Email, data map[string]any) *mail.Personalization {
	p := mail.NewPersonalization()
	p.AddTos(to)

	for k, v := range data {
		p.SetDynamicTemplateData(k, v)
	}

	return p
}

// sendDynamicTemplateEmail sends an email.
func (e *Emailer) sendDynamicTemplateEmail(ctx context.Context, to, from *mail.Email, templateID string, data map[string]any, request rest.Request) error {
	ctx, op := e.o11y.Begin(ctx)
	defer op.End()

	op.Set(keys.EmailToAddressKey, to.Address).Set("email.template_id", templateID).Set(keys.EmailFromAddressKey, from.Address)

	m := mail.NewV3Mail()
	m.SetFrom(from).SetTemplateID(templateID).AddPersonalizations(e.preparePersonalization(to, data))

	request.Body = mail.GetRequestBody(m)

	res, err := e.restClient.SendWithContext(ctx, request)
	if err != nil {
		return observability.PrepareError(err, op.Span(), "sending dynamic email")
	}

	if res.StatusCode != http.StatusAccepted {
		return observability.PrepareError(ErrSendgridAPIResponse, op.Span(), "sending dynamic email yielded a %d response", res.StatusCode)
	}

	return nil
}

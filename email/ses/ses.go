package ses

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

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sesv2"
	"github.com/aws/aws-sdk-go-v2/service/sesv2/types"
)

const (
	name = "ses_emailer"
)

var (
	_ email.Emailer = (*Emailer)(nil)

	// ErrNilConfig indicates a nil config was provided.
	ErrNilConfig = platformerrors.New("ses config is nil")
	// ErrEmptyRegion indicates an empty AWS region was provided.
	ErrEmptyRegion = platformerrors.New("empty AWS region")
	// ErrNilHTTPClient indicates a nil HTTP client was provided.
	ErrNilHTTPClient = platformerrors.New("nil HTTP client")
)

// SendEmailAPI abstracts the SES v2 SendEmail call for testability.
type SendEmailAPI interface {
	SendEmail(ctx context.Context, params *sesv2.SendEmailInput, optFns ...func(*sesv2.Options)) (*sesv2.SendEmailOutput, error)
}

// Emailer uses AWS SES v2 to send email.
type Emailer struct {
	o11y           observability.Observer
	sendCounter    metrics.Int64Counter
	errorCounter   metrics.Int64Counter
	latencyHist    metrics.Float64Histogram
	client         SendEmailAPI
	circuitBreaker circuitbreaking.CircuitBreaker
}

// NewSESEmailer returns a new AWS SES-backed Emailer.
// If sesClient is non-nil it is used directly; otherwise a new SES v2 client
// is created from the default AWS credential chain using the provided HTTP client.
func NewSESEmailer(ctx context.Context, cfg *Config, logger logging.Logger, tracerProvider tracing.TracerProvider, httpClient *http.Client, circuitBreaker circuitbreaking.CircuitBreaker, metricsProvider metrics.Provider, sesClient SendEmailAPI) (*Emailer, error) {
	if cfg == nil {
		return nil, ErrNilConfig
	}

	if cfg.Region == "" {
		return nil, ErrEmptyRegion
	}

	if sesClient == nil && httpClient == nil {
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

	if sesClient != nil {
		return &Emailer{
			o11y:           observability.NewObserver(name, logger, tracerProvider),
			sendCounter:    sendCounter,
			errorCounter:   errorCounter,
			latencyHist:    latencyHist,
			client:         sesClient,
			circuitBreaker: circuitBreaker,
		}, nil
	}

	awsCfg, err := awsconfig.LoadDefaultConfig(ctx,
		awsconfig.WithRegion(cfg.Region),
		awsconfig.WithHTTPClient(httpClient),
	)
	if err != nil {
		return nil, platformerrors.Wrap(err, "loading AWS config")
	}

	return &Emailer{
		o11y:           observability.NewObserver(name, logger, tracerProvider),
		sendCounter:    sendCounter,
		errorCounter:   errorCounter,
		latencyHist:    latencyHist,
		client:         sesv2.NewFromConfig(awsCfg),
		circuitBreaker: circuitBreaker,
	}, nil
}

// SendEmail sends an email via AWS SES v2.
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

	from := details.FromAddress
	if details.FromName != "" {
		from = fmt.Sprintf("%s <%s>", details.FromName, details.FromAddress)
	}

	to := details.ToAddress
	if details.ToName != "" {
		to = fmt.Sprintf("%s <%s>", details.ToName, details.ToAddress)
	}

	input := &sesv2.SendEmailInput{
		FromEmailAddress: aws.String(from),
		Destination: &types.Destination{
			ToAddresses: []string{to},
		},
		Content: &types.EmailContent{
			Simple: &types.Message{
				Subject: &types.Content{
					Data: aws.String(details.Subject),
				},
				Body: &types.Body{
					Html: &types.Content{
						Data: aws.String(details.HTMLContent),
					},
				},
			},
		},
	}

	out, err := e.client.SendEmail(ctx, input)
	if err != nil {
		e.circuitBreaker.Failed()
		e.errorCounter.Add(ctx, 1)
		return op.Error(err, "sending email")
	}

	op.Set("email.message_id", aws.ToString(out.MessageId))

	e.circuitBreaker.Succeeded()
	e.sendCounter.Add(ctx, 1)
	return nil
}

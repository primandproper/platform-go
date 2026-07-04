package fcm

import (
	"context"
	"os"

	"github.com/primandproper/platform-go/v3/errors"
	"github.com/primandproper/platform-go/v3/observability"
	"github.com/primandproper/platform-go/v3/observability/logging"
	"github.com/primandproper/platform-go/v3/observability/metrics"
	"github.com/primandproper/platform-go/v3/observability/tracing"

	firebase "firebase.google.com/go/v4"
	"firebase.google.com/go/v4/messaging"
	"google.golang.org/api/option"
)

const (
	o11yName = "android_notif_sender"
)

// Config holds FCM configuration.
type Config struct {
	// CredentialsPath is the path to the Firebase service account JSON file.
	// If empty, Application Default Credentials (ADC) are used.
	CredentialsPath string
}

// Sender sends push notifications to Android devices via FCM.
type Sender struct {
	client       *messaging.Client
	o11y         observability.Observer
	sendCounter  metrics.Int64Counter
	errorCounter metrics.Int64Counter
}

// NewSender creates an FCM sender from config.
func NewSender(ctx context.Context, cfg *Config, tracerProvider tracing.TracerProvider, logger logging.Logger, metricsProvider metrics.Provider) (*Sender, error) {
	if cfg == nil {
		return nil, errors.New("fcm: config is required")
	}

	var opts []option.ClientOption
	if cfg.CredentialsPath != "" {
		creds, err := os.ReadFile(cfg.CredentialsPath)
		if err != nil {
			return nil, errors.Wrap(err, "fcm: credentials file not found")
		}
		opts = append(opts, option.WithAuthCredentialsJSON(option.ServiceAccount, creds))
	}
	// If CredentialsPath is empty, Application Default Credentials (ADC) are used.

	app, err := firebase.NewApp(ctx, nil, opts...)
	if err != nil {
		return nil, errors.Wrap(err, "fcm: initializing app")
	}

	client, err := app.Messaging(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "fcm: creating messaging client")
	}

	mp := metrics.EnsureMetricsProvider(metricsProvider)

	sendCounter, err := mp.NewInt64Counter(o11yName + "_sends")
	if err != nil {
		return nil, errors.Wrap(err, "fcm: creating send counter")
	}

	errorCounter, err := mp.NewInt64Counter(o11yName + "_errors")
	if err != nil {
		return nil, errors.Wrap(err, "fcm: creating error counter")
	}

	return &Sender{
		client:       client,
		o11y:         observability.NewObserver(o11yName, logger, tracerProvider),
		sendCounter:  sendCounter,
		errorCounter: errorCounter,
	}, nil
}

// Send sends a push notification to a single device token.
func (s *Sender) Send(ctx context.Context, deviceToken, title, body string) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	op.Set("title", title)

	msg := &messaging.Message{
		Token: deviceToken,
		Notification: &messaging.Notification{
			Title: title,
			Body:  body,
		},
	}

	messageID, err := s.client.Send(ctx, msg)
	if err != nil {
		s.errorCounter.Add(ctx, 1)
		return op.Error(err, "sending fcm message")
	}

	op.Set("fcm.message_id", messageID)

	s.sendCounter.Add(ctx, 1)
	return nil
}

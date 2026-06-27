package apns

import (
	"context"
	"regexp"

	"github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/keys"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"

	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/payload"
	"github.com/sideshow/apns2/token"
)

// apnsDeviceTokenHexPattern validates a 64-character hex string (32-byte token).
var apnsDeviceTokenHexPattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

const (
	o11yName = "ios_notif_sender"
)

// Config holds APNs configuration.
type Config struct {
	AuthKeyPath string
	KeyID       string
	TeamID      string
	BundleID    string
	Production  bool
}

// Sender sends push notifications to iOS devices via APNs.
type Sender struct {
	o11y         observability.Observer
	client       *apns2.Client
	sendCounter  metrics.Int64Counter
	errorCounter metrics.Int64Counter
	topic        string
}

// NewSender creates an APNs sender from config.
func NewSender(cfg *Config, tracerProvider tracing.TracerProvider, logger logging.Logger, metricsProvider metrics.Provider) (*Sender, error) {
	if cfg == nil || cfg.AuthKeyPath == "" || cfg.KeyID == "" || cfg.TeamID == "" || cfg.BundleID == "" {
		return nil, errors.New("apns: missing required config (authKeyPath, keyID, teamID, bundleID)")
	}

	authKey, err := token.AuthKeyFromFile(cfg.AuthKeyPath)
	if err != nil {
		return nil, errors.Wrap(err, "apns: loading auth key")
	}

	t := &token.Token{
		AuthKey: authKey,
		KeyID:   cfg.KeyID,
		TeamID:  cfg.TeamID,
	}
	if _, err = t.Generate(); err != nil {
		return nil, errors.Wrap(err, "apns: generating token")
	}

	client := apns2.NewTokenClient(t)
	if cfg.Production {
		client = client.Production()
	} else {
		client = client.Development()
	}

	mp := metrics.EnsureMetricsProvider(metricsProvider)

	sendCounter, err := mp.NewInt64Counter(o11yName + "_sends")
	if err != nil {
		return nil, errors.Wrap(err, "apns: creating send counter")
	}

	errorCounter, err := mp.NewInt64Counter(o11yName + "_errors")
	if err != nil {
		return nil, errors.Wrap(err, "apns: creating error counter")
	}

	return &Sender{
		client:       client,
		topic:        cfg.BundleID,
		o11y:         observability.NewObserver(o11yName, logger, tracerProvider),
		sendCounter:  sendCounter,
		errorCounter: errorCounter,
	}, nil
}

// Send sends a push notification to a single device token.
// The device token must be a 64-character hex string (APNs format).
// badgeCount is optional; when non-nil, sets aps.badge on the app icon.
func (s *Sender) Send(ctx context.Context, deviceToken, title, body string, badgeCount *int) error {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	if !apnsDeviceTokenHexPattern.MatchString(deviceToken) {
		return errors.Newf("apns: invalid device token format (expected 64 hex chars, got len %d)", len(deviceToken))
	}

	op.Set("title", title)

	p := payload.NewPayload().
		AlertTitle(title).
		AlertBody(body)
	if badgeCount != nil {
		p = p.Badge(*badgeCount)
	}

	n := &apns2.Notification{
		DeviceToken: deviceToken,
		Topic:       s.topic,
		Payload:     p,
		Priority:    apns2.PriorityHigh,
	}

	res, err := s.client.PushWithContext(ctx, n)
	if err != nil {
		s.errorCounter.Add(ctx, 1)
		return errors.Wrap(err, "apns: push failed")
	}

	if !res.Sent() {
		s.errorCounter.Add(ctx, 1)
		err = errors.Newf("apns: %s (status %d)", res.Reason, res.StatusCode)
		op.Set("statusCode", res.StatusCode).
			Set(keys.ReasonKey, res.Reason).
			Set("apnsID", res.ApnsID)
		return op.Error(err, "sending apns notification")
	}

	s.sendCounter.Add(ctx, 1)
	return nil
}

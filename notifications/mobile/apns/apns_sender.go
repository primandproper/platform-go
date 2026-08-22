package apns

import (
	"context"

	"github.com/primandproper/platform-go/v13/charset"
	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	validation "github.com/go-ozzo/ozzo-validation/v4"
	"github.com/sideshow/apns2"
	"github.com/sideshow/apns2/payload"
	"github.com/sideshow/apns2/token"
)

// apnsDeviceToken is a 64-character hex string, which is how APNs spells the
// 32-byte token. Either case: the token is minted by the device and arrives as
// it was given, so rejecting one for its case would refuse a value that is not
// wrong.
var apnsDeviceToken = charset.New(charset.HexDigits, charset.WithExactLength(64))

const (
	o11yName = "ios_notif_sender"
)

// Config holds APNs configuration.
//
// It carries its own env tags and validation, rather than being filled in field
// by field from a parallel struct in the config subpackage. The parallel one was
// where the env tags and the validation lived, so this — the struct a caller
// building a Sender by hand actually writes — was the one with neither, and a
// field added here reached a deployment's environment only if somebody
// remembered to add it in two places.
//
// AuthKeyPath is the .p8 signing key downloaded from Apple, KeyID identifies it,
// TeamID is the Apple Developer team it belongs to, and BundleID is the app's
// bundle identifier — which is also the APNs topic. Production selects Apple's
// production gateway rather than the sandbox one.
type Config struct {
	AuthKeyPath string `env:"AUTH_KEY_PATH" json:"authKeyPath,omitempty" yaml:"authKeyPath,omitempty"`
	KeyID       string `env:"KEY_ID"        json:"keyID,omitempty"       yaml:"keyID,omitempty"`
	TeamID      string `env:"TEAM_ID"       json:"teamID,omitempty"      yaml:"teamID,omitempty"`
	BundleID    string `env:"BUNDLE_ID"     json:"bundleID,omitempty"    yaml:"bundleID,omitempty"`
	Production  bool   `env:"PRODUCTION"    json:"production,omitempty"  yaml:"production,omitempty"`
}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config. APNs has no ambient-credential
// equivalent of FCM's Application Default Credentials, so every one of these has
// to be supplied.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.AuthKeyPath, validation.Required),
		validation.Field(&cfg.KeyID, validation.Required),
		validation.Field(&cfg.TeamID, validation.Required),
		validation.Field(&cfg.BundleID, validation.Required),
	)
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
//
// It takes a context so it can validate through the config's own rules rather
// than repeating the required-field list here, which is the FCM sibling's
// signature as well.
func NewSender(ctx context.Context, cfg *Config, opts ...Option) (*Sender, error) {
	if cfg == nil {
		return nil, errors.Wrap(errors.ErrNilInputParameter, "apns: config is required")
	}

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "apns: validating config")
	}

	o := newOptions(opts)

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

	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

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
		o11y:         observability.NewObserver(o11yName, o.logger, o.tracerProvider),
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

	if !apnsDeviceToken.Valid(deviceToken) {
		return op.Error(errors.Newf("apns: invalid device token format (expected 64 hex chars, got len %d)", len(deviceToken)), "validating device token")
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
		// The rejection path below goes through op.Error and this one used to
		// return a bare wrap, so the transport failures — the ones where APNs was
		// unreachable rather than unhappy — were the failures that left a green
		// span. The FCM sibling reports its equivalent through op.Error.
		return op.Error(err, "apns: push failed")
	}

	if !res.Sent() {
		s.errorCounter.Add(ctx, 1)
		err = errors.Newf("apns: %s (status %d)", res.Reason, res.StatusCode)
		op.Set("statusCode", res.StatusCode).
			Set(keys.ReasonKey, res.Reason).
			Set("apnsID", res.ApnsID)
		return op.Error(err, "sending apns notification")
	}

	op.Set("apnsID", res.ApnsID)

	s.sendCounter.Add(ctx, 1)
	return nil
}

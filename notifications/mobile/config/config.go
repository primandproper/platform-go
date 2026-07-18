package config

import (
	"context"
	"strings"

	"github.com/primandproper/platform-go/v5/errors"
	"github.com/primandproper/platform-go/v5/notifications/mobile"
	"github.com/primandproper/platform-go/v5/notifications/mobile/apns"
	"github.com/primandproper/platform-go/v5/notifications/mobile/fcm"
	"github.com/primandproper/platform-go/v5/notifications/mobile/noop"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	"github.com/primandproper/platform-go/v5/observability/tracing"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderAPNsFCM represents the real APNs + FCM implementation.
	ProviderAPNsFCM = "apns_fcm"
	// ProviderNoop represents the no-op implementation.
	ProviderNoop = "noop"
)

type (
	// APNsConfig configures APNs for iOS push notifications.
	APNsConfig struct {
		AuthKeyPath string `env:"AUTH_KEY_PATH" json:"authKeyPath" yaml:"authKeyPath"`
		KeyID       string `env:"KEY_ID"        json:"keyID"       yaml:"keyID"`
		TeamID      string `env:"TEAM_ID"       json:"teamID"      yaml:"teamID"`
		BundleID    string `env:"BUNDLE_ID"     json:"bundleID"    yaml:"bundleID"`
		Production  bool   `env:"PRODUCTION"    json:"production"  yaml:"production"`
	}

	// FCMConfig configures FCM for Android push notifications.
	FCMConfig struct {
		// CredentialsPath is the path to the Firebase service account JSON file.
		// If empty, Application Default Credentials (ADC) are used.
		CredentialsPath string `env:"CREDENTIALS_PATH" json:"credentialsPath" yaml:"credentialsPath"`
	}

	// Config is the push notifications configuration.
	Config struct {
		APNs     *APNsConfig `env:",init"    envPrefix:"APNS_" json:"apns"     yaml:"apns"`
		FCM      *FCMConfig  `env:",init"    envPrefix:"FCM_"  json:"fcm"      yaml:"fcm"`
		Provider string      `env:"PROVIDER" json:"provider"   yaml:"provider"`
	}
)

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates the Config.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	return validation.ValidateStructWithContext(
		ctx,
		cfg,
		validation.Field(&cfg.APNs, validation.When(
			provider == ProviderAPNsFCM && cfg.FCM == nil,
			validation.Required,
		)),
		validation.Field(&cfg.FCM, validation.When(
			provider == ProviderAPNsFCM && cfg.APNs == nil,
			validation.Required,
		)),
	)
}

// NewPushSender returns a PushNotificationSender based on config.
// When provider is "apns_fcm", each configured platform must initialize
// successfully; a failed init surfaces as an error rather than silently
// degrading to a noop that would report every SendPush as a success.
func (cfg *Config) NewPushSender(
	ctx context.Context,
	logger logging.Logger,
	tracerProvider tracing.TracerProvider,
	metricsProvider metrics.Provider,
) (mobile.PushNotificationSender, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case ProviderAPNsFCM:
		var apnsSender *apns.Sender
		if cfg.APNs != nil {
			apnsCfg := &apns.Config{
				AuthKeyPath: cfg.APNs.AuthKeyPath,
				KeyID:       cfg.APNs.KeyID,
				TeamID:      cfg.APNs.TeamID,
				BundleID:    cfg.APNs.BundleID,
				Production:  cfg.APNs.Production,
			}
			s, err := apns.NewSender(apnsCfg, tracerProvider, logger, metricsProvider)
			if err != nil {
				return nil, errors.Wrap(err, "initializing APNs sender")
			}
			apnsSender = s
		}

		var fcmSender *fcm.Sender
		if cfg.FCM != nil {
			fcmCfg := &fcm.Config{CredentialsPath: cfg.FCM.CredentialsPath}
			s, err := fcm.NewSender(ctx, fcmCfg, tracerProvider, logger, metricsProvider)
			if err != nil {
				return nil, errors.Wrap(err, "initializing FCM sender")
			}
			fcmSender = s
		}

		if apnsSender == nil && fcmSender == nil {
			return nil, errors.New("apns_fcm provider selected but neither APNs nor FCM is configured")
		}
		return mobile.NewMultiPlatformPushSender(apnsSender, fcmSender, logger, tracerProvider), nil
	default:
		logger.Debug("push notifications: using noop sender")
		return noop.NewPushNotificationSender(), nil
	}
}

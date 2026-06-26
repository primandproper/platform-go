package config

import (
	"context"
	"strings"

	"github.com/primandproper/platform-go/notifications/mobile"
	"github.com/primandproper/platform-go/notifications/mobile/apns"
	"github.com/primandproper/platform-go/notifications/mobile/fcm"
	"github.com/primandproper/platform-go/notifications/mobile/noop"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"

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
		AuthKeyPath string `env:"AUTH_KEY_PATH" json:"authKeyPath"`
		KeyID       string `env:"KEY_ID"        json:"keyID"`
		TeamID      string `env:"TEAM_ID"       json:"teamID"`
		BundleID    string `env:"BUNDLE_ID"     json:"bundleID"`
		Production  bool   `env:"PRODUCTION"    json:"production"`
	}

	// FCMConfig configures FCM for Android push notifications.
	FCMConfig struct {
		// CredentialsPath is the path to the Firebase service account JSON file.
		// If empty, Application Default Credentials (ADC) are used.
		CredentialsPath string `env:"CREDENTIALS_PATH" json:"credentialsPath"`
	}

	// Config is the push notifications configuration.
	Config struct {
		APNs     *APNsConfig `env:"init"     envPrefix:"APNS_" json:"apns"`
		FCM      *FCMConfig  `env:"init"     envPrefix:"FCM_"  json:"fcm"`
		Provider string      `env:"PROVIDER" json:"provider"`
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

// ProvidePushSender returns a PushNotificationSender based on config.
// When provider is "apns_fcm" and at least one platform config is valid, returns MultiPlatformPushSender.
// Each platform is initialized independently; a failed init for one does not disable the other.
func (cfg *Config) ProvidePushSender(
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
				logger.WithValue("error", err).Debug("push notifications: APNs sender init failed, iOS push disabled")
			} else {
				apnsSender = s
			}
		}

		var fcmSender *fcm.Sender
		if cfg.FCM != nil {
			fcmCfg := &fcm.Config{CredentialsPath: cfg.FCM.CredentialsPath}
			s, err := fcm.NewSender(ctx, fcmCfg, tracerProvider, logger, metricsProvider)
			if err != nil {
				logger.WithValue("error", err).Debug("push notifications: FCM sender init failed, Android push disabled")
			} else {
				fcmSender = s
			}
		}

		if apnsSender == nil && fcmSender == nil {
			logger.Debug("push notifications: no platform senders available, using noop")
			return noop.NewPushNotificationSender(), nil
		}
		return mobile.NewMultiPlatformPushSender(apnsSender, fcmSender, logger, tracerProvider), nil
	default:
		logger.Debug("push notifications: using noop sender")
		return noop.NewPushNotificationSender(), nil
	}
}

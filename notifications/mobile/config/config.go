// Package mobilecfg selects and builds a mobile.PushSender from configuration:
// APNs for iOS, FCM for Android, apns_fcm for both, or noop.
//
// The provider names which platforms are on, and nothing else does. Presence of
// a sub-config decides nothing, which is what lets an empty FCM block mean "use
// Application Default Credentials" rather than "Android is off" — and what makes
// the provider, not the credentials, the thing to read when a platform stops
// receiving pushes.
//
// The sub-configs are the leaf packages' own structs rather than parallel copies
// of them, so a field added to apns.Config is configurable the moment it exists.
package mobilecfg

import (
	"context"
	"slices"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/cfgnorm"
	"github.com/primandproper/platform-go/v13/notifications/mobile"
	"github.com/primandproper/platform-go/v13/notifications/mobile/apns"
	"github.com/primandproper/platform-go/v13/notifications/mobile/fcm"
	"github.com/primandproper/platform-go/v13/notifications/mobile/noop"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderAPNs sends to iOS only.
	ProviderAPNs = "apns"
	// ProviderFCM sends to Android only.
	ProviderFCM = "fcm"
	// ProviderAPNsFCM sends to both iOS and Android.
	ProviderAPNsFCM = "apns_fcm"
	// ProviderNoop represents the no-op implementation, which reports every
	// SendPush as a success. It must be selected deliberately — an unset or
	// typo'd provider is an error, because a push sender that silently succeeds
	// without sending anything is only noticed by the users who got no push.
	ProviderNoop = "noop"
)

type (
	// Config is the push notifications configuration.
	//
	// The sub-configs are the leaf packages' own, not parallel copies of them.
	// The copies existed so that the env tags and the validation could live here
	// while the leaves stayed plain, and the cost was a hand-written field-by-field
	// assignment between two structs that had to be kept in step: a field added
	// to apns.Config was invisible to every deployment until somebody remembered
	// to add it here too, and nothing said so.
	Config struct {
		APNs     *apns.Config `env:",init"    envPrefix:"APNS_"         json:"apns,omitempty"     yaml:"apns,omitempty"`
		FCM      *fcm.Config  `env:",init"    envPrefix:"FCM_"          json:"fcm,omitempty"      yaml:"fcm,omitempty"`
		Provider string       `env:"PROVIDER" json:"provider,omitempty" yaml:"provider,omitempty"`
	}
)

// providers are every provider this package implements. Validation and
// NewPushSender both read it.
var providers = []string{ProviderAPNs, ProviderFCM, ProviderAPNsFCM, ProviderNoop}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates the Config.
//
// The provider is the gate: it names which platforms are on, and each named one
// must be usable. Presence of a sub-config decides nothing, which is what lets an
// empty FCM block mean "use Application Default Credentials" rather than
// "Android is off".
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	provider := cfgnorm.Provider(cfg.Provider)

	// Release an APNs block env parsing's ",init" allocated and nothing filled in,
	// so a deployment that selected FCM alone is not asked for iOS credentials by
	// the APNsConfig validation below.
	cfgnorm.ZeroToNil(&cfg.APNs)

	return validation.ValidateStructWithContext(
		ctx,
		cfg,
		validation.Field(&cfg.Provider, validation.Required, validation.By(func(any) error {
			// Checked normalized, matching dispatch: validating the raw string
			// rejected "APNS" and " apns_fcm " while NewPushSender built them —
			// and, worse, the When guard below keys on the same normalized
			// value, so a mixed-case selection skipped its credential check
			// entirely on the way to being rejected for the spelling.
			if !slices.Contains(providers, provider) {
				return errors.Wrapf(errors.ErrUnknownProvider, "push notification provider %q", cfg.Provider)
			}

			return nil
		})),
		validation.Field(&cfg.APNs, validation.When(
			provider == ProviderAPNs || provider == ProviderAPNsFCM,
			validation.Required,
		)),
	)
}

// NewPushSender returns a PushNotificationSender based on config.
//
// The provider names the platforms, and each one it names must initialize
// successfully; a failed init surfaces as an error rather than silently degrading
// to a noop that would report every SendPush as a success.
func (cfg *Config) NewPushSender(
	ctx context.Context,
	opts ...Option,
) (mobile.PushNotificationSender, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	provider, err := cfgnorm.SelectProvider(cfg.Provider, providers, "push notification provider")
	if err != nil {
		return nil, err
	}

	if err = cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating push notification config")
	}

	switch provider {
	case ProviderAPNs, ProviderFCM, ProviderAPNsFCM:
		var apnsSender *apns.Sender
		if provider == ProviderAPNs || provider == ProviderAPNsFCM {
			if cfg.APNs == nil {
				return nil, errors.Newf("push notification provider %q selected with no APNs config", provider)
			}

			s, senderErr := apns.NewSender(ctx, cfg.APNs, apns.WithTracerProvider(tracerProvider), apns.WithLogger(logger), apns.WithMetricsProvider(metricsProvider))
			if senderErr != nil {
				return nil, errors.Wrap(senderErr, "initializing APNs sender")
			}
			apnsSender = s
		}

		var fcmSender *fcm.Sender
		if provider == ProviderFCM || provider == ProviderAPNsFCM {
			// A nil or empty FCM block asks for Application Default Credentials,
			// so there is nothing to require here.
			fcmCfg := cfg.FCM
			if fcmCfg == nil {
				fcmCfg = &fcm.Config{}
			}

			s, senderErr := fcm.NewSender(ctx, fcmCfg, fcm.WithTracerProvider(tracerProvider), fcm.WithLogger(logger), fcm.WithMetricsProvider(metricsProvider))
			if senderErr != nil {
				return nil, errors.Wrap(senderErr, "initializing FCM sender")
			}
			fcmSender = s
		}

		return mobile.NewMultiPlatformPushSender(apnsSender, fcmSender, mobile.WithLogger(logger), mobile.WithTracerProvider(tracerProvider)), nil
	case ProviderNoop:
		return noop.NewPushNotificationSender(), nil
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "push notification provider %q", cfg.Provider)
	}
}

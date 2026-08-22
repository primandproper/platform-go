package mobilecfg

import (
	"testing"

	"github.com/primandproper/platform-go/v13/notifications/mobile/apns"
	"github.com/primandproper/platform-go/v13/notifications/mobile/fcm"

	"github.com/caarlos0/env/v11"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestProviderGatesPlatforms covers the rule that the provider — not the presence
// of a sub-config — decides which platforms are on.
//
// Every case runs env.Parse first, because `env:",init"` allocates both sub-configs
// and it was that allocation which broke the previous nil-based selection: an
// Android-only deployment had a non-nil empty APNs block, so the sender was built
// from empty iOS credentials and construction failed outright.
func TestProviderGatesPlatforms(T *testing.T) {
	T.Parallel()

	completeAPNs := func() *apns.Config {
		return &apns.Config{AuthKeyPath: "/keys/apns.p8", KeyID: "K1", TeamID: "T1", BundleID: "com.example.app"}
	}

	T.Run("fcm alone validates with no FCM block at all", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderFCM}
		must.NoError(t, env.Parse(cfg))

		// The allocation happened; it just no longer means anything.
		must.NotNil(t, cfg.APNs)

		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("fcm alone validates with an empty FCM block, which asks for ADC", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderFCM, FCM: &fcm.Config{}}
		must.NoError(t, env.Parse(cfg))
		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("apns alone requires iOS credentials", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderAPNs}
		must.NoError(t, env.Parse(cfg))
		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("apns alone validates when its credentials are supplied", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderAPNs, APNs: completeAPNs()}
		must.NoError(t, env.Parse(cfg))
		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("a partially filled APNs block is refused rather than half-used", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderAPNs, APNs: &apns.Config{TeamID: "T1"}}
		must.NoError(t, env.Parse(cfg))
		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("both platforms still require the iOS half", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderAPNsFCM}
		must.NoError(t, env.Parse(cfg))
		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("both platforms validate with iOS credentials and ADC for Android", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderAPNsFCM, APNs: completeAPNs()}
		must.NoError(t, env.Parse(cfg))
		must.NoError(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("an unset provider is still an error", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{}
		must.NoError(t, env.Parse(cfg))
		must.Error(t, cfg.ValidateWithContext(t.Context()))
	})

	T.Run("noop needs no platform config", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderNoop}
		must.NoError(t, env.Parse(cfg))
		must.NoError(t, cfg.ValidateWithContext(t.Context()))

		sender, err := cfg.NewPushSender(t.Context())
		must.NoError(t, err)
		test.NotNil(t, sender)
	})

	T.Run("selecting APNs without a config is refused at construction too", func(t *testing.T) {
		t.Parallel()

		cfg := &Config{Provider: ProviderAPNs}
		_, err := cfg.NewPushSender(t.Context())
		must.Error(t, err)
	})
}

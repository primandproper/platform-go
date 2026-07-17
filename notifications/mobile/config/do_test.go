package config

import (
	"context"
	"testing"

	"github.com/primandproper/platform-go/v5/notifications/mobile"
	loggingnoop "github.com/primandproper/platform-go/v5/observability/logging/noop"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	tracingnoop "github.com/primandproper/platform-go/v5/observability/tracing/noop"

	"github.com/samber/do/v2"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestRegisterPushSender(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		i := do.New()
		do.ProvideValue[context.Context](i, t.Context())
		do.ProvideValue(i, loggingnoop.NewLogger())
		do.ProvideValue(i, tracingnoop.NewTracerProvider())
		do.ProvideValue[metrics.Provider](i, nil)
		do.ProvideValue(i, Config{Provider: ProviderNoop})

		RegisterPushSender(i)

		sender, err := do.Invoke[mobile.PushNotificationSender](i)
		must.NoError(t, err)
		test.NotNil(t, sender)
	})
}

func TestNewPushSender(T *testing.T) {
	T.Parallel()

	T.Run("standard", func(t *testing.T) {
		t.Parallel()

		sender, err := NewPushSender(
			t.Context(),
			Config{Provider: ProviderNoop},
			loggingnoop.NewLogger(),
			tracingnoop.NewTracerProvider(),
			nil,
		)
		must.NoError(t, err)
		test.NotNil(t, sender)
	})
}

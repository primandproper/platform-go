package asynccfg

import (
	"context"
	"strings"

	"github.com/primandproper/platform-go/v5/errors"
	"github.com/primandproper/platform-go/v5/notifications/async"
	"github.com/primandproper/platform-go/v5/notifications/async/ably"
	"github.com/primandproper/platform-go/v5/notifications/async/noop"
	"github.com/primandproper/platform-go/v5/notifications/async/pusher"
	asyncsse "github.com/primandproper/platform-go/v5/notifications/async/sse"
	asyncws "github.com/primandproper/platform-go/v5/notifications/async/websocket"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	"github.com/primandproper/platform-go/v5/observability/tracing"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderPusher is the Pusher provider.
	ProviderPusher = "pusher"
	// ProviderAbly is the Ably provider.
	ProviderAbly = "ably"
	// ProviderWebSocket is the WebSocket provider.
	ProviderWebSocket = "websocket"
	// ProviderSSE is the SSE provider.
	ProviderSSE = "sse"
	// ProviderNoop is the no-op provider.
	ProviderNoop = "noop"
)

type (
	// Config is the configuration for the async notifications provider.
	Config struct {
		Pusher    *pusher.Config   `env:"init"     envPrefix:"PUSHER_"    json:"pusher,omitempty"    yaml:"pusher,omitempty"`
		Ably      *ably.Config     `env:"init"     envPrefix:"ABLY_"      json:"ably,omitempty"      yaml:"ably,omitempty"`
		WebSocket *asyncws.Config  `env:"init"     envPrefix:"WEBSOCKET_" json:"websocket,omitempty" yaml:"websocket,omitempty"`
		SSE       *asyncsse.Config `env:"init"     envPrefix:"SSE_"       json:"sse,omitempty"       yaml:"sse,omitempty"`
		Provider  string           `env:"PROVIDER" json:"provider"        yaml:"provider"`
	}
)

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Provider, validation.In(ProviderPusher, ProviderAbly, ProviderWebSocket, ProviderSSE, ProviderNoop, "")),
		validation.Field(&cfg.Pusher, validation.When(cfg.Provider == ProviderPusher, validation.Required)),
		validation.Field(&cfg.Ably, validation.When(cfg.Provider == ProviderAbly, validation.Required)),
		validation.Field(&cfg.WebSocket, validation.When(cfg.Provider == ProviderWebSocket, validation.Required)),
	)
}

// NewAsyncNotifier provides an AsyncNotifier based on configuration.
func (cfg *Config) NewAsyncNotifier(logger logging.Logger, tracerProvider tracing.TracerProvider, metricsProvider metrics.Provider) (async.AsyncNotifier, error) {
	switch strings.TrimSpace(strings.ToLower(cfg.Provider)) {
	case ProviderPusher:
		return pusher.NewNotifier(cfg.Pusher, logger, tracerProvider, metricsProvider)
	case ProviderAbly:
		return ably.NewNotifier(cfg.Ably, logger, tracerProvider, metricsProvider)
	case ProviderWebSocket:
		return asyncws.NewNotifier(cfg.WebSocket, logger, tracerProvider)
	case ProviderSSE:
		return asyncsse.NewNotifier(cfg.SSE, logger, tracerProvider)
	case "", ProviderNoop:
		return noop.NewAsyncNotifier()
	default:
		return nil, errors.Newf("unknown async notifications provider: %q", cfg.Provider)
	}
}

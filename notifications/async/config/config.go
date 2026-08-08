package asynccfg

import (
	"context"
	"strings"

	"github.com/primandproper/platform-go/v10/errors"
	"github.com/primandproper/platform-go/v10/messagequeue"
	"github.com/primandproper/platform-go/v10/notifications/async"
	"github.com/primandproper/platform-go/v10/notifications/async/ably"
	"github.com/primandproper/platform-go/v10/notifications/async/fanout"
	"github.com/primandproper/platform-go/v10/notifications/async/noop"
	"github.com/primandproper/platform-go/v10/notifications/async/pusher"
	asyncsse "github.com/primandproper/platform-go/v10/notifications/async/sse"
	asyncws "github.com/primandproper/platform-go/v10/notifications/async/websocket"

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
		Pusher    *pusher.Config   `env:",init"    envPrefix:"PUSHER_"       json:"pusher,omitempty"    yaml:"pusher,omitempty"`
		Ably      *ably.Config     `env:",init"    envPrefix:"ABLY_"         json:"ably,omitempty"      yaml:"ably,omitempty"`
		WebSocket *asyncws.Config  `env:",init"    envPrefix:"WEBSOCKET_"    json:"websocket,omitempty" yaml:"websocket,omitempty"`
		SSE       *asyncsse.Config `env:",init"    envPrefix:"SSE_"          json:"sse,omitempty"       yaml:"sse,omitempty"`
		FanOut    *fanout.Config   `env:",init"    envPrefix:"FANOUT_"       json:"fanOut,omitempty"    yaml:"fanOut,omitempty"`
		Provider  string           `env:"PROVIDER" json:"provider,omitempty" yaml:"provider,omitempty"`
	}
)

var (
	_ validation.ValidatableWithContext = (*Config)(nil)

	// ErrFanOutNotApplicable is returned when the messagequeue backplane is
	// enabled for a provider that does not need it.
	//
	// Pusher and Ably are already fleet-safe: a hosted broker holds the
	// connections. Fanning out over one of them would have every replica consume
	// the topic and republish to the hosted broker, so every client would receive
	// one copy per replica — a duplicate-delivery bug introduced by a setting
	// whose entire purpose is to prevent a delivery bug. Noop delivers nowhere,
	// so a backplane in front of it moves messages for no reason.
	ErrFanOutNotApplicable = errors.New("fan-out applies only to the self-hosted async notification providers")
)

// fanOutEnabled reports whether the backplane was asked for. `env:",init"`
// leaves FanOut non-nil, so presence is not the signal — Enabled is.
func (cfg *Config) fanOutEnabled() bool {
	return cfg.FanOut != nil && cfg.FanOut.Enabled
}

// fanOutApplies reports whether a provider holds its connections in process
// memory, which is the only case a backplane helps.
func fanOutApplies(provider string) bool {
	switch cleanProvider(provider) {
	case ProviderWebSocket, ProviderSSE:
		return true
	default:
		return false
	}
}

func cleanProvider(provider string) string {
	return strings.TrimSpace(strings.ToLower(provider))
}

// EnsureDefaults fills in zero fields on the sub-configs that have defaults.
//
// Call it before ValidateWithContext, not after: the fan-out topic has a
// documented default, and validating first would turn the ordinary case —
// fan-out switched on, topic left alone — into a validation failure.
// NewAsyncNotifier does this for callers who go through it.
func (cfg *Config) EnsureDefaults() {
	if cfg.FanOut != nil {
		cfg.FanOut.EnsureDefaults()
	}
}

// ValidateWithContext validates a Config struct.
//
// The sub-configs for providers that were not selected are skipped rather than
// merely unguarded: ozzo validates any non-nil pointer to a Validatable once a
// field's rules have run, and `env:",init"` leaves every sub-config non-nil. A
// validation.When guard alone stops the Required rule and nothing else, so
// Pusher's and Ably's credentials were required at once and no config could load.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	if err := validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Provider, validation.In(ProviderPusher, ProviderAbly, ProviderWebSocket, ProviderSSE, ProviderNoop, "")),
		validation.Field(&cfg.Pusher, validation.Skip.When(cfg.Provider != ProviderPusher), validation.Required),
		validation.Field(&cfg.Ably, validation.Skip.When(cfg.Provider != ProviderAbly), validation.Required),
		validation.Field(&cfg.WebSocket, validation.Skip.When(cfg.Provider != ProviderWebSocket), validation.Required),
		validation.Field(&cfg.FanOut, validation.Skip.When(!cfg.fanOutEnabled())),
	); err != nil {
		return err
	}

	// Checked outside the struct rules because it is a relationship between two
	// fields rather than a property of either: the fan-out config is perfectly
	// valid, and so is the provider — together they are a mistake.
	if cfg.fanOutEnabled() && !fanOutApplies(cfg.Provider) {
		return errors.Wrapf(ErrFanOutNotApplicable, "provider %q", cfg.Provider)
	}

	return nil
}

// NewAsyncNotifier provides an AsyncNotifier based on configuration.
//
// The messagequeue providers are only consulted when fan-out is enabled, which
// is why they may be nil: a deployment that has not asked for a backplane has no
// reason to stand a broker up for one.
func (cfg *Config) NewAsyncNotifier(
	ctx context.Context,
	publisherProvider messagequeue.PublisherProvider,
	consumerProvider messagequeue.ConsumerProvider,
	opts ...Option,
) (async.AsyncNotifier, error) {
	cfg.EnsureDefaults()

	local, err := cfg.newLocalNotifier(opts...)
	if err != nil {
		return nil, err
	}

	if !cfg.fanOutEnabled() {
		return local, nil
	}

	if !fanOutApplies(cfg.Provider) {
		// Reachable without a prior ValidateWithContext, and silence here would
		// mean either duplicate delivery or none — see ErrFanOutNotApplicable.
		return nil, errors.Join(errors.Wrapf(ErrFanOutNotApplicable, "provider %q", cfg.Provider), local.Close())
	}

	o := newOptions(opts)

	wrapped, err := fanout.New(ctx, cfg.FanOut, local, publisherProvider, consumerProvider,
		fanout.WithLogger(o.logger), fanout.WithTracerProvider(o.tracerProvider), fanout.WithMetricsProvider(o.metricsProvider))
	if err != nil {
		// The local notifier is this function's to release: nobody else has a
		// reference to it yet.
		return nil, errors.Join(errors.Wrap(err, "wrapping async notifier in messagequeue fan-out"), local.Close())
	}

	return wrapped, nil
}

// newLocalNotifier builds the notifier the Provider field names, with no
// backplane around it. Under fan-out this is the local delivery sink rather than
// the notifier callers publish through.
func (cfg *Config) newLocalNotifier(opts ...Option) (async.AsyncNotifier, error) {
	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	switch cleanProvider(cfg.Provider) {
	case ProviderPusher:
		return pusher.NewNotifier(cfg.Pusher, pusher.WithLogger(logger), pusher.WithTracerProvider(tracerProvider), pusher.WithMetricsProvider(metricsProvider))
	case ProviderAbly:
		return ably.NewNotifier(cfg.Ably, ably.WithLogger(logger), ably.WithTracerProvider(tracerProvider), ably.WithMetricsProvider(metricsProvider))
	case ProviderWebSocket:
		return asyncws.NewNotifier(cfg.WebSocket, asyncws.WithLogger(logger), asyncws.WithTracerProvider(tracerProvider))
	case ProviderSSE:
		return asyncsse.NewNotifier(cfg.SSE, asyncsse.WithLogger(logger), asyncsse.WithTracerProvider(tracerProvider))
	case "", ProviderNoop:
		return noop.NewAsyncNotifier()
	default:
		return nil, errors.Newf("unknown async notifications provider: %q", cfg.Provider)
	}
}

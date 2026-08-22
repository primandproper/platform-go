// Package eventstreamcfg selects and builds an eventstream upgrader from
// configuration: SSE or WebSocket.
//
// The two constructors do not accept the same set of providers. SSE is
// server-to-client only, so NewBidirectionalEventStreamUpgrader errors when it
// is selected while NewEventStreamUpgrader builds it — a config valid for one
// call is not necessarily valid for the other, and which one a service needs is
// decided by the code, not by the environment.
//
// An absent WebSocket block is a configured WebSocket rather than a missing one:
// every field of websocket.Config has a default, and the upgrader documents a
// nil config as "use them".
package eventstreamcfg

import (
	"context"
	"slices"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/eventstream"
	"github.com/primandproper/platform-go/v13/eventstream/sse"
	"github.com/primandproper/platform-go/v13/eventstream/websocket"
	"github.com/primandproper/platform-go/v13/internal/cfgnorm"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderSSE is the SSE provider.
	ProviderSSE = "sse"
	// ProviderWebSocket is the websocket provider.
	ProviderWebSocket = "websocket"
)

type (
	// Config is the configuration for the event stream provider.
	Config struct {
		WebSocket *websocket.Config `env:",init"    envPrefix:"WEBSOCKET_"    json:"websocket,omitempty" yaml:"websocket,omitempty"`
		Provider  string            `env:"PROVIDER" json:"provider,omitempty" yaml:"provider,omitempty"`
	}
)

// providers are every provider this package implements. Validation and both
// upgrader constructors read it.
var providers = []string{ProviderSSE, ProviderWebSocket}

var _ validation.ValidatableWithContext = (*Config)(nil)

// ValidateWithContext validates a Config struct.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	provider := cfgnorm.Provider(cfg.Provider)

	return validation.ValidateStructWithContext(ctx, cfg,
		// Required as well as known: an unset provider was accepted here and
		// then refused by both constructors, so the one config that could not
		// work was also the one validation had nothing to say about.
		validation.Field(&cfg.Provider, validation.Required, validation.By(func(any) error {
			// Checked normalized, matching dispatch: validating the raw string
			// rejected "WebSocket" and " sse " while the constructors built them.
			if !slices.Contains(providers, provider) {
				return errors.Wrapf(errors.ErrUnknownProvider, "eventstream provider %q", cfg.Provider)
			}

			return nil
		})),
		// Present-when-selected is deliberately not required here: every field
		// of websocket.Config has a default, and NewUpgrader documents a nil
		// config as "use them", so an absent block is a configured websocket
		// rather than a missing one. The rule said otherwise and went unnoticed
		// for as long as nothing ran it.
		validation.Field(&cfg.WebSocket, validation.Skip.When(provider != ProviderWebSocket)),
	)
}

// prepare validates cfg, shared by both constructors, and returns the
// normalized provider they dispatch on.
func prepare(ctx context.Context, cfg *Config) (string, error) {
	if cfg == nil {
		return "", errors.ErrNilInputParameter
	}

	provider, err := cfgnorm.SelectProvider(cfg.Provider, providers, "eventstream provider")
	if err != nil {
		return "", err
	}

	if err = cfg.ValidateWithContext(ctx); err != nil {
		return "", errors.Wrap(err, "validating eventstream config")
	}

	return provider, nil
}

// NewEventStreamUpgrader provides an EventStreamUpgrader based on configuration.
func NewEventStreamUpgrader(ctx context.Context, cfg *Config, opts ...Option) (eventstream.EventStreamUpgrader, error) {
	o := newOptions(opts)
	logger, tracerProvider := o.logger, o.tracerProvider

	provider, err := prepare(ctx, cfg)
	if err != nil {
		return nil, err
	}

	switch provider {
	case ProviderSSE:
		return sse.NewUpgrader(sse.WithLogger(logger), sse.WithTracerProvider(tracerProvider)), nil
	case ProviderWebSocket:
		return websocket.NewUpgrader(cfg.WebSocket, websocket.WithLogger(logger), websocket.WithTracerProvider(tracerProvider)), nil
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "eventstream provider %q", cfg.Provider)
	}
}

// NewBidirectionalEventStreamUpgrader provides a BidirectionalEventStreamUpgrader based on configuration.
func NewBidirectionalEventStreamUpgrader(ctx context.Context, cfg *Config, opts ...Option) (eventstream.BidirectionalEventStreamUpgrader, error) {
	o := newOptions(opts)
	logger, tracerProvider := o.logger, o.tracerProvider

	provider, err := prepare(ctx, cfg)
	if err != nil {
		return nil, err
	}

	switch provider {
	case ProviderSSE:
		return nil, errors.New("SSE does not support bidirectional event streams")
	case ProviderWebSocket:
		return websocket.NewUpgrader(cfg.WebSocket, websocket.WithLogger(logger), websocket.WithTracerProvider(tracerProvider)), nil
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "eventstream provider %q", cfg.Provider)
	}
}

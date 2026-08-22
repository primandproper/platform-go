// Package asynccfg selects and builds an async.AsyncNotifier from configuration:
// Pusher, Ably, WebSocket, SSE, or noop.
//
// The providers split into two classes, and the split is a deployment constraint
// rather than a preference. Pusher and Ably are fleet-safe, because a hosted
// broker holds the connections. WebSocket and SSE hold them in this process's
// memory and are therefore correct at exactly one replica.
//
// That is why this config carries Topology, which no sibling seam does. A
// process cannot count its own replicas, so nothing here can tell a correct
// single-replica deployment from one that silently stopped delivering half its
// notifications when it scaled out. Selecting a self-hosted provider requires
// declaring the topology, which turns the assumption into something somebody
// wrote down.
package asynccfg

import (
	"context"
	"slices"

	"github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/internal/cfgnorm"
	"github.com/primandproper/platform-go/v13/notifications/async"
	"github.com/primandproper/platform-go/v13/notifications/async/ably"
	"github.com/primandproper/platform-go/v13/notifications/async/noop"
	"github.com/primandproper/platform-go/v13/notifications/async/pusher"
	asyncsse "github.com/primandproper/platform-go/v13/notifications/async/sse"
	asyncws "github.com/primandproper/platform-go/v13/notifications/async/websocket"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

// The providers split into two classes that this constant block is the only
// place anyone chooses between, and the difference is a deployment constraint
// rather than a preference:
//
//   - pusher and ably are fleet-safe. A hosted broker holds the connections, so
//     any replica can publish to a client connected to any other.
//   - websocket and sse hold connections in the publishing process, and are
//     therefore correct only at one replica. At two, a client connected to one
//     instance silently misses every event published on another. See those
//     packages' docs for why there is no messagequeue backplane that would lift
//     the constraint.
const (
	// ProviderPusher is the Pusher provider. Fleet-safe.
	ProviderPusher = "pusher"
	// ProviderAbly is the Ably provider. Fleet-safe.
	ProviderAbly = "ably"
	// ProviderWebSocket is the WebSocket provider. Correct at one replica only.
	ProviderWebSocket = "websocket"
	// ProviderSSE is the SSE provider. Correct at one replica only.
	ProviderSSE = "sse"
	// ProviderNoop is the no-op provider.
	ProviderNoop = "noop"
)

const (
	// TopologySingleReplica declares that exactly one replica of this service
	// runs. It is the only topology the self-hosted providers support.
	TopologySingleReplica = "single_replica"
	// TopologyFleet declares that more than one replica runs, which requires a
	// provider whose broker is outside this process.
	TopologyFleet = "fleet"
)

// selfHosted reports whether a provider holds its client connections in this
// process's memory, which is what makes replica count load-bearing.
func selfHosted(provider string) bool {
	switch cfgnorm.Provider(provider) {
	case ProviderSSE, ProviderWebSocket:
		return true
	default:
		return false
	}
}

// providers are every provider this package implements. Validation and
// NewAsyncNotifier both read it.
//
// The empty string is deliberately absent, unlike in the sibling seams: an
// unset provider used to select the noop notifier, which is indistinguishable
// from a deployment that meant to turn notifications off. Not sending them has
// to be asked for by name.
var providers = []string{ProviderPusher, ProviderAbly, ProviderWebSocket, ProviderSSE, ProviderNoop}

var (
	// ErrTopologyRequired is returned when a self-hosted provider is selected
	// without declaring a Topology.
	//
	// A process cannot count its own replicas, so nothing here can tell a
	// correct single-replica deployment from one that silently lost half its
	// notifications when it scaled to two. Requiring the declaration is what
	// converts that into a decision somebody made on purpose.
	ErrTopologyRequired = errors.New("sse and websocket require an explicit topology declaration")

	// ErrFleetUnsupportedForSelfHostedProvider is returned when a self-hosted
	// provider is paired with TopologyFleet.
	//
	// sse and websocket hold connections in process memory, so a Publish on one
	// replica cannot reach subscribers on another. Use pusher or ably for a
	// fleet: their brokers hold the connections and already have the semantics
	// this combination is reaching for.
	ErrFleetUnsupportedForSelfHostedProvider = errors.New("sse and websocket cannot serve a fleet; use a hosted provider")
)

type (
	// Config is the configuration for the async notifications provider.
	Config struct {
		Pusher    *pusher.Config   `env:",init"    envPrefix:"PUSHER_"       json:"pusher,omitempty"    yaml:"pusher,omitempty"`
		Ably      *ably.Config     `env:",init"    envPrefix:"ABLY_"         json:"ably,omitempty"      yaml:"ably,omitempty"`
		WebSocket *asyncws.Config  `env:",init"    envPrefix:"WEBSOCKET_"    json:"websocket,omitempty" yaml:"websocket,omitempty"`
		SSE       *asyncsse.Config `env:",init"    envPrefix:"SSE_"          json:"sse,omitempty"       yaml:"sse,omitempty"`
		Provider  string           `env:"PROVIDER" json:"provider,omitempty" yaml:"provider,omitempty"`

		// Topology declares how many replicas of this service run. It is
		// required for the self-hosted providers and ignored for the rest,
		// which are correct at any replica count.
		//
		// See the async package documentation for why this is declared rather
		// than detected.
		Topology string `env:"TOPOLOGY" json:"topology,omitempty" yaml:"topology,omitempty"`
	}
)

var _ validation.ValidatableWithContext = (*Config)(nil)

// validateTopology reports whether Provider and Topology agree.
//
// It is separate from ValidateWithContext because NewAsyncNotifier applies it
// too: a Config reaching the constructor without having been validated is the
// case where a silent single-replica assumption would otherwise survive.
func (cfg *Config) validateTopology() error {
	if !selfHosted(cfg.Provider) {
		return nil
	}

	switch cfgnorm.Provider(cfg.Topology) {
	case TopologySingleReplica:
		return nil
	case TopologyFleet:
		return errors.Wrapf(ErrFleetUnsupportedForSelfHostedProvider, "provider %q", cfg.Provider)
	default:
		// The observed value goes in the message because this branch also
		// catches a typo, and "requires a declaration" alone reads as a denial
		// that one was made.
		return errors.Wrapf(ErrTopologyRequired, "provider %q, topology %q", cfg.Provider, cfg.Topology)
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
	provider := cfgnorm.Provider(cfg.Provider)

	if err := validation.ValidateStructWithContext(ctx, cfg,
		// Required as well as constrained: ozzo's rules skip an empty value, so
		// an unset provider would validate cleanly and be left for the rule
		// below to catch only because providers omits "".
		validation.Field(&cfg.Provider, validation.Required, validation.By(func(any) error {
			// Checked normalized, matching dispatch: validating the raw string
			// rejected "Pusher" and " ably " while NewAsyncNotifier built them.
			if !slices.Contains(providers, provider) {
				return errors.Wrapf(errors.ErrUnknownProvider, "async notifications provider %q", cfg.Provider)
			}

			return nil
		})),
		// Normalized for the same reason, and because validateTopology reads it
		// the same way: a "Fleet" that reached the constructor was refused there
		// and accepted here.
		validation.Field(&cfg.Topology, validation.By(func(any) error {
			switch cfgnorm.Provider(cfg.Topology) {
			case "", TopologySingleReplica, TopologyFleet:
				return nil
			default:
				return errors.Newf("unknown topology %q", cfg.Topology)
			}
		})),
		validation.Field(&cfg.Pusher, validation.Skip.When(provider != ProviderPusher), validation.Required),
		validation.Field(&cfg.Ably, validation.Skip.When(provider != ProviderAbly), validation.Required),
		validation.Field(&cfg.WebSocket, validation.Skip.When(provider != ProviderWebSocket), validation.Required),
		// Required despite having no fields to fill in, so the two direct-connection
		// providers answer a missing block the same way. Its constructor refuses a
		// nil Config like the websocket one does.
		validation.Field(&cfg.SSE, validation.Skip.When(provider != ProviderSSE), validation.Required),
	); err != nil {
		return err
	}

	return cfg.validateTopology()
}

// NewAsyncNotifier provides an AsyncNotifier based on configuration.
//
// It takes a context so that the whole config goes through
// ValidateWithContext, of which the topology agreement was previously the only
// part this path ran — a pusher deployment with no credentials got as far as
// its first publish.
//
// Every branch assigns into a variable and returns only once its error is
// known to be nil: the provider packages hand back their own *Notifier, and
// returning one straight through would convert a nil pointer into a non-nil
// async.AsyncNotifier on the error path — a value that passes a caller's nil
// check and panics on the first publish.
func (cfg *Config) NewAsyncNotifier(ctx context.Context, opts ...Option) (async.AsyncNotifier, error) {
	if cfg == nil {
		return nil, errors.ErrNilInputParameter
	}

	provider, err := cfgnorm.SelectProvider(cfg.Provider, providers, "async notifications provider")
	if err != nil {
		return nil, err
	}

	if err = cfg.ValidateWithContext(ctx); err != nil {
		return nil, errors.Wrap(err, "validating async notifications config")
	}

	o := newOptions(opts)
	logger, tracerProvider, metricsProvider := o.logger, o.tracerProvider, o.metricsProvider

	var notifier async.AsyncNotifier

	switch provider {
	case ProviderPusher:
		notifier, err = pusher.NewNotifier(cfg.Pusher, pusher.WithLogger(logger), pusher.WithTracerProvider(tracerProvider), pusher.WithMetricsProvider(metricsProvider))
	case ProviderAbly:
		notifier, err = ably.NewNotifier(cfg.Ably, ably.WithLogger(logger), ably.WithTracerProvider(tracerProvider), ably.WithMetricsProvider(metricsProvider))
	case ProviderWebSocket:
		notifier, err = asyncws.NewNotifier(cfg.WebSocket, asyncws.WithLogger(logger), asyncws.WithTracerProvider(tracerProvider))
	case ProviderSSE:
		notifier, err = asyncsse.NewNotifier(cfg.SSE, asyncsse.WithLogger(logger), asyncsse.WithTracerProvider(tracerProvider))
	case ProviderNoop:
		// Only by name. An unset provider never reaches here — SelectProvider
		// refuses it, because "notify nobody, forever" is a decision somebody
		// has to make.
		notifier, err = noop.NewAsyncNotifier()
	default:
		return nil, errors.Wrapf(errors.ErrUnknownProvider, "async notifications provider %q", cfg.Provider)
	}

	if err != nil {
		return nil, err
	}

	return notifier, nil
}

/*
Package inboundcfg assembles an inbound webhook receiver from environment
configuration: the Verifier for the provider's signing scheme, and the Receiver
that mounts on a router and publishes what it verifies.

One Config describes one provider endpoint, because a Receiver serves one — it
holds one scheme, one set of secrets, and one topic. A service taking webhooks
from two providers carries two of these, under two env prefixes, and mounts two
receivers.

The topic is a config field and the PublisherProvider is a parameter, which is
the split the rest of the module uses: the name of the destination is an
operator's decision and belongs in the environment, while the broker connection
is a live dependency this package cannot build and messagequeuecfg already
does.
*/
package inboundcfg

import (
	"context"
	"slices"
	"strings"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/messagequeue"
	"github.com/primandproper/platform-go/v13/webhooks/inbound"

	validation "github.com/go-ozzo/ozzo-validation/v4"
)

const (
	// ProviderStripe verifies Stripe's Stripe-Signature scheme.
	ProviderStripe = "stripe"
	// ProviderGitHub verifies GitHub's X-Hub-Signature-256.
	ProviderGitHub = "github"
	// ProviderHMAC verifies a configurable single-header HMAC over the raw
	// body, for providers this package does not name. It must be selected
	// deliberately — it is never what an unrecognized provider falls back to.
	ProviderHMAC = "hmac"
)

// providers are every provider this package implements. Validation and the
// dispatch switch both read it, so they cannot drift apart.
var providers = []string{ProviderStripe, ProviderGitHub, ProviderHMAC}

type (
	// HMACConfig describes the generic single-header HMAC scheme. It is read
	// only when Provider is ProviderHMAC.
	HMACConfig struct {
		_ struct{} `json:"-" yaml:"-"`

		// Header names the request header carrying the MAC, e.g.
		// "X-Shopify-Hmac-Sha256". Required for ProviderHMAC.
		Header string `env:"HEADER" json:"header,omitempty" yaml:"header,omitempty"`

		// Prefix is the algorithm label the provider writes ahead of the
		// encoded MAC, e.g. "sha256=". Empty when the header carries the MAC
		// alone.
		Prefix string `env:"PREFIX" json:"prefix,omitempty" yaml:"prefix,omitempty"`

		// Digest selects the hash: "sha256" (the default) or "sha512".
		Digest string `env:"DIGEST" json:"digest,omitempty" yaml:"digest,omitempty"`

		// Encoding selects how the MAC is rendered: "hex" (the default) or
		// "base64".
		Encoding string `env:"ENCODING" json:"encoding,omitempty" yaml:"encoding,omitempty"`

		// Provider is the label stamped onto every Delivery and onto this
		// package's metrics. Required for ProviderHMAC, where "hmac" would
		// name the scheme rather than who sent it.
		Provider string `env:"PROVIDER" json:"provider,omitempty" yaml:"provider,omitempty"`
	}

	// Config configures one inbound webhook endpoint.
	Config struct {
		_ struct{} `json:"-" yaml:"-"`

		// HMAC configures the generic scheme, and is read only when Provider
		// is "hmac".
		HMAC HMACConfig `env:",init" envPrefix:"HMAC_" json:"hmac,omitzero" yaml:"hmac,omitempty"`

		// Provider selects the signing scheme: "stripe", "github", or "hmac".
		Provider string `env:"PROVIDER" json:"provider,omitempty" yaml:"provider,omitempty"`

		// Topic is the queue topic verified deliveries are published to.
		// Required.
		Topic string `env:"TOPIC" json:"topic,omitempty" yaml:"topic,omitempty"`

		// Secret is the provider's webhook signing secret. Required.
		//
		// It is json:"-" and yaml:"-" so that a config dump — a debug endpoint,
		// a startup log line, an error message rendering the struct — cannot
		// print the one value that makes this endpoint's verification mean
		// anything.
		Secret string `env:"SECRET" json:"-" yaml:"-"`

		// AdditionalSecrets are further secrets a delivery may be signed
		// under, held while a secret is being rotated. Comma-separated in the
		// environment.
		AdditionalSecrets []string `env:"ADDITIONAL_SECRETS" json:"-" yaml:"-"`

		// ForwardedHeaders narrows the headers a Delivery carries to those
		// named. Empty forwards everything but credential headers.
		ForwardedHeaders []string `env:"FORWARDED_HEADERS" json:"forwardedHeaders,omitempty" yaml:"forwardedHeaders,omitempty"`

		// Tolerance is how far a signed timestamp may sit from the receiver's
		// clock. Defaults to inbound.DefaultTolerance; read only by schemes
		// that sign a timestamp.
		Tolerance time.Duration `env:"TOLERANCE" json:"tolerance,omitempty" yaml:"tolerance,omitempty"`

		// MaxBodyBytes bounds how much of a request body the receiver reads.
		// Defaults to inbound.DefaultMaxBodyBytes.
		MaxBodyBytes int64 `env:"MAX_BODY_BYTES" json:"maxBodyBytes,omitempty" yaml:"maxBodyBytes,omitempty"`
	}
)

var _ validation.ValidatableWithContext = (*Config)(nil)

// normalize canonicalizes a provider name the way the dispatch switch does.
func normalize(provider string) string {
	return strings.TrimSpace(strings.ToLower(provider))
}

// EnsureDefaults fills in zero fields.
func (cfg *Config) EnsureDefaults() {
	if cfg.Tolerance <= 0 {
		cfg.Tolerance = inbound.DefaultTolerance
	}

	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = inbound.DefaultMaxBodyBytes
	}
}

// ValidateWithContext validates a Config.
//
// It checks the normalized provider, not the raw string, because dispatch
// lowercases and trims — validating the raw value would reject "Stripe" and
// " stripe " that the factory happily accepts.
func (cfg *Config) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Provider, validation.Required, validation.By(func(any) error {
			if !slices.Contains(providers, normalize(cfg.Provider)) {
				return platformerrors.Wrapf(platformerrors.ErrUnknownProvider, "inbound webhook provider %q", cfg.Provider)
			}

			return nil
		})),
		validation.Field(&cfg.Topic, validation.Required),
		validation.Field(&cfg.Secret, validation.Required),
		validation.Field(&cfg.HMAC, validation.By(func(any) error {
			if normalize(cfg.Provider) != ProviderHMAC {
				return nil
			}

			return cfg.HMAC.ValidateWithContext(ctx)
		})),
	)
}

var _ validation.ValidatableWithContext = (*HMACConfig)(nil)

// ValidateWithContext validates an HMACConfig. It is only reached when the
// selected provider is "hmac".
func (cfg *HMACConfig) ValidateWithContext(ctx context.Context) error {
	return validation.ValidateStructWithContext(ctx, cfg,
		validation.Field(&cfg.Provider, validation.Required),
		validation.Field(&cfg.Header, validation.Required),
	)
}

// NewVerifier builds the Verifier for the configured provider.
//
// Explicit options run after the ones derived from configuration, so a caller
// can still override anything — a pinned verification time in a test, an extra
// secret held somewhere the environment does not reach.
//
// Each provider is built into a variable and returned only once its error is
// known to be nil. The provider constructors return their own concrete types,
// so returning one straight through would convert a nil *inbound.StripeVerifier into a
// non-nil inbound.Verifier on the error path, and a caller testing the result against
// nil would find a verifier that panics on first use.
func NewVerifier(ctx context.Context, cfg *Config, opts ...Option) (inbound.Verifier, error) {
	if cfg == nil {
		return nil, platformerrors.ErrNilInputParameter
	}

	cfg.EnsureDefaults()

	if err := cfg.ValidateWithContext(ctx); err != nil {
		return nil, platformerrors.Wrap(err, "validating inbound webhook config")
	}

	o := newOptions(opts)

	base := []inbound.VerifierOption{
		inbound.WithAdditionalSecrets(cfg.AdditionalSecrets...),
		inbound.WithTolerance(cfg.Tolerance),
	}
	base = append(base, o.verifier...)

	switch normalize(cfg.Provider) {
	case ProviderStripe:
		v, verifierErr := inbound.NewStripeVerifier(cfg.Secret, base...)
		if verifierErr != nil {
			return nil, verifierErr
		}

		return v, nil
	case ProviderGitHub:
		v, verifierErr := inbound.NewGitHubVerifier(cfg.Secret, base...)
		if verifierErr != nil {
			return nil, verifierErr
		}

		return v, nil
	case ProviderHMAC:
		v, verifierErr := inbound.NewHMACVerifier(&inbound.HMACScheme{
			Provider: cfg.HMAC.Provider,
			Header:   cfg.HMAC.Header,
			Prefix:   cfg.HMAC.Prefix,
			Digest:   inbound.Digest(normalize(cfg.HMAC.Digest)),
			Encoding: inbound.Encoding(normalize(cfg.HMAC.Encoding)),
		}, cfg.Secret, base...)
		if verifierErr != nil {
			return nil, verifierErr
		}

		return v, nil
	default:
		// Unreachable: validation already rejected anything else. It is here
		// because a provider added to the constants and forgotten here should
		// fail loudly rather than fall through to a nil verifier.
		return nil, platformerrors.Wrapf(platformerrors.ErrUnknownProvider, "inbound webhook provider %q", cfg.Provider)
	}
}

// NewReceiver builds the Receiver for the configured provider, publishing
// verified deliveries to the configured topic.
//
// It takes the PublisherProvider rather than a ready-made Publisher so that the
// topic comes from the same Config as the scheme and the secret: a receiver
// wired to a topic nobody consumes verifies deliveries perfectly and loses
// every one of them, and that is a mistake worth making impossible to spell.
//
// Explicit options run after the ones derived from configuration.
func NewReceiver(ctx context.Context, cfg *Config, publishers messagequeue.PublisherProvider, opts ...Option) (*inbound.Receiver, error) {
	if publishers == nil {
		return nil, platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil publisher provider")
	}

	verifier, err := NewVerifier(ctx, cfg, opts...)
	if err != nil {
		return nil, err
	}

	publisher, err := publishers.NewPublisher(ctx, cfg.Topic)
	if err != nil {
		return nil, platformerrors.Wrapf(err, "building the publisher for topic %q", cfg.Topic)
	}

	o := newOptions(opts)

	base := []inbound.ReceiverOption{
		inbound.WithMaxBodyBytes(cfg.MaxBodyBytes),
		inbound.WithForwardedHeaders(cfg.ForwardedHeaders...),
	}
	if o.logger != nil {
		base = append(base, inbound.WithReceiverLogger(o.logger))
	}
	if o.tracerProvider != nil {
		base = append(base, inbound.WithReceiverTracerProvider(o.tracerProvider))
	}
	if o.metricsProvider != nil {
		base = append(base, inbound.WithReceiverMetricsProvider(o.metricsProvider))
	}

	return inbound.NewReceiver(verifier, publisher, append(base, o.receiver...)...)
}

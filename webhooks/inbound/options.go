package inbound

import (
	"net/http"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/cryptography/requestsigning"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// VerifierOption configures a Verifier this package builds. One type serves
// every scheme, because they share a notion of what secrets are held and what
// time it is; each option's doc says which constructors read it.
type VerifierOption func(*verifierConfig)

// verifierConfig collects what the verifier options set.
//
// The clock/pin/tolerance triple is requestsigning.Freshness, embedded rather
// than restated: a provider's scheme and this module's own bind a timestamp
// into the signed material for the same reason, and there is no version of
// "how far from now is too far" that should differ between them. What is this
// package's own is the secret list, because these schemes take a shared secret
// where requestsigning takes a keyring.
type verifierConfig struct {
	requestsigning.Freshness

	additions []string
}

// newVerifierConfig applies opts, ignoring nil entries.
func newVerifierConfig(opts []VerifierOption) *verifierConfig {
	cfg := &verifierConfig{Freshness: requestsigning.Freshness{Tolerance: DefaultTolerance}}

	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	return cfg
}

// secretsWith returns the primary secret followed by any additional ones,
// dropping empties. A constructor that gets nothing back has no secret at all
// and must refuse to build.
func (c *verifierConfig) secretsWith(primary string) []string {
	secrets := make([]string, 0, 1+len(c.additions))

	for _, s := range append([]string{primary}, c.additions...) {
		if s != "" {
			secrets = append(secrets, s)
		}
	}

	return secrets
}

// WithAdditionalSecrets adds secrets a delivery may also be signed under, so
// that rotating a webhook secret is not an outage. Read by every verifier
// constructor. Empty entries are ignored.
//
// A rotation has a window in which the provider may be signing with either
// value — the receiver cannot make both sides switch at the same instant, and
// a provider's console generally shows the new secret before it starts using
// it. Holding both through that window is what makes the rotation survivable;
// the old one is dropped from configuration once deliveries have moved.
//
// This is the receiver's own rotation. Stripe's endpoint-secret rollover shows
// up differently, as several v1 elements in one header, and is handled without
// any configuration at all.
func WithAdditionalSecrets(secrets ...string) VerifierOption {
	return func(cfg *verifierConfig) { cfg.additions = append(cfg.additions, secrets...) }
}

// WithTolerance overrides DefaultTolerance — how far a signed timestamp may sit
// from the verifier's clock. A non-positive duration leaves the default in
// place. Read by NewStripeVerifier; schemes with no signed timestamp ignore it.
//
// There is deliberately no way to disable the check. A signature with no
// freshness bound is replayable forever, which is the property a signed
// timestamp exists to remove.
func WithTolerance(d time.Duration) VerifierOption {
	return func(cfg *verifierConfig) {
		if d > 0 {
			cfg.Tolerance = d
		}
	}
}

// WithClock swaps the source of time a verifier compares a signed timestamp
// against. Read by NewStripeVerifier. A nil clock is ignored.
//
// Inside a testing/synctest bubble clock.NewClock already reads the bubble's
// fake time, so this is for what a bubble cannot express — a deliberately
// skewed peer, a clock driven by a harness.
func WithClock(c clock.Clock) VerifierOption {
	return func(cfg *verifierConfig) {
		if c != nil {
			cfg.Clock = c
		}
	}
}

// WithVerificationTime pins the instant a verification compares a signed
// timestamp against, instead of reading a clock. It wins over WithClock, and
// exists for tests and for replaying a captured delivery against a known
// instant. Read by NewStripeVerifier.
//
// A zero time is ignored, so this cannot accidentally pin verification to the
// Unix epoch and reject everything.
func WithVerificationTime(t time.Time) VerifierOption {
	return func(cfg *verifierConfig) {
		if !t.IsZero() {
			cfg.At = t
		}
	}
}

// ReceiverOption configures a Receiver.
//
// The observability dependencies are options rather than parameters because
// each is genuinely optional: an absent logger logs nowhere, an absent tracer
// provider traces nowhere, and an absent metrics provider records nothing.
type ReceiverOption func(*Receiver)

// WithReceiverLogger attaches a logger.
func WithReceiverLogger(logger logging.Logger) ReceiverOption {
	return func(r *Receiver) { r.logger = logger }
}

// WithReceiverTracerProvider attaches a tracer provider, enabling a span per
// received delivery.
func WithReceiverTracerProvider(tracerProvider tracing.Provider) ReceiverOption {
	return func(r *Receiver) { r.tracerProvider = tracerProvider }
}

// WithReceiverMetricsProvider attaches a metrics provider. An absent provider
// records nothing.
func WithReceiverMetricsProvider(metricsProvider metrics.Provider) ReceiverOption {
	return func(r *Receiver) { r.metricsProvider = metricsProvider }
}

// WithReceiverClock swaps the source of time stamped onto Delivery.ReceivedAt.
// A nil clock is ignored.
func WithReceiverClock(c clock.Clock) ReceiverOption {
	return func(r *Receiver) {
		if c != nil {
			r.clock = c
		}
	}
}

// WithMaxBodyBytes overrides DefaultMaxBodyBytes — how much of a request body
// the receiver will read before answering 413. A non-positive value leaves the
// default in place, because an unbounded read on a public endpoint is not a
// configuration this package offers.
//
// Raise it for a provider that sends genuinely large payloads. Lowering it
// below what the provider sends turns every delivery into a 413, which the
// provider retries and eventually gives up on.
func WithMaxBodyBytes(n int64) ReceiverOption {
	return func(r *Receiver) {
		if n > 0 {
			r.maxBodyBytes = n
		}
	}
}

// WithForwardedHeaders narrows Delivery.Headers to the named headers, dropping
// everything else. Names are matched case-insensitively, as HTTP header lookup
// always is; an empty call is ignored.
//
// The default forwards what arrived, minus credential headers, because a
// consumer generally needs a provider-specific header it did not think to name
// — GitHub's delivery ID, Shopify's shop domain, the provider's API version.
// Narrow it when a topic's messages are retained somewhere their size or their
// contents matter, and remember that none of these values are authenticated by
// any of these schemes.
func WithForwardedHeaders(names ...string) ReceiverOption {
	return func(r *Receiver) {
		for _, name := range names {
			if name == "" {
				continue
			}

			if r.forwarded == nil {
				r.forwarded = map[string]struct{}{}
			}

			r.forwarded[http.CanonicalHeaderKey(name)] = struct{}{}
		}
	}
}

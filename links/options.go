package links

import (
	"maps"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/cryptography/hashing"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
	"github.com/primandproper/platform-go/v13/random"
)

type (
	// Option configures a Minter at construction.
	Option func(*minterOptions)

	// minterOptions accumulates what the options set.
	minterOptions struct {
		actions         map[Action]ActionPolicy
		clock           clock.Clock
		hasher          hashing.Hasher
		generator       random.Generator
		logger          logging.Logger
		tracerProvider  tracing.Provider
		metricsProvider metrics.Provider

		keyPrefix      *string
		retention      time.Duration
		tokenBytes     int
		maxTokenLength int
		allowInsecure  bool
	}
)

// WithAction registers an action and the policy for it, and is the only way an
// action becomes mintable.
//
// Registering the same action twice replaces the earlier policy, so a caller
// assembling a Minter from configuration and then overriding one action can do
// so by appending an option rather than by editing the map it built.
func WithAction(action Action, policy ActionPolicy) Option {
	return func(o *minterOptions) {
		if action == "" {
			return
		}

		if o.actions == nil {
			o.actions = make(map[Action]ActionPolicy, 1)
		}

		o.actions[action] = policy
	}
}

// WithActions registers several actions at once, on the same terms as
// WithAction. It exists for the configuration path, which holds a map already.
func WithActions(actions map[Action]ActionPolicy) Option {
	return func(o *minterOptions) {
		for action, policy := range actions {
			if action == "" {
				continue
			}

			if o.actions == nil {
				o.actions = make(map[Action]ActionPolicy, len(actions))
			}

			o.actions[action] = policy
		}
	}
}

// WithHasher overrides the digest a token is stored and looked up by.
//
// It must be a cryptographic hash. hashing.Hasher also has adler32, crc64, and
// fnv implementations behind it, and any of them turns the store from something
// that reveals nothing into something an attacker inverts on a laptop.
//
// It must not be a password KDF either, and for two independent reasons.
// Lookup is by digest, so the function has to be deterministic and unsalted —
// argon2 is neither. And the property a KDF buys, survivability of a
// low-entropy secret, is not a property a 256-bit random token needs: there is
// no dictionary to run against it. A fast cryptographic hash is both the
// correct answer and the only workable one.
//
// The default is SHA-256.
func WithHasher(hasher hashing.Hasher) Option {
	return func(o *minterOptions) {
		if hasher != nil {
			o.hasher = hasher
		}
	}
}

// WithGenerator overrides the source of token randomness. The default is
// random.NewGenerator, which reads crypto/rand.
//
// Tokens are base64url-encoded by the generator, and the URL templates rely on
// that alphabet being safe unescaped. A generator producing anything else would
// need its output escaped at every position a template could put it.
func WithGenerator(generator random.Generator) Option {
	return func(o *minterOptions) {
		if generator != nil {
			o.generator = generator
		}
	}
}

// WithTokenBytes sets how many random bytes a token carries before encoding.
//
// Values below DefaultTokenBytes are accepted but hard to justify: the token is
// the entire credential, there is no second factor behind it, and the redemption
// endpoint is reachable by anyone. Shortening it to fit a layout is trading the
// security of the flow for the length of a line.
func WithTokenBytes(tokenBytes int) Option {
	return func(o *minterOptions) {
		if tokenBytes > 0 {
			o.tokenBytes = tokenBytes
		}
	}
}

// WithMaxTokenLength overrides the longest token Redeem will hash.
func WithMaxTokenLength(maxLength int) Option {
	return func(o *minterOptions) {
		if maxLength > 0 {
			o.maxTokenLength = maxLength
		}
	}
}

// WithRetention sets how long a resolved link stays in the store after it stops
// working, which is how long redemption can still say why it failed.
//
// A resolved record holds the subject and the metadata, so this is also how
// long that outlives the link. A deployment with a short data-retention posture
// should shorten it, at the cost of a used link answering "not found".
func WithRetention(retention time.Duration) Option {
	return func(o *minterOptions) {
		if retention > 0 {
			o.retention = retention
		}
	}
}

// WithKeyPrefix overrides the namespace applied to store and lock keys.
//
// An empty prefix is honored rather than ignored, so a caller can deliberately
// opt out of namespacing; that is why this is the one setting held as a pointer.
func WithKeyPrefix(prefix string) Option {
	return func(o *minterOptions) {
		o.keyPrefix = &prefix
	}
}

// WithInsecureURLs permits http action URLs against hosts that are not
// loopback.
//
// It is spelled this way so it is legible in a diff. The token is a bearer
// credential carried entirely in the URL, so cleartext delivery hands it to
// every proxy, resolver, and access log between the mail client and the app.
// Loopback http already works without this — see ActionPolicy.URL — so this is
// for environments that are neither production nor local, and should not
// outlive them.
func WithInsecureURLs() Option {
	return func(o *minterOptions) { o.allowInsecure = true }
}

// WithClock swaps the clock used to stamp and expire links.
func WithClock(c clock.Clock) Option {
	return func(o *minterOptions) {
		if c != nil {
			o.clock = c
		}
	}
}

// WithLogger attaches a logger. An absent logger logs nowhere.
func WithLogger(logger logging.Logger) Option {
	return func(o *minterOptions) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider. An absent one traces nowhere.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *minterOptions) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider. An absent one records
// nothing.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *minterOptions) { o.metricsProvider = metricsProvider }
}

type (
	// MintOption overrides an action's policy for one link.
	//
	// The policy is the default for every link of that action; these exist for
	// the link that genuinely differs — an invitation that must expire when the
	// billing period does — rather than as a second place to configure the
	// action.
	MintOption func(*mintOptions)

	// mintOptions holds the per-call overrides. A nil field means "inherit from
	// the action's policy", which keeps an option's absence distinguishable from
	// an option set to a zero value.
	mintOptions struct {
		metadata map[string]string
		ttl      *time.Duration
	}
)

// newMintOptions applies opts, ignoring nil entries.
func newMintOptions(opts []MintOption) *mintOptions {
	o := &mintOptions{}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithTTL overrides how long this one link stays redeemable. A non-positive
// value inherits the action's policy.
func WithTTL(ttl time.Duration) MintOption {
	return func(o *mintOptions) {
		if ttl > 0 {
			o.ttl = &ttl
		}
	}
}

// WithMetadata attaches values to the link, returned verbatim by redemption.
//
// This is the safe place for what a JWT would have put in the URL: the page to
// land on, the invited role, the campaign a message belongs to. It is stored
// server-side and never travels, so the bearer can neither read it nor change
// it, and a handler may act on it without verifying anything.
//
// The map is copied, so a caller may reuse or mutate its own afterwards. It is
// not a place for secrets: it lands in the store in the clear, which is the one
// thing the token deliberately does not.
func WithMetadata(metadata map[string]string) MintOption {
	return func(o *mintOptions) {
		if len(metadata) == 0 {
			return
		}

		if o.metadata == nil {
			o.metadata = make(map[string]string, len(metadata))
		}

		maps.Copy(o.metadata, metadata)
	}
}

package passwordreset

import (
	"context"
	"time"

	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/cryptography/hashing"
	"github.com/primandproper/primitives-go/v2/cryptography/hashing/sha256"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	"github.com/primandproper/primitives-go/v2/random"
)

// DefaultSecretBytes is how much randomness a token carries when no other
// amount is configured.
//
// Thirty-two bytes is what makes the digest column safe to store unsalted:
// there is no dictionary against 256 bits from a CSPRNG, so nothing is bought
// by making the digest expensive to compute. It renders as 43 characters of
// URL-safe base64, which fits in a link without wrapping in a mail client.
const DefaultSecretBytes = 32

// MinimumSecretBytes is the least randomness WithSecretBytes will accept.
//
// Sixteen bytes is the floor below which a token stops being a bearer
// credential and becomes something worth guessing: an attacker who can present
// candidates at any rate at all is bounded by 2^128 above it and by whatever
// rate limiting the application remembered to configure below it. A smaller
// value is ignored rather than honored, because the alternative is a
// deployment quietly issuing weak links.
const MinimumSecretBytes = 16

type (
	// Option configures a SQLStore at construction.
	Option func(*options)

	options struct {
		clock           clock.Clock
		generator       random.Generator
		hasher          hashing.Hasher
		logger          logging.Logger
		tracerProvider  tracing.Provider
		metricsProvider metrics.Provider

		//nolint:containedctx // deliberate: see WithSweeper
		sweepCtx      context.Context
		sweepInterval time.Duration

		secretBytes int
	}
)

// newOptions applies opts over the defaults, ignoring nil entries.
func newOptions(opts []Option) *options {
	o := &options{
		clock:       clock.NewClock(),
		generator:   random.NewGenerator(),
		hasher:      sha256.NewSHA256Hasher(),
		secretBytes: DefaultSecretBytes,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithClock swaps the clock the expiry deadline is stamped from, the one Verify
// and Consume compare against, and the one the sweeper ticks on.
func WithClock(c clock.Clock) Option {
	return func(o *options) {
		if c != nil {
			o.clock = c
		}
	}
}

// WithGenerator swaps the source the token's randomness is drawn from.
//
// It exists for the test that needs a token it can predict, and for a
// deployment drawing from a hardware source. It is not a place to make tokens
// shorter or friendlier to type: whatever this returns is what an attacker has
// to guess.
func WithGenerator(generator random.Generator) Option {
	return func(o *options) {
		if generator != nil {
			o.generator = generator
		}
	}
}

// WithHasher swaps what the token_digest column holds.
//
// The default is SHA-256, and a replacement must be a cryptographic hash —
// hashing.Hasher also has adler32, crc64 and fnv implementations, and a
// checksum here is a column an attacker can find preimages for at will. It is a
// digest rather than a password hash on purpose: argon2 over 256 random bits
// buys nothing over SHA-256 and turns every verification into a deliberate
// fraction of a second.
//
// Changing it on a deployed store invalidates every outstanding link, since the
// digests already stored were computed by the old one. That costs the resets in
// flight at that moment and nothing more.
func WithHasher(hasher hashing.Hasher) Option {
	return func(o *options) {
		if hasher != nil {
			o.hasher = hasher
		}
	}
}

// WithSecretBytes sets how much randomness a token carries.
//
// Values below MinimumSecretBytes are ignored, which is the one place in this
// package where a caller's argument does not win: a store that honored an
// eight-byte token would be a store whose whole reason for existing had been
// configured away.
func WithSecretBytes(n int) Option {
	return func(o *options) {
		if n >= MinimumSecretBytes {
			o.secretBytes = n
		}
	}
}

// WithSweeper starts a background delete of rows whose deadlines have passed,
// every interval, until ctx is done.
//
// Unlike a cache, a table does not reclaim its own expired rows, and without a
// sweep this one grows by a row for every password anybody ever forgot. It is
// not what makes a token expire — Verify and Consume refuse a row past its
// deadline regardless — so a deployment that runs the sweep from a scheduler
// instead, one for the fleet rather than one per replica, loses nothing by
// leaving this off.
//
// The context bounds the goroutine's life. Passing a nil context or a
// non-positive interval starts nothing.
func WithSweeper(ctx context.Context, interval time.Duration) Option {
	return func(o *options) {
		if ctx == nil || interval <= 0 {
			return
		}

		o.sweepCtx = ctx
		o.sweepInterval = interval
	}
}

// WithLogger attaches a logger. An absent logger logs nowhere.
func WithLogger(logger logging.Logger) Option {
	return func(o *options) { o.logger = logger }
}

// WithTracerProvider attaches a tracer provider. An absent one traces nowhere.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider for the store's counters. An
// absent one records nothing.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) { o.metricsProvider = metricsProvider }
}

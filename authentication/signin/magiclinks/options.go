package magiclinks

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

// DefaultSecretBytes is how much randomness a sign-in link carries when no
// other amount is configured.
//
// Thirty-two bytes is what makes the hash column safe to store unsalted: there
// is no dictionary against 256 bits from a CSPRNG, so nothing is bought by
// making the digest expensive to compute. It renders as 43 characters of
// URL-safe base64, which fits in a header and in a cookie without encoding
// twice.
const DefaultSecretBytes = 32

// MinimumSecretBytes is the least randomness WithSecretBytes will accept.
//
// Sixteen bytes is the floor below which a token stops being a bearer credential
// and becomes something worth guessing: an attacker who can present candidates
// at any rate at all is bounded by 2^128 above it and by whatever rate limiting
// the application remembered to configure below it. A smaller value is ignored
// rather than honored, because the alternative is a deployment quietly issuing
// weak credentials.
const MinimumSecretBytes = 16

// DefaultRetention is how long past its own deadline a row is kept before the
// sweeper may collect it.
//
// Twenty-four hours, and the length is not what matters — being greater than
// zero is. A row collected at its own expiry can no longer be told from a row
// that never existed, and the difference is what an operator reconstructs an
// incident from: "this link was already used" and "no such link" are two
// different stories about the same request. Neither is told to the caller — see
// signin.ErrInvalidMagicLink — so the window buys a span, a log line and a
// support answer rather than a different refusal.
//
// It is a day rather than the thirty the refresh token store keeps, because the
// credential it describes lives fifteen minutes rather than thirty days. What is
// worth keeping is a window around the link's own life, and a month of rows for
// a quarter-hour credential is a month of rows.
const DefaultRetention = 24 * time.Hour

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

		retention time.Duration

		secretBytes int
	}
)

// newOptions applies opts over the defaults, ignoring nil entries.
func newOptions(opts []Option) *options {
	o := &options{
		clock:       clock.NewClock(),
		generator:   random.NewGenerator(),
		hasher:      sha256.NewSHA256Hasher(),
		retention:   DefaultRetention,
		secretBytes: DefaultSecretBytes,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithClock swaps the clock a token's deadlines are stamped from, the one the
// exchange's liveness guard is bound from, and the one the sweeper ticks on.
func WithClock(c clock.Clock) Option {
	return func(o *options) {
		if c != nil {
			o.clock = c
		}
	}
}

// WithGenerator swaps the source a token's randomness is drawn from.
//
// It exists for the test that needs a token it can predict, and for a deployment
// drawing from a hardware source. It is not a place to make tokens shorter or
// friendlier to type: whatever this returns is what an attacker has to guess.
func WithGenerator(generator random.Generator) Option {
	return func(o *options) {
		if generator != nil {
			o.generator = generator
		}
	}
}

// WithHasher swaps what renders the hash column from a token.
//
// Rows written with one hasher are unfindable through another, and a stored row
// carries no record of which wrote it. Changing this on a deployed store
// therefore signs out everybody holding a sign-in link. Choose it once.
func WithHasher(hasher hashing.Hasher) Option {
	return func(o *options) {
		if hasher != nil {
			o.hasher = hasher
		}
	}
}

// WithSecretBytes sets how much randomness a token carries. A value below
// MinimumSecretBytes is ignored, leaving whatever was configured before it.
func WithSecretBytes(count int) Option {
	return func(o *options) {
		if count >= MinimumSecretBytes {
			o.secretBytes = count
		}
	}
}

// WithRetention sets how long past its own deadline a row is kept before the
// sweeper may collect it. A non-positive duration is ignored, leaving
// DefaultRetention.
//
// It is this store's rather than the service's, unlike the token lifetime beside
// it, and the split is the same one every retention window in this module makes:
// how long a credential works is policy, and how long the evidence that it
// existed is kept is storage. What this one buys is stated on DefaultRetention —
// shortening it to zero would not shorten any sign-in, it would take away the
// only thing that can still tell "that link was already used" from "no such
// link".
func WithRetention(retention time.Duration) Option {
	return func(o *options) {
		if retention > 0 {
			o.retention = retention
		}
	}
}

// WithSweeper starts a background sweep that removes rows past their purge
// deadline, every interval, until ctx is done.
//
// Unlike a cache, a table does not reclaim its own expired rows, and without a
// sweep this one grows by a row for every link ever mailed — which is a row per
// request rather than a row per sign-in, since asking again mints another one
// and leaves the first standing. Running it is not optional in any long-lived
// deployment; what is optional is running it here rather than from a scheduler
// that calls Sweep, which is the better answer for a fleet — one sweeper, not
// one per replica.
//
// It is not what makes a link stop working. That is the redemption's own guard,
// so a row this has not reached yet is already refused.
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
//
// It takes a provider rather than a ready-made tracer so that this package's
// spans carry this package's instrumentation scope.
func WithTracerProvider(tracerProvider tracing.Provider) Option {
	return func(o *options) { o.tracerProvider = tracerProvider }
}

// WithMetricsProvider attaches a metrics provider for the sweeper's counters. An
// absent one records nothing.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) { o.metricsProvider = metricsProvider }
}

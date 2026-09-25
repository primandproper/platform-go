package passwordreset

import (
	"context"
	"time"

	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/cryptography/hashing"
	"github.com/primandproper/primitives-go/v2/cryptography/hashing/sha256"
	"github.com/primandproper/primitives-go/v2/observability"
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

// DefaultTokenLifetime is how long a link Service.Request mints stays
// spendable when no other lifetime is configured.
//
// An hour is long enough to survive a mail queue, a commute and somebody
// reading the message on the other device, and short enough that a link sitting
// in an inbox is not a standing credential. It is a Service default rather than
// a Config one for the reason Config states: the store takes a TTL per issuance
// because an administrator resetting somebody else's password wants minutes
// where a self-service link wants an hour, and this flow is the self-service
// one.
const DefaultTokenLifetime = time.Hour

// DefaultRequestFloor is how long Service.Request takes at a minimum.
//
// The floor is what makes the answer for an address nobody holds
// indistinguishable from the answer for one somebody does. Without it the two
// differ by a transaction and a mail send, which is a stopwatch away from being
// the account-enumeration oracle the identical responses were meant to close.
//
// Half a second is chosen to sit above the work — a single-row insert, a commit,
// and one call to a mailer — on a deployment whose mail provider answers
// promptly. A deployment whose mailer is slower than the floor leaks the
// difference again and should raise it; a floor below the work buys nothing at
// all, which is why WithRequestFloor's documentation says to measure rather than
// guess.
//
// The measurement is already on the dashboard. Service.Request records its
// latency after the pad rather than before it, so the operation's histogram
// sitting flat on the floor is the floor covering the work, and a histogram that
// has climbed above it is the timing difference coming back.
const DefaultRequestFloor = 500 * time.Millisecond

// ServiceOption configures a Service.
//
// It is a second option type beside Option because this package holds two
// components with different dependencies — the store, and the flow over it —
// and the observability options are named apart for the same reason identity's
// are: a consumer wiring both hands each its own logger, and one WithLogger
// serving two constructors is one that silently attaches to whichever was
// written first.
type ServiceOption func(*Service)

// WithTokenLifetime sets how long a link Service.Request mints stays spendable.
//
// It replaces DefaultTokenLifetime. A non-positive value is refused by
// NewService rather than ignored: zero is an unset configuration field, and
// honoring it would issue dead links for every reset anybody asked for.
func WithTokenLifetime(d time.Duration) ServiceOption {
	return func(s *Service) { s.lifetime = d }
}

// WithRequestFloor sets how long Service.Request takes at a minimum, replacing
// DefaultRequestFloor.
//
// Set it from a measurement rather than a preference: the floor is worth
// exactly as much as the margin between it and the slowest the known-address
// path actually runs, which on most deployments is decided by the mailer. A
// value at or below zero turns the padding off, which is a deployment deciding
// that the timing difference is somebody else's problem — a proxy that
// normalizes response times, or a queue the mail is handed to — rather than a
// value this package will choose for one.
func WithRequestFloor(d time.Duration) ServiceOption {
	return func(s *Service) { s.requestFloor = d }
}

// WithPasswordPolicy sets the rule a password must pass before Service.Complete
// writes it. A nil policy is ignored, leaving none, which admits any password
// that is not empty. See [PasswordPolicy].
func WithPasswordPolicy(policy PasswordPolicy) ServiceOption {
	return func(s *Service) {
		if policy != nil {
			s.passwordPolicy = policy
		}
	}
}

// WithServiceClock replaces the clock the request floor is measured against.
//
// It is the flow's clock and not the store's: a test that drives both hands one
// to WithClock and one to this, and a deployment that replaces neither gets the
// system clock in both. A nil clock is ignored.
func WithServiceClock(c clock.Clock) ServiceOption {
	return func(s *Service) {
		if c != nil {
			s.clk = c
		}
	}
}

// WithServiceLogger attaches a logger to the flow. An absent logger logs
// nowhere.
func WithServiceLogger(logger logging.Logger) ServiceOption {
	return func(s *Service) { s.logger = logger }
}

// WithServiceTracerProvider attaches a tracer provider to the flow, enabling a
// span per operation. An absent provider traces nowhere.
//
// It takes a provider rather than a ready-made tracer for the reason
// WithTracerProvider does: the spans carry this package's instrumentation scope
// rather than whoever built the tracer.
func WithServiceTracerProvider(tracerProvider tracing.Provider) ServiceOption {
	return func(s *Service) { s.tracerProvider = tracerProvider }
}

// WithServiceMetricsProvider attaches a metrics provider to the flow, enabling
// the request, error and latency instruments each operation records. An absent
// provider records nothing.
func WithServiceMetricsProvider(metricsProvider metrics.Provider) ServiceOption {
	return func(s *Service) { s.metricsProvider = metricsProvider }
}

// WithServicePillars attaches a logger, tracer provider and metrics provider to
// the flow in one go. A nil Pillars attaches nothing.
//
// Options apply in order, so a caller can hand over its pillars and then
// override one of them.
func WithServicePillars(p *observability.Pillars) ServiceOption {
	return func(s *Service) { s.logger, s.tracerProvider, s.metricsProvider = p.Deps() }
}

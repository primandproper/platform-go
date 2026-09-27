package phonecodes

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

// DefaultCodeLength is how many digits a code has when nothing says otherwise.
const DefaultCodeLength = 6

// MinCodeLength and MaxCodeLength bound WithCodeLength.
//
// Four digits is ten thousand values, and with the default five attempts a
// guesser's chance is one in two thousand per code — the floor below which the
// attempt limit stops being much of a limit. Ten is past what anybody types off
// a lock screen without a mistake.
const (
	MinCodeLength = 4
	MaxCodeLength = 10
)

// DefaultLifetime is how long a code stays redeemable when nothing says
// otherwise. Ten minutes is long enough for a text to arrive late and short
// enough that a digest in a dump is dead before anybody reads it.
const DefaultLifetime = 10 * time.Minute

// DefaultMaxAttempts is how many wrong codes a code survives when neither the
// store nor the request says otherwise.
const DefaultMaxAttempts = 5

// DefaultRetention is how long past its own deadline a row is kept before the
// sweeper may collect it.
//
// What it buys is an operator's reading: a row still there says "that code was
// already used" or "that code ran out of attempts", where a row already swept
// says only "no such code". Neither is told to the caller. It is a day because
// the credential it describes lives minutes, and a month of rows for a
// ten-minute code is a month of phone numbers kept for nothing.
const DefaultRetention = 24 * time.Hour

// Option configures a SQLStore at construction.
type Option func(*options)

type options struct {
	clock           clock.Clock
	generator       random.Generator
	hasher          hashing.Hasher
	logger          logging.Logger
	tracerProvider  tracing.Provider
	metricsProvider metrics.Provider

	//nolint:containedctx // deliberate: see WithSweeper
	sweepCtx context.Context

	prefix string

	sweepInterval time.Duration

	lifetime  time.Duration
	retention time.Duration

	codeLength  int
	maxAttempts int
}

// newOptions applies opts over the defaults, ignoring nil entries.
func newOptions(opts []Option) *options {
	o := &options{
		clock:       clock.NewClock(),
		generator:   random.NewGenerator(),
		hasher:      sha256.NewSHA256Hasher(),
		prefix:      DefaultTablePrefix,
		lifetime:    DefaultLifetime,
		retention:   DefaultRetention,
		codeLength:  DefaultCodeLength,
		maxAttempts: DefaultMaxAttempts,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithTablePrefix namespaces the code table. It must match the prefix the
// migrations were rendered with; nothing here can check that, and a mismatch
// surfaces as a missing table on the first query rather than at construction.
func WithTablePrefix(prefix string) Option {
	return func(o *options) { o.prefix = prefix }
}

// WithCodeLength sets how many digits a code has. NewSQLStore refuses a length
// outside [MinCodeLength, MaxCodeLength] with ErrInvalidSetting.
func WithCodeLength(digits int) Option {
	return func(o *options) { o.codeLength = digits }
}

// WithLifetime sets how long a code stays redeemable. NewSQLStore refuses a
// non-positive one with ErrInvalidSetting.
func WithLifetime(lifetime time.Duration) Option {
	return func(o *options) { o.lifetime = lifetime }
}

// WithMaxAttempts sets how many wrong codes a code survives when its request
// names no limit of its own. NewSQLStore refuses one below one with
// ErrInvalidSetting.
func WithMaxAttempts(attempts int) Option {
	return func(o *options) { o.maxAttempts = attempts }
}

// WithRetention sets how long past its own deadline a row is kept before the
// sweeper may collect it. NewSQLStore refuses a non-positive one with
// ErrInvalidSetting.
//
// Lengthening it lengthens how long a phone number stays in this table after
// the code it received is dead. See DefaultRetention.
func WithRetention(retention time.Duration) Option {
	return func(o *options) { o.retention = retention }
}

// WithClock swaps the clock deadlines are stamped from, the one the
// redemption's liveness guard is bound from, and the one the sweeper ticks on.
func WithClock(c clock.Clock) Option {
	return func(o *options) {
		if c != nil {
			o.clock = c
		}
	}
}

// WithGenerator swaps the source a code's digits are drawn from. It exists for
// the test that needs a code it can predict; whatever this returns is what a
// guesser has to guess.
func WithGenerator(generator random.Generator) Option {
	return func(o *options) {
		if generator != nil {
			o.generator = generator
		}
	}
}

// WithHasher swaps what renders the digest column. Rows written with one hasher
// are unredeemable through another, so changing it on a deployed store kills
// every outstanding code. Choose it once.
func WithHasher(hasher hashing.Hasher) Option {
	return func(o *options) {
		if hasher != nil {
			o.hasher = hasher
		}
	}
}

// WithSweeper starts a background sweep that removes rows past their purge
// deadline, every interval, until ctx is done. A nil context or a non-positive
// interval starts nothing.
//
// It is not what makes a code stop working — the redemption's own guard is —
// but without it, or a scheduler calling Sweep, the table keeps every phone
// number anybody was ever texted a code at.
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

// WithMetricsProvider attaches a metrics provider. An absent one records
// nothing.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) { o.metricsProvider = metricsProvider }
}

// WithPillars attaches a logger, tracer provider, and metrics provider in one
// go. A nil Pillars attaches nothing. Options apply in order, so a caller can
// hand over its pillars and then override one of them.
func WithPillars(p *observability.Pillars) Option {
	return func(o *options) { o.logger, o.tracerProvider, o.metricsProvider = p.Deps() }
}

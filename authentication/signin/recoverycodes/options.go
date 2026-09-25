package recoverycodes

import (
	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/cryptography/hashing"
	"github.com/primandproper/primitives-go/v2/cryptography/hashing/sha256"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	"github.com/primandproper/primitives-go/v2/random"
)

type (
	// Option configures a SQLStore at construction.
	Option func(*options)

	options struct {
		clock          clock.Clock
		generator      random.Generator
		hasher         hashing.Hasher
		logger         logging.Logger
		tracerProvider tracing.Provider
	}
)

// newOptions applies opts over the defaults, ignoring nil entries.
func newOptions(opts []Option) *options {
	o := &options{
		clock:     clock.NewClock(),
		generator: random.NewGenerator(),
		hasher:    sha256.NewSHA256Hasher(),
	}

	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithClock swaps the clock a set's issue stamp and a code's spend stamp are
// read from. Nothing compares either against a clock, so this moves what a
// record says rather than what a code does.
func WithClock(c clock.Clock) Option {
	return func(o *options) {
		if c != nil {
			o.clock = c
		}
	}
}

// WithGenerator swaps the source a code's randomness is drawn from.
//
// It exists for the test that needs a code it can predict, and for a deployment
// drawing from a hardware source. It is not a place to make codes shorter or
// friendlier to type: whatever this returns is what an attacker has to guess.
func WithGenerator(generator random.Generator) Option {
	return func(o *options) {
		if generator != nil {
			o.generator = generator
		}
	}
}

// WithHasher swaps what renders the hash column from a code.
//
// Rows written with one hasher are unfindable through another, and a stored row
// carries no record of which wrote it. Changing this on a deployed store
// therefore invalidates every code anybody holds — and unlike a sign-in link,
// which is one more mail away, a recovery code is the thing somebody reaches for
// when every other way in is already gone. Choose it once.
func WithHasher(hasher hashing.Hasher) Option {
	return func(o *options) {
		if hasher != nil {
			o.hasher = hasher
		}
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

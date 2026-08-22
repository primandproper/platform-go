package database

import (
	"context"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/encoding"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/logging"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/observability/tracing"
)

// ErrNilClient indicates NewBackend was called without a database client. It
// wraps errors.ErrNilInputParameter, so a caller may check either.
var ErrNilClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil session database client")

// DefaultPayloadContentType is what session payloads are encoded as when no
// codec is supplied: CBOR, which is compact, binary, and readable outside Go.
const DefaultPayloadContentType = encoding.ContentTypeCBOR

type (
	// Option configures a Backend at construction.
	//
	// It carries no type parameter even though NewBackend does: Go cannot infer
	// a type argument from a call's result type, so an Option[T] would force
	// every call site to spell the payload type out by hand forever.
	Option func(*options)

	options struct {
		codec           encoding.Codec
		clock           clock.Clock
		logger          logging.Logger
		tracerProvider  tracing.Provider
		metricsProvider metrics.Provider

		//nolint:containedctx // deliberate: see WithSweeper
		sweepCtx      context.Context
		sweepInterval time.Duration
	}
)

// newOptions applies opts over the defaults, ignoring nil entries.
func newOptions(opts []Option) *options {
	o := &options{
		codec: encoding.NewClientEncoder(DefaultPayloadContentType),
		clock: clock.NewClock(),
	}

	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithCodec sets how session payloads are encoded into the data column.
//
// Rows written with one encoding are unreadable through another, and a stored
// row carries no record of which wrote it. Changing this on a deployed store
// therefore signs everybody out — the payloads decode to nothing, which
// Record.Version cannot help with because the version is a column rather than
// part of the blob. Choose it once.
func WithCodec(codec encoding.Codec) Option {
	return func(o *options) {
		if codec != nil {
			o.codec = codec
		}
	}
}

// WithSweeper starts a background sweep that removes rows whose deadlines have
// passed, every interval, until ctx is done.
//
// Unlike a cache, a table does not reclaim its own expired rows, and without a
// sweep this one grows with every session ever created. Running it is not
// optional in any long-lived deployment; what is optional is running it here
// rather than from a scheduler that calls Sweep, which is the better answer for
// a fleet — one sweeper, not one per replica.
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

// WithClock swaps the clock the expires_at column is stamped from, and the one
// the sweeper ticks on.
func WithClock(c clock.Clock) Option {
	return func(o *options) {
		if c != nil {
			o.clock = c
		}
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

// WithMetricsProvider attaches a metrics provider for the sweeper's counters.
// An absent one records nothing.
func WithMetricsProvider(metricsProvider metrics.Provider) Option {
	return func(o *options) { o.metricsProvider = metricsProvider }
}

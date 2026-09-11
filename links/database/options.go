package database

import (
	"context"
	"time"

	"github.com/primandproper/primitives-go/v2/clock"
	"github.com/primandproper/primitives-go/v2/encoding"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
)

var (
	// ErrNilConfig indicates New was called without a config. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilConfig = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil action link database config")

	// ErrNilClient indicates New was called without a database client. It wraps
	// errors.ErrNilInputParameter, so a caller may check either.
	ErrNilClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil action link database client")
)

// DefaultMetadataContentType is what link metadata is encoded as when no codec
// is supplied.
//
// JSON rather than the CBOR sessions defaults to, because what this column
// holds is a flat map of strings rather than a Go type. Nothing decodes it but
// this package, so the format is free — and an operator answering "what was
// this link for" reads it with the database's own JSON functions instead of
// piping a blob through a decoder.
const DefaultMetadataContentType = encoding.ContentTypeJSON

type (
	// Option configures a Store at construction.
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
		codec: encoding.NewClientEncoder(DefaultMetadataContentType),
		clock: clock.NewClock(),
	}

	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithCodec sets how link metadata is encoded into the metadata column.
//
// Rows written with one encoding are unreadable through another, and a stored
// row carries no record of which wrote it. Changing this on a deployed store
// therefore invalidates every outstanding link that carries metadata —
// links.RecordVersion cannot help, because the version is a column rather than
// part of the blob. Choose it once.
func WithCodec(codec encoding.Codec) Option {
	return func(o *options) {
		if codec != nil {
			o.codec = codec
		}
	}
}

// WithSweeper starts a background sweep that removes rows past their purge
// deadline, every interval, until ctx is done.
//
// Unlike a cache, a table does not reclaim its own expired rows, and without a
// sweep this one grows by a row for every link ever minted. Running it is not
// optional in any long-lived deployment; what is optional is running it here
// rather than from a scheduler that calls Sweep, which is the better answer for
// a fleet — one sweeper, not one per replica.
//
// It is not what makes a link stop working. Expiry is decided by
// links.Record.Usable against the Minter's clock, so a row this has not reached
// yet is already refused.
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

// WithClock swaps the clock the sweeper ticks on and binds its horizon from.
//
// It is the sweeper's clock alone. Nothing else here reads one: when a link
// expires and when its row may be collected are both decided by the Minter and
// arrive as arguments, so a store whose clock disagrees with the Minter's
// collects a row early or late and can never make a dead link redeemable.
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

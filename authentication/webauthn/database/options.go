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

// ErrNilClient indicates NewSessionStore was called without a database client.
// It wraps errors.ErrNilInputParameter, so a caller may check either.
var ErrNilClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil webauthn session database client")

// DefaultSessionContentType is what ceremony state is encoded as when no codec
// is supplied: JSON, which is what the SessionData type's own field tags
// describe and what makes a stuck ceremony readable with a SELECT.
//
// Rows are short-lived — a ceremony is over in a minute — so changing this on a
// deployed store costs the ceremonies in flight at that moment and nothing
// more. That is the one encoding decision in this module that is genuinely
// reversible.
const DefaultSessionContentType = encoding.ContentTypeJSON

type (
	// Option configures a SessionStore at construction.
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
		codec: encoding.NewClientEncoder(DefaultSessionContentType),
		clock: clock.NewClock(),
	}

	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	return o
}

// WithCodec sets how ceremony state is encoded into the session_data column.
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
// sweep this one grows by a row per ceremony forever. It is not what makes a
// ceremony expire — Consume refuses a row past its deadline regardless — so a
// deployment that runs the sweep from a scheduler instead, one for the fleet
// rather than one per replica, loses nothing by leaving this off.
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

// WithClock swaps the clock the expires_at column is stamped from, the one
// Consume compares against, and the one the sweeper ticks on.
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

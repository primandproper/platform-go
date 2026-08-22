package inbound

import (
	"testing"
	"time"

	clockmock "github.com/primandproper/platform-go/v13/clock/mock"
	loggingnoop "github.com/primandproper/platform-go/v13/observability/logging/noop"
	metricsnoop "github.com/primandproper/platform-go/v13/observability/metrics/noop"
	tracingnoop "github.com/primandproper/platform-go/v13/observability/tracing/noop"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewVerifierConfig(T *testing.T) {
	T.Parallel()

	T.Run("defaults to the standard tolerance and the wall clock", func(t *testing.T) {
		t.Parallel()

		cfg := newVerifierConfig(nil)

		must.NotNil(t, cfg)
		test.EqOp(t, DefaultTolerance, cfg.Tolerance)
		test.False(t, cfg.Now().IsZero())
	})

	T.Run("skips nil options", func(t *testing.T) {
		t.Parallel()

		cfg := newVerifierConfig([]VerifierOption{nil, WithTolerance(time.Minute), nil})

		must.NotNil(t, cfg)
		test.EqOp(t, time.Minute, cfg.Tolerance)
	})
}

func TestVerifierConfig_now(T *testing.T) {
	T.Parallel()

	at := time.Unix(1614556800, 0)

	T.Run("a pinned time wins over a clock", func(t *testing.T) {
		t.Parallel()

		cfg := newVerifierConfig([]VerifierOption{
			WithClock(&clockmock.ClockMock{NowFunc: func() time.Time { return at.Add(time.Hour) }}),
			WithVerificationTime(at),
		})

		test.EqOp(t, at, cfg.Now())
	})

	// A zero time would otherwise pin verification to the Unix epoch and reject everything.
	T.Run("a zero pinned time is ignored", func(t *testing.T) {
		t.Parallel()

		cfg := newVerifierConfig([]VerifierOption{
			WithVerificationTime(time.Time{}),
			WithClock(&clockmock.ClockMock{NowFunc: func() time.Time { return at }}),
		})

		test.EqOp(t, at, cfg.Now())
	})

	T.Run("a nil clock is ignored", func(t *testing.T) {
		t.Parallel()

		cfg := newVerifierConfig([]VerifierOption{WithClock(nil)})

		test.Nil(t, cfg.Clock)
		test.False(t, cfg.Now().IsZero())
	})
}

func TestWithTolerance(T *testing.T) {
	T.Parallel()

	// There is deliberately no way to switch the freshness check off, so a non-positive
	// duration has to leave the default standing rather than mean "forever".
	T.Run("a non-positive duration leaves the default in place", func(t *testing.T) {
		t.Parallel()

		for _, d := range []time.Duration{0, -time.Hour} {
			test.EqOp(t, DefaultTolerance, newVerifierConfig([]VerifierOption{WithTolerance(d)}).Tolerance)
		}
	})
}

func TestVerifierConfig_secretsWith(T *testing.T) {
	T.Parallel()

	T.Run("orders the primary first and drops empties", func(t *testing.T) {
		t.Parallel()

		cfg := newVerifierConfig([]VerifierOption{WithAdditionalSecrets("", "outgoing", "")})

		test.Eq(t, []string{"incoming", "outgoing"}, cfg.secretsWith("incoming"))
	})

	T.Run("reports nothing when every secret is empty", func(t *testing.T) {
		t.Parallel()

		test.SliceLen(t, 0, newVerifierConfig([]VerifierOption{WithAdditionalSecrets("")}).secretsWith(""))
	})
}

func TestReceiverOptions(T *testing.T) {
	T.Parallel()

	T.Run("applies every option", func(t *testing.T) {
		t.Parallel()

		r := &Receiver{}
		for _, opt := range []ReceiverOption{
			WithReceiverLogger(loggingnoop.NewLogger()),
			WithReceiverTracerProvider(tracingnoop.NewTracerProvider()),
			WithReceiverMetricsProvider(metricsnoop.NewMetricsProvider()),
			WithReceiverClock(&clockmock.ClockMock{}),
			WithMaxBodyBytes(1024),
			WithForwardedHeaders("x-acme-delivery"),
		} {
			opt(r)
		}

		test.NotNil(t, r.logger)
		test.NotNil(t, r.tracerProvider)
		test.NotNil(t, r.metricsProvider)
		test.NotNil(t, r.clock)
		test.EqOp(t, int64(1024), r.maxBodyBytes)
		test.MapLen(t, 1, r.forwarded)

		// Canonicalized on the way in, so a caller may name a header however it likes.
		_, ok := r.forwarded["X-Acme-Delivery"]
		test.True(t, ok)
	})

	// An unbounded read on a public endpoint is not a configuration this package offers.
	T.Run("a non-positive body cap is ignored", func(t *testing.T) {
		t.Parallel()

		r := &Receiver{maxBodyBytes: DefaultMaxBodyBytes}

		WithMaxBodyBytes(0)(r)
		WithMaxBodyBytes(-1)(r)

		test.EqOp(t, DefaultMaxBodyBytes, r.maxBodyBytes)
	})

	T.Run("a nil clock is ignored", func(t *testing.T) {
		t.Parallel()

		r := &Receiver{}

		WithReceiverClock(nil)(r)

		test.Nil(t, r.clock)
	})

	// Otherwise a caller that meant to forward nothing in particular would silently switch the
	// receiver from "everything but credentials" to "nothing at all".
	T.Run("an empty allowlist is ignored", func(t *testing.T) {
		t.Parallel()

		r := &Receiver{}

		WithForwardedHeaders()(r)
		WithForwardedHeaders("")(r)

		test.Nil(t, r.forwarded)
	})
}

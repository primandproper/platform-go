package requestsigning

import (
	"time"

	"github.com/primandproper/platform-go/v13/clock"
)

// Option configures signing and verification. One type serves Sign's
// counterpart Verify, NewSigner, and NewVerifier, because the three share a
// notion of what time it is; each option's doc says which of them read it.
type Option func(*config)

// config is what the options set. The clock/pin/tolerance triple is Freshness,
// embedded rather than restated, so this package and every scheme that borrows
// it resolve "now" the same way.
type config struct {
	Freshness
}

func newConfig(opts []Option) *config {
	cfg := &config{Freshness: Freshness{Tolerance: DefaultTolerance}}

	for _, opt := range opts {
		if opt != nil {
			opt(cfg)
		}
	}

	return cfg
}

// WithClock swaps the source of time a Signer stamps with and a Verifier
// compares against. Read by Verify, NewSigner, and NewVerifier.
//
// Inside a testing/synctest bubble clock.NewClock already reads the bubble's
// fake time, so this is for the cases a bubble cannot express — a deliberately
// skewed peer, a clock driven by a test harness. A nil clock is ignored.
func WithClock(c clock.Clock) Option {
	return func(cfg *config) {
		if c != nil {
			cfg.Clock = c
		}
	}
}

// WithTolerance overrides DefaultTolerance — how far the signature's timestamp
// may sit from the verifier's clock. A non-positive duration leaves the default
// in place. Read by Verify and NewVerifier; a Signer has no use for it.
//
// There is deliberately no way to disable the check. A signature with no
// freshness bound is replayable forever, which is the property this scheme
// exists to remove.
func WithTolerance(d time.Duration) Option {
	return func(cfg *config) {
		if d > 0 {
			cfg.Tolerance = d
		}
	}
}

// WithVerificationTime pins the instant a verification compares the signature's
// timestamp against, instead of reading a clock. It exists for tests and for
// replaying a captured request against a known instant, and it wins over
// WithClock. Read by Verify and NewVerifier.
//
// A zero time is ignored, so this cannot accidentally pin verification to the
// Unix epoch and reject everything.
func WithVerificationTime(t time.Time) Option {
	return func(cfg *config) {
		if !t.IsZero() {
			cfg.At = t
		}
	}
}

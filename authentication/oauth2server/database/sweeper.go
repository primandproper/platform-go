package database

import (
	"context"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/metrics"
)

// sweptKey is this store's one observability key.
const sweptKey = "oauth2server.swept"

// sweepEvery sweeps on every tick until ctx is done.
//
// Ticks come from the injected clock, so inside a testing/synctest bubble the
// sweeper advances with the bubble's fake time and needs no test double.
func (s *Store) sweepEvery(ctx context.Context, interval time.Duration) {
	ticker := s.clock.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.Chan():
			// Logged rather than returned: nothing is waiting on this
			// goroutine, and a sweep that fails is four tables that grow for
			// another interval, not a token that misbehaves.
			if _, err := s.Sweep(ctx, s.now()); err != nil {
				s.o11y.Logger().Error("sweeping expired oauth2 records", err)
			}
		}
	}
}

// newSweepInstruments builds the two counters the sweeper owns. They live here
// rather than on the Server because nothing above this layer knows a sweep
// happened.
func newSweepInstruments(provider metrics.Provider) (swept, errs metrics.Int64Counter, err error) {
	mp := metrics.EnsureMetricsProvider(provider)

	if swept, err = mp.NewInt64Counter(serviceName + "_rows_swept"); err != nil {
		return nil, nil, platformerrors.Wrap(err, "creating swept oauth2 rows counter")
	}

	if errs, err = mp.NewInt64Counter(serviceName + "_sweep_errors"); err != nil {
		return nil, nil, platformerrors.Wrap(err, "creating oauth2 sweep errors counter")
	}

	return swept, errs, nil
}

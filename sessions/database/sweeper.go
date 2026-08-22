package database

import (
	"context"
	"fmt"
	"time"

	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability/metrics"
)

// sweptKey is this backend's one observability key.
const sweptKey = "sessions.swept"

// Sweep removes every row whose deadline has passed, reporting how many it
// removed.
//
// It is not what makes a session expire — the store decides that from the
// record's own anchors, so a row this has not reached yet is already unusable.
// What it does is stop the table growing with every session ever created.
//
// One statement, no batching. Session rows are small and the index on
// expires_at makes the delete proportional to what is actually dead rather than
// to the table; a fleet that outgrows that wants a scheduled sweep with its own
// batching rather than a bigger one here.
func (b *Backend[T]) Sweep(ctx context.Context) (int64, error) {
	ctx, op := b.o11y.Begin(ctx)
	defer op.End()

	query, args := buildSweep(b.dialect, b.table, b.clock.Now())

	swept, err := b.exec(ctx, b.db.Writer(), query, args)
	if err != nil {
		b.sweepErrorsCounter.Add(ctx, 1)

		return 0, op.Error(err, "sweeping expired session rows")
	}

	b.sweptCounter.Add(ctx, swept)
	op.Set(sweptKey, swept)

	return swept, nil
}

// sweepEvery sweeps on every tick until ctx is done.
//
// Ticks come from the injected clock, so inside a testing/synctest bubble the
// sweeper advances with the bubble's fake time and needs no test double.
func (b *Backend[T]) sweepEvery(ctx context.Context, interval time.Duration) {
	ticker := b.clock.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.Chan():
			// Logged rather than returned: nothing is waiting on this
			// goroutine, and a sweep that fails is a table that grows for
			// another interval, not a session that misbehaves.
			if _, err := b.Sweep(ctx); err != nil {
				b.o11y.Logger().Error("sweeping expired session rows", err)
			}
		}
	}
}

// newSweepInstruments builds the two counters the sweeper owns. They live here
// rather than on the store because nothing above this layer knows a sweep
// happened.
func newSweepInstruments(provider metrics.Provider) (swept, errs metrics.Int64Counter, err error) {
	mp := metrics.EnsureMetricsProvider(provider)

	if swept, err = mp.NewInt64Counter(fmt.Sprintf("%s_rows_swept", serviceName)); err != nil {
		return nil, nil, platformerrors.Wrap(err, "creating swept session rows counter")
	}

	if errs, err = mp.NewInt64Counter(fmt.Sprintf("%s_sweep_errors", serviceName)); err != nil {
		return nil, nil, platformerrors.Wrap(err, "creating session sweep errors counter")
	}

	return swept, errs, nil
}

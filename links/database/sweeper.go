package database

import (
	"context"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v14/links/database/internal/linksdb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
)

// sweptKey is this store's one observability key.
const sweptKey = "links.swept"

// backgroundSweepFailure is what the background loop logs when a sweep fails.
// It is a constant because a test asserts on it: the loop's only effect is this
// line, so a loop that stopped emitting it would otherwise fail silently.
const backgroundSweepFailure = "background sweep of collectable action link rows failed"

// Sweep removes every row past its purge deadline, reporting how many it
// removed.
//
// It is not what makes a link expire — links.Record.Usable decides that from
// the record's own deadline against the Minter's clock, so a row this has not
// reached yet is already refused. What it does is stop the table growing by a
// row for every link ever minted, and it is what a cache gets for free.
//
// It collects resolved rows at their purge deadline rather than at their
// resolution, which is the whole retention policy: a spent link keeps answering
// "already used" for exactly as long as the Minter said it should.
//
// One statement, no batching. Link rows are small and the index on purge_after
// makes the delete proportional to what is actually dead rather than to the
// table; a fleet that outgrows that wants a scheduled sweep with its own
// batching rather than a bigger one here.
func (s *Store) Sweep(ctx context.Context) (int64, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	// The horizon is this store's own clock rather than the server's. It is not
	// the clock that stamped purge_after — that one belongs to the Minter — but
	// it is one this deployment can point at the same source, where
	// CURRENT_TIMESTAMP is a third party to the comparison and, under a test
	// clock that only moves when a test moves it, years away. See
	// querygen.AtMostArgument, and the sweep statement in internal/queries.
	swept, err := s.q.SweepLinks(ctx, s.db.Writer(),
		linksdb.SweepLinksParams{PurgeBefore: s.clock.Now().UTC()})
	if err != nil {
		s.sweepErrorsCounter.Add(ctx, 1)

		return 0, op.Error(err, "sweeping collectable action link rows")
	}

	s.sweptCounter.Add(ctx, swept)
	op.Set(sweptKey, swept)

	return swept, nil
}

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
			// goroutine, and a sweep that fails is a table that grows for
			// another interval, not a link that misbehaves.
			if _, err := s.Sweep(ctx); err != nil {
				s.o11y.Logger().Error(backgroundSweepFailure, err)
			}
		}
	}
}

// newSweepInstruments builds the two counters the sweeper owns. They live here
// rather than on the Minter because nothing above this layer knows a sweep
// happened.
func newSweepInstruments(provider metrics.Provider) (swept, errs metrics.Int64Counter, err error) {
	mp := metrics.EnsureMetricsProvider(provider)

	if swept, err = mp.NewInt64Counter(fmt.Sprintf("%s_rows_swept", serviceName)); err != nil {
		return nil, nil, platformerrors.Wrap(err, "creating swept action link rows counter")
	}

	if errs, err = mp.NewInt64Counter(fmt.Sprintf("%s_sweep_errors", serviceName)); err != nil {
		return nil, nil, platformerrors.Wrap(err, "creating action link sweep errors counter")
	}

	return swept, errs, nil
}

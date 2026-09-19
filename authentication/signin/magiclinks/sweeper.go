package magiclinks

import (
	"context"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v14/authentication/signin/magiclinks/internal/magiclinkdb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
)

// sweptKey is the sweeper's one observability key.
const sweptKey = "signin.magic_links_swept"

// backgroundSweepFailure is what the background loop logs when a sweep fails. It
// is a constant because a test asserts on it: the loop's only effect is this
// line, so a loop that stopped emitting it would otherwise fail silently.
const backgroundSweepFailure = "background sweep of collectable sign-in link rows failed"

// Sweep removes every row past its purge deadline, reporting how many it
// removed.
//
// It is not what makes a sign-in link stop working — the exchange's own guard
// decides that against the deadline in the row, so a row this has not reached yet
// is already refused. What it does is stop the table growing by a row for every
// exchange ever made, which under rotation is a row per refresh rather than a row
// per sign-in.
//
// The purge deadline is later than the expiry, and the gap is load-bearing rather
// than tidy: a row collected at its own expiry can no longer be told from a row
// that never existed, and only one of those two stories is one an operator can
// act on. See WithRetention.
//
// It takes neither a transaction nor a scope, which is the carve-out CLAUDE.md
// names for a component's own machinery: a worker on a timer is this store
// servicing itself rather than answering a consumer's read, and a lease on
// somebody else's transaction is exactly what it must not hold. It runs on the
// handle the store was built with.
//
// One statement, no batching. Token rows are small and the index on purge_after
// makes the delete proportional to what is actually dead rather than to the
// table; a fleet that outgrows that wants a scheduled sweep with its own batching
// rather than a bigger one here.
func (s *SQLStore) Sweep(ctx context.Context) (int64, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	// The horizon is this store's own clock rather than the server's. It is the
	// clock that stamped purge_after, so the two sides of the comparison come
	// from one source — where CURRENT_TIMESTAMP is a third party to it and, under
	// a test clock that only moves when a test moves it, years away. See
	// querygen.AtMostArgument, and the sweep statement in internal/queries.
	swept, err := s.q.SweepMagicLinks(ctx, s.db.Writer(),
		magiclinkdb.SweepMagicLinksParams{PurgeBefore: s.clock.Now().UTC()})
	if err != nil {
		s.sweepErrorsCounter.Add(ctx, 1)

		return 0, op.Error(err, "sweeping collectable sign-in link rows")
	}

	s.sweptCounter.Add(ctx, swept)
	op.Set(sweptKey, swept)

	return swept, nil
}

// sweepEvery sweeps on every tick until ctx is done.
//
// Ticks come from the injected clock, so inside a testing/synctest bubble the
// sweeper advances with the bubble's fake time and needs no test double.
func (s *SQLStore) sweepEvery(ctx context.Context, interval time.Duration) {
	ticker := s.clock.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.Chan():
			// Logged rather than returned: nothing is waiting on this goroutine,
			// and a sweep that fails is a table that grows for another interval,
			// not a sign-in that misbehaves.
			if _, err := s.Sweep(ctx); err != nil {
				s.o11y.Logger().Error(backgroundSweepFailure, err)
			}
		}
	}
}

// newSweepInstruments builds the two counters the sweeper owns. They live here
// rather than on the sign-in service because nothing above this layer knows a
// sweep happened.
func newSweepInstruments(provider metrics.Provider) (swept, errs metrics.Int64Counter, err error) {
	mp := metrics.EnsureMetricsProvider(provider)

	if swept, err = mp.NewInt64Counter(fmt.Sprintf("%s_rows_swept", serviceName)); err != nil {
		return nil, nil, platformerrors.Wrap(err, "creating swept sign-in link rows counter")
	}

	if errs, err = mp.NewInt64Counter(fmt.Sprintf("%s_sweep_errors", serviceName)); err != nil {
		return nil, nil, platformerrors.Wrap(err, "creating sign-in link sweep errors counter")
	}

	return swept, errs, nil
}

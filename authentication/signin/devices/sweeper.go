package devices

import (
	"context"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v15/authentication/signin/devices/internal/devicesdb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
)

// sweptKey is the sweeper's one observability key.
const sweptKey = "signin.devices_swept"

// backgroundSweepFailure is what the background loop logs when a sweep fails. It
// is a constant because a test asserts on it: the loop's only effect is this
// line, so a loop that stopped emitting it would otherwise fail silently.
const backgroundSweepFailure = "background sweep of expired sign-in device rows failed"

// Sweep removes the row of every login that can no longer be alive, reporting
// how many it removed.
//
// It is not what hides an ended login's device — the annotator only answers for
// the logins a listing returns, so a row this has not reached yet is already
// unread. What it does is stop the table holding an address somebody signed in
// from for longer than anybody could be shown it.
//
// It takes neither a transaction nor a scope, which is the carve-out CLAUDE.md
// names for a component's own machinery: a worker on a timer is this store
// servicing itself rather than answering a consumer's read, and it collects
// what has expired in every scope at once. It runs on the handle the store was
// built with.
//
// One statement, no batching, for the reason refreshtokens' sweep gives: the
// index on expires_at makes the delete proportional to what is actually dead
// rather than to the table.
func (s *SQLStore) Sweep(ctx context.Context) (int64, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	// The horizon is this store's own clock rather than the server's: it is the
	// clock that stamps the row, so the comparison has one source rather than
	// two.
	swept, err := s.q.SweepSignInDevices(ctx, s.db.Writer(),
		devicesdb.SweepSignInDevicesParams{ExpiresBefore: s.clock.Now().UTC()})
	if err != nil {
		s.sweepErrorsCounter.Add(ctx, 1)

		return 0, op.Error(err, "sweeping expired sign-in device rows")
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

// newSweepInstruments builds the two counters the sweeper owns.
func newSweepInstruments(provider metrics.Provider) (swept, errs metrics.Int64Counter, err error) {
	mp := metrics.EnsureMetricsProvider(provider)

	if swept, err = mp.NewInt64Counter(fmt.Sprintf("%s_rows_swept", serviceName)); err != nil {
		return nil, nil, platformerrors.Wrap(err, "creating swept sign-in device rows counter")
	}

	if errs, err = mp.NewInt64Counter(fmt.Sprintf("%s_sweep_errors", serviceName)); err != nil {
		return nil, nil, platformerrors.Wrap(err, "creating sign-in device sweep errors counter")
	}

	return swept, errs, nil
}

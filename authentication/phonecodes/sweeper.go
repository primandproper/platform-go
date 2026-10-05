package phonecodes

import (
	"context"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v15/authentication/phonecodes/internal/phonecodesdb"

	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability/metrics"
)

// sweptKey is the sweeper's one observability key.
const sweptKey = serviceName + ".swept"

// backgroundSweepFailure is what the background loop logs when a sweep fails. It
// is a constant because a test asserts on it: the loop's only effect is this
// line, so a loop that stopped emitting it would otherwise fail silently.
const backgroundSweepFailure = "background sweep of collectable phone code rows failed"

// Sweep removes every row past its purge deadline, reporting how many it
// removed.
//
// It is not what makes a code stop working — the redemption's guards decide
// that against the row's own deadline, so a row this has not reached yet is
// already refused. What it does is stop the table keeping a phone number for
// every code anybody was ever sent.
//
// It spans every scope and takes neither a transaction nor a scope, which is the
// carve-out CLAUDE.md names for a component's own machinery: a worker on a timer
// is this store servicing itself rather than answering a consumer's read. It
// runs on the handle the store was built with.
//
// The horizon is this store's own clock rather than the server's, because that
// is the clock that stamped purge_after.
func (s *SQLStore) Sweep(ctx context.Context) (int64, error) {
	ctx, op := s.o11y.Begin(ctx)
	defer op.End()

	swept, err := s.q.SweepPhoneCodes(ctx, s.db.Writer(),
		phonecodesdb.SweepPhoneCodesParams{PurgeBefore: s.clock.Now().UTC()})
	if err != nil {
		s.sweepErrorsCounter.Add(ctx, 1)

		return 0, op.Error(err, "sweeping collectable phone code rows")
	}

	s.sweptCounter.Add(ctx, swept)
	op.Set(sweptKey, swept)

	return swept, nil
}

// sweepEvery sweeps on every tick until ctx is done. Ticks come from the
// injected clock.
func (s *SQLStore) sweepEvery(ctx context.Context, interval time.Duration) {
	ticker := s.clock.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.Chan():
			// Logged rather than returned: nothing is waiting on this goroutine,
			// and a sweep that fails is a table that grows for another interval.
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
		return nil, nil, platformerrors.Wrap(err, "creating swept phone code rows counter")
	}

	if errs, err = mp.NewInt64Counter(fmt.Sprintf("%s_sweep_errors", serviceName)); err != nil {
		return nil, nil, platformerrors.Wrap(err, "creating phone code sweep errors counter")
	}

	return swept, errs, nil
}

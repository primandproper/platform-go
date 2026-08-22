package retrycfg

import (
	"context"
	"math"
	"time"

	"github.com/primandproper/platform-go/v13/clock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"
	"github.com/primandproper/platform-go/v13/retry"

	"go.opentelemetry.io/otel/metric"
)

// serviceName scopes this package's spans, logger, and instrument names.
const serviceName = "retry"

// Span and log attribute keys.
const (
	attemptsKey    = "retry.attempts"
	maxAttemptsKey = "retry.max_attempts"
	delayKey       = "retry.delay"
	exhaustedKey   = "retry.exhausted"
)

// DelayFor returns the backoff before attempt, which is 1-indexed: attempt 1 is
// the first retry and waits InitialDelay. The delay grows by Multiplier per
// attempt and is capped at MaxDelay. An attempt below 1 is treated as 1.
//
// It exists because not every caller can retry by sleeping. Execute owns its
// loop and can wait in place; a worker that persists "try again at T" into a
// row cannot, because the wait has to survive the process. Both want the same
// schedule, and computing it twice is how the two quietly stop agreeing.
//
// Jitter is deliberately not applied here. Sleeping and scheduling want
// different distributions — Execute uses retry.Equal to keep a floor under each
// wait, while a fleet writing wake-up times wants retry.Full to spread them —
// so the shared part is the schedule and the caller decides how to perturb it.
// ScheduledDelayFor is the second of those, for callers that cannot sleep.
// Callers pass a Config that has been through EnsureDefaults; DelayFor does not
// mutate its argument.
func DelayFor(cfg Config, attempt uint) time.Duration {
	if attempt < 1 {
		attempt = 1
	}

	delay := float64(cfg.InitialDelay) * math.Pow(cfg.Multiplier, float64(attempt-1))

	if maxDelay := float64(cfg.MaxDelay); delay > maxDelay {
		return cfg.MaxDelay
	}

	return time.Duration(delay)
}

// minScheduledDelay floors a scheduled wait. A full-jittered delay can land
// arbitrarily close to zero, and a row that becomes claimable immediately spins
// against the same failure rather than waiting out whatever caused it.
const minScheduledDelay = time.Millisecond

// ScheduledDelayFor returns the delay before the next attempt for a caller that
// writes a wake-up timestamp instead of sleeping.
//
// The schedule is DelayFor's, so a persisted retry and a retry.Policy grow their
// delays identically from the same Config. What differs is everything around it:
// the wait survives a process restart because it is a column rather than a
// goroutine, and the jitter is retry.Full rather than retry.Equal — several
// workers share one table, and spreading their next attempts across the whole
// window is what keeps them from re-colliding on every round after one contended
// claim. The floor is what makes that safe for a caller who cannot sleep off a
// near-zero draw.
//
// attempt is 1-indexed and signed because it usually arrives as a column: a
// value below 1, negative included, is treated as the first attempt rather than
// wrapping into an enormous exponent.
func ScheduledDelayFor(cfg Config, attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}

	var jitter retry.Jitter = retry.None
	if cfg.UseJitter {
		jitter = retry.Full(nil)
	}

	return jitter.AtLeast(minScheduledDelay)(DelayFor(cfg, uint(attempt)))
}

var _ retry.Policy = (*ExponentialBackoffPolicy)(nil)

// ExponentialBackoffPolicy retries with exponential backoff and optional
// jitter. It is what NewExponentialBackoffPolicy returns, so a caller who has
// chosen this schedule can depend on that choice rather than on the Policy seam
// every schedule shares.
type ExponentialBackoffPolicy struct {
	o11y   observability.Observer
	clock  clock.Clock
	jitter retry.Jitter

	attemptCounter    metrics.Int64Counter
	exhaustionCounter metrics.Int64Counter
	addOptions        []metric.AddOption

	config Config
}

// NewExponentialBackoffPolicy returns a Policy that retries with exponential
// backoff.
//
// Name it. A Config is embedded in most of the packages here that talk over a
// network, so a deployment runs many of these at once and an unnamed one's
// attempts land in the same counter as everyone else's.
func NewExponentialBackoffPolicy(cfg Config, opts ...Option) (*ExponentialBackoffPolicy, error) {
	cfg.EnsureDefaults()

	o := newOptions(opts)

	name := serviceName
	if o.name != "" {
		name = serviceName + "_" + o.name
	}

	p := &ExponentialBackoffPolicy{
		config:     cfg,
		clock:      o.clock,
		jitter:     retry.None,
		o11y:       observability.NewObserver(name, o.logger, o.tracerProvider),
		addOptions: o.addOptions(),
	}

	if cfg.UseJitter {
		p.jitter = retry.Equal(o.rand)
	}

	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

	var err error
	if p.attemptCounter, err = mp.NewInt64Counter(serviceName + "_attempts"); err != nil {
		return nil, platformerrors.Wrap(err, "creating retry attempt counter")
	}

	// The number that says a service is spending its latency budget on retries
	// rather than on work. Attempts alone cannot: a loop that succeeds on its
	// second try and one that fails on its third both report attempts, and only
	// one of them is a problem.
	if p.exhaustionCounter, err = mp.NewInt64Counter(serviceName + "_exhaustions"); err != nil {
		return nil, platformerrors.Wrap(err, "creating retry exhaustion counter")
	}

	return p, nil
}

// Execute runs the operation, retrying on failure up to MaxAttempts times.
//
// An operation that never succeeds comes back as a retry.ExhaustedError
// carrying the number of attempts that produced it. The last error is still in
// the chain, so everything a caller could match against before still matches;
// what is new is that the caller can also tell "this failed" from "this failed
// five times over four seconds".
func (e *ExponentialBackoffPolicy) Execute(ctx context.Context, operation func(ctx context.Context) error) error {
	ctx, op := e.o11y.Begin(ctx)
	defer op.End()

	op.SpanOnly(maxAttemptsKey, e.config.MaxAttempts)

	var (
		lastErr  error
		attempts uint
	)

	for attempt := uint(0); attempt < e.config.MaxAttempts; attempt++ {
		select {
		case <-ctx.Done():
			op.SpanOnly(attemptsKey, attempts)

			if lastErr != nil {
				return lastErr
			}

			return ctx.Err()
		default:
		}

		attempts++
		e.attemptCounter.Add(ctx, 1, e.addOptions...)

		lastErr = operation(ctx)
		if lastErr == nil {
			op.SpanOnly(attemptsKey, attempts)

			return nil
		}

		// A canceled/expired loop context or an explicitly non-retryable error can
		// never be resolved by another attempt — return immediately instead of
		// sleeping and burning the remaining attempts. Neither is exhaustion: the
		// loop stopped for a reason of its own rather than for want of attempts.
		if retry.IsTerminal(ctx, lastErr) {
			op.SpanOnly(attemptsKey, attempts)

			return lastErr
		}

		if attempt == e.config.MaxAttempts-1 {
			break
		}

		if err := e.wait(ctx, op, attempt); err != nil {
			op.SpanOnly(attemptsKey, attempts)

			return lastErr
		}
	}

	return e.exhausted(ctx, op, attempts, lastErr)
}

// wait sleeps out the backoff before the next attempt, reporting a context that
// went away underneath it.
//
// The jitter is retry.Equal rather than retry.Full because this caller sleeps in
// place: half the schedule stays under every wait, so a loop that has backed off
// to seconds cannot draw a near-zero one and become hot again.
func (e *ExponentialBackoffPolicy) wait(ctx context.Context, op observability.Operation, attempt uint) error {
	// attempt is 0-indexed here and DelayFor is 1-indexed: the wait after
	// the first failed attempt is the first retry's delay.
	sleepDuration := e.jitter(DelayFor(e.config, attempt+1))

	op.SpanOnly(delayKey, sleepDuration.String())

	return e.clock.Sleep(ctx, sleepDuration)
}

// exhausted reports a loop that spent every attempt it had.
//
// A loop with no error to report never ran — MaxAttempts is at least 1 after
// EnsureDefaults, so this is unreachable — and nil is the honest answer either
// way.
func (e *ExponentialBackoffPolicy) exhausted(ctx context.Context, op observability.Operation, attempts uint, lastErr error) error {
	op.SpanOnly(attemptsKey, attempts)

	if lastErr == nil {
		return nil
	}

	op.SpanOnly(exhaustedKey, true)
	e.exhaustionCounter.Add(ctx, 1, e.addOptions...)

	return op.Error(retry.Exhausted(attempts, lastErr), "retrying operation")
}

package postgres

import (
	"context"
	stderrors "errors"
	"fmt"

	"github.com/primandproper/platform-go/v13/circuitbreaking"
	circuitbreakingcfg "github.com/primandproper/platform-go/v13/circuitbreaking/config"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/distributedlock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/keys"
	"github.com/primandproper/platform-go/v13/observability/metrics"
)

const scopedServiceName = "postgres_scoped_lock"

// errScopedNotAcquired is the internal sentinel TryWithLock threads through the
// transaction boundary on contention. It is deliberately not
// distributedlock.ErrLockNotAcquired so an fn that itself returns that sentinel
// cannot be mistaken for a contended acquisition.
var errScopedNotAcquired = platformerrors.New("scoped advisory lock contended")

var _ distributedlock.ScopedLocker = (*ScopedLocker)(nil)

// ScopedLocker implements distributedlock.ScopedLocker with transaction-scoped
// advisory locks. Each call opens a throwaway transaction that exists only to
// hold the lock while fn runs: the lock dies with the transaction on return,
// on error, on panic, or — crucially — with the connection if the process
// crashes mid-fn. There is no session pinning, no outstanding-lock
// bookkeeping, and no TTL: the lock is held exactly as long as fn runs.
type ScopedLocker struct {
	o11y           observability.Observer
	db             database.Client
	circuitBreaker circuitbreaking.CircuitBreaker
	acquireCounter metrics.Int64Counter
	contendCounter metrics.Int64Counter
	errCounter     metrics.Int64Counter
	latencyHist    metrics.Float64Histogram
	namespace      int32
}

// NewPostgresScopedLocker constructs a transaction-scoped
// distributedlock.ScopedLocker. Unlike NewPostgresLocker it needs only the
// safe database.Client surface (WithTransaction), not RawAccess.
//
// fn runs while the calling transaction holds the advisory lock, but receives
// only a context: any database work fn performs goes through its own
// connections and is NOT part of the lock-holding transaction. WithLock waits
// in the database (pg_advisory_xact_lock queues server-side, so waiters need
// no polling and are granted the lock in request order); each in-flight call
// occupies one write-pool connection for fn's duration.
func NewPostgresScopedLocker(
	cfg *Config,
	db database.Client,
	cb circuitbreaking.CircuitBreaker,
	opts ...Option,
) (*ScopedLocker, error) {
	if cfg == nil {
		return nil, distributedlock.ErrNilConfig
	}
	if db == nil {
		return nil, distributedlock.ErrNilDatabaseClient
	}

	o := newOptions(opts)

	mp := metrics.EnsureMetricsProvider(o.metricsProvider)

	acquireCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_acquires", scopedServiceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating acquire counter")
	}
	contendCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_contended", scopedServiceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating contention counter")
	}
	errCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_errors", scopedServiceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating error counter")
	}
	latencyHist, err := mp.NewFloat64Histogram(fmt.Sprintf("%s_latency_ms", scopedServiceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating latency histogram")
	}

	return &ScopedLocker{
		o11y:           observability.NewObserver(scopedServiceName, o.logger, o.tracerProvider),
		db:             db,
		circuitBreaker: circuitbreakingcfg.EnsureCircuitBreaker(cb, circuitbreakingcfg.WithLogger(o.logger)),
		acquireCounter: acquireCounter,
		contendCounter: contendCounter,
		errCounter:     errCounter,
		latencyHist:    latencyHist,
		namespace:      cfg.Namespace,
	}, nil
}

// WithLock implements distributedlock.ScopedLocker. The wait happens in the
// database: pg_advisory_xact_lock blocks until the lock is granted, and a
// canceled ctx aborts the wait through the driver.
func (s *ScopedLocker) WithLock(ctx context.Context, key string, fn func(ctx context.Context) error) error {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(keys.LockKeyKey, key))
	defer op.End()

	if key == "" {
		return distributedlock.ErrEmptyKey
	}
	if s.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	defer op.Time(ctx, nil, s.latencyHist)()

	lockID := hashLockID(s.namespace, key)
	op.Set(keys.LockIDKey, lockID)

	var acquired bool
	err := s.db.WithTransaction(ctx, func(tx database.SQLQueryExecutor) error {
		if _, execErr := tx.ExecContext(ctx, queryLockXact, lockID); execErr != nil {
			return platformerrors.Wrap(execErr, "acquiring transaction-scoped advisory lock")
		}

		acquired = true
		s.acquireCounter.Add(ctx, 1)

		return fn(ctx)
	})

	// Only a failure to acquire is an infrastructure signal; fn's own errors
	// pass through without tripping the breaker or the error counter.
	if !acquired && err != nil {
		s.errCounter.Add(ctx, 1)
		s.circuitBreaker.Failed()
		return op.Error(err, "acquiring scoped advisory lock")
	}

	s.circuitBreaker.Succeeded()

	return err
}

// TryWithLock implements distributedlock.ScopedLocker without waiting.
func (s *ScopedLocker) TryWithLock(ctx context.Context, key string, fn func(ctx context.Context) error) (bool, error) {
	ctx, op := s.o11y.Begin(ctx, observability.WithValue(keys.LockKeyKey, key))
	defer op.End()

	if key == "" {
		return false, distributedlock.ErrEmptyKey
	}
	if s.circuitBreaker.CannotProceed() {
		return false, circuitbreaking.ErrCircuitBroken
	}

	defer op.Time(ctx, nil, s.latencyHist)()

	lockID := hashLockID(s.namespace, key)
	op.Set(keys.LockIDKey, lockID)

	var acquired bool
	err := s.db.WithTransaction(ctx, func(tx database.SQLQueryExecutor) error {
		var got bool
		if scanErr := tx.QueryRowContext(ctx, queryTryLockXact, lockID).Scan(&got); scanErr != nil {
			return platformerrors.Wrap(scanErr, "calling pg_try_advisory_xact_lock")
		}
		if !got {
			return errScopedNotAcquired
		}

		acquired = true
		s.acquireCounter.Add(ctx, 1)

		return fn(ctx)
	})

	if stderrors.Is(err, errScopedNotAcquired) {
		s.contendCounter.Add(ctx, 1)
		s.circuitBreaker.Succeeded()
		return false, nil
	}

	if !acquired && err != nil {
		s.errCounter.Add(ctx, 1)
		s.circuitBreaker.Failed()
		return false, op.Error(err, "trying scoped advisory lock")
	}

	s.circuitBreaker.Succeeded()

	return acquired, err
}

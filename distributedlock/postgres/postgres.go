package postgres

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"github.com/primandproper/platform-go/circuitbreaking"
	circuitbreakingcfg "github.com/primandproper/platform-go/circuitbreaking/config"
	"github.com/primandproper/platform-go/database"
	"github.com/primandproper/platform-go/distributedlock"
	platformerrors "github.com/primandproper/platform-go/errors"
	"github.com/primandproper/platform-go/identifiers"
	"github.com/primandproper/platform-go/observability"
	"github.com/primandproper/platform-go/observability/keys"
	"github.com/primandproper/platform-go/observability/logging"
	"github.com/primandproper/platform-go/observability/metrics"
	"github.com/primandproper/platform-go/observability/tracing"
)

const serviceName = "postgres_distributed_lock"

var (
	_ distributedlock.Locker = (*locker)(nil)
	_ distributedlock.Lock   = (*lock)(nil)
)

type locker struct {
	o11y           observability.Observer
	db             database.Client
	circuitBreaker circuitbreaking.CircuitBreaker
	acquireCounter metrics.Int64Counter
	releaseCounter metrics.Int64Counter
	refreshCounter metrics.Int64Counter
	contendCounter metrics.Int64Counter
	errCounter     metrics.Int64Counter
	latencyHist    metrics.Float64Histogram
	outstanding    map[string]*lock
	namespace      int32
	mu             sync.Mutex
}

// NewPostgresLocker constructs a new postgres-backed distributedlock.Locker.
func NewPostgresLocker(
	cfg *Config,
	db database.Client,
	logger logging.Logger,
	tracerProvider tracing.TracerProvider,
	metricsProvider metrics.Provider,
	cb circuitbreaking.CircuitBreaker,
) (distributedlock.Locker, error) {
	if cfg == nil {
		return nil, distributedlock.ErrNilConfig
	}
	if db == nil {
		return nil, distributedlock.ErrNilDatabaseClient
	}

	mp := metrics.EnsureMetricsProvider(metricsProvider)

	acquireCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_acquires", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating acquire counter")
	}
	releaseCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_releases", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating release counter")
	}
	refreshCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_refreshes", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating refresh counter")
	}
	contendCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_contended", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating contention counter")
	}
	errCounter, err := mp.NewInt64Counter(fmt.Sprintf("%s_errors", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating error counter")
	}
	latencyHist, err := mp.NewFloat64Histogram(fmt.Sprintf("%s_latency_ms", serviceName))
	if err != nil {
		return nil, platformerrors.Wrap(err, "creating latency histogram")
	}

	return &locker{
		o11y:           observability.NewObserver(serviceName, logger, tracerProvider),
		db:             db,
		circuitBreaker: circuitbreakingcfg.EnsureCircuitBreaker(cb),
		acquireCounter: acquireCounter,
		releaseCounter: releaseCounter,
		refreshCounter: refreshCounter,
		contendCounter: contendCounter,
		errCounter:     errCounter,
		latencyHist:    latencyHist,
		namespace:      cfg.Namespace,
		outstanding:    make(map[string]*lock),
	}, nil
}

// Acquire implements distributedlock.Locker.
func (l *locker) Acquire(ctx context.Context, key string, ttl time.Duration) (distributedlock.Lock, error) {
	ctx, op := l.o11y.Begin(ctx)
	defer op.End()
	op.Set(keys.NameKey, key).Set("lock.ttl", ttl)

	if key == "" {
		return nil, distributedlock.ErrEmptyKey
	}
	if ttl <= 0 {
		return nil, distributedlock.ErrInvalidTTL
	}
	if l.circuitBreaker.CannotProceed() {
		return nil, circuitbreaking.ErrCircuitBroken
	}

	startTime := time.Now()
	defer func() {
		l.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	conn, err := l.db.WriteDB().Conn(ctx)
	if err != nil {
		l.errCounter.Add(ctx, 1)
		l.circuitBreaker.Failed()
		return nil, op.Error(err, "reserving postgres conn")
	}

	lockID := hashLockID(l.namespace, key)
	op.Set("lock.id", lockID)
	var ok bool
	if scanErr := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, lockID).Scan(&ok); scanErr != nil {
		// Best-effort return the conn to the pool.
		if closeErr := conn.Close(); closeErr != nil {
			op.Acknowledge(closeErr, "returning postgres conn to pool after failed advisory lock")
		}
		l.errCounter.Add(ctx, 1)
		l.circuitBreaker.Failed()
		return nil, op.Error(scanErr, "calling pg_try_advisory_lock")
	}

	if !ok {
		if closeErr := conn.Close(); closeErr != nil {
			op.Acknowledge(closeErr, "returning postgres conn to pool after contention")
		}
		l.contendCounter.Add(ctx, 1)
		l.circuitBreaker.Succeeded()
		return nil, distributedlock.ErrLockNotAcquired
	}

	token := identifiers.New()
	h := &lock{
		locker: l,
		conn:   conn,
		key:    key,
		token:  token,
		lockID: lockID,
		ttl:    ttl,
	}

	l.mu.Lock()
	l.outstanding[token] = h
	l.mu.Unlock()

	l.acquireCounter.Add(ctx, 1)
	l.circuitBreaker.Succeeded()
	return h, nil
}

// Ping implements distributedlock.Locker by pinging the underlying read DB.
func (l *locker) Ping(ctx context.Context) error {
	return l.db.ReadDB().PingContext(ctx)
}

// Close releases all outstanding locks held by this Locker. After Close, individual
// Lock handles will see ErrLockNotHeld on Release/Refresh.
func (l *locker) Close() error {
	l.mu.Lock()
	outstanding := l.outstanding
	l.outstanding = make(map[string]*lock)
	l.mu.Unlock()

	var firstErr error
	for _, h := range outstanding {
		if err := h.releaseLocked(context.Background()); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// release runs the unlock SQL on the dedicated conn and returns it to the pool.
// It removes the handle from the locker's outstanding map.
func (l *locker) release(ctx context.Context, h *lock) error {
	ctx, op := l.o11y.Begin(ctx)
	defer op.End()
	op.Set(keys.NameKey, h.key).Set("lock.id", h.lockID)

	if l.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	startTime := time.Now()
	defer func() {
		l.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	l.mu.Lock()
	if _, ok := l.outstanding[h.token]; !ok {
		l.mu.Unlock()
		return distributedlock.ErrLockNotHeld
	}
	delete(l.outstanding, h.token)
	l.mu.Unlock()

	if err := h.releaseLocked(ctx); err != nil {
		l.errCounter.Add(ctx, 1)
		l.circuitBreaker.Failed()
		return op.Error(err, "releasing postgres advisory lock")
	}

	l.releaseCounter.Add(ctx, 1)
	l.circuitBreaker.Succeeded()
	return nil
}

// refresh validates that the underlying conn is still alive. Postgres advisory
// locks have no native TTL; refreshing is purely a liveness check that lets the
// caller bump their local TTL bookkeeping.
func (l *locker) refresh(ctx context.Context, h *lock, ttl time.Duration) error {
	ctx, op := l.o11y.Begin(ctx)
	defer op.End()
	op.Set(keys.NameKey, h.key).Set("lock.id", h.lockID).Set("lock.ttl", ttl)

	if ttl <= 0 {
		return distributedlock.ErrInvalidTTL
	}
	if l.circuitBreaker.CannotProceed() {
		return circuitbreaking.ErrCircuitBroken
	}

	startTime := time.Now()
	defer func() {
		l.latencyHist.Record(ctx, float64(time.Since(startTime).Milliseconds()))
	}()

	l.mu.Lock()
	_, stillHeld := l.outstanding[h.token]
	l.mu.Unlock()
	if !stillHeld {
		return distributedlock.ErrLockNotHeld
	}

	// SELECT 1 verifies the conn is alive without altering server state.
	var one int
	if err := h.conn.QueryRowContext(ctx, `SELECT 1`).Scan(&one); err != nil {
		l.errCounter.Add(ctx, 1)
		l.circuitBreaker.Failed()
		return distributedlock.ErrLockNotHeld
	}

	l.refreshCounter.Add(ctx, 1)
	l.circuitBreaker.Succeeded()
	return nil
}

// lock is the postgres-backed Lock handle. Each handle owns a dedicated *sql.Conn.
type lock struct {
	locker *locker
	conn   *sql.Conn
	key    string
	token  string
	lockID int64
	ttl    time.Duration
}

// Key implements distributedlock.Lock.
func (l *lock) Key() string { return l.key }

// TTL implements distributedlock.Lock.
func (l *lock) TTL() time.Duration { return l.ttl }

// Release implements distributedlock.Lock.
func (l *lock) Release(ctx context.Context) error {
	return l.locker.release(ctx, l)
}

// Refresh implements distributedlock.Lock.
func (l *lock) Refresh(ctx context.Context, ttl time.Duration) error {
	if err := l.locker.refresh(ctx, l, ttl); err != nil {
		return err
	}
	l.ttl = ttl
	return nil
}

// releaseLocked runs the unlock SQL and returns the conn to the pool. It does not
// touch the locker's outstanding map — the caller must do that under the locker
// mutex before calling this method.
func (l *lock) releaseLocked(ctx context.Context) error {
	defer func() {
		if err := l.conn.Close(); err != nil {
			observability.AcknowledgeError(err, l.locker.o11y.Logger(), nil, "returning postgres conn to pool")
		}
	}()
	var unlocked bool
	if err := l.conn.QueryRowContext(ctx, `SELECT pg_advisory_unlock($1)`, l.lockID).Scan(&unlocked); err != nil {
		return platformerrors.Wrap(err, "calling pg_advisory_unlock")
	}
	return nil
}

// hashLockID derives a stable int64 lock id from a (namespace, key) pair using
// FNV-64a. The namespace prefix lets independent services share a Postgres cluster
// without colliding on the advisory-lock id space.
func hashLockID(namespace int32, key string) int64 {
	h := fnv.New64a()
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], uint32(namespace))
	_, _ = h.Write(buf[:])
	_, _ = h.Write([]byte(key))
	return int64(h.Sum64())
}

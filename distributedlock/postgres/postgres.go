package postgres

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	stderrors "errors"
	"fmt"
	"hash/fnv"
	"sync"
	"time"

	"github.com/primandproper/platform-go/v5/circuitbreaking"
	circuitbreakingcfg "github.com/primandproper/platform-go/v5/circuitbreaking/config"
	"github.com/primandproper/platform-go/v5/database"
	"github.com/primandproper/platform-go/v5/distributedlock"
	platformerrors "github.com/primandproper/platform-go/v5/errors"
	"github.com/primandproper/platform-go/v5/identifiers"
	"github.com/primandproper/platform-go/v5/observability"
	"github.com/primandproper/platform-go/v5/observability/keys"
	"github.com/primandproper/platform-go/v5/observability/logging"
	"github.com/primandproper/platform-go/v5/observability/metrics"
	"github.com/primandproper/platform-go/v5/observability/tracing"
)

const serviceName = "postgres_distributed_lock"

var (
	_ distributedlock.Locker = (*locker)(nil)
	_ distributedlock.Lock   = (*lock)(nil)
)

type locker struct {
	o11y            observability.Observer
	readDB          *sql.DB
	writeDB         *sql.DB
	circuitBreaker  circuitbreaking.CircuitBreaker
	acquireCounter  metrics.Int64Counter
	releaseCounter  metrics.Int64Counter
	refreshCounter  metrics.Int64Counter
	contendCounter  metrics.Int64Counter
	errCounter      metrics.Int64Counter
	latencyHist     metrics.Float64Histogram
	outstanding     map[string]*lock
	connWaitTimeout time.Duration
	namespace       int32
	mu              sync.Mutex
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
	// Advisory locks pin a specific session, which needs the concrete *sql.DB (Conn) —
	// available only through the RawAccess capability, not the safe Client surface.
	raw, ok := db.(database.RawAccess)
	if !ok {
		return nil, platformerrors.New("database client does not expose raw access required for advisory locks")
	}

	connWaitTimeout := cfg.ConnWaitTimeout
	if connWaitTimeout == 0 {
		connWaitTimeout = DefaultConnWaitTimeout
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
		o11y:            observability.NewObserver(serviceName, logger, tracerProvider),
		readDB:          raw.ReadDB(),
		writeDB:         raw.WriteDB(),
		circuitBreaker:  circuitbreakingcfg.EnsureCircuitBreaker(cb),
		acquireCounter:  acquireCounter,
		releaseCounter:  releaseCounter,
		refreshCounter:  refreshCounter,
		contendCounter:  contendCounter,
		errCounter:      errCounter,
		latencyHist:     latencyHist,
		namespace:       cfg.Namespace,
		outstanding:     make(map[string]*lock),
		connWaitTimeout: connWaitTimeout,
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

	// Each held lock pins its own write-pool connection for its entire lifetime
	// (the advisory lock lives on that session). If every connection in the write
	// pool is pinned by a held lock, Conn() would otherwise block indefinitely.
	// Bound the wait so a saturated pool surfaces as contention instead of a hang.
	connCtx := ctx
	if l.connWaitTimeout > 0 {
		var cancel context.CancelFunc
		connCtx, cancel = context.WithTimeout(ctx, l.connWaitTimeout)
		defer cancel()
	}

	conn, err := l.writeDB.Conn(connCtx)
	if err != nil {
		// If our own bounding timeout fired while the caller's context is still
		// live, the write pool is saturated by other held locks. Report that as
		// contention rather than an opaque error or an unbounded block.
		if l.connWaitTimeout > 0 && stderrors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			l.contendCounter.Add(ctx, 1)
			l.circuitBreaker.Succeeded()
			return nil, distributedlock.ErrLockNotAcquired
		}
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
		locker:    l,
		conn:      conn,
		key:       key,
		token:     token,
		lockID:    lockID,
		ttl:       ttl,
		expiresAt: time.Now().Add(ttl),
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
	return l.readDB.PingContext(ctx)
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

	// If the TTL has already elapsed the caller no longer owns the lock, but the
	// underlying advisory lock/conn is still pinned — free it so it isn't leaked,
	// then report the lock as no longer held per the Lock contract.
	if h.expired() {
		if err := h.releaseLocked(ctx); err != nil {
			l.errCounter.Add(ctx, 1)
			l.circuitBreaker.Failed()
			return op.Error(err, "releasing expired postgres advisory lock")
		}
		l.circuitBreaker.Succeeded()
		return distributedlock.ErrLockNotHeld
	}

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

	// A lock whose TTL has elapsed can no longer be refreshed. Drop it from the
	// outstanding set and free the pinned advisory lock/conn, then report it as no
	// longer held so the caller reacquires rather than assuming continued ownership.
	if h.expired() {
		l.mu.Lock()
		delete(l.outstanding, h.token)
		l.mu.Unlock()
		if err := h.releaseLocked(ctx); err != nil {
			observability.AcknowledgeError(err, l.o11y.Logger(), nil, "releasing expired postgres advisory lock during refresh")
		}
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
	locker    *locker
	conn      *sql.Conn
	expiresAt time.Time
	key       string
	token     string
	lockID    int64
	ttl       time.Duration
}

// expired reports whether the lock's TTL has elapsed. Postgres advisory locks have
// no server-side expiry, so we track it client-side to honor the Lock contract's
// TTL semantics (mirroring the redis/memory backends): once expired, the handle is
// treated as no longer held.
func (l *lock) expired() bool {
	return time.Now().After(l.expiresAt)
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
	l.expiresAt = time.Now().Add(ttl)
	return nil
}

// releaseLocked runs the unlock SQL and returns the conn to the pool. It does not
// touch the locker's outstanding map — the caller must do that under the locker
// mutex before calling this method.
func (l *lock) releaseLocked(ctx context.Context) (err error) {
	defer func() {
		if err != nil {
			// The unlock did not complete (e.g. an already-canceled ctx makes database/sql
			// return without touching the wire), so this session may still hold the advisory
			// lock. Returning the conn healthy to the pool would leak the lock until the conn
			// ages out. Force the driver to discard the connection instead: closing the
			// physical connection ends the Postgres session and releases all locks it held.
			if rawErr := l.conn.Raw(func(any) error { return driver.ErrBadConn }); rawErr != nil && !stderrors.Is(rawErr, driver.ErrBadConn) {
				observability.AcknowledgeError(rawErr, l.locker.o11y.Logger(), nil, "discarding postgres conn after failed unlock")
			}
			return
		}

		if closeErr := l.conn.Close(); closeErr != nil {
			observability.AcknowledgeError(closeErr, l.locker.o11y.Logger(), nil, "returning postgres conn to pool")
		}
	}()

	var unlocked bool
	if err = l.conn.QueryRowContext(ctx, `SELECT pg_advisory_unlock($1)`, l.lockID).Scan(&unlocked); err != nil {
		return platformerrors.Wrap(err, "calling pg_advisory_unlock")
	}

	if !unlocked {
		// pg_advisory_unlock returns false when the current session did not hold the
		// advisory lock. We believed we held it (it was in the outstanding set), so this
		// is a real inconsistency — surface it rather than reporting a clean release. The
		// deferred handler will discard the suspect connection.
		err = platformerrors.New("pg_advisory_unlock reported the lock was not held by this session")
		return err
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

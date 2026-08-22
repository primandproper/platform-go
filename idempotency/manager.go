package idempotency

import (
	"context"
	stderrors "errors"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/cache"
	"github.com/primandproper/platform-go/v13/clock"
	"github.com/primandproper/platform-go/v13/distributedlock"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/identifiers"
	"github.com/primandproper/platform-go/v13/observability"
	"github.com/primandproper/platform-go/v13/observability/metrics"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// NewManager builds a Manager over a record store and a locker.
//
// The locker is required and has no default. An implicit noop would leave
// replay working while quietly removing mutual exclusion, which is the failure
// mode hardest to notice and most expensive to meet.
func NewManager[T any](
	store cache.Cache[Record[T]],
	locker distributedlock.ScopedLocker,
	opts ...Option,
) (*Manager[T], error) {
	if store == nil {
		return nil, ErrNilStore
	}
	if locker == nil {
		return nil, ErrNilLocker
	}

	o := &managerOptions{
		clock:        clock.NewClock(),
		ttl:          DefaultTTL,
		inFlightTTL:  DefaultInFlightTTL,
		maxKeyLength: DefaultMaxKeyLength,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(o)
		}
	}

	m := &Manager[T]{
		store:              store,
		locker:             locker,
		clock:              o.clock,
		tracerProvider:     o.tracerProvider,
		metricsProvider:    o.metricsProvider,
		keyPrefix:          DefaultKeyPrefix,
		ttl:                o.ttl,
		inFlightTTL:        o.inFlightTTL,
		maxKeyLength:       o.maxKeyLength,
		storeFailurePolicy: o.storeFailurePolicy,
		recordable:         func(*T) bool { return true },
	}

	if o.keyPrefix != nil {
		m.keyPrefix = *o.keyPrefix
	}

	// Asserted rather than assumed: Option cannot name T, so this is where a
	// predicate built for another type is caught. Failing here means it is
	// caught at construction, before a single request has run through the
	// Manager believing its results were being filtered.
	if o.recordable != nil {
		recordable, ok := o.recordable.(func(*T) bool)
		if !ok {
			return nil, platformerrors.Wrapf(
				ErrRecordableTypeMismatch, "predicate is %T, want func(*%T) bool", o.recordable, *new(T),
			)
		}

		m.recordable = recordable
	}

	if m.ttl <= 0 || m.inFlightTTL <= 0 {
		return nil, ErrInvalidTTL
	}

	m.o11y = observability.NewObserver(serviceName, o.logger, m.tracerProvider)

	mp := metrics.EnsureMetricsProvider(m.metricsProvider)

	var err error
	if m.requestCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_requests", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating requests counter")
	}
	if m.claimLostCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_claims_lost", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating claims lost counter")
	}
	if m.recordFailureCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_record_failures", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating record failures counter")
	}
	if m.storeErrorCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_store_errors", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating store errors counter")
	}
	if m.staleRecordCounter, err = mp.NewInt64Counter(fmt.Sprintf("%s_stale_records", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating stale records counter")
	}
	if m.latencyHist, err = mp.NewFloat64Histogram(fmt.Sprintf("%s_latency_ms", serviceName)); err != nil {
		return nil, platformerrors.Wrap(err, "creating latency histogram")
	}

	return m, nil
}

// Do runs fn at most once for key.
//
// The fingerprint identifies the request the key is being used for. A stored
// record whose fingerprint differs yields ErrFingerprintMismatch rather than a
// replay, which is what stops one key from silently answering two different
// requests.
//
// fn runs outside the lock. Only the claim is serialized, so the lock is held
// for two store round trips regardless of how long the work takes — see the
// package documentation on why that matters.
//
// An error from fn is returned as-is and nothing is recorded, so the next
// attempt runs the work again. A panic does the same and keeps unwinding.
func (m *Manager[T]) Do(
	ctx context.Context,
	key Key,
	fingerprint Fingerprint,
	fn func(ctx context.Context) (*T, error),
	opts ...DoOption,
) (*Result[T], error) {
	ctx, op := m.o11y.Begin(ctx)
	defer op.End()

	defer op.Time(ctx, m.clock, m.latencyHist)()

	o := newDoOptions(opts)

	ttl := m.ttl
	if o.ttl != nil {
		ttl = *o.ttl
	}

	if err := ValidateKey(key, m.maxKeyLength); err != nil {
		return nil, op.Error(err, "validating idempotency key")
	}
	if fingerprint == "" {
		return nil, op.Error(ErrEmptyFingerprint, "checking idempotency fingerprint")
	}
	if fn == nil {
		return nil, op.Error(ErrNilFunc, "checking idempotency func")
	}

	// Converted rather than passed through: the span attacher's type switch
	// matches string exactly, so a named string type would miss it.
	op.Set(keyKey, string(key)).Set(fingerprintKey, string(fingerprint))

	storeKey := m.storeKey(key)

	// Read before locking. The overwhelmingly common case is a replay of a
	// completed record, and it costs one round trip with no coordination.
	record, found, err := m.load(ctx, op, storeKey)
	if err != nil {
		return nil, op.Error(err, "loading idempotency record")
	}
	if found {
		return m.replay(ctx, op, record, fingerprint)
	}

	claimID, existing, err := m.claim(ctx, op, key, storeKey, fingerprint)
	if err != nil {
		return nil, op.Error(err, "claiming idempotency key")
	}
	if existing != nil {
		// Someone landed a record between the read above and the lock.
		return m.replay(ctx, op, existing, fingerprint)
	}

	value, err := m.invoke(ctx, op, key, storeKey, claimID, fn)
	if err != nil {
		m.release(ctx, op, key, storeKey, claimID)

		return nil, err
	}

	if !m.recordable(value) {
		op.Set(recordedKey, false)
		m.release(ctx, op, key, storeKey, claimID)
		m.count(ctx, outcomeExecuted)

		return &Result[T]{Value: value}, nil
	}

	m.commit(ctx, op, key, storeKey, claimID, fingerprint, value, ttl)
	m.count(ctx, outcomeExecuted)

	return &Result[T]{Value: value}, nil
}

// replay turns a stored record into an answer: a mismatched fingerprint is
// reported, a completed record is returned, and a claim someone else holds is
// refused.
//
// The fingerprint is checked before the state, deliberately. A client reusing
// one key for two different requests has a bug worth surfacing immediately;
// answering ErrInFlight instead would tell it to retry, which is the one thing
// that cannot help.
func (m *Manager[T]) replay(
	ctx context.Context,
	op observability.Operation,
	record *Record[T],
	fingerprint Fingerprint,
) (*Result[T], error) {
	if record.Fingerprint != fingerprint {
		m.count(ctx, outcomeMismatch)

		return nil, op.Error(ErrFingerprintMismatch, "matching idempotency fingerprint")
	}

	switch record.State {
	case StateCompleted:
		op.Set(replayedKey, true)
		m.count(ctx, outcomeReplayed)

		return &Result[T]{Value: record.Value, Replayed: true}, nil
	case StateInFlight:
		m.count(ctx, outcomeInFlight)

		return nil, op.Error(ErrInFlight, "checking idempotency claim")
	default:
		// A state this binary does not know is treated the same as a shape it
		// cannot read: refuse rather than guess, since the alternative is
		// running work that may already have run.
		m.staleRecordCounter.Add(ctx, 1)

		return nil, op.Error(ErrInFlight, "reading idempotency record state")
	}
}

// claim writes the in-flight record under the lock, returning either the
// ClaimID it took or the record that made claiming unnecessary.
//
// The lock covers a re-read and a write and nothing else. The re-read is what
// makes it correct: two callers that both missed the pre-lock read would
// otherwise both claim, and the second would overwrite the first.
func (m *Manager[T]) claim(
	ctx context.Context,
	op observability.Operation,
	key Key,
	storeKey string,
	fingerprint Fingerprint,
) (claimID string, existing *Record[T], err error) {
	lockErr := m.locker.WithLock(ctx, m.lockKey(key), func(ctx context.Context) error {
		record, found, loadErr := m.load(ctx, op, storeKey)
		if loadErr != nil {
			return loadErr
		}
		if found {
			existing = record

			return nil
		}

		id := identifiers.New()
		if setErr := m.store.Set(ctx, storeKey, &Record[T]{
			CreatedAt:   m.clock.Now().UTC(),
			Fingerprint: fingerprint,
			ClaimID:     id,
			Version:     recordVersion,
			State:       StateInFlight,
		}, cache.WithExpiry(m.inFlightTTL)); setErr != nil {
			return setErr
		}

		claimID = id

		return nil
	})
	if lockErr != nil {
		return "", nil, lockErr
	}

	return claimID, existing, nil
}

// invoke runs fn, releasing the claim if fn panics.
//
// The deferred release deliberately does not recover: a panic belongs to
// whatever recovery the caller has installed, and swallowing it here would
// turn a crash into a silent wrong answer. All this does is make sure the
// claim does not outlive the work on the way past.
func (m *Manager[T]) invoke(
	ctx context.Context,
	op observability.Operation,
	key Key,
	storeKey, claimID string,
	fn func(ctx context.Context) (*T, error),
) (value *T, err error) {
	returned := false
	defer func() {
		if !returned {
			m.release(ctx, op, key, storeKey, claimID)
		}
	}()

	value, err = fn(ctx)
	returned = true

	return value, err
}

// commit records a finished result, but only if the claim is still ours.
//
// A failure here is counted and logged, never returned: the work already
// happened and the caller is entitled to its result. What the caller loses is
// the replay, so the next attempt runs the work again — which is exactly what
// idempotency_record_failures is for.
func (m *Manager[T]) commit(
	ctx context.Context,
	op observability.Operation,
	key Key,
	storeKey, claimID string,
	fingerprint Fingerprint,
	value *T,
	ttl time.Duration,
) {
	// Under the same lock the claim was taken with. "Only its owner may complete
	// a claim" is a read followed by a write, and unlocked those two steps race
	// the expiry of our own InFlightTTL: the check can pass, the claim can lapse
	// and be retaken by another caller, and the write then lands on top of the
	// new owner's record — handing them our result for work they are still doing.
	if lockErr := m.locker.WithLock(ctx, m.lockKey(key), func(ctx context.Context) error {
		if !m.stillOurs(ctx, op, storeKey, claimID, "completing") {
			return nil
		}

		if err := m.store.Set(ctx, storeKey, &Record[T]{
			CreatedAt:   m.clock.Now().UTC(),
			Value:       value,
			Fingerprint: fingerprint,
			ClaimID:     claimID,
			Version:     recordVersion,
			State:       StateCompleted,
		}, cache.WithExpiry(ttl)); err != nil {
			return err
		}

		op.Set(recordedKey, true)

		return nil
	}); lockErr != nil {
		m.recordFailureCounter.Add(ctx, 1)
		op.Acknowledge(lockErr, "recording idempotency result")
	}
}

// release drops our claim so the next attempt can run the work again.
//
// Best-effort by design: if it fails, the claim simply expires on its own
// InFlightTTL and callers see ErrInFlight until then. Surfacing the failure
// would replace a delay with an error for work that already completed.
func (m *Manager[T]) release(ctx context.Context, op observability.Operation, key Key, storeKey, claimID string) {
	// Under the same lock as commit, and for the same reason: an unlocked
	// check-then-delete can delete a claim that stopped being ours between the
	// two steps, which lets a second caller's in-flight work be re-entered.
	if lockErr := m.locker.WithLock(ctx, m.lockKey(key), func(ctx context.Context) error {
		if !m.stillOurs(ctx, op, storeKey, claimID, "releasing") {
			return nil
		}

		return m.store.Delete(ctx, storeKey)
	}); lockErr != nil {
		op.Acknowledge(lockErr, "releasing idempotency claim")
	}
}

// stillOurs reports whether the stored record is the claim this execution took.
//
// It is false when the work outran InFlightTTL and someone else re-claimed the
// key — the one remaining path to a duplicate effect, and the reason
// idempotency_claims_lost is the counter to alert on. Writing through it would
// compound the problem by handing the new owner our result.
func (m *Manager[T]) stillOurs(
	ctx context.Context,
	op observability.Operation,
	storeKey, claimID, action string,
) bool {
	record, found, err := m.load(ctx, op, storeKey)
	if err != nil {
		m.recordFailureCounter.Add(ctx, 1)
		op.Acknowledge(err, "reading idempotency claim before %s", action)

		return false
	}

	if !found || record.ClaimID != claimID {
		m.claimLostCounter.Add(ctx, 1)
		op.Logger().WithValues(map[string]any{
			claimIDKey: claimID,
			actionKey:  action,
		}).Error("idempotency claim lost before it could be completed; the work may run again", ErrInFlight)

		return false
	}

	return true
}

// load reads a record, reporting whether one usable to this binary was found.
//
// A record written by a different shape of this package reads as absent rather
// than as an error: with a day-long TTL, failing on it would turn one bad
// deploy into a day of failures. A decode failure is indistinguishable from a
// transport failure through the cache interface, so it goes through the store
// failure policy instead.
func (m *Manager[T]) load(
	ctx context.Context,
	op observability.Operation,
	storeKey string,
) (record *Record[T], found bool, err error) {
	record, err = m.store.Get(ctx, storeKey)
	switch {
	case err == nil:
	case stderrors.Is(err, cache.ErrNotFound):
		return nil, false, nil
	default:
		m.storeErrorCounter.Add(ctx, 1)

		if m.storeFailurePolicy == FailOpen {
			op.Acknowledge(err, "reading idempotency record, failing open")

			return nil, false, nil
		}

		// Joined rather than wrapped by message: the caller gets
		// ErrStoreUnavailable to branch on, and errors.Is/errors.As still reach
		// whatever the store actually returned — a cache sentinel, a context
		// deadline, a driver error. Wrapping err.Error() put all of that in a
		// string, where the only thing a caller could do with it was print it,
		// which is a poor trade on the one path that exists to be branched on.
		return nil, false, platformerrors.Wrap(
			platformerrors.Join(ErrStoreUnavailable, err),
			"reading idempotency record",
		)
	}

	if record == nil {
		return nil, false, nil
	}

	if record.Version != recordVersion {
		m.staleRecordCounter.Add(ctx, 1)
		op.Logger().
			WithValue(recordVersionKey, record.Version).
			Debug("ignoring idempotency record written by a different record version")

		return nil, false, nil
	}

	return record, true, nil
}

// count records one resolved request against its outcome.
func (m *Manager[T]) count(ctx context.Context, outcome string) {
	m.requestCounter.Add(ctx, 1, metric.WithAttributes(attribute.String(outcomeKey, outcome)))
}

// storeKey namespaces a caller's key for the record store.
func (m *Manager[T]) storeKey(key Key) string {
	return m.keyPrefix + string(key)
}

// lockKey namespaces a caller's key for the locker. It is deliberately
// distinct from the store key: the two live in different systems, and a shared
// spelling invites the assumption that one can be derived from the other.
func (m *Manager[T]) lockKey(key Key) string {
	return m.keyPrefix + "lock:" + string(key)
}

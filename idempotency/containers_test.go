package idempotency

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/cache"
	cacheredis "github.com/primandproper/platform-go/v13/cache/redis"
	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/postgres"
	"github.com/primandproper/platform-go/v13/distributedlock"
	dlpostgres "github.com/primandproper/platform-go/v13/distributedlock/postgres"
	dlredis "github.com/primandproper/platform-go/v13/distributedlock/redis"
	"github.com/primandproper/platform-go/v13/testutils/containers/pgtest"
	"github.com/primandproper/platform-go/v13/testutils/containers/redistest"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// wireShaped mirrors what a transport adapter actually stores: a status, a
// header map, and a byte body. Only a real serializing provider proves those
// survive a round trip — the memory cache hands back the same pointer and
// would pass whatever the shape.
type wireShaped struct {
	CreatedAt  time.Time
	Header     http.Header
	Body       []byte
	StatusCode int
	Truncated  bool
}

// testClientConfig is the minimum database.ClientConfig a postgres client
// needs.
type testClientConfig struct {
	connectionString string
}

var _ database.ClientConfig = (*testClientConfig)(nil)

func (c *testClientConfig) GetReadConnectionString() string   { return c.connectionString }
func (c *testClientConfig) GetWriteConnectionString() string  { return c.connectionString }
func (c *testClientConfig) GetMaxPingAttempts() uint64        { return 5 }
func (c *testClientConfig) GetPingWaitPeriod() time.Duration  { return time.Second }
func (c *testClientConfig) GetMaxIdleConns() int              { return 4 }
func (c *testClientConfig) GetMaxOpenConns() int              { return 16 }
func (c *testClientConfig) GetConnMaxLifetime() time.Duration { return time.Minute }

// newRedisManager builds a manager backed entirely by one redis instance, and
// a fresh key prefix so subtests sharing the container do not share records.
func newRedisManager(
	tb testing.TB,
	address, prefix string,
	opts ...Option,
) *Manager[wireShaped] {
	tb.Helper()

	store, err := cacheredis.NewRedisCache[Record[wireShaped]](
		&cacheredis.Config{Addresses: []string{address}, Namespace: prefix},
		time.Hour,
		nil,
	)
	must.NoError(tb, err)

	locker, err := dlredis.NewRedisLocker(&dlredis.Config{Addresses: []string{address}, KeyPrefix: prefix + "lock:"}, nil)
	must.NoError(tb, err)

	scoped, err := distributedlock.NewScopedLocker(locker)
	must.NoError(tb, err)

	m, err := NewManager(store, scoped, opts...)
	must.NoError(tb, err)

	return m
}

// TestIdempotency_Redis runs the behavior matrix against real redis, which is
// the only place the gob round trip of Record[T] is actually exercised.
func TestIdempotency_Redis(T *testing.T) {
	T.Parallel()

	container := redistest.Start(T)

	address, setupErr := container.ConnectionString(T.Context())
	must.NoError(T, setupErr)
	address = strings.TrimPrefix(address, "redis://")

	T.Run("round-trips a wire-shaped record", func(t *testing.T) {
		t.Parallel()

		m := newRedisManager(t, address, "roundtrip:")
		ctx := t.Context()

		value := &wireShaped{
			Header:     http.Header{"Content-Type": []string{"application/json"}, "X-Multi": []string{"a", "b"}},
			Body:       []byte(`{"id":"ch_1"}`),
			CreatedAt:  time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC),
			StatusCode: http.StatusCreated,
		}

		first, err := m.Do(ctx, testKey, testFingerprint, func(context.Context) (*wireShaped, error) {
			return value, nil
		})
		must.NoError(t, err)
		test.False(t, first.Replayed)

		second, err := m.Do(ctx, testKey, testFingerprint, func(context.Context) (*wireShaped, error) {
			t.Error("handler ran on a replay")

			return nil, nil
		})
		must.NoError(t, err)
		must.True(t, second.Replayed)

		// Every field has to survive gob, not just the scalars.
		got := second.Value
		test.EqOp(t, http.StatusCreated, got.StatusCode)
		test.EqOp(t, `{"id":"ch_1"}`, string(got.Body))
		test.EqOp(t, "application/json", got.Header.Get("Content-Type"))
		test.SliceLen(t, 2, got.Header.Values("X-Multi"))
		test.True(t, got.CreatedAt.Equal(value.CreatedAt))
		test.False(t, got.Truncated)
	})

	T.Run("reports a mismatched fingerprint", func(t *testing.T) {
		t.Parallel()

		m := newRedisManager(t, address, "mismatch:")
		ctx := t.Context()

		_, err := m.Do(ctx, testKey, testFingerprint, func(context.Context) (*wireShaped, error) {
			return &wireShaped{StatusCode: http.StatusCreated}, nil
		})
		must.NoError(t, err)

		_, err = m.Do(ctx, testKey, "different", func(context.Context) (*wireShaped, error) {
			return &wireShaped{}, nil
		})
		test.ErrorIs(t, err, ErrFingerprintMismatch)
	})

	T.Run("a completed record expires on its TTL", func(t *testing.T) {
		t.Parallel()

		m := newRedisManager(t, address, "expiry:", WithTTL(time.Second))
		ctx := t.Context()

		var calls atomic.Int64
		work := func(context.Context) (*wireShaped, error) {
			calls.Add(1)

			return &wireShaped{StatusCode: http.StatusCreated}, nil
		}

		_, err := m.Do(ctx, testKey, testFingerprint, work)
		must.NoError(t, err)

		// Real redis expiry, not a fake clock: the record has to actually go
		// away in the store, which is the thing being tested.
		time.Sleep(2 * time.Second)

		result, err := m.Do(ctx, testKey, testFingerprint, work)
		must.NoError(t, err)
		test.False(t, result.Replayed)
		test.EqOp(t, int64(2), calls.Load())
	})

	// The closest in-process stand-in for two replicas: two managers that share
	// nothing but redis, racing on one key.
	T.Run("two independent managers run the work once", func(t *testing.T) {
		t.Parallel()

		const managers = 8

		ctx := t.Context()

		var (
			executions atomic.Int64
			replays    atomic.Int64
			conflicts  atomic.Int64
			wg         sync.WaitGroup
			release    = make(chan struct{})
		)

		wg.Add(managers)
		for range managers {
			// Each gets its own store and locker handle, as separate processes
			// would.
			m := newRedisManager(t, address, "replicas:")

			go func() {
				defer wg.Done()

				<-release

				result, err := m.Do(ctx, testKey, testFingerprint, func(context.Context) (*wireShaped, error) {
					executions.Add(1)
					time.Sleep(50 * time.Millisecond)

					return &wireShaped{StatusCode: http.StatusCreated}, nil
				})
				switch {
				case err == nil && result.Replayed:
					replays.Add(1)
				case err == nil:
				default:
					test.ErrorIs(t, err, ErrInFlight)
					conflicts.Add(1)
				}
			}()
		}

		close(release)
		wg.Wait()

		test.EqOp(t, int64(1), executions.Load())
		test.EqOp(t, int64(managers-1), replays.Load()+conflicts.Load())
	})
}

// TestIdempotency_PostgresLock pairs redis records with the native postgres
// scoped locker.
//
// That locker runs its callback inside a database transaction, which is the
// reason the claim is the only thing under the lock. This test is where that
// holds up or does not: if the work were inside it, every in-flight request
// would pin a connection, and the pool would be the first thing to break.
func TestIdempotency_PostgresLock(T *testing.T) {
	T.Parallel()

	redisContainer := redistest.Start(T)

	address, setupErr := redisContainer.ConnectionString(T.Context())
	must.NoError(T, setupErr)
	address = strings.TrimPrefix(address, "redis://")

	pgtest.Run(T, func(ctx context.Context, pg *pgtest.Instance) {
		client, clientErr := postgres.NewDatabaseClient(ctx, &testClientConfig{connectionString: pg.ConnectionString})
		must.NoError(T, clientErr)
		T.Cleanup(func() { _ = client.Close() })

		scoped, lockErr := dlpostgres.NewPostgresScopedLocker(&dlpostgres.Config{}, client, nil)
		must.NoError(T, lockErr)

		store, storeErr := cacheredis.NewRedisCache[Record[wireShaped]](
			&cacheredis.Config{Addresses: []string{address}, Namespace: "pglock:"},
			time.Hour,
			nil,
		)
		must.NoError(T, storeErr)

		m, managerErr := NewManager(store, scoped)
		must.NoError(T, managerErr)

		var executions atomic.Int64

		// More concurrent callers than the pool has connections. Under a
		// held-lock design this deadlocks; under a short one each caller holds
		// a connection for two cache round trips and the pool never saturates.
		const callers = 32

		var (
			wg      sync.WaitGroup
			release = make(chan struct{})
		)

		wg.Add(callers)
		for range callers {
			go func() {
				defer wg.Done()

				<-release

				_, doErr := m.Do(ctx, testKey, testFingerprint, func(context.Context) (*wireShaped, error) {
					executions.Add(1)
					// Work that comfortably outlasts the critical section.
					time.Sleep(100 * time.Millisecond)

					return &wireShaped{StatusCode: http.StatusCreated}, nil
				})
				if doErr != nil {
					test.ErrorIs(T, doErr, ErrInFlight)
				}
			}()
		}

		close(release)
		wg.Wait()

		test.EqOp(T, int64(1), executions.Load())

		// The pool is still usable, which it would not be if the work had run
		// inside the lock's transaction: 32 callers holding connections for
		// 100ms apiece against a pool of 8 would have starved it.
		var one int
		must.NoError(T, client.Reader().QueryRowContext(ctx, "SELECT 1").Scan(&one))
		test.EqOp(T, 1, one)
	}, pgtest.WithMaxOpenConns(8))
}

// TestIdempotency_RedisExpiry_InFlight covers the claim expiring on its own
// TTL, which is what unblocks a key after a process dies mid-execution.
func TestIdempotency_RedisExpiry_InFlight(T *testing.T) {
	T.Parallel()

	container := redistest.Start(T)

	address, setupErr := container.ConnectionString(T.Context())
	must.NoError(T, setupErr)
	address = strings.TrimPrefix(address, "redis://")

	T.Run("an abandoned claim expires and the work runs again", func(t *testing.T) {
		t.Parallel()

		m := newRedisManager(t, address, "abandoned:", WithInFlightTTL(time.Second))
		ctx := t.Context()

		// Stand in for a process killed mid-execution: the claim is written
		// and never completed.
		must.NoError(t, m.store.Set(ctx, m.storeKey(testKey), &Record[wireShaped]{
			CreatedAt:   time.Now().UTC(),
			Fingerprint: testFingerprint,
			ClaimID:     "dead-process",
			Version:     recordVersion,
			State:       StateInFlight,
		}, cache.WithExpiry(time.Second)))

		_, err := m.Do(ctx, testKey, testFingerprint, func(context.Context) (*wireShaped, error) {
			return &wireShaped{}, nil
		})
		test.ErrorIs(t, err, ErrInFlight)

		time.Sleep(2 * time.Second)

		result, err := m.Do(ctx, testKey, testFingerprint, func(context.Context) (*wireShaped, error) {
			return &wireShaped{StatusCode: http.StatusCreated}, nil
		})
		must.NoError(t, err)
		test.False(t, result.Replayed)
	})
}

package workqueue

import (
	"strings"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/workqueue/internal/workqueuedb"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	databasemock "github.com/primandproper/primitives-go/v2/database/mock"
	"github.com/primandproper/primitives-go/v2/observability/logging"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// clientFor is a database.Client that reports one dialect and nothing else.
// New reads the dialect off it and never touches the pools, so this is the whole
// dependency for every construction test below.
func clientFor(d dialect.Dialect) database.Client {
	return &databasemock.ClientMock{
		DialectFunc: func() dialect.Dialect { return d },
	}
}

func TestNew(T *testing.T) {
	T.Parallel()

	T.Run("builds a queue over a postgres client", func(t *testing.T) {
		t.Parallel()

		q, err := New[string](t.Context(), validConfig(), clientFor(dialect.Postgres))
		must.NoError(t, err)
		must.NotNil(t, q)
		t.Cleanup(func() { _ = q.Close(t.Context()) })

		test.EqOp(t, "jobs", q.Name())
	})

	T.Run("rejects a nil config", func(t *testing.T) {
		t.Parallel()

		_, err := New[string](t.Context(), nil, clientFor(dialect.Postgres))
		test.ErrorIs(t, err, ErrNilConfig)
	})

	T.Run("rejects a nil client", func(t *testing.T) {
		t.Parallel()

		_, err := New[string](t.Context(), validConfig(), nil)
		test.ErrorIs(t, err, ErrNilDatabaseClient)
	})

	// Degrading to a lease-only claim on a dialect without SKIP LOCKED would
	// look like it worked while quietly handing the same item to every worker,
	// so the dialects this package has no SQL for are refused outright.
	T.Run("refuses any dialect but postgres", func(t *testing.T) {
		t.Parallel()

		for _, d := range []dialect.Dialect{dialect.MySQL, dialect.SQLite, dialect.Dialect("oracle")} {
			_, err := New[string](t.Context(), validConfig(), clientFor(d))
			test.ErrorIs(t, err, dialect.ErrUnsupported, test.Sprintf("dialect %q", d))
		}
	})

	// One table holds every queue in the database, so an unnamed one would
	// quietly share rows with every other unnamed one.
	T.Run("requires a queue name", func(t *testing.T) {
		t.Parallel()

		_, err := New[string](t.Context(), &Config{}, clientFor(dialect.Postgres))
		test.ErrorIs(t, err, ErrEmptyQueueName)
	})

	T.Run("rejects a table prefix that is not an identifier", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.TablePrefix = "not a prefix; DROP TABLE x"

		_, err := New[string](t.Context(), cfg, clientFor(dialect.Postgres))
		test.ErrorIs(t, err, dialect.ErrInvalidIdentifier)
	})

	// EnsureDefaults rescues every unset knob, so what reaches validation is a
	// value a caller set deliberately and got wrong.
	T.Run("surfaces a config validation failure", func(t *testing.T) {
		t.Parallel()

		cfg := validConfig()
		cfg.Name = strings.Repeat("q", MaxKeyLength+1)

		_, err := New[string](t.Context(), cfg, clientFor(dialect.Postgres))
		test.Error(t, err)
	})

	T.Run("accepts every observability option", func(t *testing.T) {
		t.Parallel()

		q, err := New[string](t.Context(), validConfig(), clientFor(dialect.Postgres),
			WithLogger(logging.EnsureLogger(nil)),
			WithTracerProvider(nil),
			WithMetricsProvider(nil),
			nil,
		)
		must.NoError(t, err)
		t.Cleanup(func() { _ = q.Close(t.Context()) })
	})

	T.Run("takes a matching key codec", func(t *testing.T) {
		t.Parallel()

		q, err := New[string](t.Context(), validConfig(), clientFor(dialect.Postgres),
			WithKeyCodec[string](upperCodec{}))
		must.NoError(t, err)
		t.Cleanup(func() { _ = q.Close(t.Context()) })

		encoded, err := q.codec.EncodeKey("abc")
		must.NoError(t, err)
		test.EqOp(t, "ABC", encoded)
	})

	// Option carries no type parameter, so the compiler cannot catch a codec
	// built for another key type. Catching it at construction means it is caught
	// before a single key has been written under a rendering nothing will
	// decode.
	T.Run("reports a key codec for the wrong type", func(t *testing.T) {
		t.Parallel()

		_, err := New[string](t.Context(), validConfig(), clientFor(dialect.Postgres),
			WithKeyCodec[pairKey](DefaultKeyCodec[pairKey]()))
		test.ErrorIs(t, err, ErrKeyCodecTypeMismatch)
	})

	T.Run("defaults the codec when none is supplied", func(t *testing.T) {
		t.Parallel()

		q, err := New[orderID](t.Context(), validConfig(), clientFor(dialect.Postgres))
		must.NoError(t, err)
		t.Cleanup(func() { _ = q.Close(t.Context()) })

		encoded, err := q.codec.EncodeKey(orderID("plain"))
		must.NoError(t, err)
		test.EqOp(t, "plain", encoded)
	})
}

func TestQueue_Close(T *testing.T) {
	T.Parallel()

	T.Run("is safe to call more than once", func(t *testing.T) {
		t.Parallel()

		q, err := New[string](t.Context(), validConfig(), clientFor(dialect.Postgres))
		must.NoError(t, err)

		must.NoError(t, q.Close(t.Context()))
		must.NoError(t, q.Close(t.Context()))
	})

	T.Run("refuses enqueues afterwards", func(t *testing.T) {
		t.Parallel()

		q, err := New[string](t.Context(), validConfig(), clientFor(dialect.Postgres))
		must.NoError(t, err)
		must.NoError(t, q.Close(t.Context()))

		test.ErrorIs(t, q.EnqueueKeys(t.Context(), "late"), ErrClosed)
	})
}

// The keyed writers all short-circuit an empty batch before touching the
// database, which is what lets a caller pass a filtered slice without checking
// it first. The mock client would panic if any of them reached it.
func TestQueue_EmptyBatchesTouchNothing(T *testing.T) {
	T.Parallel()

	q, err := New[string](T.Context(), validConfig(), clientFor(dialect.Postgres))
	must.NoError(T, err)
	T.Cleanup(func() { _ = q.Close(T.Context()) })

	T.Run("enqueue", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, q.Enqueue(t.Context()))
		test.NoError(t, q.EnqueueKeys(t.Context()))
	})

	T.Run("complete", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, q.Complete(t.Context()))
	})

	T.Run("release", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, q.Release(t.Context(), 0, nil))
	})

	T.Run("remove", func(t *testing.T) {
		t.Parallel()

		test.NoError(t, q.Remove(t.Context()))
	})

	T.Run("requeue", func(t *testing.T) {
		t.Parallel()

		revived, requeueErr := q.Requeue(t.Context())
		test.NoError(t, requeueErr)
		test.EqOp(t, int64(0), revived)
	})
}

func TestQueue_Claim_RejectsAnUnusableLease(T *testing.T) {
	T.Parallel()

	q, err := New[string](T.Context(), validConfig(), clientFor(dialect.Postgres))
	must.NoError(T, err)
	T.Cleanup(func() { _ = q.Close(T.Context()) })

	// A zero lease is handed out already expired, so every concurrent claimer
	// would take the same item — which is the one thing a lease exists to
	// prevent.
	for _, lease := range []time.Duration{0, -time.Second} {
		_, claimErr := q.Claim(T.Context(), 10, lease)
		test.ErrorIs(T, claimErr, ErrInvalidLease)
	}
}

// upperCodec is a deliberately non-default rendering, to prove WithKeyCodec is
// actually consulted.
type upperCodec struct{}

func (upperCodec) EncodeKey(key string) (string, error)     { return strings.ToUpper(key), nil }
func (upperCodec) DecodeKey(encoded string) (string, error) { return strings.ToLower(encoded), nil }

// The retrier is a struct field rather than a method, so an unwired one is a
// zero value that runs every write exactly once — retrying nothing, silently.
// This is the test that says it was wired, and wired to the configured ceiling.
func TestQueue_retrier(T *testing.T) {
	T.Parallel()

	T.Run("re-runs a deadlock up to the configured attempt ceiling", func(t *testing.T) {
		t.Parallel()

		queue, err := New[string](t.Context(), validConfig(), clientFor(dialect.Postgres))
		must.NoError(t, err)

		must.Greater(t, uint(1), queue.cfg.WriteAttempts)

		var calls int

		writeErr := queue.retrier.Do(t.Context(), "writing", func() error {
			calls++

			return &pgconn.PgError{Code: "40P01"}
		})

		must.Error(t, writeErr)
		test.EqOp(t, int(queue.cfg.WriteAttempts), calls)
	})
}

func TestQueue_Extend(T *testing.T) {
	T.Parallel()

	// A zero lease would be handed out already expired, which is not an
	// extension of anything.
	T.Run("rejects a non-positive lease", func(t *testing.T) {
		t.Parallel()

		q, stub := stubbedQueue(t)

		_, err := q.Extend(t.Context(), 0, Item[string]{Key: "a", LeasedBy: "claim"})
		test.ErrorIs(t, err, ErrInvalidLease)

		_, _, extended := stub.calls()
		test.SliceEmpty(t, extended)
	})

	// An empty batch is answered without a round trip, as every other batched
	// writer here answers it.
	T.Run("issues no statement for an empty batch", func(t *testing.T) {
		t.Parallel()

		q, stub := stubbedQueue(t)

		held, err := q.Extend(t.Context(), time.Minute)
		must.NoError(t, err)
		test.EqOp(t, int64(0), held)

		_, _, extended := stub.calls()
		test.SliceEmpty(t, extended)
	})

	// The key and the claim holding it, in primary-key order — the same lock
	// ordering and the same fence the completion and the hand-back bind.
	T.Run("binds the claim beside the key, in key order", func(t *testing.T) {
		t.Parallel()

		q, stub := stubbedQueue(t)

		held, err := q.Extend(t.Context(), 90*time.Second,
			Item[string]{Key: "c", LeasedBy: "second"},
			Item[string]{Key: "a", LeasedBy: "first"},
		)
		must.NoError(t, err)
		test.EqOp(t, int64(2), held)

		_, _, extended := stub.calls()
		must.SliceLen(t, 1, extended)
		test.Eq(t, []string{"a", "c"}, extended[0].ItemKeys)
		test.Eq(t, []string{"first", "second"}, extended[0].LeasedBys)
		test.EqOp(t, (90 * time.Second).Microseconds(), extended[0].LeaseMicroseconds)
	})

	// The count is what the statement matched, which is how a caller learns its
	// leases are shorter than its work: the difference is items somebody else is
	// already running.
	T.Run("reports how many it still holds", func(t *testing.T) {
		t.Parallel()

		q, stub := stubbedQueue(t)

		stub.extend = func(workqueuedb.ExtendItemsParams) (int64, error) { return 1, nil }

		held, err := q.Extend(t.Context(), time.Minute,
			Item[string]{Key: "a", LeasedBy: "mine"},
			Item[string]{Key: "b", LeasedBy: "lost"},
		)
		must.NoError(t, err)
		test.EqOp(t, int64(1), held)
	})
}

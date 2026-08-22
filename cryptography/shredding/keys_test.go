package shredding

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/cryptography/encryption"
	encryptionmock "github.com/primandproper/platform-go/v13/cryptography/encryption/mock"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewKeys(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil store", func(t *testing.T) {
		t.Parallel()

		keys, err := NewKeys(nil, newTestWrapper(t))
		test.Nil(t, keys)
		test.ErrorIs(t, err, ErrNilStore)
	})

	T.Run("refuses a nil key wrapper", func(t *testing.T) {
		t.Parallel()

		// The whole guarantee rests on the data key being unreadable without
		// the root key, so there is no degraded mode where wrapping is skipped.
		keys, err := NewKeys(newSQLiteEnv(t).newStore(t), nil)
		test.Nil(t, keys)
		test.ErrorIs(t, err, ErrNilKeyWrapper)
	})
}

func TestKeys_RoundTrip(T *testing.T) {
	T.Parallel()

	T.Run("encrypts and decrypts under a minted key", func(t *testing.T) {
		t.Parallel()

		keys, _ := newTestKeys(t, newStubClock())

		sealed, err := keys.Encrypt(t.Context(), testSubject, []byte("home address"), []byte("users.address:1"))
		must.NoError(t, err)
		test.NotEq(t, string(sealed), "home address")

		opened, err := keys.Decrypt(t.Context(), testSubject, sealed, []byte("users.address:1"))
		must.NoError(t, err)
		test.Eq(t, []byte("home address"), opened)
	})

	T.Run("refuses a ciphertext moved to another row", func(t *testing.T) {
		t.Parallel()

		keys, _ := newTestKeys(t, newStubClock())

		sealed, err := keys.Encrypt(t.Context(), testSubject, []byte("home address"), []byte("users.address:1"))
		must.NoError(t, err)

		opened, err := keys.Decrypt(t.Context(), testSubject, sealed, []byte("users.address:2"))
		test.Nil(t, opened)
		test.ErrorIs(t, err, encryption.ErrAuthenticationFailed)
	})

	T.Run("gives two subjects two keys", func(t *testing.T) {
		t.Parallel()

		keys, _ := newTestKeys(t, newStubClock())
		other := Subject{Type: "user", ID: "user-2"}

		sealed, err := keys.Encrypt(t.Context(), testSubject, []byte("home address"), nil)
		must.NoError(t, err)

		_, err = keys.Encrypt(t.Context(), other, []byte("theirs"), nil)
		must.NoError(t, err)

		// Not merely a different result: the other subject's key, which exists
		// and is perfectly usable, must not open this. Otherwise shredding one
		// subject would leave the other's ciphertext readable and vice versa.
		opened, err := keys.Decrypt(t.Context(), other, sealed, nil)
		test.Nil(t, opened)
		test.ErrorIs(t, err, encryption.ErrAuthenticationFailed)
	})

	T.Run("treats a type and an ID as one identity", func(t *testing.T) {
		t.Parallel()

		keys, _ := newTestKeys(t, newStubClock())

		user := Subject{Type: "user", ID: "shared-id"}
		account := Subject{Type: "account", ID: "shared-id"}

		sealed, err := keys.Encrypt(t.Context(), user, []byte("secret"), nil)
		must.NoError(t, err)

		_, err = keys.Encrypt(t.Context(), account, []byte("other secret"), nil)
		must.NoError(t, err)

		opened, err := keys.Decrypt(t.Context(), account, sealed, nil)
		test.Nil(t, opened)
		test.ErrorIs(t, err, encryption.ErrAuthenticationFailed)
	})

	T.Run("refuses a subject with no ID", func(t *testing.T) {
		t.Parallel()

		keys, _ := newTestKeys(t, newStubClock())

		_, err := keys.Encrypt(t.Context(), Subject{Type: "user"}, []byte("secret"), nil)
		test.ErrorIs(t, err, ErrEmptySubjectID)
	})

	T.Run("refuses to decrypt for a subject that never had a key", func(t *testing.T) {
		t.Parallel()

		keys, _ := newTestKeys(t, newStubClock())

		// Reading does not mint. A key minted here would open nothing, and the
		// caller would get ErrAuthenticationFailed for what is really a missing
		// row.
		opened, err := keys.Decrypt(t.Context(), testSubject, []byte("nonsense"), nil)
		test.Nil(t, opened)
		test.ErrorIs(t, err, ErrNoKey)
	})
}

func TestKeys_Shred(T *testing.T) {
	T.Parallel()

	T.Run("makes existing ciphertext permanently unreadable", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		keys, _ := newTestKeys(t, c)

		sealed, err := keys.Encrypt(t.Context(), testSubject, []byte("home address"), nil)
		must.NoError(t, err)

		receipt, err := keys.Shred(t.Context(), testSubject)
		must.NoError(t, err)
		test.True(t, receipt.Destroyed)
		test.EqOp(t, baseTime, receipt.ShreddedAt)
		test.EqOp(t, testSubject, receipt.Subject)

		opened, err := keys.Decrypt(t.Context(), testSubject, sealed, nil)
		test.Nil(t, opened)
		test.ErrorIs(t, err, ErrSubjectShredded)
	})

	T.Run("takes effect immediately rather than at the cache TTL", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		keys, _ := newTestKeys(t, c, WithKeyTTL(time.Hour))

		sealed, err := keys.Encrypt(t.Context(), testSubject, []byte("home address"), nil)
		must.NoError(t, err)

		// The key is warm in this process's cache: the encrypt just put it
		// there, and the TTL has an hour to run. The shred has to reach it
		// anyway, or erasure would complete an hour after the call.
		_, err = keys.Shred(t.Context(), testSubject)
		must.NoError(t, err)

		_, err = keys.Decrypt(t.Context(), testSubject, sealed, nil)
		test.ErrorIs(t, err, ErrSubjectShredded)
	})

	T.Run("refuses to mint a new key afterwards", func(t *testing.T) {
		t.Parallel()

		keys, _ := newTestKeys(t, newStubClock())

		_, err := keys.Shred(t.Context(), testSubject)
		must.NoError(t, err)

		// A system still writing about somebody it was told to forget is a bug.
		// Minting them a fresh key is how that bug goes unnoticed for a year.
		_, err = keys.Encrypt(t.Context(), testSubject, []byte("home address"), nil)
		test.ErrorIs(t, err, ErrSubjectShredded)
	})

	T.Run("is idempotent and reports the original destruction time", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()
		keys, _ := newTestKeys(t, c)

		_, err := keys.Encrypt(t.Context(), testSubject, []byte("home address"), nil)
		must.NoError(t, err)

		first, err := keys.Shred(t.Context(), testSubject)
		must.NoError(t, err)
		must.True(t, first.Destroyed)

		c.advance(time.Hour)

		// The caller is a retrying job, so a second shred must succeed — and it
		// must not claim to have destroyed something an hour later than it did.
		second, err := keys.Shred(t.Context(), testSubject)
		must.NoError(t, err)
		test.False(t, second.Destroyed)
		test.EqOp(t, first.ShreddedAt, second.ShreddedAt)
	})

	T.Run("forecloses a key for a subject that never had one", func(t *testing.T) {
		t.Parallel()

		keys, _ := newTestKeys(t, newStubClock())

		receipt, err := keys.Shred(t.Context(), testSubject)
		must.NoError(t, err)
		test.False(t, receipt.Destroyed)
		test.EqOp(t, baseTime, receipt.ShreddedAt)

		_, err = keys.Encrypt(t.Context(), testSubject, []byte("home address"), nil)
		test.ErrorIs(t, err, ErrSubjectShredded)
	})

	T.Run("shreds one subject without touching another", func(t *testing.T) {
		t.Parallel()

		keys, _ := newTestKeys(t, newStubClock())
		other := Subject{Type: "user", ID: "user-2"}

		mine, err := keys.Encrypt(t.Context(), testSubject, []byte("mine"), nil)
		must.NoError(t, err)

		theirs, err := keys.Encrypt(t.Context(), other, []byte("theirs"), nil)
		must.NoError(t, err)

		_, err = keys.Shred(t.Context(), testSubject)
		must.NoError(t, err)

		_, err = keys.Decrypt(t.Context(), testSubject, mine, nil)
		test.ErrorIs(t, err, ErrSubjectShredded)

		opened, err := keys.Decrypt(t.Context(), other, theirs, nil)
		must.NoError(t, err)
		test.Eq(t, []byte("theirs"), opened)
	})

	T.Run("refuses a subject with no ID", func(t *testing.T) {
		t.Parallel()

		keys, _ := newTestKeys(t, newStubClock())

		_, err := keys.Shred(t.Context(), Subject{Type: "user"})
		test.ErrorIs(t, err, ErrEmptySubjectID)
	})
}

func TestKeys_Cache(T *testing.T) {
	T.Parallel()

	T.Run("unwraps once for repeated reads", func(t *testing.T) {
		t.Parallel()

		wrapper, unwraps := countingWrapper(t)
		store := newSQLiteEnv(t).newStore(t)

		keys, err := NewKeys(store, wrapper, WithClock(newStubClock()))
		must.NoError(t, err)

		sealed, err := keys.Encrypt(t.Context(), testSubject, []byte("home address"), nil)
		must.NoError(t, err)

		for range 5 {
			_, decErr := keys.Decrypt(t.Context(), testSubject, sealed, nil)
			must.NoError(t, decErr)
		}

		// Zero, not one: the mint already had the plaintext key in hand and
		// cached it, so nothing has had to open the wrapped copy yet. A round
		// trip to a KMS per read is the cost this cache exists to avoid.
		test.EqOp(t, 0, *unwraps)
	})

	T.Run("expires a cached key at the TTL", func(t *testing.T) {
		t.Parallel()

		wrapper, unwraps := countingWrapper(t)
		c := newStubClock()

		keys, err := NewKeys(newSQLiteEnv(t).newStore(t), wrapper,
			WithClock(c), WithKeyTTL(time.Minute))
		must.NoError(t, err)

		sealed, err := keys.Encrypt(t.Context(), testSubject, []byte("home address"), nil)
		must.NoError(t, err)

		c.advance(time.Minute + time.Second)

		_, err = keys.Decrypt(t.Context(), testSubject, sealed, nil)
		must.NoError(t, err)

		// The TTL is the erasure guarantee, so it has to actually expire
		// something. One unwrap is the proof that the cached copy is gone.
		test.EqOp(t, 1, *unwraps)
	})

	T.Run("unwraps every time when the TTL is zero", func(t *testing.T) {
		t.Parallel()

		wrapper, unwraps := countingWrapper(t)

		keys, err := NewKeys(newSQLiteEnv(t).newStore(t), wrapper,
			WithClock(newStubClock()), WithKeyTTL(0))
		must.NoError(t, err)

		sealed, err := keys.Encrypt(t.Context(), testSubject, []byte("home address"), nil)
		must.NoError(t, err)

		for range 3 {
			_, decErr := keys.Decrypt(t.Context(), testSubject, sealed, nil)
			must.NoError(t, decErr)
		}

		test.EqOp(t, 3, *unwraps)
	})

	T.Run("stays under the configured cap", func(t *testing.T) {
		t.Parallel()

		c := newStubClock()

		held, err := NewKeys(newSQLiteEnv(t).newStore(t), newTestWrapper(t),
			WithClock(c), WithMaxCachedKeys(2))
		must.NoError(t, err)

		for i := range 10 {
			_, encErr := held.Encrypt(t.Context(),
				Subject{Type: "user", ID: string(rune('a' + i))}, []byte("secret"), nil)
			must.NoError(t, encErr)
		}

		// Every cached key is a key a shred cannot reach yet, so the cap is a
		// bound on how many subjects can be mid-erasure at once as much as it is
		// a bound on memory.
		test.LessEq(t, 2, held.cache.len())
	})

	T.Run("drops a key on an invalidation from elsewhere", func(t *testing.T) {
		t.Parallel()

		wrapper, unwraps := countingWrapper(t)
		meter := newCountingMeter()

		keys, err := NewKeys(newSQLiteEnv(t).newStore(t), wrapper,
			WithClock(newStubClock()), WithKeyTTL(time.Hour), WithMetricsProvider(meter))
		must.NoError(t, err)

		sealed, err := keys.Encrypt(t.Context(), testSubject, []byte("home address"), nil)
		must.NoError(t, err)

		keys.Invalidate(t.Context(), testSubject)

		_, err = keys.Decrypt(t.Context(), testSubject, sealed, nil)
		must.NoError(t, err)

		test.EqOp(t, 1, *unwraps)

		// Erasure finished on the broadcast rather than on the TTL, which is
		// the distinction the attribute carries and the only thing that says
		// the fleet-wide half is worth having.
		test.EqOp(t, int64(1), meter.count(serviceName+"_invalidations_applied"))
		test.EqOp(t, 1, meter.countWhere(serviceName+"_invalidations_applied", droppedKey, true))
	})

	T.Run("reports an invalidation that found nothing to drop", func(t *testing.T) {
		t.Parallel()

		meter := newCountingMeter()
		clk := newStubClock()

		keys, err := NewKeys(newSQLiteEnv(t).newStore(t), newTestWrapper(t),
			WithClock(clk), WithKeyTTL(time.Minute), WithMetricsProvider(meter))
		must.NoError(t, err)

		_, err = keys.Encrypt(t.Context(), testSubject, []byte("home address"), nil)
		must.NoError(t, err)

		// A broadcast that arrives after the key has expired is an ordinary
		// outcome, not a failure — but a fleet where every invalidation looks
		// like this one has a bus slower than the guarantee assumes, and the two
		// are only distinguishable here.
		clk.advance(2 * time.Minute)
		keys.Invalidate(t.Context(), testSubject)

		test.EqOp(t, 1, meter.countWhere(serviceName+"_invalidations_applied", droppedKey, false))
		test.EqOp(t, 0, meter.countWhere(serviceName+"_invalidations_applied", droppedKey, true))
	})
}

func TestKeys_Broadcast(T *testing.T) {
	T.Parallel()

	T.Run("announces a shred", func(t *testing.T) {
		t.Parallel()

		broadcaster := &recordingBroadcaster{}
		keys, _ := newTestKeys(t, newStubClock(), WithBroadcaster(broadcaster))

		_, err := keys.Shred(t.Context(), testSubject)
		must.NoError(t, err)

		test.SliceLen(t, 1, broadcaster.seen())
		test.EqOp(t, testSubject, broadcaster.seen()[0])
	})

	T.Run("does not fail a shred whose broadcast failed", func(t *testing.T) {
		t.Parallel()

		broadcaster := &recordingBroadcaster{err: errors.New("bus is down")}
		keys, store := newTestKeys(t, newStubClock(), WithBroadcaster(broadcaster))

		// The destruction has already happened by the time the broadcast is
		// attempted. Returning the error would fail an erasure that succeeded
		// and send the caller round again for a shred that is already done.
		receipt, err := keys.Shred(t.Context(), testSubject)
		must.NoError(t, err)
		test.EqOp(t, baseTime, receipt.ShreddedAt)

		record, err := store.Load(t.Context(), testSubject)
		must.NoError(t, err)
		test.True(t, record.Shredded())
	})
}

func TestKeys_Mint(T *testing.T) {
	T.Parallel()

	T.Run("yields to whoever minted first", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		wrapper := newTestWrapper(t)
		c := newStubClock()

		// Two Keys over one store, which is what two replicas are. Both cache
		// separately, so neither knows the other exists until the insert.
		first, err := NewKeys(store, wrapper, WithClock(c))
		must.NoError(t, err)

		second, err := NewKeys(store, wrapper, WithClock(c))
		must.NoError(t, err)

		sealed, err := first.Encrypt(t.Context(), testSubject, []byte("home address"), nil)
		must.NoError(t, err)

		// The second replica generates a key, loses the insert, and must use
		// the winner's — otherwise two live keys exist for one subject and a
		// shred destroys only one of them.
		alsoSealed, err := second.Encrypt(t.Context(), testSubject, []byte("other value"), nil)
		must.NoError(t, err)

		opened, err := second.Decrypt(t.Context(), testSubject, sealed, nil)
		must.NoError(t, err)
		test.Eq(t, []byte("home address"), opened)

		opened, err = first.Decrypt(t.Context(), testSubject, alsoSealed, nil)
		must.NoError(t, err)
		test.Eq(t, []byte("other value"), opened)
	})

	T.Run("binds a wrapped key to its subject", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)
		keys, err := NewKeys(store, newTestWrapper(t), WithClock(newStubClock()))
		must.NoError(t, err)

		_, err = keys.Encrypt(t.Context(), testSubject, []byte("home address"), nil)
		must.NoError(t, err)

		record, err := store.Load(t.Context(), testSubject)
		must.NoError(t, err)

		// Transplanting the wrapped key into another subject's row must not
		// hand that subject a working key. Without the associated data it
		// would, and the row would decrypt somebody else's columns.
		other := Subject{Type: "user", ID: "user-2"}
		inserted, err := store.Insert(t.Context(), &Record{
			Subject: other, Wrapped: record.Wrapped, CreatedAt: baseTime,
		})
		must.NoError(t, err)
		must.True(t, inserted)

		_, err = keys.Encrypt(t.Context(), other, []byte("secret"), nil)
		test.ErrorIs(t, err, encryption.ErrAuthenticationFailed)
	})
}

func TestKeys_Failures(T *testing.T) {
	T.Parallel()

	T.Run("reports a wrapper that cannot wrap", func(t *testing.T) {
		t.Parallel()

		sentinel := errors.New("kms unavailable")
		wrapper := &encryptionmock.KeyWrapperMock{
			WrapFunc: func(context.Context, []byte, []byte) ([]byte, error) { return nil, sentinel },
		}

		keys, err := NewKeys(newSQLiteEnv(t).newStore(t), wrapper, WithClock(newStubClock()))
		must.NoError(t, err)

		_, err = keys.Encrypt(t.Context(), testSubject, []byte("home address"), nil)
		test.ErrorIs(t, err, sentinel)
	})

	T.Run("reports a wrapper that cannot unwrap", func(t *testing.T) {
		t.Parallel()

		sentinel := errors.New("kms unavailable")
		store := newSQLiteEnv(t).newStore(t)

		inserted, err := store.Insert(t.Context(), &Record{
			Subject: testSubject, Wrapped: []byte("wrapped"), CreatedAt: baseTime,
		})
		must.NoError(t, err)
		must.True(t, inserted)

		wrapper := &encryptionmock.KeyWrapperMock{
			UnwrapFunc: func(context.Context, []byte, []byte) ([]byte, error) { return nil, sentinel },
		}

		keys, err := NewKeys(store, wrapper, WithClock(newStubClock()))
		must.NoError(t, err)

		_, err = keys.Decrypt(t.Context(), testSubject, []byte("ciphertext"), nil)
		test.ErrorIs(t, err, sentinel)
	})
}

// countingWrapper is the local wrapper with a tally of how many times something
// has had to open a wrapped key — which is the KMS bill in a real deployment,
// and the only way to observe the cache from outside.
func countingWrapper(t *testing.T) (wrapper encryption.KeyWrapper, unwraps *int) {
	t.Helper()

	underlying := newTestWrapper(t)
	unwraps = new(int)

	return &encryptionmock.KeyWrapperMock{
		WrapFunc: underlying.Wrap,
		UnwrapFunc: func(ctx context.Context, wrapped, associatedData []byte) ([]byte, error) {
			*unwraps++

			return underlying.Unwrap(ctx, wrapped, associatedData)
		},
	}, unwraps
}

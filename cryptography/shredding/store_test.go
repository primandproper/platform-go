package shredding

import (
	"testing"
	"time"

	"github.com/primandproper/platform-go/v13/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

func TestNewSQLStore(T *testing.T) {
	T.Parallel()

	T.Run("refuses a nil client", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(nil)
		test.Nil(t, store)
		test.ErrorIs(t, err, ErrNilDatabaseClient)
	})

	T.Run("refuses a prefix that would render an illegal identifier", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		store, err := NewSQLStore(env.client, WithTablePrefix("trailing_"))
		test.Nil(t, store)
		test.Error(t, err)
	})
}

func TestSQLStore(T *testing.T) {
	T.Parallel()

	runStoreSuite(T, newSQLiteEnv(T))
}

// runStoreSuite is the Store contract, run against whichever database the
// environment provides. SQLite runs it here; the container tests run the same
// suite against Postgres and MySQL, so a dialect difference in the
// insert-ignore clause or the guarded update fails in one place.
func runStoreSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	suiteInsertAndLoad(t, env)
	suiteShred(t, env)
}

func suiteInsertAndLoad(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("stores and reads a wrapped key", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		inserted, err := store.Insert(t.Context(), &Record{
			Subject: testSubject, Wrapped: []byte("wrapped"), CreatedAt: baseTime,
		})
		must.NoError(t, err)
		test.True(t, inserted)

		record, err := store.Load(t.Context(), testSubject)
		must.NoError(t, err)
		test.EqOp(t, testSubject, record.Subject)
		test.Eq(t, []byte("wrapped"), record.Wrapped)
		test.False(t, record.Shredded())
		test.EqOp(t, baseTime, record.CreatedAt.UTC())
	})

	t.Run("reports a subject with no row", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		record, err := store.Load(t.Context(), testSubject)
		test.Nil(t, record)
		test.ErrorIs(t, err, ErrNoKey)
	})

	t.Run("declines a second insert for one subject", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		first, err := store.Insert(t.Context(), &Record{
			Subject: testSubject, Wrapped: []byte("first"), CreatedAt: baseTime,
		})
		must.NoError(t, err)
		must.True(t, first)

		// Zero rows affected rather than a constraint violation, so the loser
		// of a mint race can react without parsing a driver error.
		second, err := store.Insert(t.Context(), &Record{
			Subject: testSubject, Wrapped: []byte("second"), CreatedAt: baseTime,
		})
		must.NoError(t, err)
		test.False(t, second)

		record, err := store.Load(t.Context(), testSubject)
		must.NoError(t, err)
		test.Eq(t, []byte("first"), record.Wrapped)
	})

	t.Run("keeps two subject types apart", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)
		account := Subject{Type: "account", ID: testSubject.ID}

		_, err := store.Insert(t.Context(), &Record{
			Subject: testSubject, Wrapped: []byte("user key"), CreatedAt: baseTime,
		})
		must.NoError(t, err)

		inserted, err := store.Insert(t.Context(), &Record{
			Subject: account, Wrapped: []byte("account key"), CreatedAt: baseTime,
		})
		must.NoError(t, err)
		test.True(t, inserted)

		record, err := store.Load(t.Context(), account)
		must.NoError(t, err)
		test.Eq(t, []byte("account key"), record.Wrapped)
	})

	t.Run("refuses a record with no key material", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		inserted, err := store.Insert(t.Context(), &Record{Subject: testSubject, CreatedAt: baseTime})
		test.False(t, inserted)
		test.ErrorIs(t, err, ErrKeyMaterialMissing)
	})

	t.Run("refuses a record with no subject", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		inserted, err := store.Insert(t.Context(), &Record{Wrapped: []byte("wrapped"), CreatedAt: baseTime})
		test.False(t, inserted)
		test.ErrorIs(t, err, ErrEmptySubjectID)
	})
}

func suiteShred(t *testing.T, env *storeEnv) {
	t.Helper()

	t.Run("destroys the key material and keeps the row", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := store.Insert(t.Context(), &Record{
			Subject: testSubject, Wrapped: []byte("wrapped"), CreatedAt: baseTime,
		})
		must.NoError(t, err)

		receipt, err := store.Shred(t.Context(), testSubject, baseTime)
		must.NoError(t, err)
		test.True(t, receipt.Destroyed)
		test.EqOp(t, baseTime, receipt.ShreddedAt)

		// The row survives so the destruction is a record rather than an
		// absence, and so a later read can tell "destroyed" from "never had
		// one".
		record, err := store.Load(t.Context(), testSubject)
		must.NoError(t, err)
		test.True(t, record.Shredded())
		test.SliceEmpty(t, record.Wrapped)
	})

	t.Run("writes a tombstone for a subject with no row", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		receipt, err := store.Shred(t.Context(), testSubject, baseTime)
		must.NoError(t, err)
		test.False(t, receipt.Destroyed)

		record, err := store.Load(t.Context(), testSubject)
		must.NoError(t, err)
		test.True(t, record.Shredded())
	})

	t.Run("refuses a mint after a tombstone", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := store.Shred(t.Context(), testSubject, baseTime)
		must.NoError(t, err)

		inserted, err := store.Insert(t.Context(), &Record{
			Subject: testSubject, Wrapped: []byte("wrapped"), CreatedAt: baseTime,
		})
		must.NoError(t, err)
		test.False(t, inserted)
	})

	t.Run("reports the first destruction on a second call", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := store.Insert(t.Context(), &Record{
			Subject: testSubject, Wrapped: []byte("wrapped"), CreatedAt: baseTime,
		})
		must.NoError(t, err)

		first, err := store.Shred(t.Context(), testSubject, baseTime)
		must.NoError(t, err)
		must.True(t, first.Destroyed)

		later := baseTime.Add(time.Hour)

		second, err := store.Shred(t.Context(), testSubject, later)
		must.NoError(t, err)
		test.False(t, second.Destroyed)
		test.EqOp(t, baseTime, second.ShreddedAt)
	})

	t.Run("refuses a subject with no ID", func(t *testing.T) {
		t.Parallel()

		store := env.newStore(t)

		_, err := store.Shred(t.Context(), Subject{Type: "user"}, baseTime)
		test.ErrorIs(t, err, ErrEmptySubjectID)
	})
}

func TestIgnorePrefix(T *testing.T) {
	T.Parallel()

	T.Run("renders each dialect's skip-a-duplicate clause", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "", ignorePrefix(dialect.Postgres))
		test.EqOp(t, "IGNORE ", ignorePrefix(dialect.MySQL))
		test.EqOp(t, "OR IGNORE ", ignorePrefix(dialect.SQLite))
	})
}

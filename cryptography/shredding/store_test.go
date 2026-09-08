package shredding

import (
	"database/sql"
	"testing"
	"time"

	"github.com/primandproper/platform-go/v14/cryptography/shredding/internal/shreddingdb"

	"github.com/primandproper/primitives-go/database/dialect"

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

	// The two columns describe one event, so they hold one instant rather than
	// two clock reads. It is the one thing this schema asks for that the
	// conventional tables do not — every other update in the module stamps
	// last_updated_at from the server's clock — so it is pinned here as well as
	// in the rendered SQL, where dropping it would be a one-line change.
	t.Run("stamps the destruction and the row's last update from one instant", func(t *testing.T) {
		t.Parallel()

		store, prefix := env.newPrefixedStore(t)

		_, err := store.Insert(t.Context(), &Record{
			Subject: testSubject, Wrapped: []byte("wrapped"), CreatedAt: baseTime,
		})
		must.NoError(t, err)

		receipt, err := store.Shred(t.Context(), testSubject, baseTime)
		must.NoError(t, err)
		must.True(t, receipt.Destroyed)

		var shreddedAt, lastUpdatedAt sql.NullTime

		must.NoError(t, env.client.Reader().QueryRowContext(t.Context(),
			"SELECT shredded_at, last_updated_at FROM "+keysTable(prefix)+
				" WHERE subject_type = "+env.dialect.Placeholder(1)+
				" AND subject_id = "+env.dialect.Placeholder(2),
			testSubject.Type, testSubject.ID,
		).Scan(&shreddedAt, &lastUpdatedAt))

		must.True(t, shreddedAt.Valid)
		must.True(t, lastUpdatedAt.Valid)

		// Compared to each other rather than to baseTime, because both come
		// back through the same driver and the same column type: what is under
		// test is that they agree, not what either engine does with a zone.
		test.EqOp(t, shreddedAt.Time.UTC(), lastUpdatedAt.Time.UTC())
	})
}

func TestShreddingDBDialect(T *testing.T) {
	T.Parallel()

	T.Run("maps every dialect this package serves", func(t *testing.T) {
		t.Parallel()

		want := map[dialect.Dialect]shreddingdb.Dialect{
			dialect.Postgres: shreddingdb.DialectPostgreSQL,
			dialect.MySQL:    shreddingdb.DialectMySQL,
			dialect.SQLite:   shreddingdb.DialectSQLite,
		}

		for _, d := range allDialects {
			got, err := shreddingdbDialect(d)
			must.NoError(t, err, must.Sprintf("dialect %q", d))
			test.EqOp(t, want[d], got, test.Sprintf("dialect %q", d))
		}
	})

	// Reachable only if this module learns a dialect the generated package was
	// not generated for, which is a construction failure that should name the
	// dialect rather than surfacing as an empty string reaching shreddingdb.New.
	T.Run("names a dialect the generated package does not carry", func(t *testing.T) {
		t.Parallel()

		got, err := shreddingdbDialect(dialect.Dialect("cockroach"))
		test.EqOp(t, shreddingdb.Dialect(""), got)
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})
}

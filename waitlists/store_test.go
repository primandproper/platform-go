package waitlists

import (
	"testing"

	"github.com/primandproper/primitives-go/v2/cryptography/hashing"
	"github.com/primandproper/primitives-go/v2/cryptography/hashing/sha512"
	"github.com/primandproper/primitives-go/v2/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestSQLStore runs the whole behavioral suite against SQLite, which is the
// dialect a developer has without a container. The same suite runs against
// Postgres and MySQL in containers_test.go.
func TestSQLStore(T *testing.T) {
	T.Parallel()

	runStoreSuite(T, newSQLiteEnv(T))
}

// runStoreSuite is every assertion this package makes against a live database.
// It takes the environment so that one suite serves SQLite and both containers.
func runStoreSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	runListSuite(t, env)
	runSignupSuite(t, env)
	runWithdrawalSuite(t, env)
	runTransactionSuite(t, env)
}

func TestNewSQLStore(T *testing.T) {
	T.Parallel()

	T.Run("nil client", func(t *testing.T) {
		t.Parallel()

		store, err := NewSQLStore(nil)
		test.Nil(t, store)
		test.ErrorIs(t, err, ErrNilDatabaseClient)
	})

	T.Run("illegal prefix", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		// A prefix ending in '_' is the one database/ddl refuses, because the
		// separator is the schema's to supply — the check runs against every
		// identifier the DDL renders rather than against a pattern.
		store, err := NewSQLStore(env.client, WithTablePrefix("trailing_"))
		test.Nil(t, store)
		test.Error(t, err)
	})

	T.Run("nil options are ignored", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		store, err := NewSQLStore(env.client, nil, WithTablePrefix("nilopt"))
		must.NoError(t, err)
		test.EqOp(t, "nilopt", store.TablePrefix())
	})

	T.Run("nil clock and hasher are ignored", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		// Both options refuse a nil rather than installing one: a store whose
		// clock is nil panics on the first read of a list, and one whose hasher
		// is nil panics on the first signup.
		store, err := NewSQLStore(env.client, WithClock(nil), WithHasher(nil))
		must.NoError(t, err)
		must.NotNil(t, store)

		test.NotEqOp(t, "", store.Digest("someone@example.com"))
		test.False(t, store.clock.Now().IsZero())
	})

	T.Run("the dialect comes from the client", func(t *testing.T) {
		t.Parallel()

		// The store cannot be built for a dialect the generated package was not
		// generated for, and the mapping is total over dialect.Valid — so the
		// only way to reach the refusal is to name a dialect this module does
		// not have.
		d, err := waitlistsdbDialect(dialect.SQLite)
		must.NoError(t, err)
		test.EqOp(t, "sqlite", string(d))

		_, err = waitlistsdbDialect(dialect.Dialect("cassandra"))
		test.Error(t, err)
	})
}

func TestSQLStore_Digest(T *testing.T) {
	T.Parallel()

	T.Run("is stable, one-way, and normalizing", func(t *testing.T) {
		t.Parallel()

		store := newSQLiteEnv(t).newStore(t)

		digest := store.Digest("Ada@Example.com")

		test.EqOp(t, digest, store.Digest("Ada@Example.com"))
		test.NotEqOp(t, "Ada@Example.com", digest)
		test.NotEqOp(t, digest, store.Digest("grace@example.com"))

		// The whole reason the column holds a digest of Normalize's output:
		// two capitalizations of one address are one person, and a suppression
		// that missed the second would not be a suppression.
		test.EqOp(t, digest, store.Digest("  ada@example.com  "))
	})

	T.Run("the hasher decides what the column holds", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		def := env.newStore(t)
		wide := env.newStore(t, WithHasher(hashing.Hasher(sha512.NewSHA512Hasher())))

		test.NotEqOp(t, def.Digest("ada@example.com"), wide.Digest("ada@example.com"))
	})
}

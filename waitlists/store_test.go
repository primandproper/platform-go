package waitlists

import (
	"testing"

	"github.com/primandproper/primitives-go/v2/cryptography/hashing"
	"github.com/primandproper/primitives-go/v2/cryptography/hashing/sha512"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"

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

// TestSQLStore_RefusedWritesAnswerWithNoRow is the claim the `refused` helper
// rests on, made once for all nine writes rather than at each of the fifty-odd
// places a refusal is asserted.
//
// Every write here answers with a row *and* an error, and the pairing is the
// part a caller has to be able to rely on: a row beside a non-nil error is a
// value that looks readable and describes a write that did not happen. The
// refusals below are each a different shape of miss — a nil argument, a wrong
// tenant, a guard that was not satisfied, a row that is not there — so what is
// pinned is the pairing rather than one path through it.
func TestSQLStore_RefusedWritesAnswerWithNoRow(T *testing.T) {
	T.Parallel()

	env := newSQLiteEnv(T)
	store := env.newStore(T)

	list := mustCreateList(T, env, store, testScope, openList("Launch"))
	signup := mustJoin(T, env, store, testScope, list.ID, &Signup{Contact: "ada@example.com"})

	T.Run("the catalog", func(t *testing.T) {
		t.Parallel()

		created, err := env.createList(t, store, testScope, nil)
		test.Nil(t, created)
		test.ErrorIs(t, err, ErrNilList)

		updated, err := env.updateList(t, store, otherScope, &List{ID: list.ID, Name: "x", ClosesAt: testNow})
		test.Nil(t, updated)
		test.ErrorIs(t, err, ErrListNotFound)

		archived, err := env.archiveList(t, store, otherScope, list.ID)
		test.Nil(t, archived)
		test.ErrorIs(t, err, ErrListNotFound)
	})

	T.Run("the queue", func(t *testing.T) {
		t.Parallel()

		joined, err := env.join(t, store, testScope, list.ID, nil)
		test.Nil(t, joined)
		test.ErrorIs(t, err, ErrNilSignup)

		noted, err := env.updateNotes(t, store, testScope, list.ID, "sg_nope", "x")
		test.Nil(t, noted)
		test.ErrorIs(t, err, ErrSignupNotFound)

		// The guard, rather than the row being absent: this signup is waiting,
		// so the conversion is the move that is out of order.
		converted, err := env.convert(t, store, testScope, list.ID, signup.ID)
		test.Nil(t, converted)
		test.ErrorIs(t, err, ErrWrongStatus)

		invited, err := env.invite(t, store, otherScope, list.ID, signup.ID)
		test.Nil(t, invited)
		test.ErrorIs(t, err, ErrSignupNotFound)

		// The withdrawal reads before it writes, so this is the one case where
		// a row was in hand when the refusal was decided. It is not handed back.
		withdrawn, err := env.withdraw(t, store, testScope, "wl_nope", signup.ID)
		test.Nil(t, withdrawn)
		test.ErrorIs(t, err, ErrSignupNotFound)

		retired, err := env.archiveSignup(t, store, testScope, list.ID, "")
		test.Nil(t, retired)
		test.ErrorIs(t, err, platformerrors.ErrInvalidIDProvided)
	})
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

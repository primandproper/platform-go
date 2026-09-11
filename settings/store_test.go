package settings

import (
	"testing"

	"github.com/primandproper/primitives-go/v2/database/dialect"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestSQLStore runs the whole behavioral suite against SQLite, which is the
// dialect a developer has without a container. The same suite runs against
// Postgres and MySQL in containers_test.go, because two of the three statements
// that differ beyond their placeholders — the upsert's conflict clause and the
// batched read's set membership — are the ones SQLite is least likely to catch.
func TestSQLStore(T *testing.T) {
	T.Parallel()

	runStoreSuite(T, newSQLiteEnv(T))
}

// runStoreSuite is every assertion this package makes against a live database.
// It takes the environment so that one suite serves SQLite and both containers.
func runStoreSuite(t *testing.T, env *storeEnv) {
	t.Helper()

	runDefinitionSuite(t, env)
	runValueSuite(t, env)
	runResolutionSuite(t, env)
	runTransactionSuite(t, env)
	runErasureSuite(t, env)
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

	T.Run("the dialect comes from the client", func(t *testing.T) {
		t.Parallel()

		// The store cannot be built for a dialect the generated package was not
		// generated for, and the mapping is total over dialect.Valid — so the
		// only way to reach the refusal is to name a dialect this module does
		// not have.
		d, err := settingsdbDialect(dialect.SQLite)
		must.NoError(t, err)
		test.EqOp(t, "sqlite", string(d))

		_, err = settingsdbDialect(dialect.Dialect("cassandra"))
		test.Error(t, err)
	})
}

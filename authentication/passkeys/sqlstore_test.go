package passkeys

import (
	"testing"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/observability"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestNewSQLStore covers what construction refuses, and what it settles for a
// caller who named nothing.
func TestNewSQLStore(T *testing.T) {
	T.Parallel()

	T.Run("a nil client is refused", func(t *testing.T) {
		t.Parallel()

		_, err := NewSQLStore(nil)
		must.ErrorIs(t, err, ErrNilDatabaseClient)
	})

	T.Run("a prefix ending in the separator is refused", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		// The separator is database/ddl's to supply, so a prefix that brought
		// its own would render a double underscore in every identifier — and a
		// table whose name differs from the migration's by one character is a
		// missing-table error on the first query.
		_, err := NewSQLStore(env.client, WithTablePrefix("ddb_"))
		must.Error(t, err)
	})

	T.Run("observability is optional", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		// An unconfigured store logs nowhere, traces nowhere and records
		// nothing, which is what naming none of the three asks for.
		store, err := NewSQLStore(env.client)
		must.NoError(t, err)
		must.NotNil(t, store)
		test.NotNil(t, store.o11y)
		test.NotNil(t, store.instruments)
	})

	T.Run("pillars can be handed over and then overridden", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		// Options apply in order, which is what lets a caller supply its pillars
		// and then leave this one component unmetered.
		store, err := NewSQLStore(env.client,
			WithPillars(&observability.Pillars{}),
			WithMetricsProvider(nil),
		)
		must.NoError(t, err)
		must.NotNil(t, store)
	})

	T.Run("a nil option is skipped", func(t *testing.T) {
		t.Parallel()

		env := newSQLiteEnv(t)

		store, err := NewSQLStore(env.client, nil, WithTablePrefix("pk_nil"))
		must.NoError(t, err)
		test.EqOp(t, "pk_nil", store.prefix)
	})
}

// TestPasskeysdbDialect pins the mapping between this module's dialect names and
// the generated package's. The default arm is reachable only when this module
// learns a dialect the generated package was not generated for.
func TestPasskeysdbDialect(t *testing.T) {
	t.Parallel()

	for _, d := range []dialect.Dialect{dialect.Postgres, dialect.MySQL, dialect.SQLite} {
		mapped, err := passkeysdbDialect(d)
		must.NoError(t, err)
		test.NotEqOp(t, "", string(mapped))
	}

	_, err := passkeysdbDialect(dialect.Dialect("cassandra"))
	must.ErrorIs(t, err, dialect.ErrUnsupported)
}

// TestEncodeCredentialID pins the spelling a span records a credential ID under:
// base64url without padding, which is how WebAuthn writes one on the wire, so a
// line here and a line in a browser's console name the same value the same way.
func TestEncodeCredentialID(t *testing.T) {
	t.Parallel()

	test.EqOp(t, "", encodeCredentialID(nil))
	test.EqOp(t, "AQID", encodeCredentialID([]byte{0x01, 0x02, 0x03}))
}

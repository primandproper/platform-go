package audit

import (
	"testing"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// newTestErasure builds an Erasure over the SQLite tables the in-process tests
// create.
func newTestErasure(t *testing.T, opts ...ErasureOption) *Erasure {
	t.Helper()

	e, err := NewErasure(dialect.SQLite, opts...)
	must.NoError(t, err)

	return e
}

// eraseScopes runs a deletion inside a transaction, the way a dataprivacy
// Worker does.
func eraseScopes(t *testing.T, client database.Client, e *Erasure, scopes []tenancy.Scope) int64 {
	t.Helper()

	var deleted int64

	must.NoError(t, client.WithTransaction(t.Context(), func(q database.Tx) error {
		var err error
		deleted, err = e.DeleteScopes(t.Context(), q, scopes)

		return err
	}))

	return deleted
}

func TestErasure_DeleteScopesRefusesAnUnsetScope(T *testing.T) {
	T.Parallel()

	T.Run("refuses the whole set rather than deleting the global chain", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())

		record(t, client, recorder,
			entryFor(tenancy.Global(), "config_1"),
			entryFor(tenancy.Of("user_1"), "recipe_1"))

		var deleted int64

		err := client.WithTransaction(t.Context(), func(q database.Tx) error {
			var deleteErr error
			deleted, deleteErr = newTestErasure(t).DeleteScopes(t.Context(), q,
				[]tenancy.Scope{tenancy.Of("user_1"), {}})

			return deleteErr
		})

		test.ErrorIs(t, err, tenancy.ErrNoScope)
		test.EqOp(t, int64(0), deleted)

		// Nothing went, including the scope that was well formed: the set is
		// one statement and a member that names nobody is a set that was not
		// resolved rather than a member to drop.
		test.EqOp(t, 2, countRows(t, client, "audit_log_entries", "1=1"))
	})
}

func TestNewErasure(T *testing.T) {
	T.Parallel()

	T.Run("rejects an unsupported dialect", func(t *testing.T) {
		t.Parallel()

		_, err := NewErasure(dialect.Dialect("oracle"))
		test.ErrorIs(t, err, dialect.ErrUnsupported)
	})

	T.Run("rejects a prefix that is not an identifier fragment", func(t *testing.T) {
		t.Parallel()

		for _, prefix := range []string{"drop table;--", "has space", "1leading"} {
			_, err := NewErasure(dialect.SQLite, WithErasureTablePrefix(prefix))
			test.ErrorIs(t, err, ErrInvalidTablePrefix, test.Sprintf("prefix %q", prefix))
		}
	})

	T.Run("names the tables it was built for", func(t *testing.T) {
		t.Parallel()

		test.EqOp(t, "audit_log_entries", newTestErasure(t).Describe())
		test.EqOp(t, "ddb_audit_log_entries",
			newTestErasure(t, WithErasureTablePrefix("ddb")).Describe())
	})
}

func TestErasure_DeleteScopes(T *testing.T) {
	T.Parallel()

	T.Run("removes a scope's entries and its chain row together", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())

		record(t, client, recorder, entryFor(tenancy.Of("user_1"), "r1"), entryFor(tenancy.Of("user_1"), "r2"))
		record(t, client, recorder, entryFor(tenancy.Of("acct_9"), "r3"))

		test.EqOp(t, int64(2), eraseScopes(t, client, newTestErasure(t), []tenancy.Scope{tenancy.Of("user_1")}))

		test.EqOp(t, 0, countRows(t, client, "audit_log_entries", "scope = 'user_1'"))

		// The chain row goes with the entries. Leaving it would leave a scope
		// whose recorded head is ahead of any surviving entry, and a later
		// write would be assigned a position the chain claims is already used.
		test.EqOp(t, 0, countRows(t, client, "audit_log_chains", "scope = 'user_1'"))

		// Somebody else's tenant is untouched, chain row and all.
		test.EqOp(t, 1, countRows(t, client, "audit_log_entries", "scope = 'acct_9'"))
		test.EqOp(t, 1, countRows(t, client, "audit_log_chains", "scope = 'acct_9'"))
	})

	T.Run("removes several scopes in one statement", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())

		record(t, client, recorder, entryFor(tenancy.Of("tenant_a"), "r1"))
		record(t, client, recorder, entryFor(tenancy.Of("tenant_b"), "r2"))
		record(t, client, recorder, entryFor(tenancy.Of("tenant_c"), "r3"))

		test.EqOp(t, int64(2),
			eraseScopes(t, client, newTestErasure(t), []tenancy.Scope{tenancy.Of("tenant_a"), tenancy.Of("tenant_b")}))

		test.EqOp(t, 1, countRows(t, client, "audit_log_entries", "1=1"))
	})

	T.Run("a surviving scope still verifies across the deletion", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())
		reader := newTestReader(t, client)

		record(t, client, recorder, entryFor(tenancy.Of("user_1"), "r1"))

		// Three entries in somebody else's tenant, the middle one by the
		// subject. Deleting that middle entry is what would break the chain,
		// and this eraser never does: it takes whole scopes or nothing.
		record(t, client, recorder,
			entryFor(tenancy.Of("acct_9"), "r2"), entryFor(tenancy.Of("acct_9"), "r3"), entryFor(tenancy.Of("acct_9"), "r4"))

		eraseScopes(t, client, newTestErasure(t), []tenancy.Scope{tenancy.Of("user_1")})

		result, err := reader.Verify(t.Context(), tenancy.Of("acct_9"), time.Time{}, time.Time{})
		must.NoError(t, err)
		test.True(t, result.Intact(), test.Sprintf("break: %+v", result.FirstBreak))
		test.EqOp(t, 3, result.Checked)
	})

	T.Run("an empty set is not a statement", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())

		record(t, client, recorder, entryFor(tenancy.Of("user_1"), "r1"))

		// `IN ()` is a syntax error on two of the three dialects, so the empty
		// batch is the caller's to answer: no scopes, no statement, nothing
		// deleted.
		test.EqOp(t, int64(0), eraseScopes(t, client, newTestErasure(t), nil))
		test.EqOp(t, 1, countRows(t, client, "audit_log_entries", "1=1"))
	})

	T.Run("refuses a nil executor", func(t *testing.T) {
		t.Parallel()

		_, err := newTestErasure(t).DeleteScopes(t.Context(), nil, []tenancy.Scope{tenancy.Of("user_1")})
		test.ErrorIs(t, err, ErrNilExecutor)
	})

	T.Run("reports a table it cannot write", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		exec(t, client, "DROP TABLE audit_log_entries")

		err := client.WithTransaction(t.Context(), func(q database.Tx) error {
			_, deleteErr := newTestErasure(t).DeleteScopes(t.Context(), q, []tenancy.Scope{tenancy.Of("user_1")})

			return deleteErr
		})
		test.Error(t, err)
	})

	T.Run("reports a chain table it cannot write", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		exec(t, client, "DROP TABLE audit_log_chains")

		// The entries go and the chain rows cannot, which is a half-applied
		// migration. Reporting it is what lets the erasure transaction roll the
		// entry deletion back rather than leaving a scope whose head is ahead
		// of every surviving row.
		err := client.WithTransaction(t.Context(), func(q database.Tx) error {
			_, deleteErr := newTestErasure(t).DeleteScopes(t.Context(), q, []tenancy.Scope{tenancy.Of("user_1")})

			return deleteErr
		})
		test.Error(t, err)
	})
}

func TestErasure_CountMentions(T *testing.T) {
	T.Parallel()

	T.Run("counts an entry naming the subject twice only once", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())

		// The subject as the actor, as the resource, and as both — three
		// entries, not four mentions.
		acted := entryFor(tenancy.Of("acct_9"), "recipe_1")
		acted.Actor.ID = "user_1"

		actedOn := entryFor(tenancy.Of("acct_9"), "user_1")
		actedOn.Actor.ID = "user_7"

		both := entryFor(tenancy.Of("acct_9"), "user_1")
		both.Actor.ID = "user_1"

		elsewhere := entryFor(tenancy.Of("acct_9"), "recipe_2")
		elsewhere.Actor.ID = "user_7"

		record(t, client, recorder, acted, actedOn, both, elsewhere)

		count, err := newTestErasure(t).CountMentions(t.Context(), client.Reader(), "user_1")
		must.NoError(t, err)
		test.EqOp(t, int64(3), count)
	})

	T.Run("counts nothing for a subject nobody named", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		recorder := newTestRecorder(t, newStubClock())

		record(t, client, recorder, entryFor(tenancy.Of("acct_9"), "recipe_1"))

		count, err := newTestErasure(t).CountMentions(t.Context(), client.Reader(), "user_404")
		must.NoError(t, err)
		test.EqOp(t, int64(0), count)
	})

	T.Run("refuses a nil executor", func(t *testing.T) {
		t.Parallel()

		_, err := newTestErasure(t).CountMentions(t.Context(), nil, "user_1")
		test.ErrorIs(t, err, ErrNilExecutor)
	})

	T.Run("reports a table it cannot read", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		exec(t, client, "DROP TABLE audit_log_entries")

		_, err := newTestErasure(t).CountMentions(t.Context(), client.Reader(), "user_1")
		test.Error(t, err)
	})
}

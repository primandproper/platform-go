package audit

import (
	"testing"

	"github.com/primandproper/platform-go/v14/audit/migrations"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// applyAppendOnly installs the update-rejecting triggers.
func applyAppendOnly(t *testing.T, client database.Client, d dialect.Dialect, prefix string) {
	t.Helper()

	stmts, err := migrations.AppendOnlyStatements(d, prefix)
	must.NoError(t, err)

	for _, stmt := range stmts {
		_, execErr := client.Writer().ExecContext(t.Context(), stmt)
		must.NoError(t, execErr, must.Sprintf("executing %q", stmt))
	}
}

func TestAppendOnlyTriggers(T *testing.T) {
	T.Parallel()

	T.Run("refuse an update", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		applyAppendOnly(t, client, dialect.SQLite, DefaultTablePrefix)

		recorder := newTestRecorder(t, newStubClock())
		entry := entryFor(tenancy.Of("acct_1"), "recipe_1")
		record(t, client, recorder, entry)

		// Without the trigger this is the edit the chain would merely reveal
		// after the fact. With it, the database refuses outright.
		_, err := client.Writer().ExecContext(t.Context(),
			"UPDATE audit_log_entries SET actor_id = 'somebody_else' WHERE id = ?", entry.ID)
		must.Error(t, err)
		test.StrContains(t, err.Error(), "append-only")

		test.EqOp(t, 1, countRows(t, client, "audit_log_entries", "actor_id = 'user_1'"))
	})

	T.Run("still admit new entries", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		applyAppendOnly(t, client, dialect.SQLite, DefaultTablePrefix)

		recorder := newTestRecorder(t, newStubClock())

		// Record advances the chain row with an UPDATE, so the trigger has to
		// cover the entries table alone — a blanket ban would have broken
		// recording itself.
		record(t, client, recorder, entryFor(tenancy.Of("acct_1"), "r1"))
		record(t, client, recorder, entryFor(tenancy.Of("acct_1"), "r2"))

		test.EqOp(t, 2, countRows(t, client, "audit_log_entries", "1=1"))
	})

	T.Run("survive a second application", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)

		// The statements are what a consumer's migration runs, and a migration
		// gets re-run — replayed from zero against a live database, or applied
		// twice because a previous attempt failed partway. Each statement drops
		// the trigger it is about to create, so the second pass reinstalls
		// rather than reporting a duplicate object.
		applyAppendOnly(t, client, dialect.SQLite, DefaultTablePrefix)
		applyAppendOnly(t, client, dialect.SQLite, DefaultTablePrefix)

		recorder := newTestRecorder(t, newStubClock())
		entry := entryFor(tenancy.Of("acct_1"), "recipe_1")
		record(t, client, recorder, entry)

		// Re-applying must leave the guard installed, not merely leave the
		// migration exit zero.
		_, err := client.Writer().ExecContext(t.Context(),
			"UPDATE audit_log_entries SET actor_id = 'somebody_else' WHERE id = ?", entry.ID)
		must.Error(t, err)
		test.StrContains(t, err.Error(), "append-only")
	})

	T.Run("leave deletion to retention", func(t *testing.T) {
		t.Parallel()

		client := newTestClient(t)
		applyAppendOnly(t, client, dialect.SQLite, DefaultTablePrefix)

		recorder := newTestRecorder(t, newStubClock())
		entry := entryFor(tenancy.Of("acct_1"), "recipe_1")
		record(t, client, recorder, entry)

		// Deliberately permitted: no trigger can tell the retention sweep apart
		// from an attacker, and a log that cannot be pruned grows forever. The
		// chain is what covers deletion instead.
		exec(t, client, "DELETE FROM audit_log_entries WHERE id = ?", entry.ID)
		test.EqOp(t, 0, countRows(t, client, "audit_log_entries", "1=1"))
	})
}

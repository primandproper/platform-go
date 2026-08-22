/*
Package migrations supplies the audit tables' DDL, rendered for a dialect and
table prefix.

The platform deliberately does not ship a numbered migration file. Migration
files are numbered globally per consumer, so a platform-owned number would
collide with the consumer's own the moment either side added one. The version is
therefore always the consumer's to choose.

If you already run database/migrate, hand SQL to WithGeneratedMigration and the
tables are created by your normal migration run — no DDL copied into your
repository, nothing to keep in sync as this package evolves:

	body, err := migrations.SQL(dialect.Postgres, audit.DefaultTablePrefix)
	// ...
	m, err := migrate.New(dialect.Postgres, myMigrations,
		migrate.WithGeneratedMigration(39, "create_audit_tables", body),
	)

Statements is the same DDL split into individually executable statements, for
callers running it some other way — a different migration tool, or a test that
just wants the tables.

Two tables are rendered from one prefix rather than two configurable names.
Record writes both in one transaction and Verify reads both, so a consumer who
could name them independently could also name them inconsistently, and nothing
would catch it until the first write.

# Append-only enforcement

AppendOnlyStatements renders the triggers that make the entries table reject
UPDATE outright, at the database rather than in this package. They are separate
from the schema above because they are separately privileged — the Postgres
variant creates a function — and because a consumer whose deployment already
revokes UPDATE from the application role has the same guarantee without them.

They are not offered through SQL, only as pre-split statements, and that is
deliberate: the Postgres and SQLite triggers contain semicolons inside their
bodies, so joining them into one string hands the next tool that splits on
semicolons — goose included — two halves of a trigger and no way to notice.
*/
package migrations

import (
	_ "embed"
	"fmt"
	"strings"

	"github.com/primandproper/platform-go/v13/database/ddl"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

//go:embed postgres.sql
var postgresDDL string

//go:embed mysql.sql
var mysqlDDL string

//go:embed sqlite.sql
var sqliteDDL string

// prefixPlaceholder is the token each .sql file uses for the table prefix.
const prefixPlaceholder = ddl.Placeholder

// schema is this package's DDL in each supported dialect. Rendering and prefix
// vetting go through it, as they do for every other schema-shipping package;
// what stays local is the append-only triggers, which are built in Go rather
// than embedded and must not be re-split on semicolons.
var schema = ddl.Schema{
	Component: "audit",
	Postgres:  postgresDDL,
	MySQL:     mysqlDDL,
	SQLite:    sqliteDDL,
}

// ErrInvalidPrefix indicates a prefix that is not a plain SQL identifier
// fragment.
var ErrInvalidPrefix = platformerrors.New("invalid audit table prefix")

// Statements renders the DDL for the dialect against the given table prefix and
// splits it into individually executable statements, in dependency order.
func Statements(d dialect.Dialect, prefix string) ([]string, error) {
	// The local prefix check runs first so a malformed prefix reports this
	// package's own ErrInvalidPrefix; the shared renderer then resolves the
	// dialect and vets every name the schema would create.
	if err := ValidatePrefix(prefix); err != nil {
		return nil, err
	}

	return schema.Statements(d, prefix)
}

// SQL renders the same DDL as Statements, joined back into one migration body.
// It is what you hand to database/migrate's WithGeneratedMigration, so the
// audit tables are created by the consumer's own migration run instead of being
// copied into their repository:
//
//	body, err := migrations.SQL(dialect.Postgres, audit.DefaultTablePrefix)
//	// ...
//	m, err := migrate.New(dialect.Postgres, myMigrations,
//		migrate.WithGeneratedMigration(39, "create_audit_tables", body),
//	)
//
// The comments are already stripped, which matters: goose splits a migration
// into statements on semicolons, and a '--' comment containing one would be torn
// in half.
func SQL(d dialect.Dialect, prefix string) (string, error) {
	stmts, err := Statements(d, prefix)
	if err != nil {
		return "", err
	}

	return strings.Join(stmts, ";\n\n") + ";\n", nil
}

// appendOnlyMessage is what the database reports when something tries to edit a
// past entry.
const appendOnlyMessage = "audit log entries are append-only"

// AppendOnlyStatements renders the triggers that make the entries table reject
// UPDATE, and returns them as individually executable statements.
//
// Apply them and editing a recorded entry stops being something the hash chain
// merely reveals after the fact and becomes something the database refuses. The
// chain still matters — it is what covers a row that was removed rather than
// altered, and it is what a verifier can check without trusting that these
// triggers were ever installed — but a guarantee enforced at write time is
// worth more than one enforced at audit time.
//
// DELETE is deliberately left permitted. Retention has to remove aged entries,
// and no trigger can tell that sweep apart from an attacker's DELETE, so
// blocking deletion here would mean shipping a log that grows forever. Deletion
// is covered by the chain instead: entries carry contiguous positions within a
// scope, so a removed row leaves a hole that Verify reports, and the retention
// sweep records where it pruned to so that its own holes are distinguishable
// from everyone else's.
//
// The Postgres variant creates a function as well as a trigger and therefore
// needs rights the rest of the schema does not. If that is unwelcome, revoking
// UPDATE and DELETE on the entries table from the application role achieves
// more than these triggers do — it also stops the deletions they cannot.
//
// Statements are returned pre-split and must be executed whole. Two of the
// three contain semicolons inside a trigger body, so re-joining them for a tool
// that splits on semicolons produces fragments, not statements.
func AppendOnlyStatements(d dialect.Dialect, prefix string) ([]string, error) {
	// Only the prefix is checked here; the switch below is the dialect check,
	// so an unsupported one is rejected in one place rather than two.
	if err := ValidatePrefix(prefix); err != nil {
		return nil, err
	}

	table := ddl.Qualify(prefix) + "audit_log_entries"

	switch d {
	case dialect.Postgres:
		return []string{
			fmt.Sprintf(
				"CREATE OR REPLACE FUNCTION %s_reject_update() RETURNS TRIGGER AS $$\n"+
					"BEGIN\n"+
					"    RAISE EXCEPTION '%s';\n"+
					"END;\n"+
					"$$ LANGUAGE plpgsql",
				table, appendOnlyMessage,
			),
			fmt.Sprintf(
				"CREATE TRIGGER %[1]s_no_update BEFORE UPDATE ON %[1]s "+
					"FOR EACH ROW EXECUTE FUNCTION %[1]s_reject_update()",
				table,
			),
		}, nil
	case dialect.MySQL:
		return []string{
			fmt.Sprintf(
				"CREATE TRIGGER %[1]s_no_update BEFORE UPDATE ON %[1]s "+
					"FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = '%[2]s'",
				table, appendOnlyMessage,
			),
		}, nil
	case dialect.SQLite:
		return []string{
			fmt.Sprintf(
				"CREATE TRIGGER IF NOT EXISTS %[1]s_no_update BEFORE UPDATE ON %[1]s "+
					"BEGIN SELECT RAISE(ABORT, '%[2]s'); END",
				table, appendOnlyMessage,
			),
		}, nil
	default:
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "audit migration dialect %q", d)
	}
}

// ValidatePrefix reports whether prefix yields a legal SQL identifier for every
// table and index this package creates.
func ValidatePrefix(prefix string) error {
	if !ddl.ValidNamespace(prefix) {
		return platformerrors.Wrapf(ErrInvalidPrefix, "audit table prefix %q", prefix)
	}

	return schema.ValidatePrefix(prefix)
}

/*
Package migrations supplies the audit tables' DDL, rendered for a dialect and
table prefix, as the versions it has shipped in.

# Versions

The entries table has changed since it first shipped, and a database that
already created it is not going to run a CREATE TABLE IF NOT EXISTS again to
find out. So the schema is a sequence — a database/ddl Migrations — and each
version holds only its own change:

  - Version 1 is the two tables as they shipped in v14.0.0.
  - Version 2 adds actor_impersonator, the operator behind an entry recorded
    under somebody else's identity, and the index that answers "what did this
    operator do as somebody else".

A shipped version is never edited. A change to these tables is a new version
appended here, which is what Latest then reports.

# A fresh install

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
just wants the tables. Both render the whole sequence, version 1 onwards.

# A database that already has the tables

A consumer that created the tables from an earlier release does not edit the
migration that did it. They add a migration of their own holding what SQLSince
renders from the version their database is at:

	// Created from v14.0.0 through the release before version 2: owes version 2.
	owed, err := migrations.SQLSince(dialect.Postgres, audit.DefaultTablePrefix, 1)
	// ...
	migrate.WithGeneratedMigration(47, "upgrade_audit_tables", owed)

A database already at Latest owes nothing, and SQLSince says so with an empty
body rather than an error. The sequence itself stays unexported, for the reason
authentication/signin/refreshtokens/migrations gives: a value a consumer holds is
one they can append to before rendering it.

Version 2 is safe to run under the append-only triggers below. Adding a column
with a constant default rewrites no row through an UPDATE on any of the three
engines, so the triggers have nothing to refuse.

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

What "separately privileged" costs is dialect-specific, and MySQL's is the one
a consumer meets without warning. Binary logging is on by default there, and
with it on a role without SUPER may not CREATE TRIGGER at all: the statement
comes back as error 1419, naming a privilege rather than anything about this
schema. A deployment applies these either as a role holding SUPER (or
SET_USER_ID), or with log_bin_trust_function_creators enabled, which is the
setting that grants it. Neither is something this package can do on a
consumer's behalf, and the schema above needs no such privilege — which is the
practical reason these are a separate call rather than a tail of it.

They are not offered through SQL, only as pre-split statements, and that is
deliberate: the Postgres and SQLite triggers contain semicolons inside their
bodies, so joining them into one string hands the next tool that splits on
semicolons — goose included — two halves of a trigger and no way to notice.

Like the schema above, they are re-runnable in every dialect. Each one drops the
trigger it is about to create first, because only SQLite spells CREATE TRIGGER
IF NOT EXISTS and only Postgres spells CREATE OR REPLACE TRIGGER — a consumer
applying both sets twice would otherwise succeed on one dialect and fail on the
other two. Dropping also settles what a re-run means: the trigger afterwards is
always the one this version renders, rather than whichever one an earlier
version left behind.
*/
package migrations

import (
	_ "embed"
	"fmt"

	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// component names this package's schema in the errors database/ddl renders.
const component = "audit"

//go:embed postgres.sql
var postgresV1 string

//go:embed mysql.sql
var mysqlV1 string

//go:embed sqlite.sql
var sqliteV1 string

//go:embed postgres_v2.sql
var postgresV2 string

//go:embed mysql_v2.sql
var mysqlV2 string

//go:embed sqlite_v2.sql
var sqliteV2 string

// prefixPlaceholder is the token each .sql file uses for the table prefix.
const prefixPlaceholder = ddl.Placeholder

// sequence is this package's schema over time, in the order it runs. Rendering
// and prefix vetting go through it, as they do for every other schema-shipping
// package; what stays local is the append-only triggers, which are built in Go
// rather than embedded and must not be re-split on semicolons.
//
// It is unexported so that nothing outside this file can append to it or
// overwrite a version that has shipped; see the package doc.
var sequence = ddl.Migrations{
	{Version: 1, Schema: ddl.Schema{Component: component, Postgres: postgresV1, MySQL: mysqlV1, SQLite: sqliteV1}},
	{Version: 2, Schema: ddl.Schema{Component: component, Postgres: postgresV2, MySQL: mysqlV2, SQLite: sqliteV2}},
}

// ErrInvalidPrefix indicates a prefix that is not a plain SQL identifier
// fragment.
var ErrInvalidPrefix = platformerrors.New("invalid audit migration table prefix")

// Latest is the version a database is at once it has run everything this
// package ships — what a consumer records beside the migration that ran it, and
// passes to StatementsSince or SQLSince the next time this package adds one.
func Latest() uint64 {
	return sequence.Latest()
}

// Statements renders every version's DDL for the dialect against the given
// table prefix, in version order and each table ahead of its indexes, split
// into individually executable statements. It is a fresh install:
// StatementsSince from version 0.
func Statements(d dialect.Dialect, prefix string) ([]string, error) {
	return StatementsSince(d, prefix, 0)
}

// StatementsSince renders what a database at version still owes, in version
// order, as individually executable statements. A database already at Latest
// owes nothing and gets no statements.
func StatementsSince(d dialect.Dialect, prefix string, version uint64) ([]string, error) {
	owed, err := since(d, prefix, version)
	if err != nil {
		return nil, err
	}

	return owed.Statements(d, prefix)
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
	return SQLSince(d, prefix, 0)
}

// SQLSince renders the same DDL as StatementsSince, joined back into one
// migration body — what a consumer whose database already has the tables hands
// to WithGeneratedMigration as a migration of their own. A database already at
// Latest gets the empty body.
func SQLSince(d dialect.Dialect, prefix string, version uint64) (string, error) {
	owed, err := since(d, prefix, version)
	if err != nil {
		return "", err
	}

	return owed.SQL(d, prefix)
}

// since is the part of a splice that can be refused before anything renders:
// the prefix, the dialect, and a version this package has never shipped.
//
// The local prefix check runs first so a malformed prefix reports this
// package's own ErrInvalidPrefix. A version past Latest is an error rather than
// an empty result: a database claiming one was migrated by a newer release than
// the one running now, and "nothing to do" would let an older binary go on
// writing a table whose shape it does not know.
func since(d dialect.Dialect, prefix string, version uint64) (ddl.Migrations, error) {
	if err := ValidatePrefix(prefix); err != nil {
		return nil, err
	}

	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "%s migration dialect %q", component, d)
	}

	if latest := sequence.Latest(); version > latest {
		return nil, platformerrors.Wrapf(platformerrors.ErrUnrecognizedInputValue,
			"%s migration version %d is past the latest this package ships, %d", component, version, latest)
	}

	return sequence.Since(version)
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
//
// Applying them twice is a no-op in every dialect, which is what the leading
// DROP TRIGGER IF EXISTS is for. Only SQLite has CREATE TRIGGER IF NOT EXISTS
// and only Postgres has CREATE OR REPLACE TRIGGER, so dropping first is the one
// spelling all three share — and it is the stronger one anyway, because it
// makes the installed trigger the one this version renders rather than whatever
// an earlier version left in place.
//
// The drop and the create are two statements, so between them the table is
// briefly unguarded. Run them inside a transaction and that window does not
// exist: Postgres and SQLite both roll DDL back. MySQL commits each statement
// as it runs and cannot offer that, so a re-application there is a moment when
// an UPDATE would be accepted — which is a reason to apply these during a
// migration rather than under load, not a reason to leave the second run
// failing.
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
			// A Postgres trigger name is scoped to its table, so the drop names
			// both.
			fmt.Sprintf("DROP TRIGGER IF EXISTS %[1]s_no_update ON %[1]s", table),
			fmt.Sprintf(
				"CREATE TRIGGER %[1]s_no_update BEFORE UPDATE ON %[1]s "+
					"FOR EACH ROW EXECUTE FUNCTION %[1]s_reject_update()",
				table,
			),
		}, nil
	case dialect.MySQL:
		return []string{
			fmt.Sprintf("DROP TRIGGER IF EXISTS %s_no_update", table),
			fmt.Sprintf(
				"CREATE TRIGGER %[1]s_no_update BEFORE UPDATE ON %[1]s "+
					"FOR EACH ROW SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = '%[2]s'",
				table, appendOnlyMessage,
			),
		}, nil
	case dialect.SQLite:
		return []string{
			fmt.Sprintf("DROP TRIGGER IF EXISTS %s_no_update", table),
			fmt.Sprintf(
				"CREATE TRIGGER %[1]s_no_update BEFORE UPDATE ON %[1]s "+
					"BEGIN SELECT RAISE(ABORT, '%[2]s'); END",
				table, appendOnlyMessage,
			),
		}, nil
	default:
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "audit migration dialect %q", d)
	}
}

// ValidatePrefix reports whether prefix yields a legal SQL identifier for every
// table and index any version of this package creates. Every version is vetted
// rather than the latest, since a consumer runs all of them.
func ValidatePrefix(prefix string) error {
	if !ddl.ValidNamespace(prefix) {
		return platformerrors.Wrapf(ErrInvalidPrefix, "audit table prefix %q", prefix)
	}

	return sequence.ValidatePrefix(prefix)
}

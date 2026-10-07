/*
Package migrations supplies the identity tables' DDL, rendered for a dialect and
table prefix, as the versions it has shipped in.

# Versions

The schema has changed since it first shipped, and a database that already
created these tables is not going to run a CREATE TABLE IF NOT EXISTS again to
find out. So the schema is a sequence — a database/ddl Migrations — and each
version holds only its own change:

  - Version 1 is the seven tables as they shipped in v15.0.0.
  - Version 2 adds identity_accounts_customer_idx, the index the payment
    processor's customer read runs on. Without it every processor delivery
    scans its scope's accounts.

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

	ddl, err := migrations.SQL(dialect.Postgres, identity.DefaultTablePrefix)
	// ...
	m, err := migrate.New(dialect.Postgres, myMigrations,
		migrate.WithGeneratedMigration(44, "create_identity_tables", ddl),
	)

SQL and Statements render the whole sequence, version 1 onwards, so a fresh
install needs no other spelling. Statements is the same DDL split into
individually executable statements, for callers running it some other way — a
different migration tool, or a test that just wants the tables.

# A database that already has the tables

A consumer that created the tables from an earlier release does not edit the
migration that did it. That migration has run, and editing it changes only what
the next fresh install gets. They add a migration of their own instead, holding
what SQLSince renders from the version their database is at:

	// Created from v15.0.0: owes version 2.
	owed, err := migrations.SQLSince(dialect.Postgres, identity.DefaultTablePrefix, 1)
	// ...
	migrate.WithGeneratedMigration(45, "upgrade_identity_tables", owed)

The version passed is the one the database is at, from the list above; Latest is
the version it is at once the result has run. A database already at Latest owes
nothing, and SQLSince says so with an empty body rather than an error.

These are functions over a sequence this package keeps to itself, rather than
the sequence handed out as a ddl.Migrations. The sequence is the thing a shipped
version may never be edited in, and a value a consumer holds is a value a
consumer can append to or overwrite before rendering it — so what is exported is
the renderings, which are the only two things a caller does with it: splice it
from nothing, or splice it from a version.

# Which tables this package creates

Tables answers that, at your prefix, and the answer is complete: the names come
out of the DDL rather than from a list beside it, so a table added to this schema
is in that list the moment it is added.

	names, err := migrations.Tables(identity.DefaultTablePrefix)

Reach for it wherever the job is per-table but not per-query — the TRUNCATE
between integration tests, a backup policy, a schema inventory, a data privacy
audit. The alternative is copying seven names out of the .sql files, which is a
list that goes stale silently: nothing reports a table left out of a maintenance
TRUNCATE except a test failing somewhere else on rows the previous one left.

The same seven names reach database/querygen's table registry when identity's
generator runs, so a binary that generates for several schemas reads one list
covering all of them.

# What a consumer adds beside these tables

Columns of their own, in a side table keyed by user or account ID. This package
owns its tables and will not grow another on request, because the moment the
schema is configurable it stops being ownable — and owning it is what a consumer
is adopting. See the identity package documentation.

# created_at has a default, and scope deliberately does not

created_at is NOT NULL with a dialect-appropriate DEFAULT — NOW() on Postgres,
CURRENT_TIMESTAMP(6) on MySQL, CURRENT_TIMESTAMP on SQLite — because the row's
creation time is the database's rather than the application's. Two application
instances whose clocks differ by a second would otherwise write rows that a
created_after filter excludes at random, and a creation time that disagrees with
the row's id disagrees with the order a cursor walk pages in. The identity store
does not supply the column, and reads it back so the value a caller holds is the
value in the row.

The SQLite spelling is also what makes that column filterable there. SQLite has
no date type, so a window comparison over it is lexicographic text — and
CURRENT_TIMESTAMP is what writes the UTC YYYY-MM-DD HH:MM:SS shape that
lexicographic order agrees with. A caller-bound time.Time reaches that column as
Go's own String() rendering instead, which sorts correctly by accident of its
prefix rather than by design.

# Running the creates twice adds nothing, in every dialect

The DDL here only ever creates. Every CREATE TABLE is IF NOT EXISTS, the
Postgres and SQLite indexes are CREATE INDEX IF NOT EXISTS, and version 1's MySQL
keys are declared inline in the CREATE TABLE they belong to, because MySQL is
the one dialect with no CREATE INDEX IF NOT EXISTS.

A later version's MySQL key cannot be inline, since the table it belongs to
already exists by the time that version runs, so it is an ALTER TABLE with no
conditional to reach for. That is a version's statement rather than a schema's:
a consumer's migration tool records a version once it has run and never runs it
again. On Postgres and SQLite the whole sequence still re-runs as a no-op; on
MySQL version 1 does, and a version after it is run once.

That is the module's rule rather than this package's, and it is asserted for
every schema-shipping package at once in internal/schemaconvention, which is
also where the reasoning is written down. What is worth saying here is the one
identity-specific consequence: the three bodies spell the same twelve index
names. MySQL scopes an index name to its table and would accept shorter ones,
but ValidatePrefix measures the longest identifier a prefix renders across all
three bodies at once, so a name only two of them spelled would be a name that
check stopped measuring here.

# The scope column has no default

scope is NOT NULL with no DEFAULT, which is the one place this schema departs
from the module's habit of defaulting a text column to the empty string. The
empty string is not the absence of a scope here — it is tenancy.Global(), a
scope like any other. A column that supplied it for a write which did not name
one would hand the global scope to whoever forgot the column, which is exactly
the mistake tenancy.Scope is shaped to make unspellable in Go: an unset scope
fails at Value rather than widening a predicate. The column enforces the same
rule for a writer that did not come through SQLStore, and the write fails.

# owner_user_id has no foreign key, and every other belongs-to column does

identity_memberships references both parents, identity_invitations references
the account, and the two role tables cascade from theirs. identity_accounts's
owner_user_id references nothing, and that is a decision rather than the
oversight the asymmetry looks like.

Neither behavior the clause offers is the one this column wants. ON DELETE
CASCADE would destroy an organization, its invoices and every other member's
work because one member exercised a right to be forgotten — the erasure of a
person taking a company with it. ON DELETE RESTRICT would refuse the erasure,
which a right-to-be-forgotten transaction cannot survive: it spans every domain
and has to commit, and a subject whose rights depend on an account they may not
administer does not have them. SET NULL is not open to it either, since the
column is NOT NULL — and it could not be otherwise, because an account with no
owner is the state the whole guard exists to prevent.

So the reference is one the application keeps. identity.Store's ArchiveUser
refuses while a user still owns a live account, naming the account, which is the
path that has an alternative — transfer it, or archive it. EraseUser is the path
that does not, and it documents what it leaves behind: an account whose
owner_user_id names an id that no longer exists anywhere.

The rendering and prefix vetting live in database/ddl, shared with every other
schema-shipping package in this module.
*/
package migrations

import (
	_ "embed"

	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
)

// component names this package in the errors its schemas raise.
const component = "identity"

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

// sequence is this package's schema over time, in the order it runs. It is
// unexported so that nothing outside this file can append to it or overwrite a
// version that has shipped; see the package doc.
var sequence = ddl.Migrations{
	{Version: 1, Schema: ddl.Schema{Component: component, Postgres: postgresV1, MySQL: mysqlV1, SQLite: sqliteV1}},
	{Version: 2, Schema: ddl.Schema{Component: component, Postgres: postgresV2, MySQL: mysqlV2, SQLite: sqliteV2}},
}

// Latest is the version a database is at once it has run everything this
// package ships — what a consumer records beside the migration that ran it, and
// passes to StatementsSince or SQLSince the next time this package adds one.
func Latest() uint64 {
	return sequence.Latest()
}

// Statements renders every version's DDL for the dialect against the given
// table prefix, in version order, split into individually executable
// statements, each table before its indexes. It is a fresh install:
// StatementsSince from version 0.
func Statements(d dialect.Dialect, prefix string) ([]string, error) {
	return StatementsSince(d, prefix, 0)
}

// StatementsSince renders what a database at version still owes, in version
// order, as individually executable statements. A database already at Latest
// owes nothing and gets no statements.
//
// The dialect and the prefix are checked even when nothing is owed, so a caller
// learns about a misconfiguration the first time it renders rather than the
// first time this package ships a version.
func StatementsSince(d dialect.Dialect, prefix string, version uint64) ([]string, error) {
	owed, err := since(d, prefix, version)
	if err != nil {
		return nil, err
	}

	return owed.Statements(d, prefix)
}

// SQL renders the same DDL as Statements, joined back into one migration body.
// It is what you hand to database/migrate's WithGeneratedMigration, so the
// tables are created by the consumer's own migration run instead of being
// copied into their repository.
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
// the dialect, the prefix, and a version this package has never shipped.
//
// The last is an error rather than an empty result. A database claiming a
// version past Latest was migrated by a newer release than the one running now,
// and "nothing to do" would be the answer that lets an older binary go on
// writing tables whose shape it does not know.
func since(d dialect.Dialect, prefix string, version uint64) (ddl.Migrations, error) {
	if !d.Valid() {
		return nil, platformerrors.Wrapf(dialect.ErrUnsupported, "%s migration dialect %q", component, d)
	}

	if err := sequence.ValidatePrefix(prefix); err != nil {
		return nil, err
	}

	if latest := sequence.Latest(); version > latest {
		return nil, platformerrors.Wrapf(platformerrors.ErrUnrecognizedInputValue,
			"%s migration version %d is past the latest this package ships, %d", component, version, latest)
	}

	return sequence.Since(version)
}

// ValidatePrefix reports whether prefix yields a legal SQL identifier for every
// table and index any version of this package creates.
func ValidatePrefix(prefix string) error {
	return sequence.ValidatePrefix(prefix)
}

// Tables returns the seven table names this package creates, rendered against
// prefix and sorted.
//
// It is the complete list — identity creates no table this omits, because the
// names are read out of every version's DDL rather than from a list maintained
// next to it, so a table added to the schema is in this list the moment it is
// added. That is what a consumer needs it to be: the uses are the per-table jobs
// that are not per-query — the TRUNCATE an integration suite runs between
// tests, a backup policy, a schema audit, a data privacy inventory — and every
// one of them is wrong in a way nothing reports if the list is short by one. A
// missing table in a between-tests TRUNCATE is a different test failing later,
// on rows the previous one left behind.
//
// The prefix is vetted exactly as [Statements] and [SQL] vet it, and for the
// same reason: these names are interpolated into statement text rather than
// bound, so a caller building a TRUNCATE out of them is building it out of
// whatever this returns.
//
// It is not an ordering a caller can delete in. Foreign keys make deletion order
// a fact about the schema — memberships reference both users and accounts, and
// each role table references what it names — so a consumer clearing these tables
// wants the dialect's own way of ignoring the constraints rather than a sequence
// read off this slice.
func Tables(prefix string) ([]string, error) {
	if err := sequence.ValidatePrefix(prefix); err != nil {
		return nil, err
	}

	return sequence.Tables(prefix), nil
}

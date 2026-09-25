/*
Package migrations supplies the sign-in refresh token table's DDL, rendered for
a dialect and table prefix, as the versions it has shipped in.

# Versions

The table has changed since it first shipped, and a database that already
created it is not going to run a CREATE TABLE IF NOT EXISTS again to find out.
So the schema is a sequence — a database/ddl Migrations — and each version holds
only its own change:

  - Version 1 is the table as it shipped in v14.0.0.
  - Version 2 adds redeemed_with_key and successor_hash, which are what tell a
    client's retry of an exchange from somebody else's replay of it. They
    shipped in v14.1.0.
  - Version 3 adds signed_in_at, which is when the login a row belongs to began,
    and backfills it for the rows already there.

A shipped version is never edited. A change to this table is a new version
appended here, which is what Latest then reports.

# A fresh install

The platform deliberately does not ship a numbered migration file. Migration
files are numbered globally per consumer, so a platform-owned number would
collide with the consumer's own the moment either side added one. The version a
consumer records these statements under is therefore always the consumer's to
choose, and it is a different number from the ones above.

If you already run database/migrate, hand SQL to WithGeneratedMigration and the
table is created by your normal migration run — no DDL copied into your
repository:

	ddl, err := migrations.SQL(dialect.Postgres, refreshtokens.DefaultTablePrefix)
	// ...
	m, err := migrate.New(dialect.Postgres, myMigrations,
		migrate.WithGeneratedMigration(46, "create_signin_refresh_tokens_table", ddl),
	)

SQL and Statements render the whole sequence, version 1 onwards, so a fresh
install needs no other spelling.

# A database that already has the table

A consumer that created the table from an earlier release does not edit the
migration that did it. That migration has run, and editing it changes only what
the next fresh install gets. They add a migration of their own instead, holding
what SQLSince renders from the version their database is at:

	// Created from v14.0.0: owes versions 2 and 3.
	// Created from v14.1.0: owes version 3 alone, so pass 2.
	owed, err := migrations.SQLSince(dialect.Postgres, refreshtokens.DefaultTablePrefix, 1)
	// ...
	migrate.WithGeneratedMigration(47, "upgrade_signin_refresh_tokens_table", owed)

The version passed is the one the database is at, from the list above; Latest is
the version it is at once the result has run. A database already at Latest owes
nothing, and SQLSince says so with an empty body rather than an error.

These are functions over a sequence this package keeps to itself, rather than
the sequence handed out as a ddl.Migrations. The sequence is the thing a shipped
version may never be edited in, and a value a consumer holds is a value a
consumer can append to or overwrite before rendering it — so what is exported is
the renderings, which are the only two things a caller does with it: splice it
from nothing, or splice it from a version.

# The rest

Statements is the same DDL split into individually executable statements, in
version order and each table before its indexes, for callers running it some
other way — a different migration tool, or a test that just wants the table.

Tables answers which tables exist at a prefix, read out of every version's DDL,
so a between-tests TRUNCATE, a backup policy, or a privacy inventory names them
without anybody copying a name out of the schema.

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
const component = "sign-in refresh token"

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

//go:embed postgres_v3.sql
var postgresV3 string

//go:embed mysql_v3.sql
var mysqlV3 string

//go:embed sqlite_v3.sql
var sqliteV3 string

// sequence is this package's schema over time, in the order it runs. It is
// unexported so that nothing outside this file can append to it or overwrite a
// version that has shipped; see the package doc.
var sequence = ddl.Migrations{
	{Version: 1, Schema: ddl.Schema{Component: component, Postgres: postgresV1, MySQL: mysqlV1, SQLite: sqliteV1}},
	{Version: 2, Schema: ddl.Schema{Component: component, Postgres: postgresV2, MySQL: mysqlV2, SQLite: sqliteV2}},
	{Version: 3, Schema: ddl.Schema{Component: component, Postgres: postgresV3, MySQL: mysqlV3, SQLite: sqliteV3}},
}

// Latest is the version a database is at once it has run everything this
// package ships — what a consumer records beside the migration that ran it, and
// passes to StatementsSince or SQLSince the next time this package adds one.
func Latest() uint64 {
	return sequence.Latest()
}

// Statements renders every version's DDL for the dialect against the given table
// prefix, in version order, split into individually executable statements. It
// is a fresh install: StatementsSince from version 0.
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
// It is what you hand to database/migrate's WithGeneratedMigration, so the table
// is created by the consumer's own migration run instead of being copied into
// their repository.
func SQL(d dialect.Dialect, prefix string) (string, error) {
	return SQLSince(d, prefix, 0)
}

// SQLSince renders the same DDL as StatementsSince, joined back into one
// migration body — what a consumer whose database already has the table hands to
// WithGeneratedMigration as a migration of their own. A database already at
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
// writing a table whose shape it does not know.
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
//
// The indexes are the reason this is not a check on the prefix alone: the
// longest identifier here is the sweeper's index, at 37 bytes before a prefix is
// applied, and a prefix that is a legal name on its own can still push that past
// what an engine accepts — a failure that would otherwise surface as a migration
// that half ran. Every version is vetted rather than the latest, since a
// consumer runs all of them: SQLite's version 3 renames the table aside under a
// name of its own for the length of the rebuild, and that name reaches statement
// text too.
func ValidatePrefix(prefix string) error {
	return sequence.ValidatePrefix(prefix)
}

// Tables returns every table this package creates under prefix.
//
// It reads them out of every version's DDL rather than from a list maintained
// beside it, so a table added in a new version is in this list the moment it is
// added. The name SQLite's version 3 renames the old table to is not here: it is
// never created, only renamed to and dropped within the one version.
func Tables(prefix string) ([]string, error) {
	if err := sequence.ValidatePrefix(prefix); err != nil {
		return nil, err
	}

	return sequence.Tables(prefix), nil
}

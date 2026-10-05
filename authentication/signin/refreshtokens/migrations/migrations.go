/*
Package migrations supplies the sign-in refresh token table's DDL, rendered for
a dialect and table prefix.

# One schema, and what it replaced

v14 shipped this table as a run of four versions — the table as v14.0.0 created
it, then redeemed_with_key and successor_hash, then signed_in_at with its
backfill, then access_token_id, actor_id and credential_kind — because a table
that has shipped is created by CREATE TABLE IF NOT EXISTS, and an edit to that
CREATE reaches a fresh install and no database that already has the table. A new
major is the one place that run may be reset, so this is one schema again: the
final shape the v14 run produced, column order and index names included, and the
first version of whatever run this package grows next.

A database still on the v14 run must be at that run's latest version, 4, before
it takes this one. The schema here is a no-op on a table that already exists and
does not alter one that is behind, so a database at an earlier version would
keep a table missing the later columns and fail on the first mint that names
them.

A change to this table from here on is a new version in a database/ddl
Migrations, under the same rule as before: a shipped version is never edited.

# A fresh install

The platform deliberately does not ship a numbered migration file. Migration
files are numbered globally per consumer, so a platform-owned number would
collide with the consumer's own the moment either side added one. The version a
consumer records these statements under is therefore always the consumer's to
choose.

If you already run database/migrate, hand SQL to WithGeneratedMigration and the
table is created by your normal migration run — no DDL copied into your
repository:

	ddl, err := migrations.SQL(dialect.Postgres, refreshtokens.DefaultTablePrefix)
	// ...
	m, err := migrate.New(dialect.Postgres, myMigrations,
		migrate.WithGeneratedMigration(46, "create_signin_refresh_tokens_table", ddl),
	)

# The rest

Statements is the same DDL split into individually executable statements, the
table before its indexes, for callers running it some other way — a different
migration tool, or a test that just wants the table.

Tables answers which tables exist at a prefix, read out of the DDL, so a
between-tests TRUNCATE, a backup policy, or a privacy inventory names them
without anybody copying a name out of the schema.

The rendering and prefix vetting live in database/ddl, shared with every other
schema-shipping package in this module.
*/
package migrations

import (
	_ "embed"

	"github.com/primandproper/primitives-go/v2/database/ddl"
	"github.com/primandproper/primitives-go/v2/database/dialect"
)

//go:embed postgres.sql
var postgresDDL string

//go:embed mysql.sql
var mysqlDDL string

//go:embed sqlite.sql
var sqliteDDL string

// schema is this package's DDL in each supported dialect.
var schema = ddl.Schema{
	Component: "sign-in refresh token",
	Postgres:  postgresDDL,
	MySQL:     mysqlDDL,
	SQLite:    sqliteDDL,
}

// Statements renders the DDL for the dialect against the given table prefix and
// splits it into individually executable statements, the table before its
// indexes.
func Statements(d dialect.Dialect, prefix string) ([]string, error) {
	return schema.Statements(d, prefix)
}

// SQL renders the same DDL as Statements, joined back into one migration body.
// It is what you hand to database/migrate's WithGeneratedMigration, so the table
// is created by the consumer's own migration run instead of being copied into
// their repository.
func SQL(d dialect.Dialect, prefix string) (string, error) {
	return schema.SQL(d, prefix)
}

// ValidatePrefix reports whether prefix yields a legal SQL identifier for the
// table and every index this package creates.
//
// The indexes are the reason this is not a check on the prefix alone: the
// longest identifier here is the sweeper's index, at 37 bytes before a prefix is
// applied, and a prefix that is a legal name on its own can still push that past
// what an engine accepts — a failure that would otherwise surface as a migration
// that half ran.
func ValidatePrefix(prefix string) error {
	return schema.ValidatePrefix(prefix)
}

// Tables returns every table this package creates under prefix.
//
// It reads them out of the DDL rather than from a list maintained beside it, so
// a table added to the schema is in this list the moment it is added.
func Tables(prefix string) ([]string, error) {
	if err := schema.ValidatePrefix(prefix); err != nil {
		return nil, err
	}

	return schema.Tables(prefix), nil
}

/*
Package migrations supplies the webhook tables' DDL, rendered for a dialect and table prefix.

The platform deliberately does not ship a numbered migration file. Migration
files are numbered globally per consumer, so a platform-owned number would
collide with the consumer's own the moment either side added one. The version is
therefore always the consumer's to choose.

If you already run database/migrate, hand SQL to WithGeneratedMigration and the
tables are created by your normal migration run — no DDL copied into your
repository, nothing to keep in sync as this package evolves:

	ddl, err := migrations.SQL(dialect.Postgres, webhooks.DefaultTablePrefix)
	// ...
	m, err := migrate.New(dialect.Postgres, myMigrations,
		migrate.WithGeneratedMigration(43, "create_webhooks_tables", ddl),
	)

Statements is the same DDL split into individually executable statements, for
callers running it some other way — a different migration tool, or a test that
just wants the tables.

# The scope column has no default

The DDL here only ever creates: every statement is IF NOT EXISTS, so running it
against tables that already exist adds nothing.

scope is NOT NULL with no DEFAULT, which is the one place this schema departs
from the module's habit of defaulting a text column to the empty string. The
empty string is not the absence of a scope here — it is tenancy.Global(), a
scope like any other. A column that supplied it for a write which did not name
one would hand the global scope to whoever forgot the column, which is exactly
the mistake tenancy.Scope is shaped to make unspellable in Go: an unset scope
fails at Value rather than widening a predicate. The column enforces the same
rule for a writer that did not come through SQLStore, and the write fails.

That costs a deployment which already holds webhook rows the ability to add the
column in one statement — ADD COLUMN NOT NULL wants a default when there are
rows to fill. Such a deployment adds the column with a default, backfills the
scope each row belongs to, and drops the default again; a single-tenant one
backfills the empty string. Nothing in this module is in that position today,
and the schema is written for correctness now rather than for a migration
nobody has to perform.

The rendering and prefix vetting live in database/ddl, shared with every other
schema-shipping package in this module.
*/
package migrations

import (
	_ "embed"

	"github.com/primandproper/platform-go/v13/database/ddl"
	"github.com/primandproper/platform-go/v13/database/dialect"
)

//go:embed postgres.sql
var postgresDDL string

//go:embed mysql.sql
var mysqlDDL string

//go:embed sqlite.sql
var sqliteDDL string

// schema is this package's DDL in each supported dialect.
var schema = ddl.Schema{
	Component: "webhooks",
	Postgres:  postgresDDL,
	MySQL:     mysqlDDL,
	SQLite:    sqliteDDL,
}

// Statements renders the DDL for the dialect against the given table prefix and
// splits it into individually executable statements, each table before its
// indexes.
func Statements(d dialect.Dialect, prefix string) ([]string, error) {
	return schema.Statements(d, prefix)
}

// ValidatePrefix reports whether prefix yields a legal SQL identifier for every
// table and index this package creates.
func ValidatePrefix(prefix string) error {
	return schema.ValidatePrefix(prefix)
}

// SQL renders the same DDL as Statements, joined back into one migration body.
// It is what you hand to database/migrate's WithGeneratedMigration, so the
// tables are created by the consumer's own migration run instead of being
// copied into their repository.
func SQL(d dialect.Dialect, prefix string) (string, error) {
	return schema.SQL(d, prefix)
}

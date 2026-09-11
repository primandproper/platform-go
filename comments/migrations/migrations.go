/*
Package migrations supplies the comments table's DDL, rendered for a dialect and
table prefix.

The platform deliberately does not ship a numbered migration file. Migration
files are numbered globally per consumer, so a platform-owned number would
collide with the consumer's own the moment either side added one. The version is
therefore always the consumer's to choose.

If you already run database/migrate, hand SQL to WithGeneratedMigration and the
table is created by your normal migration run — no DDL copied into your
repository, nothing to keep in sync as this package evolves:

	ddl, err := migrations.SQL(dialect.Postgres, comments.DefaultTablePrefix)
	// ...
	m, err := migrate.New(dialect.Postgres, myMigrations,
	    migrate.WithGeneratedMigration(62, "create_comments_table", ddl),
	)

Statements is the same DDL split into individually executable statements, for
callers running it some other way — a different migration tool, or a test that
just wants the table.

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
	Component: "comments",
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

// ValidatePrefix reports whether prefix yields a legal SQL identifier for the
// table and every index this package creates.
func ValidatePrefix(prefix string) error {
	return schema.ValidatePrefix(prefix)
}

// Tables returns the table names this package creates, rendered against prefix
// and sorted.
//
// It is the complete list — comments creates no table this omits, because the
// names are read out of the DDL beside this file rather than from a list
// maintained next to it, so a table added to the schema is in this list the
// moment it is added. The uses are the per-table jobs that are not per-query:
// the TRUNCATE an integration suite runs between tests, a backup policy, a data
// privacy inventory. Every one of them is wrong in a way nothing reports if the
// list is short by one.
//
// The prefix is vetted exactly as [Statements] and [SQL] vet it, and for the
// same reason: these names are interpolated into statement text rather than
// bound, so a caller building a TRUNCATE out of them is building it out of
// whatever this returns.
func Tables(prefix string) ([]string, error) {
	if err := schema.ValidatePrefix(prefix); err != nil {
		return nil, err
	}

	return schema.Tables(prefix), nil
}

// SQL renders the same DDL as Statements, joined back into one migration body.
// It is what you hand to database/migrate's WithGeneratedMigration, so the table
// is created by the consumer's own migration run instead of being copied into
// their repository.
func SQL(d dialect.Dialect, prefix string) (string, error) {
	return schema.SQL(d, prefix)
}

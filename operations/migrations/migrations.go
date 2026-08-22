/*
Package migrations supplies the operations table's DDL, rendered for a table
prefix.

The platform deliberately does not ship a numbered migration file. Migration
files are numbered globally per consumer, so a platform-owned number would
collide with the consumer's own the moment either side added one. The version is
therefore always the consumer's to choose.

If you already run database/migrate, hand SQL to WithGeneratedMigration and the
table is created by your normal migration run — no DDL copied into your
repository, nothing to keep in sync as this package evolves:

	ddl, err := migrations.SQL(dialect.Postgres, operations.DefaultTablePrefix)
	// ...
	m, err := migrate.New(dialect.Postgres, myMigrations,
		migrate.WithGeneratedMigration(50, "create_operations_table", ddl),
	)

Statements is the same DDL split into individually executable statements, for
callers running it some other way — a different migration tool, or a test that
just wants the table.

Postgres only, on the same terms as the package it serves: the schema leans on
partial indexes and a server-side epoch default, and the operations package is
Postgres-only regardless. Passing any other dialect returns
dialect.ErrUnsupported rather than a portable approximation of a schema the
queries would not run against.

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

// schema is this package's DDL. Only the Postgres member is populated; the
// others are absent rather than approximated, so an unsupported dialect is
// reported by the shared renderer instead of by a query failing later.
var schema = ddl.Schema{
	Component: "operations",
	Postgres:  postgresDDL,
}

// requirePostgres refuses a dialect this package has no schema for.
//
// It is here rather than left to the shared renderer because that renderer
// reads the member for the dialect it was asked about, and an absent member is
// an empty string rather than a missing one. Without this, asking for the MySQL
// schema would return no statements and no error — a migration run that creates
// nothing and reports success, which is the failure mode that is only
// discovered by the first query.
func requirePostgres(d dialect.Dialect) error {
	return dialect.RequirePostgres("operations migrations", d)
}

// Statements renders the DDL against the given table prefix and splits it into
// individually executable statements, the table before its indexes.
func Statements(d dialect.Dialect, prefix string) ([]string, error) {
	if err := requirePostgres(d); err != nil {
		return nil, err
	}

	return schema.Statements(d, prefix)
}

// ValidatePrefix reports whether prefix yields a legal SQL identifier for every
// table and index this package creates.
func ValidatePrefix(prefix string) error {
	return schema.ValidatePrefix(prefix)
}

// SQL renders the same DDL as Statements, joined back into one migration body.
// It is what you hand to database/migrate's WithGeneratedMigration, so the
// operations table is created by the consumer's own migration run instead of
// being copied into their repository.
func SQL(d dialect.Dialect, prefix string) (string, error) {
	if err := requirePostgres(d); err != nil {
		return "", err
	}

	return schema.SQL(d, prefix)
}

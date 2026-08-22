/*
Package migrations supplies the work queue table's DDL, rendered for a table
prefix.

The platform deliberately does not ship a numbered migration file. Migration
files are numbered globally per consumer, so a platform-owned number would
collide with the consumer's own the moment either side added one. The version is
therefore always the consumer's to choose.

If you already run database/migrate, hand SQL to WithGeneratedMigration and the
table is created by your normal migration run — no DDL copied into your
repository, nothing to keep in sync as this package evolves:

	ddl, err := migrations.SQL(workqueue.DefaultTablePrefix)
	// ...
	m, err := migrate.New(dialect.Postgres, myMigrations,
		migrate.WithGeneratedMigration(41, "create_work_queue_tables", ddl),
	)

Statements is the same DDL split into individually executable statements, for
callers running it some other way — a different migration tool, or a test that
just wants the table.

One table holds every logical queue: workqueue.Config.Name is the leading column
of its primary key, so a second queue needs a second Config, not a second
migration.

Postgres only, like the package it serves. Statements and SQL take a dialect
anyway, and reject anything else, so a caller wiring this into a
dialect-parameterized migration run gets an error naming the dialect rather than
a schema that silently renders empty.
*/
package migrations

import (
	_ "embed"

	"github.com/primandproper/platform-go/v13/database/ddl"
	"github.com/primandproper/platform-go/v13/database/dialect"
)

//go:embed postgres.sql
var postgresDDL string

// schema is this package's DDL. Only the Postgres body is populated; see
// requirePostgres for why the other two are rejected rather than left to render
// as nothing.
var schema = ddl.Schema{
	Component: "workqueue",
	Postgres:  postgresDDL,
}

// requirePostgres rejects the dialects this package has no schema for.
//
// ddl.Schema returns an empty body rather than an error for a dialect whose
// field is unset, which would leave a caller with zero statements and no
// indication that nothing had been created. This turns that silence into the
// error it should have been.
func requirePostgres(d dialect.Dialect) error {
	return dialect.RequirePostgres("workqueue migration", d)
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
// It is what you hand to database/migrate's WithGeneratedMigration, so the queue
// table is created by the consumer's own migration run instead of being copied
// into their repository.
func SQL(d dialect.Dialect, prefix string) (string, error) {
	if err := requirePostgres(d); err != nil {
		return "", err
	}

	return schema.SQL(d, prefix)
}

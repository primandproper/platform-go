/*
Package migrations supplies the password reset token table's DDL, rendered for a
dialect and table prefix.

The platform deliberately does not ship a numbered migration file. Migration
files are numbered globally per consumer, so a platform-owned number would
collide with the consumer's own the moment either side added one. The version is
therefore always the consumer's to choose.

If you already run database/migrate, hand SQL to WithGeneratedMigration and the
table is created by your normal migration run — no DDL copied into your
repository, nothing to keep in sync as this package evolves:

	ddl, err := migrations.SQL(dialect.Postgres, passwordreset.DefaultTablePrefix)
	// ...
	m, err := migrate.New(dialect.Postgres, myMigrations,
		migrate.WithGeneratedMigration(45, "create_password_reset_tokens_table", ddl),
	)

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

	"github.com/primandproper/primitives-go/database/ddl"
	"github.com/primandproper/primitives-go/database/dialect"
)

//go:embed postgres.sql
var postgresDDL string

//go:embed mysql.sql
var mysqlDDL string

//go:embed sqlite.sql
var sqliteDDL string

// schema is this package's DDL in each supported dialect.
var schema = ddl.Schema{
	Component: "password reset",
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
// table and the three indexes this package creates.
//
// The indexes are the reason this is not a check on the prefix alone: the
// longest identifier here is the expiry index's, and a prefix that is a legal
// name on its own can still push that past what an engine accepts — a failure
// that would otherwise surface as a migration that half ran.
func ValidatePrefix(prefix string) error {
	return schema.ValidatePrefix(prefix)
}

// Tables returns every table this package creates under prefix.
//
// It reads them out of the DDL rather than from a list maintained beside it, so
// a table added to the .sql files is in this list the moment it is added.
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

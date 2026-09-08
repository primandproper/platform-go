/*
Package migrations supplies the upload registry's DDL, rendered for a dialect
and table prefix.

The platform deliberately does not ship a numbered migration file. Migration
files are numbered globally per consumer, so a platform-owned number would
collide with the consumer's own the moment either side added one. The version is
therefore always the consumer's to choose.

If you already run database/migrate, hand SQL to WithGeneratedMigration and the
table is created by your normal migration run — no DDL copied into your
repository, nothing to keep in sync as this package evolves:

	ddl, err := migrations.SQL(dialect.Postgres, registry.DefaultTablePrefix)
	// ...
	m, err := migrate.New(dialect.Postgres, myMigrations,
		migrate.WithGeneratedMigration(51, "create_uploads_objects", ddl),
	)

Statements is the same DDL split into individually executable statements, the
table before its indexes, for callers running it some other way — a different
migration tool, or a test that just wants the table.

# One table, and why the list is still read out of the DDL

Tables answers which tables this package creates, at your prefix, and the answer
comes out of the DDL rather than from a list beside it. That is worth doing for
one table for the same reason it is worth doing for seven: the uses are the
per-table jobs that are not per-query — the TRUNCATE an integration suite runs
between tests, a backup policy, a schema audit, a data privacy inventory — and a
list maintained by hand is one a second table joins without.

# The registry stores rows, not bytes

Nothing here creates a bucket, and nothing here removes an object. The row
references the key uploads wrote and no more; archiving a row is metadata-only,
which is why the unique index on (scope, object_key) covers archived rows —
their bytes are still in the bucket until the consumer's retention policy
removes them. See the retention package for the other half.

# created_at has a default, and scope deliberately does not

created_at is NOT NULL with a dialect-appropriate DEFAULT — NOW() on Postgres,
CURRENT_TIMESTAMP(6) on MySQL, CURRENT_TIMESTAMP on SQLite — because the row's
creation time is the database's rather than the application's. Two application
instances whose clocks differ by a second would otherwise write rows that a
created_after filter excludes at random, and a creation time that disagrees with
the row's id disagrees with the order a cursor walk pages in.

scope is NOT NULL with no DEFAULT, which is the one place this schema departs
from the module's habit of defaulting a text column to the empty string. The
empty string is not the absence of a scope here — it is tenancy.Global(), a
scope like any other. A column that supplied it for a write which did not name
one would hand the global scope to whoever forgot the column, which is exactly
the mistake tenancy.Scope is shaped to make unspellable in Go. The column
enforces the same rule for a writer that did not come through SQLStore, and the
write fails.

# owner_id and the belongs-to pair have no foreign keys

Neither could have one. owner_id names whoever the consumer's authorization
model calls a principal — a user row in identity, a service account, an API key
— and this package does not know which table that is. The belongs-to pair is a
reference into a table this package has never heard of by construction: its
whole point is that a receipt can hang off an invoice in the consumer's own
schema. A registry that could only reference identity would be a registry only
identity's consumers could use.

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
	Component: "uploads registry",
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
// It is the complete list — the names are read out of the DDL beside this file
// rather than from a list maintained next to it, so a table added to the schema
// is in this list the moment it is added.
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

/*
Package migrations supplies the settings tables' DDL, rendered for a dialect and
table prefix.

The platform deliberately does not ship a numbered migration file. Migration
files are numbered globally per consumer, so a platform-owned number would
collide with the consumer's own the moment either side added one. The version is
therefore always the consumer's to choose.

If you already run database/migrate, hand SQL to WithGeneratedMigration and the
tables are created by your normal migration run — no DDL copied into your
repository, nothing to keep in sync as this package evolves:

	ddl, err := migrations.SQL(dialect.Postgres, settings.DefaultTablePrefix)
	// ...
	m, err := migrate.New(dialect.Postgres, myMigrations,
		migrate.WithGeneratedMigration(51, "create_settings_tables", ddl),
	)

Statements is the same DDL split into individually executable statements, for
callers running it some other way — a different migration tool, or a test that
just wants the tables.

# Which tables this package creates

Tables answers that, at your prefix, and the answer is complete: the names come
out of the DDL rather than from a list beside it, so a table added to this schema
is in that list the moment it is added.

	names, err := migrations.Tables(settings.DefaultTablePrefix)

Reach for it wherever the job is per-table but not per-query — the TRUNCATE
between integration tests, a backup policy, a schema inventory, a data privacy
audit. The alternative is copying three names out of the .sql files, which is a
list that goes stale silently: nothing reports a table left out of a maintenance
TRUNCATE except a test failing somewhere else on rows the previous one left.

# What a consumer adds beside these tables

Columns of their own, in a table of their own keyed by the value's id or by the
subject. This package owns its tables and will not grow another column on
request, because the moment the schema is configurable it stops being ownable —
and owning it is what a consumer is adopting.

The one that comes up is a note against a stored value: why somebody chose it,
who asked for it, a ticket number. That is an annotation about a value rather
than a value, it is written and read by the consumer alone, and nothing here
would ever interpret it — so it belongs in the consumer's own row keyed on the
value's id, the same arrangement identity documents for a user's extra profile
columns.

# created_at has a default, and scope deliberately does not

created_at is NOT NULL with a dialect-appropriate DEFAULT — NOW() on Postgres,
CURRENT_TIMESTAMP(6) on MySQL, CURRENT_TIMESTAMP on SQLite — because the row's
creation time is the database's rather than the application's. Two application
instances whose clocks differ by a second would otherwise write rows that a
created_after filter excludes at random, and a creation time that disagrees with
the row's id disagrees with the order a cursor walk pages in. The settings store
does not supply the column, and reads it back so the value a caller holds is the
value in the row.

The SQLite spelling is also what makes that column filterable there. SQLite has
no date type, so a window comparison over it is lexicographic text — and
CURRENT_TIMESTAMP is what writes the UTC YYYY-MM-DD HH:MM:SS shape that
lexicographic order agrees with. A caller-bound time.Time reaches that column as
Go's own String() rendering instead, which sorts correctly by accident of its
prefix rather than by design.

scope is NOT NULL with no DEFAULT, which is the one place this schema departs
from the module's habit of defaulting a text column to the empty string. The
empty string is not the absence of a scope here — it is tenancy.Global(), a
scope like any other. A column that supplied it for a write which did not name
one would hand the global scope to whoever forgot the column, which is exactly
the mistake tenancy.Scope is shaped to make unspellable in Go: an unset scope
fails at Value rather than widening a predicate.

# default_value is the one nullable text column, and that is the feature

Every other text column here is NOT NULL, most with a DEFAULT ”. default_value
is nullable because a definition with no default and a definition whose default
is the empty string are different definitions — the first answers no subject
that has not chosen, the second answers all of them — and a NOT NULL column has
nowhere to put the difference. It is the storage half of what settings.Resolution
reports as [settings.SourceUnset].

# Both child references cascade

settings_definition_options and settings_values reference the definition they
hang off with ON DELETE CASCADE, and the cascade is the point rather than a
default. A definition is only ever hard-deleted deliberately — archiving it is
the ordinary way to retire one — and a deletion that left values behind would
leave rows interpreted against a definition that no longer says what kind they
are or which of them are legal.

The DDL here only ever creates: every CREATE TABLE is IF NOT EXISTS, so running
it against tables that already exist adds nothing. The MySQL index statements
are the exception and are not conditional, because MySQL has no CREATE INDEX IF
NOT EXISTS; a re-run there reports a duplicate key name rather than silently
succeeding, which is the same behavior every other schema-shipping package in
this module has.

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
	Component: "settings",
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

// Tables returns the three table names this package creates, rendered against
// prefix and sorted.
//
// It is the complete list — settings creates no table this omits, because the
// names are read out of the DDL beside this file rather than from a list
// maintained next to it, so a table added to the schema is in this list the
// moment it is added. That is what a consumer needs it to be: the uses are the
// per-table jobs that are not per-query — the TRUNCATE an integration suite runs
// between tests, a backup policy, a schema audit, a data privacy inventory — and
// every one of them is wrong in a way nothing reports if the list is short by
// one.
//
// The prefix is vetted exactly as [Statements] and [SQL] vet it, and for the
// same reason: these names are interpolated into statement text rather than
// bound, so a caller building a TRUNCATE out of them is building it out of
// whatever this returns.
//
// It is not an ordering a caller can delete in. Both child tables reference the
// definitions table, so a consumer clearing these tables wants the dialect's own
// way of ignoring the constraints rather than a sequence read off this slice.
func Tables(prefix string) ([]string, error) {
	if err := schema.ValidatePrefix(prefix); err != nil {
		return nil, err
	}

	return schema.Tables(prefix), nil
}

// SQL renders the same DDL as Statements, joined back into one migration body.
// It is what you hand to database/migrate's WithGeneratedMigration, so the
// tables are created by the consumer's own migration run instead of being
// copied into their repository.
func SQL(d dialect.Dialect, prefix string) (string, error) {
	return schema.SQL(d, prefix)
}

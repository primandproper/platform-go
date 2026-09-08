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

# Upgrading

UpgradeStatements and UpgradeSQL render the one schema change this package has
made since it shipped: webhooks_subscriptions became identified, archivable rows
rather than a bare (endpoint_id, event_type) mapping, and webhooks_endpoints
gained the name and created_by columns. A deployment created by Statements as it
stands today does not need it; a deployment that already holds subscription rows
written against the older shape does, and it is the migration for them.

	up, err := migrations.UpgradeSQL(dialect.Postgres, webhooks.DefaultTablePrefix)
	// ...
	m, err := migrate.New(dialect.Postgres, myMigrations,
		migrate.WithGeneratedMigration(44, "upgrade_webhooks_subscriptions", up),
	)

It is one-shot where Statements is re-runnable, and the difference is not
stylistic. Statements is CREATE ... IF NOT EXISTS throughout, so running it
against tables that already exist adds nothing. ALTER TABLE has no such spelling
in MySQL or SQLite — neither has ADD COLUMN IF NOT EXISTS — so a second run
raises a duplicate-column error rather than doing nothing. Hand it to the
migration runner that records which migrations have run, which is what one is
for, and do not put it on a startup path that executes the DDL every boot.

The data it moves is the flat subscription list: every existing row is given an
identifier and a creation time backfilled from the endpoint it belongs to, which
is when it was really subscribed. Nothing is deleted and no row changes which
endpoint or event type it names, so the fan-out query answers the same question
before and after.

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

//go:embed upgrade_postgres.sql
var postgresUpgradeDDL string

//go:embed upgrade_mysql.sql
var mysqlUpgradeDDL string

//go:embed upgrade_sqlite.sql
var sqliteUpgradeDDL string

// schema is this package's DDL in each supported dialect.
var schema = ddl.Schema{
	Component: "webhooks",
	Postgres:  postgresDDL,
	MySQL:     mysqlDDL,
	SQLite:    sqliteDDL,
}

// upgrade is the DDL that moves an older schema to the one schema creates. It
// is a Schema of its own rather than more statements in that one because the two
// have opposite preconditions: schema runs against a database that may have
// nothing, upgrade against one that must already have the tables.
var upgrade = ddl.Schema{
	Component: "webhooks upgrade",
	Postgres:  postgresUpgradeDDL,
	MySQL:     mysqlUpgradeDDL,
	SQLite:    sqliteUpgradeDDL,
}

// Statements renders the DDL for the dialect against the given table prefix and
// splits it into individually executable statements, each table before its
// indexes.
func Statements(d dialect.Dialect, prefix string) ([]string, error) {
	return schema.Statements(d, prefix)
}

// ValidatePrefix reports whether prefix yields a legal SQL identifier for every
// table and index this package creates.
//
// The upgrade DDL is vetted too. It names an index the create DDL also names, so
// today the two identifier sets agree — but a prefix that is legal for one and
// not the other would be a prefix a deployment could create tables under and
// then fail to migrate, which is the worse half to find out about late.
func ValidatePrefix(prefix string) error {
	if err := schema.ValidatePrefix(prefix); err != nil {
		return err
	}

	return upgrade.ValidatePrefix(prefix)
}

// Tables lists every table this schema creates, rendered at the given prefix
// and sorted.
//
// It is read from the DDL rather than declared beside it, so a table added to
// the schema and to nothing else still appears — which is what makes it worth
// cross-checking against the canonical names webhooks/internal/queries spells
// for the statements. Neither list derives from the other, and that is where a
// table added to one and not the other stops being invisible.
//
// It is not an ordering a caller can delete in. Foreign keys make deletion order
// a fact about the schema — a dispatch references its delivery, and a
// subscription its endpoint — so a consumer clearing these tables wants the
// dialect's own way of ignoring the constraints rather than a sequence read off
// this slice.
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

// UpgradeStatements renders the schema upgrade for the dialect against the given
// table prefix, split into individually executable statements.
//
// It is for a deployment whose webhook tables predate subscriptions being rows.
// A deployment created by Statements as it stands does not need it.
//
// Unlike Statements it is one-shot: MySQL and SQLite have no ADD COLUMN IF NOT
// EXISTS, so a second run raises a duplicate-column error rather than doing
// nothing. Run it through a migration runner that records what has run.
func UpgradeStatements(d dialect.Dialect, prefix string) ([]string, error) {
	return upgrade.Statements(d, prefix)
}

// UpgradeSQL renders the same DDL as UpgradeStatements, joined back into one
// migration body, for database/migrate's WithGeneratedMigration.
func UpgradeSQL(d dialect.Dialect, prefix string) (string, error) {
	return upgrade.SQL(d, prefix)
}

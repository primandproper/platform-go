/*
Package migrations supplies the billing tables' DDL, rendered for a dialect and
table prefix.

The platform deliberately does not ship a numbered migration file. Migration
files are numbered globally per consumer, so a platform-owned number would
collide with the consumer's own the moment either side added one. The version is
therefore always the consumer's to choose.

If you already run database/migrate, hand SQL to WithGeneratedMigration and the
tables are created by your normal migration run — no DDL copied into your
repository, nothing to keep in sync as this package evolves:

	ddl, err := migrations.SQL(dialect.Postgres, billing.DefaultTablePrefix)
	// ...
	m, err := migrate.New(dialect.Postgres, myMigrations,
		migrate.WithGeneratedMigration(53, "create_billing_tables", ddl),
	)

Statements is the same DDL split into individually executable statements, for
callers running it some other way — a different migration tool, or a test that
just wants the tables.

# Which tables this package creates

Tables answers that, at your prefix, and the answer is complete: the names come
out of the DDL rather than from a list beside it, so a table added to this schema
is in that list the moment it is added.

	names, err := migrations.Tables(billing.DefaultTablePrefix)

Reach for it wherever the job is per-table but not per-query — the TRUNCATE
between integration tests, a backup policy, a schema inventory, a data privacy
audit. The alternative is copying four names out of the .sql files, which is a
list that goes stale silently.

# created_at has a default, and scope deliberately does not

created_at is NOT NULL with a dialect-appropriate DEFAULT — NOW() on Postgres,
CURRENT_TIMESTAMP(6) on MySQL, CURRENT_TIMESTAMP on SQLite — because the row's
creation time is the database's rather than the application's. Two application
instances whose clocks differ by a second would otherwise write rows that a
created_after filter excludes at random, and a creation time that disagrees with
the row's id disagrees with the order a cursor walk pages in. The billing store
does not supply the column, and reads it back so the value a caller holds is the
value in the row.

scope is NOT NULL with no DEFAULT, which is the one place this schema departs
from the module's habit of defaulting a text column to the empty string. The
empty string is not the absence of a scope here — it is tenancy.Global(), a
scope like any other. A column that supplied it for a write which did not name
one would hand the global scope to whoever forgot the column, which is exactly
the mistake tenancy.Scope is shaped to make unspellable in Go.

# The three external id columns are nullable, and that is what makes them unique

external_product_id, external_subscription_id and external_transaction_id are
each nullable rather than NOT NULL DEFAULT ”, which is the module's habit for
text, and each carries a unique index paired with the scope.

The two facts are one decision. A free tier, a comped plan, and a subscription
granted by hand are all rows a deployment writes and never mirrors to a payment
provider, so "no provider-side counterpart" is a genuine absence rather than a
value. Under the empty string the second such row would collide with the first,
and the only way out would be dropping the uniqueness — which is the thing worth
having, because it is what makes a redelivered webhook collide instead of
recording one charge twice. All three engines treat NULLs in a unique index as
distinct, so the nullable column keeps the guarantee for the rows that have a
provider id and stays out of the way of the rows that do not.

None of the three is partial. The uniqueness covers archived rows because an
archived row still points at the provider's, and a key freed on archive is how a
second row ends up claiming something the first is still reconciled against.

# Prices are restated, not joined

billing_purchases and billing_transactions each carry their own amount_cents and
currency rather than reading them through product_id. A price is a fact about
the moment of sale: repricing a product must not rewrite what somebody already
paid, and a partial refund is a transaction whose amount was never any product's
price at all.

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
	Component: "billing",
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

// Tables returns the four table names this package creates, rendered against
// prefix and sorted.
//
// It is the complete list — billing creates no table this omits, because the
// names are read out of the DDL beside this file rather than from a list
// maintained next to it. That is what a consumer needs it to be: the uses are
// the per-table jobs that are not per-query — the TRUNCATE an integration suite
// runs between tests, a backup policy, a schema audit, a data privacy inventory
// — and every one of them is wrong in a way nothing reports if the list is short
// by one.
//
// The prefix is vetted exactly as [Statements] and [SQL] vet it, and for the
// same reason: these names are interpolated into statement text rather than
// bound, so a caller building a TRUNCATE out of them is building it out of
// whatever this returns.
//
// It is not an ordering a caller can delete in. Three of these tables reference
// another, so a consumer clearing them wants the dialect's own way of ignoring
// the constraints rather than a sequence read off this slice.
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

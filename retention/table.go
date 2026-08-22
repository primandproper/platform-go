package retention

import (
	"context"
	"fmt"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
)

// DefaultKeyColumn is the column a batch is bounded by when a Table does not
// name one.
const DefaultKeyColumn = "id"

var _ Target = Table{}

// Table is the declarative Target: a table, the timestamp column age is
// measured from, and the key column a batch is bounded by.
//
// It is a value type with no unexported state, so a policy set reads as data
// and can be written down in one place:
//
//	retention.Table{Name: "webhook_delivery_attempts", Column: "created_at"}
//
// A row whose Column is NULL is never deleted. That falls out of SQL — a
// comparison against NULL is never true — but it is worth stating, because it
// is also the right behavior: a row that has not recorded when it started
// cannot be aged out, and a policy that guessed would be deleting on the basis
// of a missing field.
//
// # There is no predicate field
//
// The obvious next field is a WHERE fragment, and it is deliberately absent.
// Everything else here is vetted as a SQL identifier at construction, which is
// what makes interpolating these names into query text safe; a free-text
// predicate cannot be, and configuration is exactly the path by which one would
// arrive unreviewed. A policy that needs a predicate needs Go: implement Target,
// and use this type's methods as the reference.
type Table struct {
	// Name is the table rows are deleted from. It may be schema-qualified
	// ("archive.captures") and must otherwise be a plain SQL identifier: it is
	// interpolated into the query text, not bound, so it is restricted rather
	// than escaped. Required.
	Name string

	// Column is the timestamp column age is measured from — a created_at for a
	// retention window, an expires_at for a grace period. Required.
	Column string

	// KeyColumn is the column a batch is selected by, and should be the primary
	// key. Defaults to DefaultKeyColumn.
	//
	// It exists because a bounded DELETE is not portable: Postgres has no
	// DELETE ... LIMIT, so a batch there is a DELETE against the keys a bounded
	// SELECT chose. Any column that uniquely identifies a row will do.
	KeyColumn string
}

// Describe names the table, for telemetry and for the audit entry.
func (t Table) Describe() string {
	return t.Name
}

// key is KeyColumn or the default.
func (t Table) key() string {
	if t.KeyColumn == "" {
		return DefaultKeyColumn
	}

	return t.KeyColumn
}

// Validate vets the dialect and the three identifiers.
//
// It runs at construction so that a typo in a table name is a process that does
// not start, rather than a policy that reports an error every night into a log
// nobody reads. The identifiers are checked rather than quoted because they are
// interpolated into query text: dialect.ValidIdentifier is the same gate the
// rest of this module's SQL-emitting packages put their table names through.
func (t Table) Validate(d dialect.Dialect) error {
	if !d.Valid() {
		return platformerrors.Wrapf(dialect.ErrUnsupported, "retention dialect %q", d)
	}

	if !dialect.ValidIdentifier(t.Name) {
		return platformerrors.Wrapf(dialect.ErrInvalidIdentifier, "retention table %q", t.Name)
	}

	for _, column := range []string{t.Column, t.key()} {
		if !dialect.ValidIdentifier(column) {
			return platformerrors.Wrapf(dialect.ErrInvalidIdentifier, "retention column %q on table %q", column, t.Name)
		}
	}

	return nil
}

// Sweep deletes at most limit rows whose Column is at or before cutoff.
//
// The oldest rows go first. Ordering costs the query an index scan it would
// otherwise be free of, and buys the property that matters when a table is
// behind: successive batches make monotonic progress through the backlog rather
// than deleting an arbitrary limit rows from anywhere in it, which is what
// makes the backlog gauge mean something between one sweep and the next.
func (t Table) Sweep(
	ctx context.Context,
	q database.SQLQueryExecutor,
	d dialect.Dialect,
	cutoff time.Time,
	limit int,
) (int64, error) {
	query, args := t.buildDelete(d, cutoff, limit)

	result, err := q.ExecContext(ctx, query, args...)
	if err != nil {
		return 0, platformerrors.Wrapf(err, "deleting expired rows from %q", t.Name)
	}

	removed, err := result.RowsAffected()
	if err != nil {
		return 0, platformerrors.Wrapf(err, "counting expired rows deleted from %q", t.Name)
	}

	return removed, nil
}

// Backlog counts the rows still at or before cutoff, saturating at ceiling.
func (t Table) Backlog(
	ctx context.Context,
	q database.SQLQueryExecutor,
	d dialect.Dialect,
	cutoff time.Time,
	ceiling int,
) (int64, error) {
	// Counted through a bounded subquery rather than with a plain COUNT, so the
	// cost of the reading does not grow with the size of the problem it is
	// reporting. The alias is not decoration: Postgres and MySQL both require a
	// derived table to have one.
	query := fmt.Sprintf(
		"SELECT COUNT(*) FROM (SELECT 1 FROM %s WHERE %s <= %s LIMIT %s) AS retention_backlog",
		t.Name, t.Column, d.Placeholder(1), d.Placeholder(2),
	)

	var backlog int64
	if err := q.QueryRowContext(ctx, query, cutoff.UTC(), ceiling).Scan(&backlog); err != nil {
		return 0, platformerrors.Wrapf(err, "counting expired rows remaining in %q", t.Name)
	}

	return backlog, nil
}

// buildDelete renders the bounded delete for a dialect.
//
// MySQL is the odd one out in the useful direction: it accepts ORDER BY and
// LIMIT on a DELETE, which is both simpler and cheaper than the alternative,
// and it refuses to read from the table it is deleting from in an IN subquery
// (error 1093) — so the portable form is not available there anyway. Postgres
// and SQLite have no DELETE ... LIMIT, so a batch there is a DELETE against the
// keys a bounded SELECT chose.
func (t Table) buildDelete(d dialect.Dialect, cutoff time.Time, limit int) (query string, args []any) {
	args = []any{cutoff.UTC(), limit}

	if d == dialect.MySQL {
		return fmt.Sprintf(
			"DELETE FROM %s WHERE %s <= %s ORDER BY %s LIMIT %s",
			t.Name, t.Column, d.Placeholder(1), t.Column, d.Placeholder(2),
		), args
	}

	return fmt.Sprintf(
		"DELETE FROM %s WHERE %s IN (SELECT %s FROM %s WHERE %s <= %s ORDER BY %s LIMIT %s)",
		t.Name, t.key(), t.key(), t.Name, t.Column, d.Placeholder(1), t.Column, d.Placeholder(2),
	), args
}

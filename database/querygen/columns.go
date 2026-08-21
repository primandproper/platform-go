package querygen

import (
	"fmt"
	"slices"

	"github.com/primandproper/platform-go/v12/filtering"
)

// The columns this module has opinions about. A table is free to hold any others
// it likes; these are the ones whose presence changes what gets emitted, and
// whose names are spelled here rather than in each generator so that a table
// calling its soft-delete column something else is a table this package does not
// claim to serve.
const (
	// IDColumn is the primary key, and also the pagination cursor. Both roles
	// require it to sort by creation time — an xid or a ULID, not a serial and
	// not a UUIDv4 — because a keyset walk over an id that does not sort that
	// way pages in an order nobody asked for.
	IDColumn = "id"
	// CreatedAtColumn carries the row's creation time and bounds the
	// created_after/created_before window.
	CreatedAtColumn = "created_at"
	// LastUpdatedAtColumn is NULL until the row is first updated, which is why
	// every predicate over it admits NULL explicitly.
	LastUpdatedAtColumn = "last_updated_at"
	// ArchivedAtColumn is the soft delete. Rows are archived rather than
	// deleted, so every read filters on it and no write removes a row.
	ArchivedAtColumn = "archived_at"
	// LastIndexedAtColumn records when a row was last written to a search
	// index. Its presence is what marks a table as one search/sync mirrors,
	// and it brings two statements with it: the scan a reindex walks, and the
	// bulk stamp that maintains the column — see IndexStampQuery.
	LastIndexedAtColumn = "last_indexed_at"
	// BelongsToAccountColumn is the conventional owner of a tenant-scoped row.
	// It is a name, not a behavior: scoping queries by it is WithOwnership's
	// job, because whether a table's rows are readable across accounts is a
	// decision about that table and not something to infer from a column.
	BelongsToAccountColumn = "belongs_to_account"
)

// The sqlc argument names the emitted queries bind. They are the SQL-side
// spelling of filtering.QueryFilter — see the package comment for the mapping
// between these, the struct fields, and the URL parameters.
//
// They are aliases rather than literals because filtering.Bind produces the
// values these arguments take, and a name is only useful if the statement and
// the binding agree on it. Spelled in both places they could disagree, and the
// failure would be silent: a value bound under a name no statement mentions
// binds nothing and filters nothing, which is what a filter nobody set looks
// like. Spelled once, adding a window argument is one edit and both halves
// follow.
const (
	CursorArg          = filtering.ArgCursor
	LimitArg           = filtering.ArgResultLimit
	IncludeArchivedArg = filtering.ArgIncludeArchived
	CreatedAfterArg    = filtering.ArgCreatedAfter
	CreatedBeforeArg   = filtering.ArgCreatedBefore
	UpdatedAfterArg    = filtering.ArgUpdatedAfter
	UpdatedBeforeArg   = filtering.ArgUpdatedBefore
)

// IDsArg is the sqlc argument the bulk stamp binds its id list through. It is
// not one of the filter arguments above — nothing in filtering.QueryFilter
// takes a set of ids — so it is spelled separately rather than smuggled into
// their block.
const IDsArg = "ids"

// NowExpression is how the emitted SQL asks for the current time.
//
// The server's clock, never the application's. A row's created_at and a
// filter's created_after are compared against each other, so they have to come
// from the same clock; two application instances whose clocks differ by a second
// would otherwise write rows that a window excludes at random.
//
// It is a constant rather than a Generator method because all three dialects
// accept the standard spelling: Postgres and MySQL both treat CURRENT_TIMESTAMP
// as the same function they spell NOW(), and SQLite has only this one. Arithmetic
// on it is where they part company — see Generator.timeHorizon.
const NowExpression = "CURRENT_TIMESTAMP"

// databaseOwnedColumns are the columns the database fills in and a caller must
// never supply. Each is set by a statement this package emits: three of them by
// the create, update and archive, and last_indexed_at by the stamp a
// search/sync Syncer issues once the index has accepted a document.
//
// Letting a caller pass created_at is not a small liberty: it is how a row ends
// up with a creation time that disagrees with its id, and the cursor walk orders
// by id while the window filters on created_at.
var databaseOwnedColumns = []string{
	ArchivedAtColumn,
	CreatedAtColumn,
	LastUpdatedAtColumn,
	LastIndexedAtColumn,
}

// ForInsert returns the columns an INSERT takes values for: everything but the
// database-owned ones, and anything else the caller names.
//
// Order is preserved, because an INSERT's column list and its VALUES list are
// positional and have to be rendered from the same slice.
func ForInsert(columns []string, exceptions ...string) []string {
	return without(columns, append(slices.Clone(databaseOwnedColumns), exceptions...)...)
}

// ForUpdate returns the columns an UPDATE assigns: ForInsert's set, less the id.
//
// The id is excluded because an UPDATE keys on it. A SET that assigns the column
// the WHERE matches on is a row that changes its own identity mid-statement,
// which is legal SQL and never what anyone meant.
func ForUpdate(columns []string, exceptions ...string) []string {
	return ForInsert(columns, append(slices.Clone(exceptions), IDColumn)...)
}

// Qualify renders a column as table.column.
func Qualify(table, column string) string {
	return fmt.Sprintf("%s.%s", table, column)
}

// QualifyAll renders every column as table.column, preserving order.
func QualifyAll(table string, columns []string) []string {
	out := make([]string, 0, len(columns))
	for _, column := range columns {
		out = append(out, Qualify(table, column))
	}

	return out
}

// without returns the elements of columns that are not in excluded, in order.
func without(columns []string, excluded ...string) []string {
	out := make([]string, 0, len(columns))

	for _, column := range columns {
		if !slices.Contains(excluded, column) {
			out = append(out, column)
		}
	}

	return out
}

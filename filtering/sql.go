package filtering

import (
	"database/sql"

	"github.com/primandproper/platform-go/v12/database"
)

// The SQL-side spelling of QueryFilter: the argument names a filtered read
// binds its window through.
//
// They live here rather than beside the SQL that consumes them because both
// halves of a filtered read now read them from one place — database/querygen
// emits the statements that name these arguments and aliases these constants,
// and Bind produces the values those arguments take. A new window argument is
// therefore added to this list once, and both the statement and the binding
// follow from it; two lists could disagree, and a binding keyed on a name no
// statement mentions binds nothing and filters nothing, which looks exactly
// like a filter nobody set.
//
// The names are snake_case because that is what sqlc reads out of
// `sqlc.arg(created_after)` and what it derives a generated Go field from. They
// are not the URL parameter names — those are the QueryKey constants — and the
// two are deliberately allowed to differ, since one is a wire format and the
// other is a statement's vocabulary.
const (
	ArgCursor          = "cursor"
	ArgResultLimit     = "result_limit"
	ArgIncludeArchived = "include_archived"
	ArgCreatedAfter    = "created_after"
	ArgCreatedBefore   = "created_before"
	ArgUpdatedAfter    = "updated_after"
	ArgUpdatedBefore   = "updated_before"
)

// SQLArgs is a QueryFilter's window in the types a database driver takes: the
// seven values a filtered read binds, converted once.
//
// It is a plain struct with no method a caller has to find, because what the
// caller does with it is copy its fields across into whatever params struct
// their query generator produced. That is the whole design: sqlc names those
// fields and this package does not get to, so requiring a consumer's generated
// struct to embed a platform type — or to be any particular shape at all —
// would strand every consumer whose generator disagrees. Seven assignments from
// a value that already holds the right things is what is left, and the seven
// conversions stop being seven decisions.
//
// Every field is nullable because absence is what an unset filter field means,
// and the emitted predicates coalesce a NULL bound to a horizon that admits
// everything. The exception is ResultLimit, which Bind always fills — see
// Bind's own comment for why an absent page size is answered here rather than
// left to the statement.
type SQLArgs struct {
	// CreatedAfter and CreatedBefore bound the row's creation time; an invalid
	// one is an open end of the window.
	CreatedAfter  sql.NullTime
	CreatedBefore sql.NullTime

	// UpdatedAfter and UpdatedBefore bound the row's last update. The column
	// they compare against is NULL until the row is first edited, which the
	// emitted predicate admits explicitly.
	UpdatedAfter  sql.NullTime
	UpdatedBefore sql.NullTime

	// Cursor is the keyset position the page resumes after. Invalid is the
	// first page.
	Cursor sql.NullString

	// ResultLimit is the page size, always valid — see Bind.
	ResultLimit sql.NullInt32

	// IncludeArchived admits soft-deleted rows. Invalid reads as false, which
	// is what the emitted COALESCE makes of it.
	//
	// This is the field a hand-written params literal is likeliest to leave
	// out, and leaving it out is silent: the query still runs, still returns
	// rows, and serves archived ones to a filter that asked for live ones.
	// Nothing fails and nothing logs.
	IncludeArchived sql.NullBool
}

// Bind converts a QueryFilter into the driver-typed values a filtered read
// takes, applying the nil-default and the page-size clamp once.
//
// A nil filter binds the defaults, so a caller that took none still produces a
// bindable statement rather than a nil dereference three frames further down.
//
// A caller whose driver takes its arguments by name rather than a generated
// struct by field has BindValues, which is these same values keyed by the
// names the emitted statements bind them under.
//
// ResultLimit is always valid. An absent page size becomes
// DefaultQueryFilterLimit here rather than being left as a NULL for the
// statement to coalesce, because only two of the three dialects can express
// that coalesce — MySQL takes a placeholder after LIMIT and nothing else, and
// what a NULL gets there is an empty page. Answering absence here means the
// value is the same number on every server, read from the same constant the
// emitted COALESCE reads.
//
// A page size that is present and over the ceiling is clamped to
// MaxQueryFilterLimit, which is the treatment MaxQueryFilterLimit documents and
// the treatment a URL parameter already gets. The clamp is applied before the
// narrowing to the driver's int32 rather than after, which is the ordering that
// matters: narrowing first turns an over-large limit into a legible-looking
// wrong answer.
//
// A page size that is present and zero is left alone, and returns no rows. That
// is the loud reading of an explicit zero, and only absence is defaulted here —
// the same distinction clampResponseSize draws, and the reason defaulting a
// zero belongs to Normalize, where a caller has asked for it.
//
// The times are bound as timestamps, which is what a server with a timestamp
// type takes. SQLite has neither a timestamp type nor a driver that speaks
// time.Time, and its comparisons over a text column are lexicographic — so a
// SQLite store binds through database/querygen's Generator.BindFilter, which
// shapes a time for the dialect it was built for. Binding these values there
// would produce a window that admits every row for every bound, which is
// indistinguishable from a caller who set no window at all.
func Bind(filter *QueryFilter) SQLArgs {
	if filter == nil {
		filter = DefaultQueryFilter()
	}

	return SQLArgs{
		CreatedAfter:    database.NullTimeFromTimePointer(filter.CreatedAfter),
		CreatedBefore:   database.NullTimeFromTimePointer(filter.CreatedBefore),
		UpdatedAfter:    database.NullTimeFromTimePointer(filter.UpdatedAfter),
		UpdatedBefore:   database.NullTimeFromTimePointer(filter.UpdatedBefore),
		Cursor:          database.NullStringFromStringPointer(filter.Cursor),
		ResultLimit:     database.NullInt32FromUint16(boundResponseSize(filter.MaxResponseSize)),
		IncludeArchived: database.NullBoolFromBoolPointer(filter.IncludeArchived),
	}
}

// boundResponseSize is the page size a filtered read is answered with: the
// default when none was asked for, and the clamp when too much was.
func boundResponseSize(size *uint16) uint16 {
	if size == nil {
		return DefaultQueryFilterLimit
	}

	return clampResponseSize(uint64(*size))
}

// BindValues is the same seven arguments keyed by the names they bind under,
// for a caller executing a statement through a positional driver rather than
// through generated code.
//
// SQLArgs is for the sqlc path, where a params literal names its fields; this
// is for the path where a statement names its arguments and something has to
// hand over a value per name — database/querygen's Bound.Bind takes exactly
// this map. Both are what Bind produced, so the two paths cannot come to bind
// different things.
//
// The map is freshly built and the caller owns it. A keyed read's own match
// columns go into it alongside these, which is why it is built rather than
// shared.
//
// The dialect caveat on Bind applies here in full: a SQLite store wants
// Generator.BindFilter, which writes these same names with times shaped for it.
func BindValues(filter *QueryFilter) map[string]any {
	args := Bind(filter)

	return map[string]any{
		ArgCreatedAfter:    args.CreatedAfter,
		ArgCreatedBefore:   args.CreatedBefore,
		ArgUpdatedAfter:    args.UpdatedAfter,
		ArgUpdatedBefore:   args.UpdatedBefore,
		ArgCursor:          args.Cursor,
		ArgResultLimit:     args.ResultLimit,
		ArgIncludeArchived: args.IncludeArchived,
	}
}

// Drain turns the rows a filtered read returned into the QueryFilteredResult it
// answers with: the page, the counts riding along on it, and the cursor that
// reaches the next one.
//
// It exists because the loop that does this is four lines with three separable
// ways to be quietly wrong, and every list query writes it again. The counts
// come off the first row rather than being reassigned per row — the windowed
// count is identical on every row, so a per-row assignment is correct by
// accident rather than by construction. The page is an empty slice rather than
// a nil one when nothing matched, so the JSON shape of an empty page does not
// depend on which store answered. And an empty page reports its counts as
// unknown rather than as zero, because a store whose counts ride along on the
// rows has no row to read them off and the zero it would otherwise report is
// "no row to carry the number on" rather than "nothing matched" — the ambiguity
// Pagination.CountsKnown exists to remove.
//
// counts may be nil, for a read whose statement carries no counts at all; the
// result then reports unknown counts however many rows came back. A caller with
// counts from a separate query has NewQueryFilteredResult and should pass them
// there, where saying so is the point.
//
// convert is the per-table half and stays the caller's: turning a generated row
// struct into a domain type is the one part of this that is genuinely about the
// table. It must not return nil — id is called on the last converted value to
// derive the next cursor.
func Drain[Row, T any](
	rows []Row,
	convert func(Row) *T,
	counts func(Row) (filtered, total int64),
	id func(*T) string,
	filter *QueryFilter,
) *QueryFilteredResult[T] {
	data := make([]*T, 0, len(rows))
	for _, row := range rows {
		data = append(data, convert(row))
	}

	if len(rows) == 0 || counts == nil {
		return NewQueryFilteredResultWithoutCounts(data, id, filter)
	}

	filtered, total := counts(rows[0])

	return NewQueryFilteredResult(data, countFromRow(filtered), countFromRow(total), id, filter)
}

// countFromRow narrows a COUNT to the unsigned pair Pagination reports.
//
// A count cannot be negative, and the guard is not for the database: it is for
// the conversion, where a negative would wrap to an enormous positive and a
// page would report more rows in the collection than a 64-bit address space
// holds. Reporting zero for a number that cannot exist is the smaller lie.
func countFromRow(count int64) uint64 {
	if count < 0 {
		return 0
	}

	return uint64(count)
}

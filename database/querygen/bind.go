package querygen

import (
	"regexp"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/dialect"
	platformerrors "github.com/primandproper/platform-go/v13/errors"
	"github.com/primandproper/platform-go/v13/filtering"
)

// A statement in this package names its arguments — created_after, cursor,
// result_limit — and something has to turn a name into the spelling the
// consumer parses. sqlc reads `sqlc.arg(created_after)` and generates a struct
// field from it; a database driver reads `$3` and takes a positional slice. The
// SQL either side of that spelling is the same SQL, and it is the part with the
// semantics in it: which bound admits NULL, whether the archived flag gates the
// exclusion or admits rows, that the cursor predicate is absent from the counts.
//
// Those semantics are the kind that can be wrong twice — a runtime renderer that
// wrote its own archived predicate the other way round would behave correctly
// until someone removed a redundant IS NULL elsewhere, and nothing would tie the
// two renderings together. So there is no second renderer. The Bound* methods
// below call the same statement builders StandardCRUD calls, and rewrite the
// argument references in what comes back.

// ErrUnboundableStatement indicates a statement that has no executable
// rendering, because the number of placeholders it needs is not known until the
// values are.
var ErrUnboundableStatement = platformerrors.New("statement cannot be rendered as bound SQL")

// sqlcArgument matches an argument reference in emitted SQL: the required and
// nullable forms, and the set form that has no single rendering.
//
// The name alphabet admits '#' because sqlc's own expansion of a set synthesizes
// one name per element, and a synthetic name has to be unmistakable for a real
// one. No identifier dialect.ValidIdentifier accepts contains a '#', so the two
// cannot collide.
var sqlcArgument = regexp.MustCompile(`sqlc\.(n?arg|slice)\(([a-zA-Z0-9_#]+)\)`)

// bindArguments rewrites a statement's sqlc argument references into d's bind
// markers, returning the statement a driver takes and the argument each marker
// stands for, in the order the driver takes them.
//
// This is a rewrite of the emitted text rather than a second way of rendering
// it, and that is the point rather than an expedient. sqlc performs exactly this
// rewrite on these same statements before its generated code hands one to a
// driver, so a bound statement is the generated one by construction — there is
// no pair of renderers to drift, and no fragment that could render correctly for
// one consumer and wrongly for the other.
//
// It also has to be a pass over the finished statement rather than a decision
// made while rendering one. A fragment here is rendered once and spliced more
// than once: filterPredicates goes into the SELECT's WHERE and into both count
// subqueries, so created_after appears three times in a list query from one
// rendering of it. Numbering at render time would give that one rendering one
// marker and record one argument while the statement carried three, and would
// additionally make the argument order depend on the order Go happened to
// evaluate the arguments of the Sprintf that assembled the statement. Numbering
// the finished text has neither problem: a marker is numbered where it appears,
// however it got there.
//
// What differs per dialect is only whether a repeat is one argument or several.
// Postgres numbers its markers, so repeats reuse the first occurrence's number
// and the value is bound once. MySQL's and SQLite's are positional — a bare `?`
// takes the next value and there is no way to name an earlier one — so there
// each occurrence appends the name again and the caller binds the value as many
// times as it appears. Both are correct; only the count differs, and Bound.Args
// reports whichever one this dialect produced.
//
// SQLite's own syntax does have a numbered form (`?NNN`), but dialect.Placeholder
// does not emit it, and treating SQLite as numbered here would collapse a
// repeat's arguments while leaving its markers in place — a statement that binds
// every value after the first into the following slot rather than one that fails
// to parse.
//
// A set reference has no rendering at all: sqlc.slice is a macro sqlc expands
// per call, because the number of markers a set needs is not known until the
// values are. None of the Bound* methods renders one — idSetPredicate is the
// only fragment that does, and only off Postgres — so reaching one here means a
// new Bound* method was written around a statement whose arity belongs to its
// caller, and that method owes its caller a count. It is a programming error,
// and says so the way the rest of this package does.
func bindArguments(d dialect.Dialect, statement string) (sql string, args []string) {
	ordinals := map[string]int{}

	sql = sqlcArgument.ReplaceAllStringFunc(statement, func(reference string) string {
		parts := sqlcArgument.FindStringSubmatch(reference)
		kind, name := parts[1], parts[2]

		if kind == "slice" {
			panic(platformerrors.Wrapf(ErrUnboundableStatement, "querygen: argument %q is a set", name))
		}

		if n, ok := ordinals[name]; ok && d == dialect.Postgres {
			return d.Placeholder(n)
		}

		args = append(args, name)
		ordinals[name] = len(args)

		return d.Placeholder(len(args))
	})

	return sql, args
}

// Bound is one statement rendered for a database driver: the SQL, and the names
// of the arguments its placeholders stand for, in the order the driver takes
// them.
//
// Args holds names rather than values because the statement is rendered once,
// at construction, and executed many times. A caller keeps the Bound and calls
// Bind per execution.
type Bound struct {
	// SQL is the statement, with this dialect's placeholders in it.
	SQL string
	// Args names each placeholder's argument, in positional order. A name
	// repeats on the positional dialects, once per occurrence — see
	// bindArguments.
	Args []string
}

// ErrUnboundArgument indicates a statement executed without a value for one of
// the arguments it names. It is a programming error rather than a caller's:
// nothing on a request path chooses which arguments a statement has.
var ErrUnboundArgument = platformerrors.New("no value supplied for a statement argument")

// Bind assembles the positional argument slice this statement takes from a map
// of values keyed by argument name.
//
// A missing name is an error rather than a nil, because a nil is a legitimate
// value for every nullable argument here and the two are indistinguishable once
// bound. A statement whose created_after is genuinely absent binds an explicit
// nil under that key.
func (b Bound) Bind(values map[string]any) ([]any, error) {
	args := make([]any, 0, len(b.Args))

	for _, name := range b.Args {
		value, ok := values[name]
		if !ok {
			return nil, platformerrors.Wrapf(ErrUnboundArgument, "argument %q", name)
		}

		args = append(args, value)
	}

	return args, nil
}

// bound rewrites one emitted statement for g's dialect.
func (g *Generator) bound(statement string) Bound {
	sql, args := bindArguments(g.dialect, statement)

	return Bound{SQL: sql, Args: args}
}

// Match is an equality predicate on one column, for a read keyed on something
// other than the row's own id — comments on one reference, signups for one
// waitlist.
//
// It is a column name rather than rendered SQL because the statements it lands
// in render it more than once: a list query carries its predicates in the SELECT
// and again in each of the two count subqueries beside it. A caller handing over
// finished SQL would have to know how many times its marker was about to appear,
// which on Postgres is once and on the positional dialects is three times.
// Handing over the column instead leaves that to bindArguments, which counts
// them where they land.
type Match struct {
	// Column is the column matched. It is bound, never interpolated, so its
	// value needs no escaping; the name itself is interpolated and is therefore
	// restricted — see dialect.ValidIdentifier.
	Column string
}

// BoundList renders a list query carrying extra equality predicates.
//
// It is listStatement — the same function StandardCRUD's list query comes from,
// with the matches where WithOwnership's column goes — so the filter window, the
// archived toggle, the cursor and the two counts are not merely the same ones a
// generated list gets, they are the same code path. A keyed read filters exactly
// as an unkeyed one does because there is nothing that could make it not.
//
// Each match binds under its own column name, so a caller assembles the argument
// map by column and Bind puts the values where this dialect wants them.
func (g *Generator) BoundList(table string, columns []string, matches ...Match) Bound {
	return g.bound(g.listStatement(table, columns, "", matches...))
}

// The five Bound* statement builders below are the executable counterparts of
// what StandardCRUD emits, and they are deliberately one per statement rather
// than one call returning the set.
//
// StandardCRUD answers a generator binary asking "what queries does this table
// need", where the set is the unit and a table gets all of it. A runtime store
// asks something narrower and per-statement: this table's reads are open within
// its scope but only its owner may write, so the get names one predicate column
// and the update names two. Expressed as one call over one options struct that
// would be a set of per-query overrides; expressed as five calls it is five
// argument lists.
//
// Each takes the extra predicate columns as Match values, and the row's own id
// is one of them where it applies rather than a special case — which is what
// lets a caller add a tenancy scope column without this package knowing what a
// tenancy scope is. Each is a call into the statement function StandardCRUD
// calls, so what a store executes is what a generator would have emitted.

// BoundGet renders the read of one row by id, plus any extra predicate columns.
func (g *Generator) BoundGet(table string, columns []string, extra ...Match) Bound {
	return g.bound(getStatement(table, columns, "", extra...))
}

// BoundExists renders the existence check for one row by id, plus any extra
// predicate columns. It reports what BoundGet would find without reading it.
func (g *Generator) BoundExists(table string, columns []string, extra ...Match) Bound {
	return g.bound(existsStatement(table, columns, "", extra...))
}

// BoundCreate renders the insert. insertColumns is what the caller supplies —
// ForInsert over the table's columns — and nullable names those whose value may
// be NULL.
func (g *Generator) BoundCreate(table string, insertColumns, nullable []string) Bound {
	return g.bound(createStatement(table, insertColumns, nullable))
}

// BoundUpdate renders the update: every mutable column assigned, last_updated_at
// stamped, keyed on the id and any extra predicate columns.
//
// A column that is both assigned and matched binds one argument to both, which
// is a statement that sets a column to the value it is being required to already
// hold. A caller wanting to move a row between owners wants that column out of
// updateColumns, which is what ForUpdate's exceptions are for.
func (g *Generator) BoundUpdate(table string, columns, updateColumns, nullable []string, extra ...Match) Bound {
	return g.bound(updateStatement(table, columns, updateColumns, "", nullable, extra...))
}

// BoundArchive renders the soft delete of one row by id, plus any extra
// predicate columns.
func (g *Generator) BoundArchive(table string, extra ...Match) Bound {
	return g.bound(archiveStatement(table, "", extra...))
}

// BindFilter writes a filtering.QueryFilter's values into an argument map under
// the names the emitted statements bind them by.
//
// It is here rather than in filtering because these names are this package's:
// filtering owns the struct and the URL parameters, and the mapping between
// those and the SQL arguments is what this package emits. A caller assembling
// its own map would be a second copy of that mapping, and a second copy that
// spelled created_after "createdAfter" would bind nothing and filter nothing —
// which looks exactly like a filter nobody set.
//
// It hangs off the Generator rather than standing alone because a time is not
// one value on all three servers — see filterTime. Everything else it binds is,
// and is bound through database's null helpers so that an unset field reaches
// the server as the NULL the emitted COALESCE expects rather than as a zero.
//
// A nil filter binds the defaults, so a caller that took none still produces a
// bindable statement. It writes only the arguments it owns: a keyed read's match
// columns go into the same map, and a filter that cleared them would be a read
// that lost its key.
func (g *Generator) BindFilter(values map[string]any, filter *filtering.QueryFilter) {
	if filter == nil {
		filter = filtering.DefaultQueryFilter()
	}

	values[CreatedAfterArg] = g.filterTime(filter.CreatedAfter)
	values[CreatedBeforeArg] = g.filterTime(filter.CreatedBefore)
	values[UpdatedAfterArg] = g.filterTime(filter.UpdatedAfter)
	values[UpdatedBeforeArg] = g.filterTime(filter.UpdatedBefore)
	values[IncludeArchivedArg] = database.NullBoolFromBoolPointer(filter.IncludeArchived)
	values[CursorArg] = database.NullStringFromStringPointer(filter.Cursor)
	values[LimitArg] = g.filterLimit(filter.MaxResponseSize)
}

// filterLimit renders the page size bound.
//
// Postgres and SQLite take an expression after LIMIT, so the emitted SQL
// coalesces an absent size to filtering.DefaultQueryFilterLimit and a NULL binds
// correctly. MySQL takes a placeholder and nothing else — COALESCE there is a
// parse error rather than a slower plan — so its LIMIT binds whatever it is
// handed, and what a NULL gets is an empty page.
//
// Binding the constant the other two coalesce to is what keeps that from being a
// dialect a caller has to remember on a path where nothing would remind them: an
// empty page is what a filter that matched nothing looks like. It is filtering's
// constant either way, read from the same place the emitted COALESCE reads it,
// so the two cannot drift.
//
// A size the caller set to zero is left alone, and returns no rows on every
// dialect. That is the documented meaning of an explicit zero — loud, rather
// than a page of some other size — and only absence is defaulted here, the same
// distinction filtering.QueryFilter.Normalize draws.
func (g *Generator) filterLimit(size *uint16) any {
	if size != nil || g.dialect != dialect.MySQL {
		return database.NullInt32FromUint16Pointer(size)
	}

	return int32(filtering.DefaultQueryFilterLimit)
}

// filterTime renders a window bound as the value this dialect's comparisons
// expect.
//
// Postgres and MySQL have a timestamp type and drivers that speak time.Time, so
// a NullTime is what they take. SQLite has neither: its columns hold the text
// CURRENT_TIMESTAMP wrote and its comparisons over one are lexicographic, so a
// time.Time arrives as whatever the driver made of it — and under SQLite's type
// affinity rules a number sorts below every string, so a text column compared
// against one is greater than it whatever the two of them mean.
//
// time.DateTime is that shape: it is the layout SQLite's own CURRENT_TIMESTAMP
// writes, which is the schema requirement the package comment states.
//
// That is the failure this exists to prevent, and it is the quiet kind: a
// created_after bound as a time on SQLite admits every row in the table, for
// every value of the bound. No error, no empty page, just a window that does
// nothing — which is indistinguishable from a caller who set no window at all.
func (g *Generator) filterTime(at *time.Time) any {
	if g.dialect != dialect.SQLite {
		return database.NullTimeFromTimePointer(at)
	}

	if at == nil {
		return nil
	}

	return at.UTC().Format(time.DateTime)
}

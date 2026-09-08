package queries

import (
	"fmt"
	"strings"

	"github.com/primandproper/primitives-go/database/dialect"
	"github.com/primandproper/primitives-go/database/querygen"
)

// InstancesTable is the one table this package owns, at its canonical
// spelling — what the emitted .sql names, and what saga/migrations renders at
// the consumer's prefix.
const InstancesTable = "saga_instances"

// TableNames is every table saga owns, in the order the DDL creates them.
//
// One entry, and it is still a list: the querygen registry is fed by the table
// existing rather than by something choosing to emit its queries, and a
// consumer reading that registry back to truncate a database between tests has
// no way to know which packages happen to own one table and which own seven.
//
// saga/migrations is where a consumer gets these names rendered at their own
// prefix. This list is the canonical spelling and migrations.Tables reads the
// DDL, so the two are cross-checked against each other in this package's tests
// rather than one being derived from the other.
var TableNames = []string{InstancesTable}

// The instance table's columns, named rather than spelled at each statement
// that uses one.
//
// They are constants because a column appears in several statements here — the
// status in a guard, a filter and an assignment; the lease in a predicate and
// in three SET lists — and a typo in one of those renders SQL that sqlc
// rejects, which is the good case, or that names a different column, which is
// not.
const (
	DefinitionColumn   = "definition"
	StatusColumn       = "status"
	CurrentStepColumn  = "current_step"
	StepNamesColumn    = "step_names"
	StateColumn        = "state"
	AttemptsColumn     = "attempts"
	LastErrorColumn    = "last_error"
	ResumeStatusColumn = "resume_status"
	NextAttemptColumn  = "next_attempt"
	ClaimedUntilColumn = "claimed_until"
)

// Columns is the instance table's shape, in the order every emitted SELECT
// projects it — which is also the order the generated row structs carry, and so
// the order saga's conversions restate.
var Columns = []string{
	querygen.IDColumn,
	DefinitionColumn,
	StatusColumn,
	CurrentStepColumn,
	StepNamesColumn,
	StateColumn,
	AttemptsColumn,
	LastErrorColumn,
	ResumeStatusColumn,
	querygen.CreatedAtColumn,
	querygen.LastUpdatedAtColumn,
	querygen.ArchivedAtColumn,
	NextAttemptColumn,
	ClaimedUntilColumn,
}

// NullableColumns names the columns a write may set to NULL, which lives in the
// schema neither this package nor querygen reads.
//
// state is the encoded application state, absent until the first step writes
// one; claimed_until is the lease, absent whenever nobody holds it, which is
// the condition the claim predicate reads.
var NullableColumns = []string{StateColumn, ClaimedUntilColumn}

// The arguments the emitted statements bind, beyond the ones named for their
// own column.
//
// The two status guards are the pair a worker can advance. They are two
// arguments rather than one because the statements they appear in also *assign*
// status, and a guard sharing a name with the value being written would set the
// column to the value it was requiring it to already hold — the same separation
// querygen.Match.Arg exists to make.
const (
	RunningStatusArg      = "running_status"
	CompensatingStatusArg = "compensating_status"

	// DueAtArg is the instant an instance's next attempt must have arrived by.
	DueAtArg = "due_at"
	// LeaseExpiredByArg is the instant a lease must have lapsed by for the row
	// to be claimable again. The store binds the same time as DueAtArg — one
	// cycle asks one question about one moment — and they are two arguments
	// because they are two comparisons against two columns, and a reader of the
	// statement should not have to work out that they must agree.
	LeaseExpiredByArg = "lease_expired_by"

	// IDsArg is the claimed batch, bound as a whole set.
	IDsArg = querygen.IDsArg
	// FromStatusesArg is the set of statuses a requeue will move an instance
	// out of. It is the caller's, unlike the guard above: an operator resuming
	// a saga names which statuses that applies to.
	FromStatusesArg = "from_statuses"
)

// StatusFilterArity is how many status arguments a listing binds, and it is the
// size of the whole Status domain rather than a page-size-style limit.
//
// A listing narrowed by a *set* would have to bind one, and a bound set is the
// one thing that cannot sit in a list query on every dialect this package
// serves: SQLite numbers the bare markers a sqlc.slice expands to one past the
// highest it has seen, so the cursor and the page size bound after it collide
// with the set's own elements — silently, on the two arguments that decide
// which rows come back.
//
// A closed domain does not need one. The filter names statuses out of a set of
// five, so the predicate names five and the store decides which five: a caller
// asking for the stuck ones binds 'stuck' in every slot, and a caller asking
// for nothing in particular binds all five, which is the same rows the
// predicate's absence would have returned. saga's own test holds the number
// here to the size of the domain, so a sixth status is a failing test rather
// than a listing that quietly stops returning it.
const StatusFilterArity = 5

// StatusFilterArgs names those arguments, in the order a caller's set is padded
// into them.
var StatusFilterArgs = statusFilterArgs()

func statusFilterArgs() []string {
	args := make([]string, 0, StatusFilterArity)
	for i := 1; i <= StatusFilterArity; i++ {
		args = append(args, fmt.Sprintf("%s_%d", StatusColumn, i))
	}

	return args
}

// The names the emitted statements carry. A query name is a Go method name on
// the generated querier, so these are what saga's store calls.
const (
	GetInstanceQuery          = "GetSagaInstance"
	ListInstancesQuery        = "ListSagaInstances"
	ListByDefinitionQuery     = "ListSagaInstancesByDefinition"
	ClaimableIDsQuery         = "ClaimableSagaInstanceIDs"
	ListByIDsQuery            = "ListSagaInstancesByIDs"
	InsertInstanceQuery       = "InsertSagaInstance"
	ClaimInstancesQuery       = "ClaimSagaInstances"
	AdvanceQuery              = "AdvanceSagaInstance"
	AdvanceAndClearLeaseQuery = "AdvanceSagaInstanceAndClearLease"
	RescheduleQuery           = "RescheduleSagaInstance"
	ReleaseQuery              = "ReleaseSagaInstance"
	RequeueQuery              = "RequeueSagaInstance"
)

// InsertColumns is what the create supplies values for.
//
// created_at is among them, and it is the one place this schema departs from
// the module's convention that the database owns a row's creation time. A saga
// instance is a schedule as much as a record: next_attempt, the lease horizon,
// and every backoff this package computes come off the clock the Runner and the
// Worker share, and created_at is the tie-break the claim orders by right after
// next_attempt. A creation time from the server's clock and a next attempt from
// the application's would order the claim index against two clocks that are
// only approximately the same — and would make a test that advances a fake
// clock unable to say when an instance was started.
//
// claimed_until is left out for the opposite reason: a lease belongs to
// whichever worker takes one, and a create has not been claimed by anybody.
var InsertColumns = insertColumns()

func insertColumns() []string {
	kept := make([]string, 0, len(Columns))

	for _, column := range Columns {
		switch column {
		case querygen.LastUpdatedAtColumn, querygen.ArchivedAtColumn, ClaimedUntilColumn:
			continue
		default:
			kept = append(kept, column)
		}
	}

	return kept
}

// Render returns the canonical sqlc input for d: every statement saga executes,
// in one file's worth of text.
//
// It is what saga/internal/queriesgen writes to the .sql beside this file and
// what CI regenerates to check the committed copy still matches. That .sql is
// sqlc-gen-unison's input, so what the store executes is this text exactly: the
// generated sagadb package carries it per dialect, with the consumer's table
// prefix substituted once at construction.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	querygen.RegisterTable(TableNames...)

	rendered := []*querygen.Query{
		g.GetQuery(GetInstanceQuery, InstancesTable, Columns),
	}

	rendered = append(rendered, listings(g)...)
	rendered = append(rendered, claimReads(g)...)
	rendered = append(rendered, g.InsertQuery(InsertInstanceQuery, InstancesTable, InsertColumns, NullableColumns))
	rendered = append(rendered, transitions(g)...)

	return querygen.RenderFile(rendered)
}

// FileName is the canonical .sql this package renders for d.
func FileName(d dialect.Dialect) string {
	return string(d) + "_generated.sql"
}

// listings renders the two paged reads, each in both directions.
//
// A paged list is two statements because a direction is which way the ORDER BY
// runs and which way the cursor comparison points — statement text on all three
// servers, with no expression that takes a bound value and orders by it. What
// the store does with a filter's SortBy is choose between them.
//
// They are two listings rather than one because the definition filter is
// optional and an optional equality is not a predicate a caller can relax: a
// listing asked for nothing in particular must not come back with the rows
// whose definition is the empty string. So the narrowing is enumerated —
// one named statement per shape a caller can ask for — and the status filter,
// whose domain is closed, rides in both of them. See [StatusFilterArity].
//
// Everything else about them is querygen's own listing, assembled from the
// fragments it exports: the filter window over the convention columns, the
// archived toggle, the keyset predicate, the two counts riding on the rows, and
// the page-size clause each dialect spells its own way. Written out here rather
// than emitted by [querygen.Generator.ListQueries] only because that one has no
// way to say "this column is one of these five".
func listings(g *querygen.Generator) []*querygen.Query {
	listings := []struct {
		name       string
		conditions []string
	}{
		{name: ListInstancesQuery, conditions: []string{statusFilter()}},
		{name: ListByDefinitionQuery, conditions: []string{statusFilter(), definitionFilter()}},
	}

	rendered := make([]*querygen.Query, 0, 2*len(listings))

	for i := range listings {
		listing := &listings[i]

		for _, direction := range []querygen.Direction{querygen.Ascending, querygen.Descending} {
			name := listing.name
			if direction == querygen.Descending {
				name = querygen.DescendingName(name)
			}

			rendered = append(rendered, &querygen.Query{
				Annotation: querygen.QueryAnnotation{Name: name, Type: querygen.ManyType},
				Content:    listStatement(g, direction, listing.conditions...),
			})
		}
	}

	return rendered
}

// listStatement is querygen's own list statement with one extra predicate: the
// page, the two counts, the window, the cursor, and the page size, in the order
// and at the indentation that package renders them.
func listStatement(g *querygen.Generator, direction querygen.Direction, conditions ...string) string {
	return fmt.Sprintf("SELECT\n\t%s,\n\t%s,\n\t%s\nFROM %s\nWHERE %s\n%s;",
		strings.Join(querygen.QualifyAll(InstancesTable, Columns), ",\n\t"),
		g.FilterCountSelect(InstancesTable, Columns, nil, conditions...),
		g.TotalCountSelect(InstancesTable, Columns, nil, conditions...),
		InstancesTable,
		g.FilterConditions(InstancesTable, Columns, direction, conditions...),
		g.CursorLimitClause(InstancesTable, direction),
	)
}

// claimReads renders the two statements a claim cycle reads through: the
// candidates it locks, and the rows it hands the worker once they are leased.
//
// Neither is a shape querygen renders. The first orders across three columns
// rather than by the id a keyset walk pages over, compares two columns against
// the same instant, admits a NULL as "nobody holds this", and — where the
// server has one — takes the row lock that makes two workers polling at once
// mean anything. The second projects the whole table keyed on a bound set
// *and* on the status guard, which is a set-membership predicate a
// [querygen.Match] has no spelling for.
//
// So both are written out here, and what they keep is the guarantee rather than
// the generator: each is a complete statement in the committed corpus, checked
// by sqlc against saga's own schema on each dialect, executed through the
// generated querier. A renamed column is a failed `make unison` for these
// exactly as it is for the get.
func claimReads(g *querygen.Generator) []*querygen.Query {
	instances := querygen.QualifyAll(InstancesTable, Columns)

	// The lease is dropped rather than expired: a row nobody holds has a NULL
	// there, and a row whose holder ran out of time has a lapsed one. Both are
	// claimable, and a predicate naming only the second would leave every
	// never-claimed instance permanently invisible.
	claimable := fmt.Sprintf("(%[1]s IS NULL OR %[1]s <= sqlc.arg(%[2]s))",
		querygen.Qualify(InstancesTable, ClaimedUntilColumn), LeaseExpiredByArg)

	candidates := fmt.Sprintf("SELECT %s\nFROM %s\nWHERE %s\n%s%s;",
		querygen.Qualify(InstancesTable, querygen.IDColumn),
		InstancesTable,
		strings.Join([]string{
			activeStatusGuard(),
			fmt.Sprintf("%s <= sqlc.arg(%s)", querygen.Qualify(InstancesTable, NextAttemptColumn), DueAtArg),
			claimable,
		}, "\n\tAND "),
		claimOrder(),
		lockSuffix(g),
	)

	// The set is rendered after the status guard, and that is a requirement
	// rather than a layout choice: an expanded set is a run of bare markers,
	// SQLite numbers a bare marker one past the highest it has seen, and an
	// argument bound after one collides with an element of the set.
	batch := fmt.Sprintf("SELECT\n\t%s\nFROM %s\nWHERE %s\n\tAND %s\n%s;",
		strings.Join(instances, ",\n\t"),
		InstancesTable,
		activeStatusGuard(),
		g.SetCondition(querygen.Qualify(InstancesTable, querygen.IDColumn), IDsArg),
		claimOrder(),
	)

	return []*querygen.Query{
		{Annotation: querygen.QueryAnnotation{Name: ClaimableIDsQuery, Type: querygen.ManyType}, Content: candidates},
		{Annotation: querygen.QueryAnnotation{Name: ListByIDsQuery, Type: querygen.ManyType}, Content: batch},
	}
}

// claimOrder is the order a claim cycle reads in: due first, oldest first among
// those, and the id as the tie-break that makes the order total.
//
// It is one rendering because the candidates and the rows they become are two
// statements describing one batch. A worker that selected in one order and read
// back in another would hand its steps out in an order nothing chose.
func claimOrder() string {
	ordered := querygen.QualifyAll(InstancesTable,
		[]string{NextAttemptColumn, querygen.CreatedAtColumn, querygen.IDColumn})

	return "ORDER BY " + strings.Join(ordered, ", ")
}

// lockSuffix renders the page size and, where the server has one, the row lock
// that makes two workers polling the same table at once mean something.
//
// SQLite has neither the clause nor the concurrency it exists for — one writer
// at a time — so it gets the page size alone. The lock is what makes the select
// and the update that follows it one decision: without it the rows are released
// before the update runs, and two workers claim the same batch.
func lockSuffix(g *querygen.Generator) string {
	clause := "\n" + g.LimitClause()

	if g.Dialect().SupportsSkipLocked() {
		clause += "\nFOR UPDATE SKIP LOCKED"
	}

	return clause
}

// transitions renders the six writes the state machine makes.
//
// Every one of them is written out here rather than emitted, and the line is
// the SET list rather than the effort. querygen assigns bound values — a column
// and the argument it takes, last_updated_at stamped by convention — and not
// one of these is that statement. The claim increments a counter server-side,
// because a worker that dies mid-step has still consumed an attempt; the
// advance zeroes one and, on the pass that is over, drops a lease to NULL; the
// requeue clears the resume hint to the empty sentinel as the instance leaves
// the stuck set. Rendering those would need an expression language in querygen,
// which is what its closed set of comparands exists to refuse.
//
// What they do not give up is the guarantee. Each is a complete statement in
// the committed corpus, checked by sqlc against saga's schema on every dialect,
// executed through the generated querier — and each is annotated :execrows
// where the count is the answer, because a guarded write that matched no row is
// how this package learns an instance left the active set while a worker was
// busy with it.
//
// Every one of them stamps last_updated_at from a bound value rather than from
// the server's clock, for the reason [InsertColumns] gives about created_at:
// an instance's timeline is one clock's, and it is the clock the Worker
// schedules against.
func transitions(g *querygen.Generator) []*querygen.Query {
	// The lease is dropped whenever the pass is over — the instance is
	// finished, or it is waiting out a step's delay — and kept when this worker
	// is about to run the next step itself. That is a SET list that differs by
	// one assignment, so it is two named statements rather than one statement
	// with a conditional clause: the store chooses, and both halves are checked.
	advance := []string{
		assign(StatusColumn),
		assign(CurrentStepColumn),
		nullableAssign(StateColumn),
		assign(LastErrorColumn),
		assign(ResumeStatusColumn),
		assign(NextAttemptColumn),
		assign(querygen.LastUpdatedAtColumn),
		AttemptsColumn + " = 0",
	}

	return []*querygen.Query{
		updateQuery(ClaimInstancesQuery, []string{
			assign(ClaimedUntilColumn),
			assign(querygen.LastUpdatedAtColumn),
			fmt.Sprintf("%[1]s = %[1]s + 1", AttemptsColumn),
		}, []string{
			activeStatusGuard(),
			g.SetCondition(querygen.Qualify(InstancesTable, querygen.IDColumn), IDsArg),
		}),

		updateQuery(AdvanceQuery, advance, []string{idPredicate(), activeStatusGuard()}),

		updateQuery(AdvanceAndClearLeaseQuery, append(advance, dropLease()),
			[]string{idPredicate(), activeStatusGuard()}),

		updateQuery(RescheduleQuery, []string{
			assign(AttemptsColumn),
			assign(NextAttemptColumn),
			assign(LastErrorColumn),
			assign(querygen.LastUpdatedAtColumn),
			dropLease(),
		}, []string{idPredicate(), activeStatusGuard()}),

		updateQuery(ReleaseQuery, []string{
			dropLease(),
			assign(querygen.LastUpdatedAtColumn),
		}, []string{idPredicate()}),

		// resume_status is cleared as the instance leaves the stuck set: it
		// answers "what would Resume do with this", and an instance that is
		// running again has already had that question answered. The empty
		// string is the sentinel the column holds when it holds nothing, so it
		// is written as one rather than bound — there is no value a caller
		// could supply that would mean anything else here.
		updateQuery(RequeueQuery, []string{
			assign(StatusColumn),
			assign(NextAttemptColumn),
			assign(querygen.LastUpdatedAtColumn),
			ResumeStatusColumn + " = ''",
			AttemptsColumn + " = 0",
			dropLease(),
		}, []string{
			idPredicate(),
			g.SetCondition(querygen.Qualify(InstancesTable, StatusColumn), FromStatusesArg),
		}),
	}
}

// updateQuery renders one guarded write. Every one of them reports its row
// count, because in this package a write that matched nothing is a fact the
// caller acts on rather than an error the driver raises.
func updateQuery(name string, assignments, predicates []string) *querygen.Query {
	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: name, Type: querygen.ExecRowsType},
		Content: fmt.Sprintf("UPDATE %s SET\n\t%s\nWHERE %s;",
			InstancesTable,
			strings.Join(assignments, ",\n\t"),
			strings.Join(predicates, "\n\tAND "),
		),
	}
}

// assign renders a column taking a bound value.
func assign(column string) string {
	return fmt.Sprintf("%s = sqlc.arg(%s)", column, column)
}

// nullableAssign renders a column a write may set to NULL.
func nullableAssign(column string) string {
	return fmt.Sprintf("%s = sqlc.narg(%s)", column, column)
}

// dropLease renders the assignment that hands an instance back. It is a
// literal rather than a bound NULL because there is no other value a statement
// naming it could mean: these are the writes after which nobody holds the row.
func dropLease() string {
	return ClaimedUntilColumn + " = NULL"
}

// idPredicate keys a statement on the row's own id.
func idPredicate() string {
	return fmt.Sprintf("%s = sqlc.arg(%s)",
		querygen.Qualify(InstancesTable, querygen.IDColumn), querygen.IDColumn)
}

// activeStatusGuard is the predicate every transition carries: the instance is
// still one a worker can advance.
//
// It is repeated on the claim even though the rows were just selected as
// active, and on the advance even though this worker holds the lease, because
// between any two of those statements another worker's advance may have
// finished the saga. The guard being in the predicate rather than in a
// read-then-write is what makes the loser of that race report zero rows instead
// of resurrecting an instance somebody already completed.
func activeStatusGuard() string {
	return fmt.Sprintf("%s IN (sqlc.arg(%s), sqlc.arg(%s))",
		querygen.Qualify(InstancesTable, StatusColumn), RunningStatusArg, CompensatingStatusArg)
}

// statusFilter is the listing's status predicate: the column against the five
// arguments the whole domain fits in. See [StatusFilterArity].
func statusFilter() string {
	bound := make([]string, 0, len(StatusFilterArgs))
	for _, arg := range StatusFilterArgs {
		bound = append(bound, fmt.Sprintf("sqlc.arg(%s)", arg))
	}

	return fmt.Sprintf("%s IN (%s)",
		querygen.Qualify(InstancesTable, StatusColumn), strings.Join(bound, ", "))
}

// definitionFilter narrows a listing to one definition.
func definitionFilter() string {
	return fmt.Sprintf("%s = sqlc.arg(%s)",
		querygen.Qualify(InstancesTable, DefinitionColumn), DefinitionColumn)
}

package queries

import (
	"fmt"
	"strings"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"
)

// The arguments only the split corpus binds. The Postgres corpus binds a set of
// states as one array; these two engines have no array type, and the sets here
// are closed, so each member is an argument of its own.
const (
	// PendingStateArg, RunningStateArg and the three terminal ones name the
	// states a guard or a sweep compares against, one argument per state.
	PendingStateArg   = "pending_state"
	RunningStateArg   = "running_state"
	SucceededStateArg = "succeeded_state"
	FailedStateArg    = "failed_state"
	CancelledStateArg = "cancelled_state"

	// KindFilterArg is the listing's optional kind narrowing. It is named apart
	// from the column because MySQL types the narrowing's NULL arm as a type of
	// its own, and one argument the engines disagree about is one unison cannot
	// converge; see unison.split.yaml.
	KindFilterArg = "kind_filter"

	// LimitArg caps the sweep's read and the reaper's.
	//
	// It is the name querygen's own listing binds its page size under, because
	// MySQL leaves no choice: it accepts only a bare placeholder in LIMIT and
	// names that parameter `limit` whatever the statement meant by it, and
	// unison.split.yaml renames it once for the whole corpus. SQLite spells the
	// same name out, so the two converge.
	LimitArg = "result_limit"

	// IDsArg is the batch the reaper deletes, as the read before it selected.
	IDsArg = "ids"
)

// StateFilterArity is how many state arguments the split listing binds, and it
// is the size of the whole operations.State domain rather than a limit.
//
// The Postgres listing binds the filter as one array. A bound set is the one
// thing a list query cannot carry on these two engines: sqlc expands it into bare
// markers, SQLite numbers a bare marker one past the highest it has seen, and the
// cursor and the page size bound after it collide with the set's own elements.
// The domain is closed, so the predicate names five and the store decides which
// five — every state for a caller who asked for none, and a narrower set padded
// by repeating a member, which matches the same rows. saga's status filter is
// the same decision, and a test holds this number to the size of the domain.
const StateFilterArity = 5

// StateFilterArgs names those arguments, in the order a caller's set is padded
// into them.
var StateFilterArgs = stateFilterArgs()

func stateFilterArgs() []string {
	args := make([]string, 0, StateFilterArity)
	for i := 1; i <= StateFilterArity; i++ {
		args = append(args, fmt.Sprintf("%s_%d", StateColumn, i))
	}

	return args
}

// renderSplit is the corpus for the two engines with no RETURNING and no
// arrays.
//
// It is a second statement set rather than a second spelling of the first,
// because the difference is shape rather than syntax. The Postgres create and
// the Postgres claim and flush hand their row back through RETURNING; here each
// is a guarded write whose row count is read first, and a read on the same
// transaction afterwards. unison refuses a query whose shape differs across a
// roster, and rightly, so the two sets are two rosters: this one is generated
// into operations/internal/operationssplitdb, and Postgres keeps operationsdb
// and its single statements.
//
// The reads are querygen's, as they are for Postgres, except the listing: its
// state filter is a bound set there and five arguments here — see
// [StateFilterArity].
//
// What does not change is anything a caller can observe. Every guard, every
// monotonic floor, the lease horizon, the conditional cancellation and the one
// clock are the same decisions as the Postgres corpus, spelled in the engine at
// hand — see each statement.
func renderSplit(d dialect.Dialect) string {
	s := newSplit(d)

	rendered := append(singleReads(s.g), setRead(s.g))

	rendered = append(rendered, s.listQueries()...)
	rendered = append(rendered, []*querygen.Query{
		{Annotation: querygen.QueryAnnotation{Name: "InsertOperation", Type: querygen.ExecRowsType},
			Content: s.insertOperation()},
		{Annotation: querygen.QueryAnnotation{Name: "BeginOperation", Type: querygen.ExecRowsType},
			Content: s.beginOperation()},
		{Annotation: querygen.QueryAnnotation{Name: "RecordOperationProgress", Type: querygen.ExecRowsType},
			Content: s.recordOperationProgress()},
		{Annotation: querygen.QueryAnnotation{Name: "GetOperationAck", Type: querygen.OneType},
			Content: s.getOperationAck()},
		{Annotation: querygen.QueryAnnotation{Name: "FinishOperation", Type: querygen.ExecRowsType},
			Content: s.finishOperation(false)},
		{Annotation: querygen.QueryAnnotation{Name: "FinishOperationWithEveryUnitDone", Type: querygen.ExecRowsType},
			Content: s.finishOperation(true)},
		{Annotation: querygen.QueryAnnotation{Name: "ReleaseOperation", Type: querygen.ExecRowsType},
			Content: s.releaseOperation()},
		{Annotation: querygen.QueryAnnotation{Name: "RequestOperationCancel", Type: querygen.ExecRowsType},
			Content: s.requestOperationCancel()},
		{Annotation: querygen.QueryAnnotation{Name: "ListStrandedOperations", Type: querygen.ManyType},
			Content: s.listStrandedOperations()},
		{Annotation: querygen.QueryAnnotation{Name: "SelectReapableOperations", Type: querygen.ManyType},
			Content: s.selectReapableOperations()},
		{Annotation: querygen.QueryAnnotation{Name: "LockReapableOperations", Type: querygen.ManyType},
			Content: s.lockReapableOperations()},
		{Annotation: querygen.QueryAnnotation{Name: "DeleteReapedOperations", Type: querygen.ExecRowsType},
			Content: s.deleteReapedOperations()},
	}...)

	return querygen.RenderFile(rendered)
}

// split renders the statements for one of the two engines.
type split struct {
	g *querygen.Generator
	d dialect.Dialect
}

func newSplit(d dialect.Dialect) split {
	return split{d: d, g: querygen.For(d)}
}

// listQueries is the paged read in both directions: querygen's own listing,
// assembled from the fragments it exports, with the state filter as five
// arguments rather than a set.
//
// The scope and the kind are the Postgres listing's two matches, rendered by
// the same generator, so the scope is still a plain equality nothing can leave
// off and the kind is still a narrowing an absent value compares against
// nothing.
func (s split) listQueries() []*querygen.Query {
	conditions := append(s.g.MatchConditions(OperationsTable,
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: KindColumn, Arg: KindFilterArg, Against: querygen.OptionalNarrowing},
	), stateFilter())

	rendered := make([]*querygen.Query, 0, 2) //nolint:mnd // one per direction

	for _, direction := range []querygen.Direction{querygen.Ascending, querygen.Descending} {
		name := "ListOperations"
		if direction == querygen.Descending {
			name = querygen.DescendingName(name)
		}

		rendered = append(rendered, &querygen.Query{
			Annotation: querygen.QueryAnnotation{Name: name, Type: querygen.ManyType},
			Content: fmt.Sprintf("SELECT\n\t%s,\n\t%s,\n\t%s\nFROM %s\nWHERE %s\n%s;",
				projection(),
				s.g.FilterCountSelect(OperationsTable, Columns, nil, conditions...),
				s.g.TotalCountSelect(OperationsTable, Columns, nil, conditions...),
				OperationsTable,
				s.g.FilterConditions(OperationsTable, Columns, direction, conditions...),
				s.g.CursorLimitClause(OperationsTable, direction),
			),
		})
	}

	return rendered
}

// stateFilter is the listing's state narrowing, as the five arguments the whole
// domain fits in. See [StateFilterArity].
func stateFilter() string {
	bound := make([]string, 0, len(StateFilterArgs))
	for _, arg := range StateFilterArgs {
		bound = append(bound, "sqlc.arg("+arg+")")
	}

	return fmt.Sprintf("%s IN (%s)", querygen.Qualify(OperationsTable, StateColumn), strings.Join(bound, ", "))
}

// insertOperation records a new operation. It reports a row count rather than
// the row, and the row itself is read back on the same transaction, which is
// the only place it can be read from while that transaction is open.
//
// On SQLite it changes nothing when the id is already taken — the Postgres
// create's decision, and its reason: a raised unique violation would abort the
// caller's transaction, so a caller writing under an id they derived would lose
// every write they had made beside it. One row is a new operation, and none is
// the collision the store reports as ErrDuplicateOperation.
//
// On MySQL the collision is the duplicate-key error, which the store reads as
// that same sentinel, and the reason above does not hold there: InnoDB rolls
// back the failed statement and leaves the transaction standing. MySQL has no
// "do nothing" whose count does not depend on the connection. INSERT IGNORE is
// wider than a duplicate key — it turns a value too long for its column into a
// truncated one and reports success — and an assignment of the key to itself
// changes nothing, which MySQL counts as zero rows by default and as one under
// clientFoundRows=true, the matched-row count the generated queriers offer a
// consumer. An error is the same under either.
//
// The three text columns the insert does not otherwise supply are bound as
// empty literals, because MySQL cannot give a TEXT column a DEFAULT. Written on
// both engines, so there is one statement to read.
func (s split) insertOperation() string {
	columns := append(InsertColumns(), "result_uri", "error_code")
	bindings := append(insertBindings(), "''", "''")

	conflict := fmt.Sprintf("\nON CONFLICT (%s) DO NOTHING", querygen.IDColumn)
	if s.d == dialect.MySQL {
		conflict = ""
	}

	return fmt.Sprintf(`INSERT INTO %s (
	%s
) VALUES (
	%s
)%s`,
		OperationsTable,
		strings.Join(columns, ",\n\t"),
		strings.Join(bindings, ",\n\t"),
		conflict,
	)
}

// beginOperation is the Postgres claim with its RETURNING taken off: pending or
// lapsed-running becomes running under a fresh lease, guarded in the statement,
// and every decision the Postgres statement documents holds — the queue's
// attempt count, started_at set on the first pass only.
//
// The row count is what the store reads first. The claim is the package's real
// mutual exclusion, so zero means somebody else holds the operation or nobody
// ever will, and the store answers that without reading a row it did not win.
// One means this statement moved it, and every assignment changes the row — the
// revision moves — so MySQL's changed-row count is the matched one here.
func (s split) beginOperation() string {
	return fmt.Sprintf(`UPDATE %s SET
	state = sqlc.arg(%s),
	attempts = sqlc.arg(attempts),
	started_at = COALESCE(started_at, %s),
	claimed_until = %s,
	revision = revision + 1,
	last_updated_at = %s
WHERE id = sqlc.arg(id)
	AND %s
	AND claimed_until <= %s`,
		OperationsTable,
		RunningStateArg,
		s.now(),
		s.after("lease_microseconds", true),
		s.now(),
		s.activeGuard(),
		s.now(),
	)
}

// recordOperationProgress is the Postgres flush, clause for clause — the
// COALESCEd denominator that cannot be cleared, the two monotonic floors, the
// lease extension, the guard on running — with the answer taken off it.
//
// The answer is getOperationAck's, read on the same transaction after this
// statement matched. MySQL assigns left to right and lets a later assignment see
// an earlier one's result; no expression here reads a column an earlier one
// wrote.
func (s split) recordOperationProgress() string {
	return fmt.Sprintf(`UPDATE %s SET
	units_total = COALESCE(sqlc.narg(units_total), units_total),
	units_done = %s,
	progress_unit = sqlc.arg(progress_unit),
	progress_count = %s,
	progress_message = sqlc.arg(progress_message),
	claimed_until = %s,
	revision = revision + 1,
	last_updated_at = %s
WHERE id = sqlc.arg(id)
	AND state = sqlc.arg(%s)`,
		OperationsTable,
		s.greatest("units_done", "sqlc.arg(units_done)"),
		s.greatest("progress_count", "sqlc.arg(progress_count)"),
		s.after("lease_microseconds", true),
		s.now(),
		RunningStateArg,
	)
}

// getOperationAck is the flush's answer, which the Postgres statement returns
// and these engines read back: whether a cancellation has been requested, and
// the revision the flush wrote.
//
// It is read on the flush's transaction, after the flush matched, and that is
// what makes the revision the flush's own rather than a later write's. The
// flush's lock is held until the transaction ends — a row lock on MySQL, the one
// writer on SQLite — so nothing else can have moved the row between the two
// statements, and the state guard is repeated so that a read-back can only find
// the row the flush's own guard admitted.
func (s split) getOperationAck() string {
	return fmt.Sprintf(`SELECT
	%[1]s.cancel_requested,
	%[1]s.revision
FROM %[1]s
WHERE %[1]s.id = sqlc.arg(id)
	AND %[1]s.state = sqlc.arg(%[2]s)`,
		OperationsTable, RunningStateArg)
}

// finishOperation is the Postgres terminal write in its two forms, guarded on
// the active set as two arguments rather than an array.
func (s split) finishOperation(everyUnitDone bool) string {
	unitsDone := ""
	if everyUnitDone {
		unitsDone = "\n\tunits_done = COALESCE(units_total, units_done),"
	}

	return fmt.Sprintf(`UPDATE %s SET
	state = sqlc.arg(state),
	result_uri = sqlc.arg(result_uri),
	result_detail = sqlc.narg(result_detail),
	error_code = sqlc.arg(error_code),
	error_message = sqlc.arg(error_message),
	error_retryable = sqlc.arg(error_retryable),%s
	progress_unit = '',
	finished_at = %s,
	claimed_until = %s,
	revision = revision + 1,
	last_updated_at = %s
WHERE id = sqlc.arg(id)
	AND %s`,
		OperationsTable, unitsDone, s.now(), s.epoch(), s.now(), s.activeGuard())
}

// releaseOperation is the Postgres hand-back, spelled in this engine's clock.
func (s split) releaseOperation() string {
	return fmt.Sprintf(`UPDATE %s SET
	state = sqlc.arg(%s),
	error_code = sqlc.arg(error_code),
	error_message = sqlc.arg(error_message),
	error_retryable = TRUE,
	progress_unit = '',
	claimed_until = %s,
	revision = revision + 1,
	last_updated_at = %s
WHERE id = sqlc.arg(id)
	AND state = sqlc.arg(%s)`,
		OperationsTable, PendingStateArg, s.epoch(), s.now(), RunningStateArg)
}

// requestOperationCancel is the Postgres cancellation: one statement that
// cancels a pending operation outright and flags a running one.
//
// The assignments are in the order MySQL needs. Postgres and SQLite evaluate
// every SET expression against the row as it was found; MySQL evaluates them
// left to right and lets a later one see an earlier one's result. Every CASE
// below tests the state, so the state is assigned last, and every branch reads
// the row before the cancellation moved it.
func (s split) requestOperationCancel() string {
	pending := "state = sqlc.arg(" + PendingStateArg + ")"

	return fmt.Sprintf(`UPDATE %s SET
	cancel_requested = TRUE,
	finished_at = CASE WHEN %[2]s THEN %[3]s ELSE finished_at END,
	claimed_until = CASE WHEN %[2]s THEN %[4]s ELSE claimed_until END,
	revision = revision + 1,
	last_updated_at = %[3]s,
	state = CASE WHEN %[2]s THEN sqlc.arg(%[5]s) ELSE state END
WHERE id = sqlc.arg(id)
	AND %[6]s`,
		OperationsTable, pending, s.now(), s.epoch(), CancelledStateArg, s.activeGuard())
}

// listStrandedOperations is the Postgres recovery sweep's read, spelled in this
// engine's clock. The grace period is rounded up on SQLite, so an operation is
// never re-offered before it has waited as long as it was allowed to.
func (s split) listStrandedOperations() string {
	state := querygen.Qualify(OperationsTable, StateColumn)

	return fmt.Sprintf(`SELECT
	%[1]s
FROM %[2]s
WHERE (%[3]s = sqlc.arg(%[4]s) AND %[2]s.created_at <= %[5]s)
	OR (%[3]s = sqlc.arg(%[6]s) AND %[2]s.claimed_until <= %[7]s)
ORDER BY %[2]s.created_at ASC
LIMIT %[8]s`,
		projection(),
		OperationsTable,
		state, PendingStateArg,
		s.before("grace_microseconds"),
		RunningStateArg,
		s.now(),
		s.limit(),
	)
}

// selectReapableOperations is the first of the reap's three statements: the
// terminal rows past the retention window, in id order, bounded — and read
// without a lock.
//
// The lock is the next statement's, taken by primary key, and that split is
// MySQL's doing. A locking read bounded by a range on an indexed column —
// finished_at at or before the horizon, here — takes a next-key lock on the
// first index record past the range, which is a row this reap was never going
// to delete and whichever writer or reaper reaches it next finds held. Point
// lookups on the primary key lock the rows they return and nothing beside them.
//
// The retention window is rounded up on SQLite, so a row is never reaped before
// it has been kept as long as it was asked to be.
func (s split) selectReapableOperations() string {
	return fmt.Sprintf(`SELECT %[1]s.id
FROM %[1]s
WHERE %[2]s
ORDER BY %[1]s.id ASC
LIMIT %[3]s`,
		OperationsTable,
		s.reapable(),
		s.limit(),
	)
}

// lockReapableOperations is the second: the candidates the read found, locked by
// primary key where the engine has a row lock, skipping whatever another
// statement holds — the Postgres reap's FOR UPDATE SKIP LOCKED, which makes a
// second reaper harmless rather than a second reaper waiting. It repeats the
// read's test, because the read took no lock and a row may have changed since.
//
// SQLite has no row lock to skip and needs none, having one writer; there the
// statement is the re-check alone.
func (s split) lockReapableOperations() string {
	return fmt.Sprintf(`SELECT %[1]s.id
FROM %[1]s
WHERE %[2]s
	AND %[3]s
ORDER BY %[1]s.id ASC%[4]s`,
		OperationsTable,
		s.reapable(),
		s.g.SetCondition(querygen.Qualify(OperationsTable, querygen.IDColumn), IDsArg),
		s.skipLocked(),
	)
}

// deleteReapedOperations removes what the lock took, repeating its test —
// a write has to hold every row-state test its select held, or it deletes
// whatever the rows became in between — with the ids bound last, because on
// both engines they expand to a placeholder per element and an argument after
// the expansion would be numbered into it.
func (s split) deleteReapedOperations() string {
	return fmt.Sprintf(`DELETE FROM %[1]s
WHERE %[2]s
	AND %[3]s%[4]s`,
		OperationsTable,
		s.reapable(),
		s.g.SetCondition(querygen.Qualify(OperationsTable, querygen.IDColumn), IDsArg),
		s.idOrder(),
	)
}

// activeGuard is the Postgres corpus's `state = ANY(active_states)`, as the two
// arguments the active set is.
func (s split) activeGuard() string {
	return fmt.Sprintf("state IN (sqlc.arg(%s), sqlc.arg(%s))", PendingStateArg, RunningStateArg)
}

// reapable is what makes a finished operation the reaper's: terminal, finished,
// and finished longer ago than the retention window.
func (s split) reapable() string {
	return strings.Join([]string{
		fmt.Sprintf("%s IN (sqlc.arg(%s), sqlc.arg(%s), sqlc.arg(%s))",
			querygen.Qualify(OperationsTable, StateColumn), SucceededStateArg, FailedStateArg, CancelledStateArg),
		querygen.Qualify(OperationsTable, "finished_at") + " IS NOT NULL",
		querygen.Qualify(OperationsTable, "finished_at") + " <= " + s.before("retention_microseconds"),
	}, "\n\tAND ")
}

// idOrder is the lock-ordering discipline for the reap's delete, where the
// engine can say it: MySQL acquires a single-table DELETE's row locks in its
// ORDER BY, which is what the Postgres reap's ordered CTE is for. SQLite has one
// writer, so there is no second party to a lock cycle.
func (s split) idOrder() string {
	if s.d != dialect.MySQL {
		return ""
	}

	return "\nORDER BY " + querygen.Qualify(OperationsTable, querygen.IDColumn)
}

// skipLocked is the lock the reap takes on its candidates, where the engine has
// one.
func (s split) skipLocked() string {
	if !s.d.SupportsSkipLocked() {
		return ""
	}

	return "\nFOR UPDATE SKIP LOCKED"
}

// limit is a read's bound. MySQL accepts only a bare placeholder there; see
// LimitArg for how the two converge on one name.
func (s split) limit() string {
	if s.d == dialect.MySQL {
		return "?"
	}

	return "sqlc.arg(" + LimitArg + ")"
}

// sqliteNow is SQLite's clock at the finest grain it has: milliseconds, as text
// in the one fixed-width shape every instant in its schema is stored in. See
// operations/migrations' sqlite.sql.
const sqliteNow = "strftime('%Y-%m-%d %H:%M:%f', 'now')"

// now is the server's clock as a statement should store it and compare against
// it.
//
// On MySQL that is querygen's stored spelling rather than the bare
// CURRENT_TIMESTAMP, which is second-granular whatever the column holds — and a
// write that stored a whole second could store the same value twice, which
// MySQL reports as no row changed.
func (s split) now() string {
	if s.d == dialect.SQLite {
		return sqliteNow
	}

	return s.g.StoredNow()
}

// after renders "the server's now, plus a bound microsecond count": the lease
// horizon.
//
// MySQL keeps the microseconds. SQLite's clock is millisecond text, so the
// count is rounded up to whole milliseconds — a lease is never made shorter
// than it was asked for — and then padded by one more. The pad is for the
// clock rather than the count: SQLite truncates its own now to the
// millisecond, so the instant a lease is measured from can already be up to a
// millisecond behind the instant it was taken, and a lease that ended a
// millisecond before it was asked to is a lease another worker takes over while
// this one still holds it. workqueue's lease is padded for the same reason.
func (s split) after(argument string, pad bool) string {
	if s.d == dialect.MySQL {
		return fmt.Sprintf("(%s + INTERVAL sqlc.arg(%s) MICROSECOND)", s.now(), argument)
	}

	milliseconds := "(sqlc.arg(" + argument + ") + 999) / 1000"
	if pad {
		milliseconds += " + 1"
	}

	return fmt.Sprintf("strftime('%%Y-%%m-%%d %%H:%%M:%%f', 'now', printf('%%+.3f seconds', (%s) / 1000.0))",
		milliseconds)
}

// before renders "the server's now, minus a bound microsecond count": the
// recovery sweep's grace horizon and the reap's retention horizon. Rounded up
// on SQLite for after's reason, so both are later rather than early.
func (s split) before(argument string) string {
	if s.d == dialect.MySQL {
		return fmt.Sprintf("(%s - INTERVAL sqlc.arg(%s) MICROSECOND)", s.now(), argument)
	}

	return fmt.Sprintf("strftime('%%Y-%%m-%%d %%H:%%M:%%f', 'now', printf('-%%.3f seconds', "+
		"((sqlc.arg(%s) + 999) / 1000) / 1000.0))", argument)
}

// epoch is the never-leased sentinel, in the shape the engine stores instants
// in. See the Postgres corpus's epoch for why claimed_until has one.
func (s split) epoch() string {
	if s.d == dialect.MySQL {
		return "'1970-01-01 00:00:00'"
	}

	return "'1970-01-01 00:00:00.000'"
}

// greatest is the two-argument maximum. MySQL spells it as Postgres does;
// SQLite's max is the scalar form when given two arguments.
func (s split) greatest(a, b string) string {
	if s.d == dialect.MySQL {
		return "GREATEST(" + a + ", " + b + ")"
	}

	return "max(" + a + ", " + b + ")"
}

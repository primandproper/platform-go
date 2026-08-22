package operations

import (
	"fmt"
	"strings"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/ddl"
	"github.com/primandproper/platform-go/v13/database/dialect"
)

// A note on the clock, because it is the one thing every statement here agrees
// about.
//
// The database's now() is the only clock. Every timestamp that governs
// scheduling — the lease, the recovery cutoff, the retention window — is
// written and compared server-side, and no timestamp is ever bound from a
// caller's process. Durations cross the seam instead, as microsecond counts
// turned into intervals. That is why this package has no clock.Clock option: a
// fleet coordinates through one table precisely because its processes never
// have to agree on the time.
//
// The exception that proves it is the timestamps read back out. Those are the
// server's own values, returned so a client can render them, and nothing in
// this package ever compares one of them to a local clock.

// tableFor renders the operations table name under a namespace. It is the one
// place the component segment is spelled, so the queries and the DDL cannot
// disagree about it.
func tableFor(prefix string) string {
	return ddl.Qualify(prefix) + "operations"
}

// epoch is the never-leased sentinel.
//
// claimed_until is NOT NULL and starts here rather than being nullable, so the
// claimability predicate is a single comparison — `claimed_until <= now()`
// covers both "never claimed" and "lease lapsed" — instead of a comparison plus
// a NULL branch that every future writer would have to remember.
const epoch = "TIMESTAMPTZ 'epoch'"

// micros renders "the bound microsecond count, as an interval".
func micros(marker string) string {
	return "(" + marker + "::bigint * INTERVAL '1 microsecond')"
}

// p renders the placeholder for the argument most recently appended to args.
func p(args []any) string {
	return dialect.Postgres.Placeholder(len(args))
}

// operationColumns is the projection every operation read scans. Declared once
// so the SELECTs and the Scan cannot drift apart.
const operationColumns = "id, kind, state, owner, request, " +
	"units_total, units_done, progress_unit, progress_count, count_label, progress_message, " +
	"result_uri, result_detail, error_code, error_message, error_retryable, " +
	"revision, attempts, cancel_requested, created_at, updated_at, started_at, finished_at"

// activeStatePredicate renders the guard every write that must not move a
// terminal row carries. It is a literal rather than a bound list because the
// active set is this package's own closed constant, not a caller's input.
const activeStatePredicate = "state IN ('pending', 'running')"

// tables holds the rendered table name, derived from one prefix so that adding
// a second table later cannot introduce an inconsistently named one.
type tables struct {
	base       string
	operations string
}

func newTables(prefix string) *tables {
	return &tables{base: prefix, operations: tableFor(prefix)}
}

// prefix returns the prefix the name was derived from, for the validation that
// has to run against every rendered name rather than against any one.
func (t *tables) prefix() string {
	return t.base
}

// insertRow is one operation's worth of bound parameters at insert time.
type insertRow struct {
	id         string
	kind       string
	owner      string
	countLabel string
	request    []byte
}

// buildInsert renders the operation write.
//
// Every timestamp is now(). An operation is pending the instant it is recorded,
// and binding the writer's clock for created_at would make the recovery sweep's
// "pending for longer than a minute" a comparison between two processes' ideas
// of what a minute is.
//
// It RETURNs the row it wrote, which is not an optimization but a requirement.
// The insert may be inside a transaction the caller has not committed, so a
// separate read would go out on another connection and find nothing — and the
// timestamps and revision a caller is handed have to be the server's, not a
// hopeful reconstruction of them.
//
// ON CONFLICT DO NOTHING rather than letting the primary key raise. A raised
// unique violation aborts the surrounding transaction, so a caller using WithID
// inside their own transaction would lose every write they had made alongside
// it; DO NOTHING returns no rows instead and leaves the transaction healthy,
// which is what makes the idempotency seam usable from where it is most wanted.
func (t *tables) buildInsert(row *insertRow) (query string, args []any) {
	args = []any{row.id, row.kind, row.owner, database.BlobOrNil(row.request), row.countLabel}

	return fmt.Sprintf(
		"INSERT INTO %s (id, kind, state, owner, request, count_label, "+
			"revision, created_at, updated_at, claimed_until) "+
			"VALUES (%s, %s, 'pending', %s, %s, %s, 1, now(), now(), %s) "+
			"ON CONFLICT (id) DO NOTHING RETURNING %s",
		t.operations, dialect.Postgres.Placeholder(1), dialect.Postgres.Placeholder(2),
		dialect.Postgres.Placeholder(3), dialect.Postgres.Placeholder(4),
		dialect.Postgres.Placeholder(5), epoch, operationColumns,
	), args
}

// buildSelect renders the single-operation read.
func (t *tables) buildSelect(id string) (query string, args []any) {
	return fmt.Sprintf("SELECT %s FROM %s WHERE id = %s",
		operationColumns, t.operations, dialect.Postgres.Placeholder(1)), []any{id}
}

// buildSelectMany renders the watcher's re-read: every subscribed operation in
// one statement.
//
// One query per wake regardless of how many subscriptions there are is the
// whole reason the watch path scales. A notification carries no payload, so a
// per-operation channel would mean a listener per subscription and a connection
// per listener; re-reading the set instead costs one statement and one index
// scan on the primary key.
func (t *tables) buildSelectMany(ids []string) (query string, args []any) {
	args = make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}

	return fmt.Sprintf("SELECT %s FROM %s WHERE id IN (%s) ORDER BY id",
		operationColumns, t.operations, dialect.Postgres.Placeholders(1, len(ids))), args
}

// buildListWhere renders the shared predicate for the listing and its count, so
// a page and the total it is a page of cannot disagree about what was asked for.
func (t *tables) buildListWhere(scope *ListScope, args []any) (where string, out []any) {
	if scope == nil {
		return "", args
	}

	clauses := make([]string, 0, 3) //nolint:mnd // the three fields of a ListScope

	if scope.Owner != "" {
		args = append(args, scope.Owner)
		clauses = append(clauses, "owner = "+p(args))
	}

	if scope.Kind != "" {
		args = append(args, scope.Kind)
		clauses = append(clauses, "kind = "+p(args))
	}

	if len(scope.States) > 0 {
		placeholders := make([]string, 0, len(scope.States))
		for _, state := range scope.States {
			args = append(args, string(state))
			placeholders = append(placeholders, p(args))
		}

		clauses = append(clauses, "state IN ("+strings.Join(placeholders, ", ")+")")
	}

	return strings.Join(clauses, " AND "), args
}

// buildList renders a listing, cursor-paginated on id.
//
// Ordering is on id alone rather than on (created_at, id). identifiers.New is
// xid, whose string form sorts in generation order, so id order is start order —
// and paginating on the single column the cursor names is what keeps a page
// boundary from skipping a row when two operations share a timestamp.
func (t *tables) buildList(scope *ListScope, cursor string, limit int, descending bool) (query string, args []any) {
	where, args := t.buildListWhere(scope, nil)

	direction, comparison := database.CursorOrder(descending)

	if cursor != "" {
		args = append(args, cursor)
		where = joinClause(where, "id"+comparison+p(args))
	}

	args = append(args, limit)

	return fmt.Sprintf("SELECT %s FROM %s%s ORDER BY id %s LIMIT %s",
		operationColumns, t.operations, wherePrefix(where), direction, p(args)), args
}

// buildCount renders the total for the paged read's Pagination.
func (t *tables) buildCount(scope *ListScope) (query string, args []any) {
	where, args := t.buildListWhere(scope, nil)

	return fmt.Sprintf("SELECT COUNT(*) FROM %s%s", t.operations, wherePrefix(where)), args
}

// buildBegin renders the transition a worker makes when it picks an operation
// up: pending or lapsed-running becomes running, under lease.
//
// This UPDATE is the package's real mutual exclusion, and it is worth being
// precise about why it rather than the work queue's lease. The queue leases the
// *key* — it says who was handed the dispatch — and its lease cannot be extended
// while long work runs. This row's lease can be, by every progress flush, which
// is the only way a lease can track work whose length is not known in advance.
// So the queue's expiry costs a wasted claim, and this predicate is what makes
// that waste rather than a second execution.
//
// attempts is written from the queue's own count rather than incremented here,
// so there is one attempt counter in the system and it is the one the claim
// incremented server-side.
//
// started_at is set only on the first pass. A reclaimed operation has been
// running since the first worker picked it up, and moving the timestamp would
// erase the fact that this is the second attempt at something that started ten
// minutes ago.
func (t *tables) buildBegin(id string, attempts int, leaseMicros int64) (query string, args []any) {
	args = []any{id, attempts, leaseMicros}

	return fmt.Sprintf(
		"UPDATE %s SET state = 'running', attempts = %s, "+
			"started_at = COALESCE(started_at, now()), updated_at = now(), "+
			"claimed_until = now() + %s, revision = revision + 1 "+
			"WHERE id = %s AND %s AND claimed_until <= now() "+
			"RETURNING %s",
		t.operations, dialect.Postgres.Placeholder(2), micros(dialect.Postgres.Placeholder(3)),
		dialect.Postgres.Placeholder(1), activeStatePredicate, operationColumns,
	), args
}

// progressRow is the buffered progress one flush writes.
type progressRow struct {
	unitsTotal *int
	unit       string
	message    string
	count      int64
	unitsDone  int
}

// buildProgress renders a progress flush, which is three statements' worth of
// work in one.
//
// It records where the Runner has got to, extends the lease that says this
// worker still has the operation, and returns whether a cancellation has been
// requested. Fusing them is not a micro-optimization: it means a Runner that
// reports progress is, by that fact alone, holding its lease and observing
// cancellations, with no second round trip and nothing for a Runner author to
// remember to call.
//
// progress_count is written with GREATEST rather than assigned. The counter is
// monotonic by contract, and the case that would otherwise break it is a
// straggler flush from a worker whose lease lapsed landing after the new
// worker's — which would walk a client's number backwards for no reason the
// client could ever explain.
//
// The guard is on state = 'running' rather than on the active set: a flush from
// a Runner whose operation has already been finished by somebody else must not
// resurrect its progress, and one arriving before Begin has no lease to extend.
func (t *tables) buildProgress(id string, row progressRow, leaseMicros int64) (query string, args []any) {
	args = []any{id, row.unitsTotal, row.unitsDone, row.unit, row.count, row.message, leaseMicros}

	// units_total is COALESCEd rather than assigned, so a flush that carries no
	// total cannot clear one the Runner already declared. A denominator that
	// appeared and then vanished would have a client's progress bar turn back
	// into a spinner mid-operation.
	return fmt.Sprintf(
		"UPDATE %s SET units_total = COALESCE(%s, units_total), units_done = GREATEST(units_done, %s), "+
			"progress_unit = %s, progress_count = GREATEST(progress_count, %s), "+
			"progress_message = %s, updated_at = now(), claimed_until = now() + %s, "+
			"revision = revision + 1 "+
			"WHERE id = %s AND state = 'running' "+
			"RETURNING cancel_requested, revision",
		t.operations,
		dialect.Postgres.Placeholder(2), dialect.Postgres.Placeholder(3), dialect.Postgres.Placeholder(4),
		dialect.Postgres.Placeholder(5), dialect.Postgres.Placeholder(6),
		micros(dialect.Postgres.Placeholder(7)), dialect.Postgres.Placeholder(1),
	), args
}

// finishRow is a terminal write's payload.
type finishRow struct {
	opErr        *Error
	result       *Result
	id           string
	state        State
	unitsAllDone bool
}

// buildFinish renders the terminal write.
//
// The lease is dropped outright: nothing will claim the operation again, and a
// claimed_until left in the future is a row every recovery sweep still has to
// consider. The guard is the active set, so a worker whose lease lapsed
// mid-operation cannot finish an operation somebody else has already finished —
// it matches no rows and is told so, which is the difference between a
// duplicate and a silently overwritten result.
//
// A successful operation with a declared unit total has units_done raised to it.
// A Runner that finished every unit but did not report the last one leaves a
// completed operation reading "8 of 9", which is the single most confusing thing
// a progress surface can show.
func (t *tables) buildFinish(row finishRow) (query string, args []any) {
	var (
		uri       string
		detail    []byte
		code      string
		message   string
		retryable bool
	)

	if row.result != nil {
		uri, detail = row.result.URI, row.result.Detail
	}

	if row.opErr != nil {
		code, message, retryable = row.opErr.Code, row.opErr.Message, row.opErr.Retryable
	}

	args = []any{row.id, string(row.state), uri, database.BlobOrNil(detail), code, message, retryable}

	unitsDone := "units_done"
	if row.unitsAllDone {
		unitsDone = "COALESCE(units_total, units_done)"
	}

	return fmt.Sprintf(
		"UPDATE %s SET state = %s, result_uri = %s, result_detail = %s, "+
			"error_code = %s, error_message = %s, error_retryable = %s, "+
			"units_done = %s, progress_unit = '', finished_at = now(), updated_at = now(), "+
			"claimed_until = %s, revision = revision + 1 "+
			"WHERE id = %s AND %s",
		t.operations, dialect.Postgres.Placeholder(2), dialect.Postgres.Placeholder(3),
		dialect.Postgres.Placeholder(4), dialect.Postgres.Placeholder(5),
		dialect.Postgres.Placeholder(6), dialect.Postgres.Placeholder(7),
		unitsDone, epoch, dialect.Postgres.Placeholder(1), activeStatePredicate,
	), args
}

// buildRelease renders the write that hands a running operation back for
// another attempt: state returns to pending, the lease drops, and the failure
// that caused it is recorded so a client polling in the gap sees why the
// operation is taking a second run rather than a blank pause.
//
// error_code and error_message are set on a row that has not failed, which is
// deliberate. They are the *last* error, not the final one; buildFinish
// overwrites them, and a succeeded operation carries no Error because the
// scanner only builds one for a failed state.
func (t *tables) buildRelease(id, code, message string) (query string, args []any) {
	args = []any{id, code, message}

	return fmt.Sprintf(
		"UPDATE %s SET state = 'pending', error_code = %s, error_message = %s, "+
			"error_retryable = TRUE, progress_unit = '', updated_at = now(), "+
			"claimed_until = %s, revision = revision + 1 "+
			"WHERE id = %s AND state = 'running'",
		t.operations, dialect.Postgres.Placeholder(2), dialect.Postgres.Placeholder(3),
		epoch, dialect.Postgres.Placeholder(1),
	), args
}

// buildRequestCancel renders the cancellation request.
//
// A pending operation is cancelled outright in the same statement: nothing has
// started, so there is nothing to ask and nobody to ask it of. A running one has
// the flag set and keeps running until its Runner notices — which is the only
// correct answer, because only the Runner knows what a half-finished unit of its
// work has left behind.
//
// A terminal operation matches the guard and is left alone, so Cancel is
// idempotent and a double click is not an error.
func (t *tables) buildRequestCancel(id string) (query string, args []any) {
	args = []any{id}

	return fmt.Sprintf(
		"UPDATE %s SET cancel_requested = TRUE, "+
			"state = CASE WHEN state = 'pending' THEN 'cancelled' ELSE state END, "+
			"finished_at = CASE WHEN state = 'pending' THEN now() ELSE finished_at END, "+
			"claimed_until = CASE WHEN state = 'pending' THEN %s ELSE claimed_until END, "+
			"updated_at = now(), revision = revision + 1 "+
			"WHERE id = %s AND %s",
		t.operations, epoch, dialect.Postgres.Placeholder(1), activeStatePredicate,
	), args
}

// buildSelectStranded renders the recovery sweep's read: operations that are
// active but that nothing is going to pick up.
//
// Two shapes qualify, and they are the same fact seen from either side of
// Start's two writes. A pending operation older than the grace period is one
// whose enqueue never landed — the process died between recording it and
// offering it. A running operation whose lease lapsed is one whose worker died
// and whose queue item went with it.
//
// The grace period is what keeps this from re-enqueueing every operation the
// fleet is starting right now, which is the moment it would hurt most.
func (t *tables) buildSelectStranded(graceMicros int64, limit int) (query string, args []any) {
	args = []any{graceMicros, limit}

	return fmt.Sprintf(
		"SELECT %s FROM %s "+
			"WHERE (state = 'pending' AND updated_at <= now() - %s) "+
			"OR (state = 'running' AND claimed_until <= now() - %s) "+
			"ORDER BY updated_at LIMIT %s",
		operationColumns, t.operations,
		micros(dialect.Postgres.Placeholder(1)), micros(dialect.Postgres.Placeholder(1)),
		dialect.Postgres.Placeholder(2),
	), args
}

// buildReap renders the retention delete.
//
// It deletes terminal rows only, and it deletes them by primary key through a
// CTE that orders and locks them explicitly. That ordering is the same lock
// discipline the work queue's documentation opens with: with one total order,
// contention between a reap and a concurrent write degrades into a queue;
// without it, two writers that overlap in opposite orders deadlock the moment
// they meet.
func (t *tables) buildReap(retentionMicros int64, limit int) (query string, args []any) {
	args = []any{retentionMicros, limit}

	return fmt.Sprintf(
		"WITH doomed AS ("+
			"SELECT id FROM %[1]s WHERE state IN ('succeeded', 'failed', 'cancelled') "+
			"AND finished_at IS NOT NULL AND finished_at <= now() - %[2]s "+
			"ORDER BY id LIMIT %[3]s FOR UPDATE SKIP LOCKED"+
			") DELETE FROM %[1]s WHERE id IN (SELECT id FROM doomed)",
		t.operations, micros(dialect.Postgres.Placeholder(1)), dialect.Postgres.Placeholder(2),
	), args
}

// joinClause ANDs a clause onto an existing predicate, tolerating an empty one.
func joinClause(where, clause string) string {
	if where == "" {
		return clause
	}

	return where + " AND " + clause
}

// wherePrefix renders a predicate as a WHERE clause, or as nothing at all.
func wherePrefix(where string) string {
	if where == "" {
		return ""
	}

	return " WHERE " + where
}

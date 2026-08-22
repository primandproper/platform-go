package saga

import (
	"fmt"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v13/database"
	"github.com/primandproper/platform-go/v13/database/ddl"
	"github.com/primandproper/platform-go/v13/database/dialect"
)

// A note on timestamps, because one dialect does something surprising.
//
// Every time this package binds is a UTC time.Time, and every comparison is
// against another such value. Postgres and MySQL store these as real temporal
// types and compare them as such. SQLite does not: modernc's driver stores a
// bound time.Time as Go's own String() rendering — "2026-07-30 12:00:00 +0000
// UTC" — so `next_attempt <= ?` there is a string comparison.
//
// That is still correct, because the rendering begins with a fixed-width
// "YYYY-MM-DD HH:MM:SS" prefix and everything is UTC, so lexical order is
// chronological order. It stops being correct the moment a value is bound in a
// non-UTC location, so do not remove the .UTC() calls at the binding sites.

// tables holds the rendered table names. Derived from one prefix so that adding
// a second table later cannot introduce an inconsistently named one — see
// saga/migrations.
type tables struct {
	base      string
	instances string
}

func newTables(prefix string) *tables {
	return &tables{
		base:      prefix,
		instances: ddl.Qualify(prefix) + "saga_instances",
	}
}

// prefix returns the prefix the names were derived from, for the validation
// that has to run against every rendered name rather than against any one.
func (t *tables) prefix() string {
	return t.base
}

// instanceColumns is the projection every instance read scans. Declared once so
// the SELECTs and the Scan cannot drift apart.
const instanceColumns = "id, definition, status, current_step, step_names, state, " +
	"attempts, last_error, resume_status, started_at, updated_at"

// buildInsertInstance renders the instance write.
func (t *tables) buildInsertInstance(d dialect.Dialect, inst *Record, stepNames []byte, nextAttempt time.Time) (query string, args []any) {
	args = []any{
		inst.ID, inst.Definition, string(inst.Status), inst.CurrentStep, string(stepNames),
		database.BlobOrNil(inst.State), inst.Attempts, inst.LastError, string(inst.ResumeStatus),
		inst.StartedAt.UTC(), inst.UpdatedAt.UTC(), nextAttempt.UTC(),
	}

	return fmt.Sprintf(
		"INSERT INTO %s (id, definition, status, current_step, step_names, state, "+
			"attempts, last_error, resume_status, started_at, updated_at, next_attempt) VALUES (%s)",
		t.instances, d.Placeholders(1, len(args)),
	), args
}

// buildSelectInstance renders the single-instance read.
func (t *tables) buildSelectInstance(d dialect.Dialect, instanceID string) (query string, args []any) {
	return fmt.Sprintf("SELECT %s FROM %s WHERE id = %s", instanceColumns, t.instances, d.Placeholder(1)),
		[]any{instanceID}
}

// buildListWhere renders the shared predicate for the listing and its count, so
// a page and the total it is a page of cannot disagree about what was asked for.
func (t *tables) buildListWhere(d dialect.Dialect, scope *ListScope, args []any) (where string, out []any) {
	clauses := make([]string, 0, 2)

	if scope == nil {
		return "", args
	}

	if scope.Definition != "" {
		args = append(args, scope.Definition)
		clauses = append(clauses, "definition = "+d.Placeholder(len(args)))
	}

	if len(scope.Statuses) > 0 {
		placeholders := make([]string, 0, len(scope.Statuses))
		for _, status := range scope.Statuses {
			args = append(args, string(status))
			placeholders = append(placeholders, d.Placeholder(len(args)))
		}

		clauses = append(clauses, "status IN ("+strings.Join(placeholders, ", ")+")")
	}

	if len(clauses) == 0 {
		return "", args
	}

	return strings.Join(clauses, " AND "), args
}

// buildListInstances renders a listing, cursor-paginated on id.
//
// Ordering is on id alone rather than on (started_at, id). identifiers.New is
// xid, whose string form sorts in generation order, so id order is start order
// — and paginating on the single column the cursor names is what keeps a page
// boundary from skipping a row when two instances share a timestamp.
func (t *tables) buildListInstances(
	d dialect.Dialect,
	scope *ListScope,
	cursor string,
	limit int,
	descending bool,
) (query string, args []any) {
	where, args := t.buildListWhere(d, scope, nil)

	direction, comparison := "ASC", " > "
	if descending {
		direction, comparison = "DESC", " < "
	}

	if cursor != "" {
		args = append(args, cursor)
		where = joinClause(where, "id"+comparison+d.Placeholder(len(args)))
	}

	args = append(args, limit)

	return fmt.Sprintf(
		"SELECT %s FROM %s%s ORDER BY id %s LIMIT %s",
		instanceColumns, t.instances, wherePrefix(where), direction, d.Placeholder(len(args)),
	), args
}

// buildCountInstances renders the total for the paged read's Pagination.
func (t *tables) buildCountInstances(d dialect.Dialect, scope *ListScope) (query string, args []any) {
	where, args := t.buildListWhere(d, scope, nil)

	return fmt.Sprintf("SELECT COUNT(*) FROM %s%s", t.instances, wherePrefix(where)), args
}

// buildSelectClaimable renders the query picking the next batch of instance IDs
// to claim: advanceable, due, and not currently leased by another worker.
func (t *tables) buildSelectClaimable(d dialect.Dialect, now time.Time, limit int, skipLocked bool) (query string, args []any) {
	args = make([]any, 0, len(activeStatuses)+3)

	placeholders := make([]string, 0, len(activeStatuses))
	for _, status := range activeStatuses {
		args = append(args, string(status))
		placeholders = append(placeholders, d.Placeholder(len(args)))
	}

	args = append(args, now.UTC())
	dueArg := d.Placeholder(len(args))

	args = append(args, now.UTC())
	leaseArg := d.Placeholder(len(args))

	args = append(args, limit)

	query = fmt.Sprintf(
		"SELECT id FROM %s WHERE status IN (%s) AND next_attempt <= %s "+
			"AND (claimed_until IS NULL OR claimed_until <= %s) "+
			"ORDER BY next_attempt, started_at, id LIMIT %s",
		t.instances, strings.Join(placeholders, ", "), dueArg, leaseArg, d.Placeholder(len(args)),
	)

	if skipLocked && d.SupportsSkipLocked() {
		query += " FOR UPDATE SKIP LOCKED"
	}

	return query, args
}

// buildClaim renders the UPDATE that leases the selected rows.
//
// The attempt count is incremented here rather than on failure: a worker that
// dies mid-step has still consumed an attempt, so a step whose Do reliably
// kills the process eventually compensates rather than being reclaimed forever.
// The status guard is repeated even though the rows were just selected as
// active, because between the SELECT and this UPDATE another worker's advance
// may have finished the saga.
func (t *tables) buildClaim(d dialect.Dialect, ids []string, claimedUntil, at time.Time) (query string, args []any) {
	args = make([]any, 0, len(ids)+len(activeStatuses)+2)
	args = append(args, claimedUntil.UTC(), at.UTC())

	for _, id := range ids {
		args = append(args, id)
	}

	idPlaceholders := d.Placeholders(3, len(ids))

	placeholders := make([]string, 0, len(activeStatuses))
	for _, status := range activeStatuses {
		args = append(args, string(status))
		placeholders = append(placeholders, d.Placeholder(len(args)))
	}

	return fmt.Sprintf(
		"UPDATE %s SET claimed_until = %s, updated_at = %s, attempts = attempts + 1 "+
			"WHERE id IN (%s) AND status IN (%s)",
		t.instances, d.Placeholder(1), d.Placeholder(2), idPlaceholders, strings.Join(placeholders, ", "),
	), args
}

// buildFetchByIDs renders the projection of a claimed batch.
func (t *tables) buildFetchByIDs(d dialect.Dialect, ids []string) (query string, args []any) {
	args = make([]any, 0, len(ids)+len(activeStatuses))
	for _, id := range ids {
		args = append(args, id)
	}

	idPlaceholders := d.Placeholders(1, len(ids))

	placeholders := make([]string, 0, len(activeStatuses))
	for _, status := range activeStatuses {
		args = append(args, string(status))
		placeholders = append(placeholders, d.Placeholder(len(args)))
	}

	return fmt.Sprintf(
		"SELECT %s FROM %s WHERE id IN (%s) AND status IN (%s) ORDER BY next_attempt, started_at, id",
		instanceColumns, t.instances, idPlaceholders, strings.Join(placeholders, ", "),
	), args
}

// buildAdvance renders the UPDATE recording a cursor that moved.
//
// The guard is on the active statuses, so an advance cannot resurrect an
// instance that another worker already finished or that an operator marked
// stuck while this one was busy. attempts is zeroed and last_error cleared
// because the step they described is behind us either way.
//
// A terminal status clears the lease outright: nothing claims the instance
// again, and a claimed_until left in the future is a row the claim index still
// has to skip.
func (t *tables) buildAdvance(d dialect.Dialect, inst *Record, nextAttempt, at time.Time) (query string, args []any) {
	args = make([]any, 0, len(activeStatuses)+8)
	args = append(args,
		string(inst.Status), inst.CurrentStep, database.BlobOrNil(inst.State),
		inst.LastError, string(inst.ResumeStatus), at.UTC(), nextAttempt.UTC(),
	)

	set := fmt.Sprintf(
		"status = %s, current_step = %s, state = %s, last_error = %s, resume_status = %s, "+
			"updated_at = %s, next_attempt = %s, attempts = 0",
		d.Placeholder(1), d.Placeholder(2), d.Placeholder(3), d.Placeholder(4),
		d.Placeholder(5), d.Placeholder(6), d.Placeholder(7),
	)

	// The lease is dropped whenever the pass is over: the instance is finished,
	// or it is waiting out a step's delay. Holding it through a delay shorter
	// than the lease would make the lease, not the delay, decide when the next
	// step runs. A mid-pass advance keeps it, because this worker is about to
	// run the next step itself.
	if inst.Status.Terminal() || nextAttempt.After(at) {
		set += ", claimed_until = NULL"
	}

	args = append(args, inst.ID)
	where := "id = " + d.Placeholder(len(args))

	placeholders := make([]string, 0, len(activeStatuses))
	for _, status := range activeStatuses {
		args = append(args, string(status))
		placeholders = append(placeholders, d.Placeholder(len(args)))
	}

	where += " AND status IN (" + strings.Join(placeholders, ", ") + ")"

	return fmt.Sprintf("UPDATE %s SET %s WHERE %s", t.instances, set, where), args
}

// buildReschedule renders the UPDATE applied to a step that failed and will be
// tried again: record why, when, and drop the lease.
//
// attempts is written rather than left as the claim incremented it, so the
// worker's count — which spans several steps in one pass — is the one that
// survives.
func (t *tables) buildReschedule(
	d dialect.Dialect,
	instanceID string,
	attempts int,
	nextAttempt time.Time,
	lastErr string,
	at time.Time,
) (query string, args []any) {
	args = make([]any, 0, len(activeStatuses)+5)
	args = append(args, attempts, nextAttempt.UTC(), lastErr, at.UTC(), instanceID)

	where := "id = " + d.Placeholder(5)

	placeholders := make([]string, 0, len(activeStatuses))
	for _, status := range activeStatuses {
		args = append(args, string(status))
		placeholders = append(placeholders, d.Placeholder(len(args)))
	}

	where += " AND status IN (" + strings.Join(placeholders, ", ") + ")"

	return fmt.Sprintf(
		"UPDATE %s SET attempts = %s, next_attempt = %s, last_error = %s, updated_at = %s, "+
			"claimed_until = NULL WHERE %s",
		t.instances, d.Placeholder(1), d.Placeholder(2), d.Placeholder(3), d.Placeholder(4), where,
	), args
}

// buildRelease renders the UPDATE handing an instance back unchanged. It does
// not touch next_attempt, so the instance is claimable on the next cycle.
func (t *tables) buildRelease(d dialect.Dialect, instanceID string, at time.Time) (query string, args []any) {
	return fmt.Sprintf(
			"UPDATE %s SET claimed_until = NULL, updated_at = %s WHERE id = %s",
			t.instances, d.Placeholder(1), d.Placeholder(2),
		), []any{
			at.UTC(), instanceID,
		}
}

// buildRequeue renders a guarded status change that also makes the instance
// claimable now.
//
// The guard is in the predicate rather than in a read-then-write, which is what
// makes Resume safe against being clicked twice: the second writer matches no
// rows and is told so, instead of both succeeding and handing two workers the
// same half-compensated saga.
func (t *tables) buildRequeue(
	d dialect.Dialect,
	instanceID string,
	from []Status,
	to Status,
	at time.Time,
) (query string, args []any) {
	args = make([]any, 0, len(from)+4)
	args = append(args, string(to), at.UTC(), at.UTC(), instanceID)

	where := "id = " + d.Placeholder(4)

	placeholders := make([]string, 0, len(from))
	for _, status := range from {
		args = append(args, string(status))
		placeholders = append(placeholders, d.Placeholder(len(args)))
	}

	where += " AND status IN (" + strings.Join(placeholders, ", ") + ")"

	// resume_status is cleared as the instance leaves StatusStuck: it answers
	// "what would Resume do with this", and an instance that is running again
	// has already had that question answered.
	return fmt.Sprintf(
		"UPDATE %s SET status = %s, next_attempt = %s, updated_at = %s, "+
			"resume_status = '', attempts = 0, claimed_until = NULL WHERE %s",
		t.instances, d.Placeholder(1), d.Placeholder(2), d.Placeholder(3), where,
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

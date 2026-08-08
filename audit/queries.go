package audit

import (
	"fmt"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v10/database/ddl"
	"github.com/primandproper/platform-go/v10/database/dialect"
	"github.com/primandproper/platform-go/v10/filtering"
)

// entryColumns is the projection every read scans. Declared once so the SELECT
// and the Scan cannot drift apart, and ordered to match scanEntries.
const entryColumns = "id, seq, scope, recorded_at, event_type, resource_type, " +
	"resource_id, actor_id, actor_type, actor_ip, change_set, metadata, prev_hash, hash"

// A note on timestamps, because one dialect does something surprising and this
// package is more sensitive to it than most.
//
// Every time this package binds is a UTC time.Time truncated to microseconds,
// and every comparison is against another such value. Postgres and MySQL store
// these as real temporal types; SQLite does not — modernc's driver stores a
// bound time.Time as Go's own String() rendering, so `recorded_at < ?` there is
// a string comparison. That is still correct, because the rendering begins with
// a fixed-width "YYYY-MM-DD HH:MM:SS" prefix and everything is UTC, so lexical
// order is chronological order.
//
// The truncation is not about ordering, it is about the hash chain. Postgres
// and MySQL keep microseconds; a nanosecond-precision timestamp would come back
// different from what went in, the recomputed digest would differ, and Verify
// would report every entry in the table as tampered. Truncating at the write
// site is what makes the round trip exact — so do not remove the .Truncate or
// the .UTC calls at the binding sites.

// tables names the two tables the package reads and writes, derived from one
// prefix. See the migrations package for why they are not independently
// configurable.
type tables struct {
	entries string
	chains  string
}

// newTables derives both table names from a prefix.
func newTables(prefix string) *tables {
	return &tables{
		entries: ddl.Qualify(prefix) + "audit_log_entries",
		chains:  ddl.Qualify(prefix) + "audit_log_chains",
	}
}

// entryRow is one entry's worth of bound parameters, with the field blobs
// already encoded — the same bytes the digest was taken over.
type entryRow struct {
	recordedAt   time.Time
	id           string
	scope        string
	eventType    string
	resourceType string
	resourceID   string
	actorID      string
	actorType    string
	actorIP      string
	prevHash     string
	hash         string
	changes      []byte
	metadata     []byte
	seq          int64
}

// buildSelectChainHead renders the read of a scope's chain state.
//
// forUpdate is what serializes two transactions recording into the same scope.
// The row is locked for the remainder of the caller's transaction, so the
// second writer blocks here and then reads the head the first one committed
// rather than computing the same next position from a stale read. Postgres and
// MySQL both take a row lock; SQLite has no FOR UPDATE and needs none, since it
// admits one writer at a time by construction.
func (t *tables) buildSelectChainHead(d dialect.Dialect, scope string, forUpdate bool) (query string, args []any) {
	query = fmt.Sprintf(
		"SELECT head_seq, head_hash, pruned_through_seq, pruned_through_hash FROM %s WHERE scope = %s",
		t.chains, d.Placeholder(1),
	)

	if forUpdate && supportsRowLock(d) {
		query += " FOR UPDATE"
	}

	return query, []any{scope}
}

// supportsRowLock reports whether the dialect can take an explicit row lock
// with FOR UPDATE.
//
// It happens to select the same two dialects as Dialect.SupportsSkipLocked, and
// is written out separately anyway: that method answers whether competing
// workers can skip past locked rows, which is a different question that only
// coincidentally has the same answer today.
func supportsRowLock(d dialect.Dialect) bool {
	return d == dialect.Postgres || d == dialect.MySQL
}

// buildInsertChain renders the genesis row for a scope that has never been
// written to.
//
// The conflict clause is what keeps the first write to a new scope from being a
// race. Two transactions that both find no chain row would otherwise both
// insert one and the loser would fail on the primary key — taking the caller's
// business transaction down with it, on nothing worse than being second. With
// the clause, the loser waits for the winner to commit and then proceeds to
// lock the row that now exists.
func (t *tables) buildInsertChain(d dialect.Dialect, scope string, now time.Time) (query string, args []any) {
	insert := fmt.Sprintf(
		"INSERT %sINTO %s (scope, head_seq, head_hash, pruned_through_seq, pruned_through_hash, updated_at) "+
			"VALUES (%s, -1, '', -1, '', %s)",
		ignorePrefix(d), t.chains, d.Placeholder(1), d.Placeholder(2),
	)

	if d == dialect.Postgres {
		insert += " ON CONFLICT (scope) DO NOTHING"
	}

	return insert, []any{scope, now}
}

// ignorePrefix renders the dialect's way of spelling "skip a row that is
// already there" in the INSERT verb itself. Postgres spells it as a trailing
// ON CONFLICT clause instead, which buildInsertChain appends.
func ignorePrefix(d dialect.Dialect) string {
	switch d {
	case dialect.MySQL:
		return "IGNORE "
	case dialect.SQLite:
		return "OR IGNORE "
	case dialect.Postgres:
		return ""
	default:
		return ""
	}
}

// buildUpdateChainHead renders the advance of a scope's chain head, run after
// the entries it covers have been inserted.
func (t *tables) buildUpdateChainHead(d dialect.Dialect, scope, hash string, seq int64, now time.Time) (query string, args []any) {
	return fmt.Sprintf(
		"UPDATE %s SET head_seq = %s, head_hash = %s, updated_at = %s WHERE scope = %s",
		t.chains, d.Placeholder(1), d.Placeholder(2), d.Placeholder(3), d.Placeholder(4),
	), []any{seq, hash, now, scope}
}

// columnsPerRow is how many parameters one entry binds. It is also what makes
// maxBatchRows what it is.
const columnsPerRow = 14

// maxBatchRows caps how many entries go into one multi-row INSERT.
//
// SQLite's default bind-parameter ceiling is the binding constraint — 999 on
// builds before 3.32 — and at fourteen columns per row, seventy rows is 980
// parameters, clear on every dialect. A caller bulk-importing a thousand
// entries in one transaction therefore issues a handful of statements rather
// than one the driver refuses.
const maxBatchRows = 70

// buildInsertEntries renders a multi-row INSERT for the supplied rows. Several
// entries recorded in one transaction cost one round trip, not one each.
func (t *tables) buildInsertEntries(d dialect.Dialect, rows []entryRow) (query string, args []any) {
	args = make([]any, 0, len(rows)*columnsPerRow)
	tuples := make([]string, 0, len(rows))

	for i := range rows {
		r := &rows[i]
		tuples = append(tuples, "("+d.Placeholders(len(args)+1, columnsPerRow)+")")
		args = append(args,
			r.id, r.seq, r.scope, r.recordedAt, r.eventType, r.resourceType,
			r.resourceID, r.actorID, r.actorType, r.actorIP, r.changes, r.metadata,
			r.prevHash, r.hash,
		)
	}

	return fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES %s",
		t.entries, entryColumns, strings.Join(tuples, ", "),
	), args
}

// buildSelectEntryByID renders the single-entry read.
func (t *tables) buildSelectEntryByID(d dialect.Dialect, id string) (query string, args []any) {
	return fmt.Sprintf(
		"SELECT %s FROM %s WHERE id = %s",
		entryColumns, t.entries, d.Placeholder(1),
	), []any{id}
}

// buildSelectEntryBySeq renders the read of one position in a scope's chain. It
// is how Verify anchors a range that begins mid-chain: the entry before the
// first one in range is what the first one's link is checked against.
func (t *tables) buildSelectEntryBySeq(d dialect.Dialect, scope string, seq int64) (query string, args []any) {
	return fmt.Sprintf(
		"SELECT %s FROM %s WHERE scope = %s AND seq = %s",
		entryColumns, t.entries, d.Placeholder(1), d.Placeholder(2),
	), []any{scope, seq}
}

// buildSelectChainRange renders Verify's walk: one scope's entries within a
// time window, in chain order.
//
// Ordered by seq rather than recorded_at, and that is not interchangeable. The
// chain is defined by position, and two entries can share a timestamp — the
// clock has microsecond resolution and a transaction can record several entries
// at once — so ordering by time would sometimes hand the walk a pair in the
// wrong order and report an intact chain as broken.
func (t *tables) buildSelectChainRange(d dialect.Dialect, scope string, from, to time.Time) (query string, args []any) {
	args = []any{scope}
	where := "scope = " + d.Placeholder(1)

	if !from.IsZero() {
		args = append(args, from.UTC())
		where += " AND recorded_at >= " + d.Placeholder(len(args))
	}
	if !to.IsZero() {
		args = append(args, to.UTC())
		where += " AND recorded_at <= " + d.Placeholder(len(args))
	}

	return fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s ORDER BY seq",
		entryColumns, t.entries, where,
	), args
}

// buildListEntries renders a filtered, cursor-paginated read.
func (t *tables) buildListEntries(d dialect.Dialect, q *Query, filter *filtering.QueryFilter, limit int) (query string, args []any) {
	where, args := q.where(d)
	where, args = applyFilterWindow(d, where, args, filter)

	descending := sortsDescending(filter)

	if filter != nil && filter.Cursor != nil && *filter.Cursor != "" {
		args = append(args, *filter.Cursor)
		if descending {
			where = append(where, "id < "+d.Placeholder(len(args)))
		} else {
			where = append(where, "id > "+d.Placeholder(len(args)))
		}
	}

	order := "id"
	if descending {
		order = "id DESC"
	}

	args = append(args, limit)

	return fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s ORDER BY %s LIMIT %s",
		entryColumns, t.entries, joinWhere(where), order, d.Placeholder(len(args)),
	), args
}

// buildCountEntries renders the total the paged read reports alongside its
// page. It applies the query's own predicates and the filter's time window, but
// not the cursor: the total is of the matching set, not of what is left after
// the cursor.
func (t *tables) buildCountEntries(d dialect.Dialect, q *Query, filter *filtering.QueryFilter) (query string, args []any) {
	where, args := q.where(d)
	where, args = applyFilterWindow(d, where, args, filter)

	return fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s", t.entries, joinWhere(where)), args
}

// buildSelectPrunableScopes renders the retention sweep's first question: which
// scopes hold anything old enough to prune.
//
// It pages by scope rather than capping how many a sweep may see, because the
// count it returns is what tells the sweep whether it has run out of work — a
// page short of the limit means there is nothing behind it. The cursor is the
// last scope of the previous page, which is a keyset and not an offset, so a
// scope written behind the cursor while the batch runs cannot displace one that
// has not been visited yet.
//
// after is a pointer for the same reason Query.Scope is: the empty string is a
// real scope, the one platform-level events are recorded in. A plain string
// would make the first page — which has no cursor — indistinguishable from a
// page positioned just past that scope, and the log's own events would be the
// ones never pruned.
func (t *tables) buildSelectPrunableScopes(
	d dialect.Dialect,
	before time.Time,
	after *string,
	limit int,
) (query string, args []any) {
	args = []any{before.UTC()}

	cursor := ""
	if after != nil {
		cursor = " AND scope > " + d.Placeholder(2)
		args = append(args, *after)
	}

	return fmt.Sprintf(
		"SELECT DISTINCT scope FROM %s WHERE recorded_at <= %s%s ORDER BY scope LIMIT %s",
		t.entries, d.Placeholder(1), cursor, d.Placeholder(len(args)+1),
	), append(args, limit)
}

// buildCountPrunableEntries renders the retention backlog reading: how many
// entries are at or before the cutoff, saturating at ceiling.
//
// Bounded by a subquery rather than counted outright, so the cost of the
// reading does not grow with the size of the problem it reports — which would
// make it most expensive exactly when somebody most needs it. The alias is not
// decoration: Postgres and MySQL both require a derived table to have one.
func (t *tables) buildCountPrunableEntries(d dialect.Dialect, before time.Time, ceiling int) (query string, args []any) {
	return fmt.Sprintf(
		"SELECT COUNT(*) FROM (SELECT 1 FROM %s WHERE recorded_at <= %s LIMIT %s) AS audit_prune_backlog",
		t.entries, d.Placeholder(1), d.Placeholder(2),
	), []any{before.UTC(), ceiling}
}

// buildSelectPruneBounds renders the two positions that decide what a sweep may
// remove from one scope: the oldest entry it still holds, and the oldest entry
// that must survive the cutoff.
//
// Both come from one statement over one index range rather than two, and the
// CASE expression is what allows it — a second aggregate cannot carry its own
// WHERE clause, but it can be fed a NULL for every row the predicate excludes.
//
// The predicate is strictly greater than the cutoff, because an entry recorded
// exactly at it is one this sweep may remove — the same at-or-before reading
// the scope listing and the backlog count use. The three have to agree, or a
// row sits in the backlog that no sweep will ever take.
//
// The second value is the reason the sweep is expressed in positions at all
// rather than deleting by timestamp directly. recorded_at comes from the
// recording process's clock, so across several processes it is not perfectly
// ordered with respect to seq; deleting every row older than the cutoff could
// therefore punch a hole in the middle of the chain, which is indistinguishable
// from the tampering this package exists to detect. Pruning strictly below the
// first entry that must survive keeps the survivors a contiguous suffix, always.
func (t *tables) buildSelectPruneBounds(d dialect.Dialect, scope string, before time.Time) (query string, args []any) {
	return fmt.Sprintf(
		"SELECT MIN(seq), MIN(CASE WHEN recorded_at > %s THEN seq END) FROM %s WHERE scope = %s",
		d.Placeholder(1), t.entries, d.Placeholder(2),
	), []any{before.UTC(), scope}
}

// buildSelectPruneTarget renders the read of the last entry a sweep will
// remove, whose hash becomes the scope's new prune watermark.
//
// It asks for the highest position at or below the boundary rather than the
// boundary itself, so a chain with a hole in it — which is to say one that has
// already been tampered with — still yields a real row rather than nothing.
func (t *tables) buildSelectPruneTarget(d dialect.Dialect, scope string, boundary int64) (query string, args []any) {
	return fmt.Sprintf(
		"SELECT seq, hash FROM %s WHERE scope = %s AND seq <= %s ORDER BY seq DESC LIMIT 1",
		t.entries, d.Placeholder(1), d.Placeholder(2),
	), []any{scope, boundary}
}

// buildDeletePruned renders the sweep's DELETE.
func (t *tables) buildDeletePruned(d dialect.Dialect, scope string, through int64) (query string, args []any) {
	return fmt.Sprintf(
		"DELETE FROM %s WHERE scope = %s AND seq <= %s",
		t.entries, d.Placeholder(1), d.Placeholder(2),
	), []any{scope, through}
}

// buildUpdateChainPruned renders the watermark update that records where
// retention pruned to, so Verify can tell the sweep's gap from anyone else's.
func (t *tables) buildUpdateChainPruned(d dialect.Dialect, scope, hash string, through int64, now time.Time) (query string, args []any) {
	return fmt.Sprintf(
		"UPDATE %s SET pruned_through_seq = %s, pruned_through_hash = %s, updated_at = %s WHERE scope = %s",
		t.chains, d.Placeholder(1), d.Placeholder(2), d.Placeholder(3), d.Placeholder(4),
	), []any{through, hash, now, scope}
}

// where renders the query's predicates and the arguments they bind.
func (q *Query) where(d dialect.Dialect) (predicates []string, args []any) {
	if q == nil {
		return nil, nil
	}

	if q.Scope != nil {
		args = append(args, *q.Scope)
		predicates = append(predicates, "scope = "+d.Placeholder(len(args)))
	}
	if q.ActorID != "" {
		args = append(args, q.ActorID)
		predicates = append(predicates, "actor_id = "+d.Placeholder(len(args)))
	}
	if q.ActorType != "" {
		args = append(args, string(q.ActorType))
		predicates = append(predicates, "actor_type = "+d.Placeholder(len(args)))
	}
	if q.ResourceID != "" {
		args = append(args, q.ResourceID)
		predicates = append(predicates, "resource_id = "+d.Placeholder(len(args)))
	}

	if len(q.ResourceTypes) > 0 {
		start := len(args) + 1
		for _, rt := range q.ResourceTypes {
			args = append(args, rt)
		}
		predicates = append(predicates, "resource_type IN ("+d.Placeholders(start, len(q.ResourceTypes))+")")
	}

	if len(q.EventTypes) > 0 {
		start := len(args) + 1
		for _, et := range q.EventTypes {
			args = append(args, string(et))
		}
		predicates = append(predicates, "event_type IN ("+d.Placeholders(start, len(q.EventTypes))+")")
	}

	return predicates, args
}

// applyFilterWindow adds the QueryFilter's time bounds. They map onto
// recorded_at, so the createdBefore and createdAfter query parameters an HTTP
// caller already knows how to send mean what they should here.
func applyFilterWindow(
	d dialect.Dialect,
	predicates []string,
	args []any,
	filter *filtering.QueryFilter,
) (out []string, outArgs []any) {
	if filter == nil {
		return predicates, args
	}

	if filter.CreatedAfter != nil {
		args = append(args, filter.CreatedAfter.UTC())
		predicates = append(predicates, "recorded_at > "+d.Placeholder(len(args)))
	}
	if filter.CreatedBefore != nil {
		args = append(args, filter.CreatedBefore.UTC())
		predicates = append(predicates, "recorded_at < "+d.Placeholder(len(args)))
	}

	return predicates, args
}

// joinWhere renders predicates as a WHERE body, standing in a tautology when
// there are none so that callers need no conditional around the keyword.
func joinWhere(predicates []string) string {
	if len(predicates) == 0 {
		return "1=1"
	}

	return strings.Join(predicates, " AND ")
}

// sortsDescending reports whether the filter asks for newest-first.
func sortsDescending(filter *filtering.QueryFilter) bool {
	return filter != nil && filter.SortBy != nil && *filter.SortBy == *filtering.SortDescending
}

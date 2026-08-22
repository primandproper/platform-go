package dataprivacy

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
// UTC" — so `expires_at <= ?` there is a string comparison.
//
// That is still correct, because the rendering begins with a fixed-width
// "YYYY-MM-DD HH:MM:SS" prefix and everything is UTC, so lexical order is
// chronological order. It stops being correct the moment a value is bound in a
// non-UTC location, so do not remove the .UTC() calls at the binding sites.

// tables holds the rendered table names. Derived from one prefix so that adding
// a second table later cannot introduce an inconsistently named one — see
// dataprivacy/migrations.
type tables struct {
	base     string
	requests string
}

func newTables(prefix string) *tables {
	return &tables{
		base:     prefix,
		requests: ddl.Qualify(prefix) + "dataprivacy_requests",
	}
}

// prefix returns the prefix the names were derived from, for the validation
// that has to run against every rendered name rather than against any one.
func (t *tables) prefix() string {
	return t.base
}

// requestColumns is the projection every request read scans. Declared once so
// the SELECTs and the Scan cannot drift apart.
const requestColumns = "id, request_type, status, operation_id, subject_id, subject_type, subject_scope, " +
	"requested_at, due_at, expires_at, completed_at, " +
	"artifact_ref, artifact_bytes, deleted_rows, anonymized_rows, failures, retained, last_error, " +
	"key_shredded_at"

// activeStatuses are the statuses a request can still move out of. Used by the
// overdue count, which is asking "what do we still owe somebody" and would be
// answered wrongly by including requests that have already been served.
var activeStatuses = []Status{StatusAwaitingConfirmation, StatusInProgress}

// terminalStatuses are the statuses retention may reap.
var terminalStatuses = []Status{StatusCompleted, StatusFailed, StatusExpired, StatusCancelled}

// buildInsertRequest renders the request write.
//
// It is a plain INSERT rather than an upsert. A resubmission is a new request
// with its own statutory clock, and an upsert here would let one quietly
// overwrite the RequestedAt that clock runs from — which is the single field in
// this table a regulator is most likely to ask about.
func (t *tables) buildInsertRequest(d dialect.Dialect, req *Request, failures, retained []byte) (query string, args []any) {
	args = []any{
		req.ID, string(req.Type), string(req.Status), req.OperationID,
		req.Subject.ID, string(req.Subject.Type), req.Subject.Scope,
		req.RequestedAt.UTC(), req.DueAt.UTC(), nullableTime(req.ExpiresAt), req.CompletedAt,
		req.ArtifactRef, req.ArtifactBytes, req.Deleted, req.Anonymized,
		database.BlobOrNil(failures), database.BlobOrNil(retained), req.LastError, req.KeyShreddedAt,
	}

	return fmt.Sprintf(
		"INSERT INTO %s (id, request_type, status, operation_id, subject_id, subject_type, subject_scope, "+
			"requested_at, due_at, expires_at, completed_at, "+
			"artifact_ref, artifact_bytes, deleted_rows, anonymized_rows, failures, retained, last_error, "+
			"key_shredded_at) "+
			"VALUES (%s)",
		t.requests, d.Placeholders(1, len(args)),
	), args
}

// buildSelectRequest renders the single-request read.
func (t *tables) buildSelectRequest(d dialect.Dialect, requestID string) (query string, args []any) {
	return fmt.Sprintf("SELECT %s FROM %s WHERE id = %s", requestColumns, t.requests, d.Placeholder(1)),
		[]any{requestID}
}

// buildListRequests renders a subject's request history, cursor-paginated on
// id.
//
// An empty Subject.Scope matches every scope rather than only the unscoped
// requests. A subject asking what has been requested in their name means all of
// it, and a listing that silently omitted the scoped requests would be the
// wrong answer to the one question this endpoint exists to answer.
//
// Ordering is on id alone rather than on (requested_at, id). identifiers.New is
// xid, whose string form sorts in generation order, so id order is submission
// order — and paginating on the single column the cursor names is what keeps a
// page boundary from skipping a row when two requests share a timestamp.
func (t *tables) buildListRequests(d dialect.Dialect, subject Subject, cursor string, limit int, descending bool) (query string, args []any) {
	args = make([]any, 0, 4)
	args = append(args, subject.ID)

	where := "subject_id = " + d.Placeholder(1)

	if subject.Scope != "" {
		args = append(args, subject.Scope)
		where += " AND subject_scope = " + d.Placeholder(len(args))
	}

	direction, comparison := database.CursorOrder(descending)

	if cursor != "" {
		args = append(args, cursor)
		where += " AND id" + comparison + d.Placeholder(len(args))
	}

	args = append(args, limit)

	return fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s ORDER BY id %s LIMIT %s",
		requestColumns, t.requests, where, direction, d.Placeholder(len(args)),
	), args
}

// buildCountRequests renders the total for the paged read's Pagination.
func (t *tables) buildCountRequests(d dialect.Dialect, subject Subject) (query string, args []any) {
	args = []any{subject.ID}

	where := "subject_id = " + d.Placeholder(1)

	if subject.Scope != "" {
		args = append(args, subject.Scope)
		where += " AND subject_scope = " + d.Placeholder(2)
	}

	return fmt.Sprintf("SELECT COUNT(*) FROM %s WHERE %s", t.requests, where), args
}

// buildTransition renders a guarded status change: the row moves only if it is
// currently in one of the `from` statuses.
//
// The guard is in the predicate rather than in a read-then-write, and that is
// what makes Confirm safe. A subject clicking confirm twice, or clicking it at
// the instant the sweeper cancels the request for having sat too long, is a
// race the database resolves here — the second writer matches no rows and is
// told so, instead of both succeeding and queueing the erasure twice.
//
// completed_at is set only for a terminal destination, and expires_at is
// cleared unconditionally: every transition this builds leaves the confirmation
// window behind, either by satisfying it or by lapsing it, and a stale window
// would have the lapse sweep pick the row back up.
//
// operationID is written only when non-empty, so a cancellation cannot blank the
// pointer to an operation that is still running. It is the confirmation path
// that supplies one, and it supplies it here rather than in a second statement
// so the row cannot become in-progress without saying what is doing the work.
func (t *tables) buildTransition(
	d dialect.Dialect,
	requestID string,
	from []Status,
	to Status,
	operationID string,
	at time.Time,
) (query string, args []any) {
	args = make([]any, 0, len(from)+4)
	args = append(args, string(to))

	set := "status = " + d.Placeholder(1) + ", expires_at = NULL"

	if operationID != "" {
		args = append(args, operationID)
		set += ", operation_id = " + d.Placeholder(len(args))
	}

	if to.Terminal() {
		args = append(args, at.UTC())
		set += ", completed_at = " + d.Placeholder(len(args))
	}

	args = append(args, requestID)
	where := "id = " + d.Placeholder(len(args))

	placeholders := make([]string, 0, len(from))
	for _, status := range from {
		args = append(args, string(status))
		placeholders = append(placeholders, d.Placeholder(len(args)))
	}

	where += " AND status IN (" + strings.Join(placeholders, ", ") + ")"

	return fmt.Sprintf("UPDATE %s SET %s WHERE %s", t.requests, set, where), args
}

// buildCompleteExport renders the UPDATE recording a fulfilled export: where the
// artifact went, how big it was, when it expires, and which sections are missing
// from it.
//
// The status guard is on in_progress, so a completion cannot resurrect a request
// that was cancelled while the runner was busy — which is exactly what a long
// export racing a cancellation would otherwise do. It is also what makes a
// duplicate execution safe: two runners on the same request produce the same
// artifact at the same key, and the second one's completion matches no row.
func (t *tables) buildCompleteExport(d dialect.Dialect, req *Request, failures []byte, at time.Time) (query string, args []any) {
	args = []any{
		string(StatusCompleted), at.UTC(),
		req.ExpiresAt.UTC(), req.ArtifactRef, req.ArtifactBytes, database.BlobOrNil(failures),
		req.ID, string(StatusInProgress),
	}

	return fmt.Sprintf(
		"UPDATE %s SET status = %s, completed_at = %s, expires_at = %s, "+
			"artifact_ref = %s, artifact_bytes = %s, failures = %s, last_error = '' "+
			"WHERE id = %s AND status = %s",
		t.requests, d.Placeholder(1), d.Placeholder(2), d.Placeholder(3), d.Placeholder(4),
		d.Placeholder(5), d.Placeholder(6), d.Placeholder(7), d.Placeholder(8),
	), args
}

// buildCompleteErasure renders the UPDATE recording a fulfilled erasure: what
// was destroyed, what was anonymized, and what was kept and why.
//
// expires_at is cleared rather than set. An erasure has no artifact to expire,
// and the column held its confirmation window — leaving that behind would have
// the lapse sweep cancel a request that has already run.
func (t *tables) buildCompleteErasure(d dialect.Dialect, req *Request, failures, retained []byte, at time.Time) (query string, args []any) {
	args = []any{
		string(StatusCompleted), at.UTC(),
		req.Deleted, req.Anonymized, database.BlobOrNil(failures), database.BlobOrNil(retained), req.KeyShreddedAt,
		req.ID, string(StatusInProgress),
	}

	return fmt.Sprintf(
		"UPDATE %s SET status = %s, completed_at = %s, expires_at = NULL, "+
			"deleted_rows = %s, anonymized_rows = %s, failures = %s, retained = %s, "+
			"key_shredded_at = %s, last_error = '' "+
			"WHERE id = %s AND status = %s",
		t.requests, d.Placeholder(1), d.Placeholder(2), d.Placeholder(3), d.Placeholder(4),
		d.Placeholder(5), d.Placeholder(6), d.Placeholder(7), d.Placeholder(8), d.Placeholder(9),
	), args
}

// buildMarkKeyShredded renders the record of a destroyed data key.
//
// Guarded on the column still being null, so a retried erasure — which re-shreds
// and is told the original destruction time — cannot move the timestamp forward
// to the moment of the retry. The one thing this column is for is saying when
// the key stopped existing, and that instant happened once.
func (t *tables) buildMarkKeyShredded(d dialect.Dialect, requestID string, at time.Time) (query string, args []any) {
	return fmt.Sprintf(
			"UPDATE %s SET key_shredded_at = %s WHERE id = %s AND key_shredded_at IS NULL",
			t.requests, d.Placeholder(1), d.Placeholder(2),
		),
		[]any{at.UTC(), requestID}
}

// buildFail renders the UPDATE recording that a request will not be fulfilled.
//
// There is no retry branch here any more, and its absence is the point. The
// retry schedule, the attempt budget, and the lease all belong to the operation
// now, so the only failure this table records is the last one — the moment at
// which "nobody is going to get an answer" becomes true, which is the only claim
// this row was ever in a position to make.
//
// expires_at is cleared for the same reason every other terminal transition
// clears it: a failed erasure that kept its confirmation window would be picked
// up and cancelled by the lapse sweep, overwriting the record of why it failed.
func (t *tables) buildFail(d dialect.Dialect, requestID, lastErr string, at time.Time) (query string, args []any) {
	args = []any{
		string(StatusFailed), lastErr, at.UTC(),
		requestID, string(StatusInProgress),
	}

	return fmt.Sprintf(
		"UPDATE %s SET status = %s, last_error = %s, completed_at = %s, expires_at = NULL "+
			"WHERE id = %s AND status = %s",
		t.requests, d.Placeholder(1), d.Placeholder(2), d.Placeholder(3),
		d.Placeholder(4), d.Placeholder(5),
	), args
}

// buildSelectExpiringArtifacts renders the expiry sweep's read: completed
// exports whose artifact is due for deletion.
//
// It selects rather than updates, because the object has to go before the row
// says it has. A bulk UPDATE marking rows expired would be one round trip and
// would leave every artifact in the bucket, which is precisely the outcome the
// expiry state exists to prevent.
func (t *tables) buildSelectExpiringArtifacts(d dialect.Dialect, now time.Time, limit int) (query string, args []any) {
	return fmt.Sprintf(
		"SELECT %s FROM %s WHERE status = %s AND artifact_ref <> '' AND expires_at IS NOT NULL "+
			"AND expires_at <= %s ORDER BY expires_at, id LIMIT %s",
		requestColumns, t.requests, d.Placeholder(1), d.Placeholder(2), d.Placeholder(3),
	), []any{string(StatusCompleted), now.UTC(), limit}
}

// buildMarkExpired renders the UPDATE retiring an artifact that has been
// deleted. The reference is cleared as the status changes, so a stale path
// cannot outlive the object it named and be handed to a signer later.
func (t *tables) buildMarkExpired(d dialect.Dialect, requestID string, at time.Time) (query string, args []any) {
	return fmt.Sprintf(
			"UPDATE %s SET status = %s, artifact_ref = '', expires_at = %s WHERE id = %s AND status = %s",
			t.requests, d.Placeholder(1), d.Placeholder(2), d.Placeholder(3), d.Placeholder(4),
		), []any{
			string(StatusExpired), at.UTC(), requestID, string(StatusCompleted),
		}
}

// buildLapseUnconfirmed renders the sweep cancelling erasures whose
// confirmation window has passed.
//
// This is a bulk UPDATE where the artifact sweep is not, because there is
// nothing outside the database to clean up: an unconfirmed erasure has touched
// no domain and written no object.
// Every bound value appears once and in the order its placeholder is rendered.
// MySQL and SQLite bind '?' positionally, so a value reused across two clauses
// has to be supplied twice rather than referenced by index the way Postgres
// would allow.
func (t *tables) buildLapseUnconfirmed(d dialect.Dialect, now time.Time, limit int) (query string, args []any) {
	inner := fmt.Sprintf(
		"SELECT id FROM %s WHERE status = %s AND expires_at IS NOT NULL AND expires_at <= %s LIMIT %s",
		t.requests, d.Placeholder(3), d.Placeholder(4), d.Placeholder(5),
	)

	// MySQL refuses a subquery that reads the table being updated
	// (ER_UPDATE_TABLE_USED), but accepts it once materialized through a
	// derived table.
	if d == dialect.MySQL {
		inner = fmt.Sprintf("SELECT id FROM (%s) AS lapsed", inner)
	}

	return fmt.Sprintf(
			"UPDATE %s SET status = %s, completed_at = %s, expires_at = NULL WHERE id IN (%s)",
			t.requests, d.Placeholder(1), d.Placeholder(2), inner,
		), []any{
			string(StatusCancelled), now.UTC(), string(StatusAwaitingConfirmation), now.UTC(), limit,
		}
}

// buildCountOverdue renders the overdue gauge's read: how many requests are
// still owed to somebody past the deadline the regulation gave.
func (t *tables) buildCountOverdue(d dialect.Dialect, now time.Time) (query string, args []any) {
	args = make([]any, 0, len(activeStatuses)+1)

	placeholders := make([]string, 0, len(activeStatuses))
	for _, status := range activeStatuses {
		args = append(args, string(status))
		placeholders = append(placeholders, d.Placeholder(len(args)))
	}

	args = append(args, now.UTC())

	return fmt.Sprintf(
		"SELECT request_type, COUNT(*) FROM %s WHERE status IN (%s) AND due_at < %s GROUP BY request_type",
		t.requests, strings.Join(placeholders, ", "), d.Placeholder(len(args)),
	), args
}

// buildReap renders the DELETE removing request records past the retention
// window.
//
// A row whose artifact_ref is still set is never reaped, whatever its age. The
// reference is the only record of where that object is, and deleting the row
// first would leave a file containing everything known about a person sitting
// in a bucket with nothing left pointing at it — which is worse than the row
// this was trying to clean up.
func (t *tables) buildReap(d dialect.Dialect, before time.Time, limit int) (query string, args []any) {
	args = make([]any, 0, len(terminalStatuses)+2)

	placeholders := make([]string, 0, len(terminalStatuses))
	for _, status := range terminalStatuses {
		args = append(args, string(status))
		placeholders = append(placeholders, d.Placeholder(len(args)))
	}

	args = append(args, before.UTC())
	timeArg := d.Placeholder(len(args))

	args = append(args, limit)

	inner := fmt.Sprintf(
		"SELECT id FROM %s WHERE status IN (%s) AND artifact_ref = '' "+
			"AND completed_at IS NOT NULL AND completed_at < %s LIMIT %s",
		t.requests, strings.Join(placeholders, ", "), timeArg, d.Placeholder(len(args)),
	)

	if d == dialect.MySQL {
		inner = fmt.Sprintf("SELECT id FROM (%s) AS doomed", inner)
	}

	return fmt.Sprintf("DELETE FROM %s WHERE id IN (%s)", t.requests, inner), args
}

// nullableTime maps the zero time to a SQL NULL. A zero timestamp bound as a
// value reads back as year 1, which every comparison in the expiry sweeps would
// treat as long overdue.
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}

	return t.UTC()
}

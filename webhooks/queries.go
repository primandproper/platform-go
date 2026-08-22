package webhooks

import (
	"fmt"
	"strings"
	"time"

	"github.com/primandproper/platform-go/v13/database/ddl"
	"github.com/primandproper/platform-go/v13/database/dialect"
	"github.com/primandproper/platform-go/v13/tenancy"
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

// tables holds the five rendered table names. Derived from one prefix so a
// consumer cannot name them inconsistently — see webhooks/migrations.
type tables struct {
	base          string
	endpoints     string
	subscriptions string
	deliveries    string
	dispatches    string
	attempts      string
}

func newTables(prefix string) *tables {
	return &tables{
		base:          prefix,
		endpoints:     ddl.Qualify(prefix) + "webhooks_endpoints",
		subscriptions: ddl.Qualify(prefix) + "webhooks_subscriptions",
		deliveries:    ddl.Qualify(prefix) + "webhooks_deliveries",
		dispatches:    ddl.Qualify(prefix) + "webhooks_dispatches",
		attempts:      ddl.Qualify(prefix) + "webhooks_attempts",
	}
}

// prefix returns the prefix the names were derived from, for the validation
// that has to run against every rendered name rather than against any one.
func (t *tables) prefix() string {
	return t.base
}

// endpointColumns is the projection every endpoint read scans. Declared once so
// the SELECTs and the Scan cannot drift apart.
const endpointColumns = "id, scope, url, content_type, secret_current, secret_previous, headers, disabled"

// dispatchColumns is the projection a claimed dispatch scans, joined across
// dispatches, deliveries, and endpoints.
const dispatchColumns = "d.id, d.delivery_id, d.endpoint_id, d.ordering_key, d.attempts, " +
	"v.event_type, v.payload, v.scope, " +
	"e.id, e.scope, e.url, e.content_type, e.secret_current, e.secret_previous, e.headers, e.disabled"

// attemptColumns is the projection an attempt read scans.
const attemptColumns = "id, delivery_id, endpoint_id, attempt_count, status_code, error, duration_ms, attempted_at"

// buildUpsertEndpoint renders the endpoint write. It is an upsert rather than
// separate insert and update paths because SaveEndpoint is the only write and a
// caller re-registering an endpoint means to replace it.
//
// The scope is written on insert and absent from every update clause: an
// endpoint does not change hands. What guards the rest of the update is not this
// statement but the scope check SaveEndpoint runs first, inside the same
// transaction — without it, a save naming an ID that exists in another scope
// would overwrite that subscriber's URL and signing secret, which is a
// cross-tenant write dressed as a re-registration.
func (t *tables) buildUpsertEndpoint(d dialect.Dialect, e *Endpoint, headers []byte, now time.Time) (query string, args []any) {
	args = []any{e.ID, e.Scope, e.URL, e.ContentType, e.Secret.Current, secretOrNil(e.Secret.Previous), headers, e.Disabled, now}

	base := fmt.Sprintf(
		"INSERT INTO %s (id, scope, url, content_type, secret_current, secret_previous, headers, disabled, created_at) VALUES (%s)",
		t.endpoints, d.Placeholders(1, len(args)),
	)

	// The conflict target is the primary key in every dialect; only the syntax
	// differs. created_at is deliberately not updated — re-registering an
	// endpoint does not make it new.
	switch d {
	case dialect.MySQL:
		return base + " ON DUPLICATE KEY UPDATE" +
			" url = VALUES(url), content_type = VALUES(content_type)," +
			" secret_current = VALUES(secret_current), secret_previous = VALUES(secret_previous)," +
			" headers = VALUES(headers), disabled = VALUES(disabled)," +
			" archived_at = NULL, updated_at = " + d.Placeholder(len(args)+1), append(args, now)
	case dialect.Postgres, dialect.SQLite:
		return base + " ON CONFLICT (id) DO UPDATE SET" +
			" url = EXCLUDED.url, content_type = EXCLUDED.content_type," +
			" secret_current = EXCLUDED.secret_current, secret_previous = EXCLUDED.secret_previous," +
			" headers = EXCLUDED.headers, disabled = EXCLUDED.disabled," +
			" archived_at = NULL, updated_at = " + d.Placeholder(len(args)+1), append(args, now)
	default:
		return base, args
	}
}

// buildDeleteSubscriptions removes an endpoint's subscriptions, so SaveEndpoint
// can replace the set wholesale rather than diffing it.
func (t *tables) buildDeleteSubscriptions(d dialect.Dialect, endpointID string) (query string, args []any) {
	return fmt.Sprintf("DELETE FROM %s WHERE endpoint_id = %s", t.subscriptions, d.Placeholder(1)),
		[]any{endpointID}
}

// buildInsertSubscriptions renders one multi-row INSERT for an endpoint's
// subscriptions. Registering an endpoint with thirty event types costs one
// round trip, not thirty.
func (t *tables) buildInsertSubscriptions(d dialect.Dialect, endpointID string, events []EventType) (query string, args []any) {
	const columnsPerRow = 2

	args = make([]any, 0, len(events)*columnsPerRow)
	tuples := make([]string, 0, len(events))

	for _, event := range events {
		tuples = append(tuples, "("+d.Placeholders(len(args)+1, columnsPerRow)+")")
		args = append(args, endpointID, event.String())
	}

	return fmt.Sprintf(
		"INSERT INTO %s (endpoint_id, event_type) VALUES %s",
		t.subscriptions, strings.Join(tuples, ", "),
	), args
}

// buildSelectEndpointScope renders the read SaveEndpoint uses to find out
// whether the ID it is about to write already belongs to somebody. It scans one
// column, so it does not go through endpointColumns.
func (t *tables) buildSelectEndpointScope(d dialect.Dialect, endpointID string) (query string, args []any) {
	return fmt.Sprintf("SELECT scope FROM %s WHERE id = %s", t.endpoints, d.Placeholder(1)),
		[]any{endpointID}
}

// buildSelectEndpoint renders the single-endpoint read, within one scope.
// Archived endpoints are still returned: Replay and the attempts log both name
// endpoints that may since have been retired.
//
// An endpoint in another scope does not read as forbidden, it reads as absent.
// That is both the honest answer — it is not in this registry — and the one that
// does not turn the read into an oracle for which endpoint IDs exist elsewhere.
func (t *tables) buildSelectEndpoint(d dialect.Dialect, scope tenancy.Scope, endpointID string) (query string, args []any) {
	return fmt.Sprintf(
		"SELECT %s FROM %s WHERE id = %s AND scope = %s",
		endpointColumns, t.endpoints, d.Placeholder(1), d.Placeholder(2),
	), []any{endpointID, scope}
}

// buildSelectEndpointsForEvent renders the fan-out lookup: which live, enabled
// endpoints in this scope want this event.
//
// The scope is a predicate on the endpoint rather than a qualifier on the event
// type. Encoding it into the subscription's key — "<accountID>:<eventType>" —
// scopes the lookup too, which is why it is the shape a consumer arrives at
// first; it also makes the pair unindexable as two facts, and the qualified
// string cannot be checked against a Catalog of unqualified event types.
//
// Disabled and archived endpoints are excluded here rather than at delivery, so
// no dispatch row is ever created for them. Creating one and skipping it later
// would leave a permanently undeliverable row in the backlog for every event.
func (t *tables) buildSelectEndpointsForEvent(d dialect.Dialect, scope tenancy.Scope, eventType EventType) (query string, args []any) {
	return fmt.Sprintf(
		"SELECT %s FROM %s AS e "+
			"INNER JOIN %s AS s ON s.endpoint_id = e.id "+
			"WHERE s.event_type = %s AND e.scope = %s AND e.disabled = FALSE AND e.archived_at IS NULL "+
			"ORDER BY e.id",
		prefixColumns("e.", endpointColumns), t.endpoints, t.subscriptions,
		d.Placeholder(1), d.Placeholder(2),
	), []any{eventType.String(), scope}
}

// buildListEndpoints renders the paged registry read for one scope,
// cursor-paginated on id.
func (t *tables) buildListEndpoints(d dialect.Dialect, scope tenancy.Scope, cursor string, limit int) (query string, args []any) {
	args = make([]any, 0, 3)
	args = append(args, scope)

	where := "scope = " + d.Placeholder(1) + " AND archived_at IS NULL"
	if cursor != "" {
		args = append(args, cursor)
		where += " AND id > " + d.Placeholder(len(args))
	}

	args = append(args, limit)

	return fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s ORDER BY id LIMIT %s",
		endpointColumns, t.endpoints, where, d.Placeholder(len(args)),
	), args
}

// buildCountEndpoints renders the total for the paged read's Pagination, over
// the same scope the page came from — a total counting every tenant's endpoints
// would report a page of three out of nine thousand.
func (t *tables) buildCountEndpoints(d dialect.Dialect, scope tenancy.Scope) (query string, args []any) {
	return fmt.Sprintf(
		"SELECT COUNT(*) FROM %s WHERE scope = %s AND archived_at IS NULL",
		t.endpoints, d.Placeholder(1),
	), []any{scope}
}

// buildArchiveEndpoint renders the retirement. The row is marked rather than
// deleted so the attempts log keeps referring to something.
func (t *tables) buildArchiveEndpoint(d dialect.Dialect, scope tenancy.Scope, endpointID string, at time.Time) (query string, args []any) {
	return fmt.Sprintf(
		"UPDATE %s SET archived_at = %s WHERE id = %s AND scope = %s AND archived_at IS NULL",
		t.endpoints, d.Placeholder(1), d.Placeholder(2), d.Placeholder(3),
	), []any{at, endpointID, scope}
}

// buildInsertDelivery renders the delivery row: the payload, stored once
// however many subscribers it fans out to.
//
// The scope is stored on the delivery rather than derived from its dispatches'
// endpoints, because it is the delivery that has an owner: the payload is one
// tenant's data whether it fanned out to five subscribers, one, or none. It is
// also what the delivery log reads through — see buildListAttempts.
func (t *tables) buildInsertDelivery(d dialect.Dialect, delivery *Delivery, now time.Time) (query string, args []any) {
	args = []any{delivery.ID, delivery.Scope, delivery.EventType.String(), []byte(delivery.Payload), delivery.OrderingKey, now}

	return fmt.Sprintf(
		"INSERT INTO %s (id, scope, event_type, payload, ordering_key, created_at) VALUES (%s)",
		t.deliveries, d.Placeholders(1, len(args)),
	), args
}

// buildInsertDispatches renders one multi-row INSERT fanning a delivery out to
// its subscribers. New dispatches are immediately eligible: next_attempt is
// their creation time.
//
// Every row in one call shares a created_at, so created_at alone does not order
// two dispatches created together. What separates them is id: identifiers.New is
// xid, whose string form sorts in generation order. buildSelectClaimable's
// per-key predicate and the ORDER BY clauses both rely on that.
func (t *tables) buildInsertDispatches(d dialect.Dialect, rows []dispatchRow) (query string, args []any) {
	// created_at is bound twice: a new dispatch is eligible immediately, so its
	// first next_attempt is its creation time.
	const columnsPerRow = 6

	args = make([]any, 0, len(rows)*columnsPerRow)
	tuples := make([]string, 0, len(rows))

	for i := range rows {
		tuples = append(tuples, "("+d.Placeholders(len(args)+1, columnsPerRow)+")")
		args = append(args,
			rows[i].id, rows[i].deliveryID, rows[i].endpointID, rows[i].orderingKey,
			rows[i].createdAt, rows[i].createdAt,
		)
	}

	return fmt.Sprintf(
		"INSERT INTO %s (id, delivery_id, endpoint_id, ordering_key, created_at, next_attempt) VALUES %s",
		t.dispatches, strings.Join(tuples, ", "),
	), args
}

// dispatchRow is one fanned-out dispatch's worth of bound parameters.
type dispatchRow struct {
	createdAt   time.Time
	id          string
	deliveryID  string
	endpointID  string
	orderingKey string
}

// buildSelectClaimable renders the query that picks the next batch of dispatch
// IDs to claim.
//
// The ordering guarantee lives in this predicate. A row with an ordering key is
// claimable only when no earlier undelivered row shares that key *and the same
// endpoint*, so at most one dispatch per (endpoint, key) is ever in flight
// across every worker in the fleet.
//
// Scoping to the endpoint as well as the key is the difference between ordering
// and head-of-line blocking. Keyed on ordering_key alone, one subscriber that
// times out on resource-42.updated would hold back every other subscriber's
// copy of the same event — a dead endpoint would stall the healthy ones, which
// is the failure per-endpoint circuit breaking exists to prevent, reintroduced
// in the claim predicate.
//
// "Earlier" is (created_at, id), not created_at alone, and the tuple is what
// makes the guarantee hold. Enqueue stamps every row in one call with a single
// timestamp, so two dispatches sharing a key and an Enqueue also share a
// created_at; under a bare `<` neither would block the other, both would be
// claimable at once, and a failure on the first would deliver the second ahead
// of it. The tiebreak is id because that is what ORDER BY breaks ties on below —
// the predicate and the delivery order have to agree on "earlier" or the batch
// can contain a pair it is about to reorder.
func (t *tables) buildSelectClaimable(d dialect.Dialect, now time.Time, limit int, skipLocked bool) (query string, args []any) {
	p := func(n int) string { return d.Placeholder(n) }

	query = fmt.Sprintf(
		"SELECT id FROM %s AS m "+
			"WHERE m.delivered_at IS NULL AND m.dead = FALSE "+
			"AND m.next_attempt <= %s "+
			"AND (m.claimed_until IS NULL OR m.claimed_until <= %s) "+
			"AND (m.ordering_key = '' OR NOT EXISTS ("+
			"SELECT 1 FROM %s AS prior "+
			"WHERE prior.endpoint_id = m.endpoint_id "+
			"AND prior.ordering_key = m.ordering_key "+
			"AND prior.delivered_at IS NULL "+
			"AND prior.dead = FALSE "+
			"AND (prior.created_at < m.created_at "+
			"OR (prior.created_at = m.created_at AND prior.id < m.id)))) "+
			"ORDER BY m.created_at, m.id LIMIT %s",
		t.dispatches, p(1), p(2), t.dispatches, p(3),
	)

	if skipLocked && d.SupportsSkipLocked() {
		query += " FOR UPDATE SKIP LOCKED"
	}

	return query, []any{now, now, limit}
}

// buildClaim renders the UPDATE that leases the selected rows. The attempt
// count is incremented here rather than on failure: a worker that crashes
// mid-delivery has still consumed an attempt, so a dispatch that reliably kills
// its worker eventually goes dead instead of being reclaimed forever.
func (t *tables) buildClaim(d dialect.Dialect, ids []string, claimedUntil time.Time) (query string, args []any) {
	args = make([]any, 0, len(ids)+1)
	args = append(args, claimedUntil)

	for _, id := range ids {
		args = append(args, id)
	}

	return fmt.Sprintf(
		"UPDATE %s SET claimed_until = %s, attempts = attempts + 1 WHERE id IN (%s)",
		t.dispatches, d.Placeholder(1), d.Placeholders(2, len(ids)),
	), args
}

// buildFetchClaimed renders the projection of claimed rows, joined to their
// delivery for the payload and to their endpoint for the target and secrets.
//
// The endpoint is read here, at claim time, rather than captured at dispatch
// time. That is deliberate: a secret rotated between the event and its delivery
// signs under the current key, and an endpoint disabled in between is filtered
// out below rather than delivered to.
//
// Ordered so that dispatches sharing an ordering key are delivered oldest-first.
func (t *tables) buildFetchClaimed(d dialect.Dialect, ids []string) (query string, args []any) {
	args = make([]any, 0, len(ids))
	for _, id := range ids {
		args = append(args, id)
	}

	return fmt.Sprintf(
		"SELECT %s FROM %s AS d "+
			"INNER JOIN %s AS v ON v.id = d.delivery_id "+
			"INNER JOIN %s AS e ON e.id = d.endpoint_id "+
			"WHERE d.id IN (%s) AND e.disabled = FALSE AND e.archived_at IS NULL "+
			"ORDER BY d.created_at, d.id",
		dispatchColumns, t.dispatches, t.deliveries, t.endpoints, d.Placeholders(1, len(ids)),
	), args
}

// buildMarkDelivered renders the UPDATE retiring an accepted dispatch. The row
// is kept, not deleted, so the delivery log has something to point at; the
// reaper removes it once it ages out.
func (t *tables) buildMarkDelivered(d dialect.Dialect, dispatchID string, at time.Time) (query string, args []any) {
	return fmt.Sprintf(
		"UPDATE %s SET delivered_at = %s, claimed_until = NULL, last_error = NULL WHERE id = %s",
		t.dispatches, d.Placeholder(1), d.Placeholder(2),
	), []any{at, dispatchID}
}

// buildRecordFailure renders the UPDATE applied to a dispatch whose delivery
// failed: release the lease, record why, and schedule the retry. Dead rows are
// excluded from every future claim, so one permanently broken subscriber cannot
// block the ordering key behind it forever.
//
// attempts is written rather than left as the claim incremented it, because not
// every failure should cost an attempt. A delivery skipped by an open circuit
// never reached the subscriber, and the worker hands back the count it had
// before this claim — see Worker.recordFailure. Without this column in the SET,
// an endpoint down for an hour would silently consume the whole budget of every
// dispatch queued behind it, and they would each die on their first real
// attempt once it recovered.
func (t *tables) buildRecordFailure(d dialect.Dialect, dispatchID string, attempts int, nextAttempt time.Time, lastErr string, dead bool) (query string, args []any) {
	return fmt.Sprintf(
		"UPDATE %s SET claimed_until = NULL, attempts = %s, next_attempt = %s, last_error = %s, dead = %s WHERE id = %s",
		t.dispatches, d.Placeholder(1), d.Placeholder(2), d.Placeholder(3), d.Placeholder(4), d.Placeholder(5),
	), []any{attempts, nextAttempt, lastErr, dead, dispatchID}
}

// buildInsertAttempt renders one row of the delivery log.
func (t *tables) buildInsertAttempt(d dialect.Dialect, a *Attempt) (query string, args []any) {
	args = []any{
		a.ID, a.DeliveryID, a.EndpointID, a.AttemptCount,
		a.StatusCode, a.Error, a.Duration.Milliseconds(), a.AttemptedAt,
	}

	return fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		t.attempts, attemptColumns, d.Placeholders(1, len(args)),
	), args
}

// buildListAttempts renders the delivery log read for one of a scope's
// deliveries, cursor-paginated on id.
//
// The scope is reached through the delivery rather than stored on the attempt.
// An attempt is a log line about a delivery, so its owner is the delivery's, and
// a second copy of that fact on every attempt row is a copy that can disagree
// with the first. The join is to a primary key.
//
// One consequence, and it is bounded: past the retention window the reaper
// removes an attempt's delivery, and the attempts that outlive it for a cycle
// stop being listable here. They are already doomed — the next reap deletes them
// — and neither is readable by anybody once the delivery is gone.
func (t *tables) buildListAttempts(d dialect.Dialect, scope tenancy.Scope, deliveryID, cursor string, limit int) (query string, args []any) {
	args = make([]any, 0, 4)
	args = append(args, deliveryID, scope)

	where := "a.delivery_id = " + d.Placeholder(1) + " AND v.scope = " + d.Placeholder(2)
	if cursor != "" {
		args = append(args, cursor)
		where += " AND a.id > " + d.Placeholder(len(args))
	}

	args = append(args, limit)

	return fmt.Sprintf(
		"SELECT %s FROM %s AS a INNER JOIN %s AS v ON v.id = a.delivery_id "+
			"WHERE %s ORDER BY a.attempted_at, a.id LIMIT %s",
		prefixColumns("a.", attemptColumns), t.attempts, t.deliveries, where, d.Placeholder(len(args)),
	), args
}

// buildCountAttempts renders the total for the delivery log's Pagination, over
// the same scope the page came from.
func (t *tables) buildCountAttempts(d dialect.Dialect, scope tenancy.Scope, deliveryID string) (query string, args []any) {
	return fmt.Sprintf(
		"SELECT COUNT(*) FROM %s AS a INNER JOIN %s AS v ON v.id = a.delivery_id "+
			"WHERE a.delivery_id = %s AND v.scope = %s",
		t.attempts, t.deliveries, d.Placeholder(1), d.Placeholder(2),
	), []any{deliveryID, scope}
}

// buildRequeue renders the operator's re-drive: make a dispatch claimable
// again, whatever state it reached.
//
// The attempt count is reset rather than continued, which is the whole point of
// a replay — a dead dispatch is one that has already exhausted its budget, and
// requeuing it without a reset would have it die again on the next attempt.
// last_error is kept: the attempts log records what happened, and clearing the
// reason a replay was needed makes the replay harder to explain afterwards.
func (t *tables) buildRequeue(d dialect.Dialect, deliveryID, endpointID string, at time.Time) (query string, args []any) {
	return fmt.Sprintf(
		"UPDATE %s SET next_attempt = %s, claimed_until = NULL, delivered_at = NULL, dead = FALSE, attempts = 0 "+
			"WHERE delivery_id = %s AND endpoint_id = %s",
		t.dispatches, d.Placeholder(1), d.Placeholder(2), d.Placeholder(3),
	), []any{at, deliveryID, endpointID}
}

// buildBacklog renders the health query: how many dispatches are waiting, and
// when the oldest of them was created.
//
// Both come back from one round trip because they answer one question — is the
// worker keeping up — and neither is useful alone. A depth of 40,000 is fine if
// the oldest is four seconds old and an incident if it is four hours old. Dead
// rows are excluded: they are never going to be delivered, so counting them
// would make a permanently broken subscriber look like a permanently growing
// backlog.
func (t *tables) buildBacklog() string {
	return fmt.Sprintf(
		"SELECT COUNT(*), MIN(created_at) FROM %s WHERE delivered_at IS NULL AND dead = FALSE",
		t.dispatches,
	)
}

// buildReapDispatches renders the DELETE removing delivered dispatches past the
// retention window. Their attempts and any orphaned delivery go with them —
// see buildReapAttempts and buildReapDeliveries.
func (t *tables) buildReapDispatches(d dialect.Dialect, before time.Time, limit int) (query string, args []any) {
	inner := fmt.Sprintf(
		"SELECT id FROM %s WHERE delivered_at IS NOT NULL AND delivered_at < %s LIMIT %s",
		t.dispatches, d.Placeholder(1), d.Placeholder(2),
	)

	// MySQL refuses a subquery that reads the table being deleted from
	// (ER_UPDATE_TABLE_USED), but accepts it once materialized through a
	// derived table.
	if d == dialect.MySQL {
		inner = fmt.Sprintf("SELECT id FROM (%s) AS doomed", inner)
	}

	return fmt.Sprintf("DELETE FROM %s WHERE id IN (%s)", t.dispatches, inner), []any{before, limit}
}

// buildReapAttempts renders the DELETE removing log rows for deliveries that no
// longer have any dispatch. The attempts outlive the dispatch within a
// transaction only briefly; this runs after buildReapDispatches.
func (t *tables) buildReapAttempts(d dialect.Dialect, limit int) (query string, args []any) {
	inner := fmt.Sprintf(
		"SELECT a.id FROM %s AS a WHERE NOT EXISTS ("+
			"SELECT 1 FROM %s AS x WHERE x.delivery_id = a.delivery_id) LIMIT %s",
		t.attempts, t.dispatches, d.Placeholder(1),
	)

	if d == dialect.MySQL {
		inner = fmt.Sprintf("SELECT id FROM (%s) AS doomed", inner)
	}

	return fmt.Sprintf("DELETE FROM %s WHERE id IN (%s)", t.attempts, inner), []any{limit}
}

// buildReapDeliveries renders the DELETE removing deliveries whose every
// dispatch has been reaped. A delivery outlives its dispatches only when some
// subscribers were reaped and others were not, so this deliberately checks for
// the absence of *any* dispatch rather than deleting alongside the first.
func (t *tables) buildReapDeliveries(d dialect.Dialect, limit int) (query string, args []any) {
	inner := fmt.Sprintf(
		"SELECT v.id FROM %s AS v WHERE NOT EXISTS ("+
			"SELECT 1 FROM %s AS x WHERE x.delivery_id = v.id) LIMIT %s",
		t.deliveries, t.dispatches, d.Placeholder(1),
	)

	if d == dialect.MySQL {
		inner = fmt.Sprintf("SELECT id FROM (%s) AS doomed", inner)
	}

	return fmt.Sprintf("DELETE FROM %s WHERE id IN (%s)", t.deliveries, inner), []any{limit}
}

// prefixColumns qualifies a bare column list with a table alias, so a
// projection declared once can be reused in a join without repeating it.
func prefixColumns(prefix, columns string) string {
	parts := strings.Split(columns, ", ")
	for i := range parts {
		parts[i] = prefix + parts[i]
	}

	return strings.Join(parts, ", ")
}

// secretOrNil maps an empty previous secret to a SQL NULL rather than an empty
// blob, so "not rotating" and "rotating to an empty key" cannot be confused in
// the column.
func secretOrNil(secret []byte) any {
	if len(secret) == 0 {
		return nil
	}

	return secret
}

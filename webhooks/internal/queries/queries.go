package queries

import (
	"fmt"
	"slices"
	"strings"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/database/querygen"
)

// The tables this package owns, at their canonical spelling — what the emitted
// .sql names, and what webhooks renders its consumer's prefix onto.
const (
	EndpointsTable     = "webhooks_endpoints"
	SubscriptionsTable = "webhooks_subscriptions"
	DeliveriesTable    = "webhooks_deliveries"
	DispatchesTable    = "webhooks_dispatches"
	AttemptsTable      = "webhooks_attempts"
)

// TableNames is every table webhooks owns, in the order the DDL creates them.
//
// webhooks/migrations is where a consumer gets these rendered at their prefix.
// This list is the canonical spelling and migrations.Tables reads the DDL, so
// the two are cross-checked against each other in this package's tests rather
// than one being derived from the other.
var TableNames = []string{
	EndpointsTable,
	SubscriptionsTable,
	DeliveriesTable,
	DispatchesTable,
	AttemptsTable,
}

// ScopeColumn is the tenancy dimension. Two of the five tables carry it — the
// endpoint, which is whose subscriber it is, and the delivery, which is whose
// event it was — and every statement addressing any of the five is keyed on one
// of those two, reaching it through a join where the row does not carry it
// itself.
//
// It is a column and not a convention. There is no statement in this corpus
// that answers a consumer read of an endpoint, a subscription, a delivery or an
// attempt without naming it. The ones that omit it are enumerated, each with
// its reason, in TestRender_ScopesEveryConsumerRead — that roster is what makes
// the claim checkable rather than a sentence in a comment.
const ScopeColumn = "scope"

// The columns statements here key on, name in a join, or assign by name.
//
// They are spelled once and read by the statements below rather than written
// into each. Nothing outside this package names them: since the port the store
// binds through the generated params, so a column's Go-side spelling is
// sqlc-gen-unison's to produce and this list's job is only to keep two
// statements over one table from disagreeing about it.
const (
	EndpointIDColumn   = "endpoint_id"
	EventTypeColumn    = "event_type"
	DeliveryIDColumn   = "delivery_id"
	DisabledColumn     = "disabled"
	OrderingKeyColumn  = "ordering_key"
	NextAttemptColumn  = "next_attempt"
	ClaimedUntilColumn = "claimed_until"
	DeliveredAtColumn  = "delivered_at"
	AttemptsColumn     = "attempts"
	LastErrorColumn    = "last_error"
	DeadColumn         = "dead"
	PayloadColumn      = "payload"

	// secretPreviousColumn is the endpoint's retiring signing key. It is
	// unexported because nothing outside this package names it: the store binds
	// it through the generated params, and what it is spelled here for is the
	// three lists an endpoint's shape is made of.
	secretPreviousColumn = "secret_previous"
)

// The arguments the authored statements bind, and the alias the one assembled
// projection carries. A rendered statement takes its argument names from its
// columns; these are the names that have nowhere else to come from, so they are
// written down once rather than at each fmt.Sprintf that interpolates one.
const (
	// NowArg is the instant a claim compares a dispatch's next attempt
	// against: a row is due when it has reached it.
	NowArg = "now"
	// LeaseExpiredByArg is the same instant, compared against a lease that may
	// not be there. It is a second name for one moment because claimed_until is
	// nullable and next_attempt is not, and no analyzer here gives one argument
	// two nullabilities — see selectClaimable. The store binds both from one
	// clock read, which is what keeps them the same moment in fact.
	LeaseExpiredByArg = "lease_expired_by"
	// ClaimedUntilArg is the lease horizon a claim writes.
	ClaimedUntilArg = "claimed_until"
	// CreatedAtArg is the creation instant the two authored inserts bind. See
	// the package comment on why those two are not the database's to stamp.
	CreatedAtArg = "created_at"
	// BeforeArg is the retention horizon the dispatch reap deletes behind.
	BeforeArg = "before"
	// IDsArg is the set of dispatch ids a claim leases and reads back.
	IDsArg = querygen.IDsArg
	// EndpointPrefix aliases the endpoint columns a claimed dispatch projects
	// alongside its own — see fetchClaimed. The batch size those statements are
	// bounded by needs no name here: querygen.Generator.LimitClause renders the
	// whole clause, argument and all.
	EndpointPrefix = "endpoint"
)

// Endpoints is the registry: one row per subscriber, owned by a scope.
//
// created_by is nullable because the attribution is optional, and it is not
// updatable because an endpoint does not change hands. Neither is the scope,
// for the same reason and a sharper one: the upsert converges on the id, so a
// scope in the update set would let a save naming somebody else's endpoint id
// take it over.
var Endpoints = Table{
	Name: EndpointsTable,
	Columns: []string{
		querygen.IDColumn,
		ScopeColumn,
		"created_by",
		"name",
		"url",
		"content_type",
		"secret_current",
		secretPreviousColumn,
		"headers",
		DisabledColumn,
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	},
	Nullable:  []string{"created_by", secretPreviousColumn},
	Updatable: []string{"name", "url", "content_type", "secret_current", secretPreviousColumn, "headers", DisabledColumn},
}

// Subscriptions is one endpoint's interest in one event type, as a row.
//
// Nothing about it is updatable: the pair is its identity, and the only thing a
// converging write does to a row it found is revive it — which the upsert does
// from the column list rather than from this, since clearing archived_at and
// stamping the update is what an upsert onto a soft-deleting table means.
var Subscriptions = Table{
	Name: SubscriptionsTable,
	Columns: []string{
		querygen.IDColumn,
		EndpointIDColumn,
		EventTypeColumn,
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	},
}

// Deliveries is the payload, stored once however many subscribers it fans out
// to, and the row every attempt reaches its scope through.
var Deliveries = Table{
	Name: DeliveriesTable,
	Columns: []string{
		querygen.IDColumn,
		ScopeColumn,
		EventTypeColumn,
		PayloadColumn,
		OrderingKeyColumn,
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	},
}

// Dispatches is one endpoint's copy of one delivery: the row the worker claims,
// retries, and eventually gives up on.
//
// Its five state columns are updatable, and the three writes that move them
// each name their own subset rather than the whole of it — a mark-delivered
// that also assigned next_attempt would reschedule the row it just retired.
var Dispatches = Table{
	Name: DispatchesTable,
	Columns: []string{
		querygen.IDColumn,
		DeliveryIDColumn,
		EndpointIDColumn,
		OrderingKeyColumn,
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
		NextAttemptColumn,
		ClaimedUntilColumn,
		DeliveredAtColumn,
		AttemptsColumn,
		LastErrorColumn,
		DeadColumn,
	},
	Nullable:  []string{ClaimedUntilColumn, DeliveredAtColumn, LastErrorColumn},
	Updatable: []string{NextAttemptColumn, ClaimedUntilColumn, DeliveredAtColumn, AttemptsColumn, LastErrorColumn, DeadColumn},
}

// Attempts is the delivery log: append-only, and read through the delivery
// whose scope it borrows.
var Attempts = Table{
	Name: AttemptsTable,
	Columns: []string{
		querygen.IDColumn,
		DeliveryIDColumn,
		EndpointIDColumn,
		"attempt_count",
		"status_code",
		"error",
		"duration_ms",
		querygen.CreatedAtColumn,
		querygen.LastUpdatedAtColumn,
		querygen.ArchivedAtColumn,
	},
	Nullable: []string{"error"},
}

// Tables is every declared table, for the tests that assert over all of them.
var Tables = []*Table{&Endpoints, &Subscriptions, &Deliveries, &Dispatches, &Attempts}

// FileName is the canonical .sql this package renders for a dialect.
func FileName(d dialect.Dialect) string {
	return string(d) + "_generated.sql"
}

// Render returns the whole canonical corpus for one dialect, byte for byte as
// the committed file holds it.
func Render(d dialect.Dialect) string {
	g := querygen.For(d)

	// Every table webhooks owns, rather than the ones a generated statement
	// happens to register: a consumer reading the registry back to truncate a
	// database between integration tests wants all five, and this list is fed
	// by the tables existing rather than by what currently produces their SQL.
	querygen.RegisterTable(TableNames...)

	var rendered []*querygen.Query

	rendered = append(rendered, endpointQueries(g)...)
	rendered = append(rendered, subscriptionQueries(g)...)
	rendered = append(rendered, deliveryQueries(g)...)
	rendered = append(rendered, dispatchQueries(g)...)
	rendered = append(rendered, attemptQueries(g)...)

	return querygen.RenderFile(rendered)
}

// endpointQueries is the registry's six statements.
//
// Five are rendered and one is the pair a paged list always is. What none of
// them omits is the scope: the get, the list and the archive each name it, and
// the two that do not are the ones that cannot — the collision check reads one
// column precisely to find out whose the row already is, and the fan-out
// lookup names it on the endpoint rather than on the subscription it joins to.
func endpointQueries(g *querygen.Generator) []*querygen.Query {
	scope := querygen.Match{Column: ScopeColumn}

	// The read that hydrates a subscriber, and the one write that creates one.
	//
	// Both address the row by id alone where the schema does, and the upsert is
	// keyed on the primary key rather than on (id, scope) because Postgres
	// matches a conflict target against a unique index the table actually has.
	// Nothing is lost by it: SQLStore.checkEndpointScope reads the scope of the
	// id it is about to write, inside the same transaction, and refuses a save
	// that would land on somebody else's row. Without that check this statement
	// is a cross-tenant overwrite dressed as a re-registration, and the
	// conflict target cannot be the thing that stops it.
	upsert := g.UpsertQuery("UpsertEndpoint", EndpointsTable,
		Endpoints.Columns,
		Endpoints.InsertColumns(),
		Endpoints.Updatable,
		Endpoints.Nullable,
		querygen.Match{Column: querygen.IDColumn},
	)

	// Archived endpoints are still returned, which is why the column list this
	// is keyed from leaves archived_at out while the projection keeps it. Replay
	// and the delivery log both name endpoints that may since have been retired,
	// and a read that hid them would leave those unanswerable.
	get := g.ReadQuery("GetEndpoint", EndpointsTable,
		Endpoints.ColumnsExcept(querygen.ArchivedAtColumn),
		querygen.Read{Projection: Endpoints.Columns},
		scope,
	)

	// The collision check in front of the upsert. It projects one column and
	// keys on the id alone — deliberately not on the scope, because the whole
	// question is which scope the row is already in.
	ownership := g.ReadQuery("GetEndpointScope", EndpointsTable,
		Endpoints.ColumnsExcept(querygen.ArchivedAtColumn),
		querygen.Read{Projection: []string{ScopeColumn}},
	)

	// The paged registry read, and the retirement.
	list := g.ListQueries("ListEndpoints", EndpointsTable, Endpoints.Columns, scope)
	archive := g.ArchiveQuery("ArchiveEndpoint", EndpointsTable, Endpoints.Columns, scope)

	queries := []*querygen.Query{upsert, get, ownership}
	queries = append(queries, list...)

	return append(queries, archive, endpointsForEvent(g))
}

// endpointsForEvent is the fan-out lookup: which live, enabled endpoints in
// this scope want this event.
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
// The disabled flag is bound rather than written as a literal FALSE: querygen's
// comparands are the ones whose meaning is identical on all three dialects, and
// a boolean literal is not among them — so the caller binds false and the
// statement says what it compares against.
//
// It is unpaged. The fan-out set is every subscriber to one event type in one
// scope, and a page of it would be a fan-out that silently missed the rest.
func endpointsForEvent(g *querygen.Generator) *querygen.Query {
	return g.JunctionListAllQuery("ListEndpointsForEvent", EndpointsTable, Endpoints.Columns,
		&querygen.Junction{
			Table:    SubscriptionsTable,
			Column:   EndpointIDColumn,
			OnColumn: querygen.IDColumn,
			Columns:  Subscriptions.Columns,
			Matches:  []querygen.Match{{Column: EventTypeColumn}},
		},
		[]querygen.Order{{Column: querygen.IDColumn}},
		querygen.Match{Column: ScopeColumn},
		querygen.Match{Column: DisabledColumn},
	)
}

// subscriptionQueries is the seven statements over the table with no scope of
// its own.
//
// A subscription's owner is its endpoint's, and a second copy of that fact on
// every row here is a copy that can disagree with the first. So every statement
// a consumer reaches reaches the scope through the endpoint: the two paged and
// single reads join to it, and the archive matches through it. The three that
// do not are the halves of a read or a write that was already scoped one
// statement earlier — the endpoint they name came out of a scoped read rather
// than off the wire — and each says so where it is rendered.
func subscriptionQueries(g *querygen.Generator) []*querygen.Query {
	endpoint := querygen.Match{Column: EndpointIDColumn}
	eventType := querygen.Match{Column: EventTypeColumn}

	// A subscription is written by converging on the pair, which is what makes
	// it an identity rather than a position in a list. Re-registering an
	// endpoint used to drop every subscription row and write fresh ones, so an
	// id handed out by one save named nothing after the next — and an archived
	// subscription came back as a live one. Here a row that already names this
	// (endpoint, event type) is revived in place, keeping its id and its
	// created_at, and only a pair that has never existed is inserted.
	//
	// The id bound is therefore a freshly generated one that the converging
	// case throws away. The pair is what identifies the row to this statement,
	// so an id the caller was holding could only ever be ignored or believed
	// for a row it does not describe — and under MySQL's ON DUPLICATE KEY,
	// which fires on whichever unique key was violated, the second of those
	// would revive somebody else's subscription.
	upsert := g.UpsertQuery("UpsertSubscription", SubscriptionsTable,
		Subscriptions.Columns,
		Subscriptions.InsertColumns(),
		Subscriptions.Updatable,
		Subscriptions.Nullable,
		endpoint, eventType,
	)

	// The read-back the upsert cannot do itself: a revived row keeps the id it
	// already had, which is not the one the INSERT bound, and no dialect here
	// reports that portably. The pair is what was written, so the pair is what
	// reads it — and archived_at is out of the keying list because the row this
	// reads may have been archived a moment before the upsert revived it.
	//
	// It takes no scope. Its caller read the endpoint within one in the same
	// transaction, and the endpoint id it binds came out of that read.
	byPair := g.ReadQuery("GetSubscriptionByPair", SubscriptionsTable,
		Subscriptions.ColumnsExcept(querygen.IDColumn, querygen.ArchivedAtColumn),
		querygen.Read{Projection: Subscriptions.Columns},
		endpoint, eventType,
	)

	// The read that fills an Endpoint's Subscriptions: its live rows, ordered
	// by event type so a rendered endpoint lists them the same way twice. Also
	// unscoped, and for the same reason as the read-back above it.
	forEndpoint := g.JunctionListAllQuery("ListSubscriptionsForEndpoint", SubscriptionsTable,
		Subscriptions.Columns, nil,
		[]querygen.Order{{Column: EventTypeColumn}},
		endpoint,
	)

	// The paged read of one of a scope's endpoints' live subscriptions.
	//
	// The endpoint is joined for its scope and nothing else, so the join's own
	// column list leaves archived_at out: an archived endpoint's subscriptions
	// are still listable, which is what "when did they stop receiving this"
	// needs, and a liveness predicate on the joined row would hide them.
	list := g.JunctionListQueries("ListSubscriptions", SubscriptionsTable, Subscriptions.Columns,
		&querygen.Junction{
			Table:    EndpointsTable,
			Column:   querygen.IDColumn,
			OnColumn: EndpointIDColumn,
			Columns:  Endpoints.ColumnsExcept(querygen.ArchivedAtColumn),
			Matches:  []querygen.Match{{Column: ScopeColumn}},
		},
		endpoint,
	)

	// The retirement of one subscription named by its pair, which is the half
	// of a save that reconciles: every live row the save did not name is
	// archived rather than deleted, because "this endpoint no longer receives
	// order.created" and "this endpoint never did" are different, and the
	// delivery log that says otherwise outlives the subscription.
	//
	// One statement per retired pair rather than one carrying the set that
	// survived. A negated set is the one predicate whose empty case means
	// opposite things on the two dialect families — Postgres's empty `<> ALL`
	// matches everything where an expanded `NOT IN (NULL)` matches nothing —
	// so the shape this corpus will not render is exactly the shape whose
	// failure mode is archiving an endpoint's whole subscription set.
	archiveByPair := g.ArchiveQuery("ArchiveSubscriptionByPair", SubscriptionsTable,
		Subscriptions.ColumnsExcept(querygen.IDColumn),
		endpoint, eventType,
	)

	queries := []*querygen.Query{upsert, byPair, forEndpoint}
	queries = append(queries, list...)

	return append(queries, getSubscription(g), archiveByPair, archiveSubscription(g))
}

// getSubscription reads one of a scope's subscriptions by its own id, archived
// ones included.
//
// It is authored because the scope is a column on another table and querygen's
// single-row reads take no join — a keyed read is one table's shape plus
// predicates, and this one is a row whose owner is somebody else's column. What
// it is not is a second way to spell the projection: that comes from the same
// column list every other subscription statement is rendered from.
//
// Archived subscriptions are returned, which is why nothing here excludes them.
// "When did they stop receiving this" is a question about a row that is
// archived by definition.
func getSubscription(g *querygen.Generator) *querygen.Query {
	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: "GetSubscription", Type: querygen.OneType},
		Content: fmt.Sprintf(`SELECT
	%s
FROM %s
	%s
WHERE %s = sqlc.arg(%s)
	AND %s = sqlc.arg(%s);`,
			strings.Join(querygen.QualifyAll(SubscriptionsTable, Subscriptions.Columns), ",\n\t"),
			SubscriptionsTable,
			querygen.JoinStatement{
				JoinTarget:   EndpointsTable,
				TargetColumn: querygen.IDColumn,
				OnTable:      SubscriptionsTable,
				OnColumn:     EndpointIDColumn,
			},
			querygen.Qualify(SubscriptionsTable, querygen.IDColumn), querygen.IDColumn,
			querygen.Qualify(EndpointsTable, ScopeColumn), ScopeColumn,
		),
	}
}

// archiveSubscription retires one of a scope's subscriptions by its own id.
//
// The scope is a subquery over the endpoints rather than a join, because MySQL
// will not UPDATE a table it is also selecting from — but it will read another
// table, and the endpoint is another table. Archiving a subscription whose
// endpoint is in a different scope matches nothing, which is what a read in the
// wrong scope gets too.
//
// It is authored for the reason the read above it is, and it assigns exactly
// what querygen's archive assigns: the retirement stamp and nothing else. The
// two archives on this table have to agree about that, or a subscription
// retired by a reconciliation and one retired by id would differ in a column
// neither caller chose.
//
// Its predicates are qualified where querygen's UPDATE leaves them bare, and
// that is the subquery's doing rather than a preference: MySQL resolves the
// WHERE clause against every table the statement names, so a bare id is
// ambiguous between the row being archived and the endpoint the subquery reads.
func archiveSubscription(g *querygen.Generator) *querygen.Query {
	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: "ArchiveSubscription", Type: querygen.ExecRowsType},
		Content: fmt.Sprintf(`UPDATE %s SET
	%s = %s
WHERE %s IS NULL
	AND %s = sqlc.arg(%s)
	AND %s IN (
		SELECT %s
		FROM %s
		WHERE %s = sqlc.arg(%s)
	);`,
			SubscriptionsTable,
			querygen.ArchivedAtColumn, g.StoredNow(),
			querygen.Qualify(SubscriptionsTable, querygen.ArchivedAtColumn),
			querygen.Qualify(SubscriptionsTable, querygen.IDColumn), querygen.IDColumn,
			querygen.Qualify(SubscriptionsTable, EndpointIDColumn),
			querygen.Qualify(EndpointsTable, querygen.IDColumn),
			EndpointsTable,
			querygen.Qualify(EndpointsTable, ScopeColumn), ScopeColumn,
		),
	}
}

// deliveryQueries is the one statement the deliveries table takes on its own.
//
// The delivery is written, read through by the attempts log, and eventually
// reaped; nothing reads it by id, because everything that would has the
// dispatch or the attempt that names it. Its scope is stored on the row rather
// than derived from its dispatches' endpoints, because it is the delivery that
// has an owner: the payload is one tenant's data whether it fanned out to five
// subscribers, one, or none.
func deliveryQueries(g *querygen.Generator) []*querygen.Query {
	return []*querygen.Query{
		g.InsertQuery("InsertDelivery", DeliveriesTable, Deliveries.InsertColumns(), Deliveries.Nullable),
	}
}

// dispatchQueries is the worker's own machinery, and it is the one group here
// that takes no scope.
//
// That is the documented exception rather than an omission: one worker drains
// one queue for a whole deployment, so the claim, the lease, the read-back, the
// two outcomes, the health probe and the reaps all span every tenant. Each
// addresses a row the worker is already holding, or the queue as a whole. The
// scope a delivery carries rides along in the claimed projection so that a
// failure is attributable to a tenant in the worker's logs, and it filters
// nothing.
//
// Four of the nine are authored. Three of those assign an expression rather
// than a bound value or read one table through itself, and the fourth binds a
// creation instant the database is not the right clock for; each says which
// where it is rendered.
func dispatchQueries(g *querygen.Generator) []*querygen.Query {
	// The three outcome writes, each naming its own SET list rather than the
	// table's mutable set. A mark-delivered that also assigned next_attempt
	// would reschedule the row it just retired, and a requeue that assigned
	// last_error would erase the reason the replay was needed.
	delivered := g.UpdateQuery("MarkDispatchDelivered", DispatchesTable,
		Dispatches.Columns,
		[]string{DeliveredAtColumn, ClaimedUntilColumn, LastErrorColumn},
		Dispatches.Nullable,
	)

	// attempts is assigned rather than left as the claim incremented it,
	// because not every failure should cost an attempt. A delivery skipped by
	// an open circuit never reached the subscriber, and the worker hands back
	// the count it had before this claim. Without this column in the SET, an
	// endpoint down for an hour would silently consume the whole budget of
	// every dispatch queued behind it.
	failed := g.UpdateQuery("RecordDispatchFailure", DispatchesTable,
		Dispatches.Columns,
		[]string{ClaimedUntilColumn, AttemptsColumn, NextAttemptColumn, LastErrorColumn, DeadColumn},
		Dispatches.Nullable,
	)

	// The operator's re-drive, keyed on the (delivery, endpoint) pair rather
	// than on the dispatch id — that pair is what a replay request names, and
	// the schema holds it unique. The attempt count is reset rather than
	// continued, which is the whole point of a replay: a dead dispatch has
	// already exhausted its budget, and requeuing it without a reset would have
	// it die again on the next attempt.
	requeue := g.UpdateQuery("RequeueDispatch", DispatchesTable,
		Dispatches.ColumnsExcept(querygen.IDColumn),
		[]string{NextAttemptColumn, ClaimedUntilColumn, DeliveredAtColumn, DeadColumn, AttemptsColumn},
		Dispatches.Nullable,
		querygen.Match{Column: DeliveryIDColumn},
		querygen.Match{Column: EndpointIDColumn},
	)

	return []*querygen.Query{
		insertDispatch(g),
		selectClaimable(g),
		claimDispatches(g),
		fetchClaimed(g),
		delivered,
		failed,
		requeue,
		dispatchBacklog(g),
		reapDispatches(g),
		reapDeliveries(g),
	}
}

// insertDispatch writes one endpoint's copy of a delivery, immediately
// eligible: its first next_attempt is its creation time.
//
// It is authored for the one column querygen will not let a caller supply.
// created_at here is not "when the row landed" — it is the instant the
// transaction that emitted the event chose, and three separate things depend on
// every row of one fan-out carrying that same instant. The claim orders on
// (created_at, id) and its per-key predicate compares the tuple, the backlog
// probe reports the oldest of them as the queue's age, and next_attempt is that
// same value, so a row is claimable the moment it exists. A database default
// would stamp each of these statements separately, and the age a dashboard
// reads would become the age of the write rather than of the event.
//
// So the instant is bound once and used twice, under one argument name: the two
// columns are the same moment, and two arguments would be two ways to disagree
// about it.
func insertDispatch(g *querygen.Generator) *querygen.Query {
	columns := []string{
		querygen.IDColumn, DeliveryIDColumn, EndpointIDColumn, OrderingKeyColumn,
		querygen.CreatedAtColumn, NextAttemptColumn,
	}

	values := []string{
		arg(querygen.IDColumn), arg(DeliveryIDColumn), arg(EndpointIDColumn), arg(OrderingKeyColumn),
		arg(CreatedAtArg), arg(CreatedAtArg),
	}

	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: "InsertDispatch", Type: querygen.ExecType},
		Content: fmt.Sprintf("INSERT INTO %s (\n\t%s\n) VALUES (\n\t%s\n);",
			DispatchesTable,
			strings.Join(columns, ",\n\t"),
			strings.Join(values, ",\n\t"),
		),
	}
}

// selectClaimable picks the next batch of dispatch ids to lease.
//
// The ordering guarantee lives in this predicate, which is why it is authored:
// it reads the dispatches table through itself, and a correlated NOT EXISTS
// over a self-join is a shape querygen does not render and should not learn to.
//
// A row with an ordering key is claimable only when no earlier undelivered row
// shares that key *and the same endpoint*, so at most one dispatch per
// (endpoint, key) is ever in flight across every worker in the fleet. Scoping
// to the endpoint as well as the key is the difference between ordering and
// head-of-line blocking: keyed on the key alone, one subscriber that times out
// on resource-42.updated would hold back every other subscriber's copy of the
// same event.
//
// "Earlier" is (created_at, id), not created_at alone, and the tuple is what
// makes the guarantee hold. One Enqueue stamps every row with a single instant,
// so two dispatches sharing a key and an Enqueue also share a created_at; under
// a bare `<` neither would block the other, both would be claimable at once,
// and a failure on the first would deliver the second ahead of it. The tiebreak
// is id because that is what the ORDER BY breaks ties on — the predicate and the
// delivery order have to agree on "earlier" or the batch can contain a pair it
// is about to reorder.
//
// The claim's two time comparisons are one instant and two arguments, and that
// is the analyzers' doing rather than a distinction anybody wants. next_attempt
// is NOT NULL and claimed_until is not, so a single argument compared against
// both is one every engine here types twice — once as an instant and once as an
// optional one — and unison refuses the divergence rather than picking. So the
// lease horizon binds under its own name, and the store passes the same moment
// to both; see the arguments' declarations.
//
// The row lock is appended only where the dialect has one. On Postgres and
// MySQL it is what keeps two workers from selecting the same batch inside their
// respective transactions; SQLite has one writer and needs nothing.
func selectClaimable(g *querygen.Generator) *querygen.Query {
	const (
		claimed = "m"
		earlier = "prior"
	)

	statement := fmt.Sprintf(`SELECT %[1]s.%[3]s
FROM %[2]s AS %[1]s
WHERE %[1]s.%[4]s IS NULL
	AND %[1]s.%[5]s = FALSE
	AND %[1]s.%[6]s <= sqlc.arg(%[7]s)
	AND (%[1]s.%[8]s IS NULL OR %[1]s.%[8]s <= sqlc.arg(%[14]s))
	AND (%[1]s.%[9]s = '' OR NOT EXISTS (
		SELECT 1
		FROM %[2]s AS %[10]s
		WHERE %[10]s.%[11]s = %[1]s.%[11]s
			AND %[10]s.%[9]s = %[1]s.%[9]s
			AND %[10]s.%[4]s IS NULL
			AND %[10]s.%[5]s = FALSE
			AND (%[10]s.%[12]s < %[1]s.%[12]s
				OR (%[10]s.%[12]s = %[1]s.%[12]s AND %[10]s.%[3]s < %[1]s.%[3]s))
	))
ORDER BY %[1]s.%[12]s, %[1]s.%[3]s
%[13]s`,
		claimed,
		DispatchesTable,
		querygen.IDColumn,
		DeliveredAtColumn,
		DeadColumn,
		NextAttemptColumn,
		NowArg,
		ClaimedUntilColumn,
		OrderingKeyColumn,
		earlier,
		EndpointIDColumn,
		querygen.CreatedAtColumn,
		g.LimitClause(),
		LeaseExpiredByArg,
	)

	if g.Dialect().SupportsSkipLocked() {
		statement += "\nFOR UPDATE SKIP LOCKED"
	}

	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: "SelectClaimableDispatches", Type: querygen.ManyType},
		Content:    statement + ";",
	}
}

// claimDispatches leases the selected rows.
//
// It is authored because it assigns an expression: the attempt count is
// incremented here rather than on failure, so that a worker which crashes
// mid-delivery has still consumed an attempt and a dispatch that reliably kills
// its worker eventually goes dead instead of being reclaimed forever. querygen
// assigns bound values, and `attempts = attempts + 1` is not one.
//
// The set binds last, as every set predicate in this module does: on the two
// dialects with no array type it expands to one marker per element, and an
// argument bound after the expansion would be numbered into the middle of it.
func claimDispatches(g *querygen.Generator) *querygen.Query {
	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: "ClaimDispatches", Type: querygen.ExecRowsType},
		Content: fmt.Sprintf(`UPDATE %s SET
	%s = sqlc.arg(%s),
	%s = %s + 1,
	%s = %s
WHERE %s;`,
			DispatchesTable,
			ClaimedUntilColumn, ClaimedUntilArg,
			AttemptsColumn, AttemptsColumn,
			querygen.LastUpdatedAtColumn, g.StoredNow(),
			g.SetCondition(querygen.IDColumn, IDsArg),
		),
	}
}

// fetchClaimed projects the leased rows, joined to their delivery for the
// payload and to their endpoint for the target and the signing keys.
//
// The endpoint is read here, at claim time, rather than captured at dispatch
// time. That is deliberate: a secret rotated between the event and its delivery
// signs under the current key, and an endpoint disabled or archived in between
// is filtered out here rather than delivered to — the dispatch is claimed and
// simply not returned, its lease expires, and it is reclaimed. That is a slow
// no-op, which is better than delivering to an endpoint an operator has just
// disabled.
//
// It is authored because it is a three-table join, which querygen renders for a
// page and not for a set. The projection is still rendered from the same column
// lists every other statement here is: the endpoint's half is aliased because
// two tables following this module's row conventions share most of their column
// names, and an unaliased projection would hand the generated row type two
// fields called id.
func fetchClaimed(g *querygen.Generator) *querygen.Query {
	projection := []string{
		querygen.Qualify(DispatchesTable, querygen.IDColumn),
		querygen.Qualify(DispatchesTable, DeliveryIDColumn),
		querygen.Qualify(DispatchesTable, OrderingKeyColumn),
		querygen.Qualify(DispatchesTable, AttemptsColumn),
		querygen.Qualify(DeliveriesTable, EventTypeColumn),
		querygen.Qualify(DeliveriesTable, PayloadColumn),
		querygen.Qualify(DeliveriesTable, ScopeColumn),
	}
	projection = append(projection, aliased(EndpointsTable, EndpointPrefix, Endpoints.Columns)...)

	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: "FetchClaimedDispatches", Type: querygen.ManyType},
		Content: fmt.Sprintf(`SELECT
	%s
FROM %s
	%s
	%s
WHERE %s = FALSE
	AND %s IS NULL
	AND %s
ORDER BY %s, %s;`,
			strings.Join(projection, ",\n\t"),
			DispatchesTable,
			querygen.JoinStatement{
				JoinTarget:   DeliveriesTable,
				TargetColumn: querygen.IDColumn,
				OnTable:      DispatchesTable,
				OnColumn:     DeliveryIDColumn,
			},
			querygen.JoinStatement{
				JoinTarget:   EndpointsTable,
				TargetColumn: querygen.IDColumn,
				OnTable:      DispatchesTable,
				OnColumn:     EndpointIDColumn,
			},
			querygen.Qualify(EndpointsTable, DisabledColumn),
			querygen.Qualify(EndpointsTable, querygen.ArchivedAtColumn),
			g.SetCondition(querygen.Qualify(DispatchesTable, querygen.IDColumn), IDsArg),
			querygen.Qualify(DispatchesTable, querygen.CreatedAtColumn),
			querygen.Qualify(DispatchesTable, querygen.IDColumn),
		),
	}
}

// dispatchBacklog is the health probe: how many dispatches are waiting, and
// when the oldest of them was created.
//
// Both come back from one round trip because they answer one question — is the
// worker keeping up — and neither is useful alone. A depth of 40,000 is fine if
// the oldest is four seconds old and an incident if it is four hours old. Two
// statements would also be two snapshots, and a depth read against a queue that
// has since drained is a number nobody can act on.
//
// Dead rows are excluded: they are never going to be delivered, so counting
// them would make a permanently broken subscriber look like a permanently
// growing backlog.
//
// It is authored because it is an aggregate, which is the one thing a corpus of
// row statements has no shape for.
//
// The oldest instant is a subquery projecting the column rather than
// MIN(created_at), and the grouping is what makes that safe. No analyzer here
// resolves a Go type for an aggregate over a timestamp — an override could name
// one, but not that it is nullable — where a subquery projecting the column
// itself resolves to the column's own type on all three engines, and that type
// is NOT NULL.
//
// So an empty queue must not be a row. Grouping on the column the predicate has
// already pinned yields exactly one group while any dispatch is waiting and no
// group at all when none is, which is the honest shape: there is no oldest row
// to report, and "no backlog" is an absent row rather than a row of zeroes. The
// store reads that absence back as a depth of zero and no instant, which is the
// answer it gave before.
func dispatchBacklog(_ *querygen.Generator) *querygen.Query {
	const queued = "queued"

	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: "DispatchBacklog", Type: querygen.OneType},
		Content: fmt.Sprintf(`SELECT
	COUNT(*) AS depth,
	(
		SELECT %[1]s.%[2]s
		FROM %[3]s AS %[1]s
		WHERE %[1]s.%[4]s IS NULL
			AND %[1]s.%[5]s = FALSE
		ORDER BY %[1]s.%[2]s ASC
		LIMIT 1
	) AS oldest
FROM %[3]s
WHERE %[6]s IS NULL
	AND %[7]s = FALSE
GROUP BY %[7]s;`,
			queued,
			querygen.CreatedAtColumn,
			DispatchesTable,
			DeliveredAtColumn,
			DeadColumn,
			querygen.Qualify(DispatchesTable, DeliveredAtColumn),
			querygen.Qualify(DispatchesTable, DeadColumn),
		),
	}
}

// reapDispatches removes delivered dispatches past the retention window. Their
// attempts and any orphaned delivery go with them — see reapAttempts and
// reapDeliveries, which run after it in the same transaction.
//
// The three reaps are authored together, and for one reason: a bounded delete
// is a DELETE whose subquery reads the table being deleted from, which no
// dialect spells the same way and which querygen's single-row delete — the row
// this key names — is not. The bound is what keeps a reaper from taking a lock
// proportional to however much history a deployment has accumulated.
func reapDispatches(g *querygen.Generator) *querygen.Query {
	doomed := fmt.Sprintf(`SELECT %s
	FROM %s
	WHERE %s IS NOT NULL
		AND %s < sqlc.arg(%s)
	%s`,
		querygen.Qualify(DispatchesTable, querygen.IDColumn),
		DispatchesTable,
		querygen.Qualify(DispatchesTable, DeliveredAtColumn),
		querygen.Qualify(DispatchesTable, DeliveredAtColumn), BeforeArg,
		g.LimitClause(),
	)

	return boundedDelete(g, "ReapDispatches", DispatchesTable, doomed)
}

// reapAttempts removes log rows for deliveries that no longer have any
// dispatch. The attempts outlive the dispatch within a transaction only
// briefly; this runs after reapDispatches.
func reapAttempts(g *querygen.Generator) *querygen.Query {
	doomed := fmt.Sprintf(`SELECT %s
	FROM %s
	WHERE NOT EXISTS (
		SELECT 1
		FROM %s
		WHERE %s = %s
	)
	%s`,
		querygen.Qualify(AttemptsTable, querygen.IDColumn),
		AttemptsTable,
		DispatchesTable,
		querygen.Qualify(DispatchesTable, DeliveryIDColumn),
		querygen.Qualify(AttemptsTable, DeliveryIDColumn),
		g.LimitClause(),
	)

	return boundedDelete(g, "ReapAttempts", AttemptsTable, doomed)
}

// reapDeliveries removes deliveries whose every dispatch has been reaped.
//
// A delivery outlives its dispatches only when some subscribers were reaped and
// others were not, so this deliberately checks for the absence of *any* dispatch
// rather than deleting alongside the first.
func reapDeliveries(g *querygen.Generator) *querygen.Query {
	doomed := fmt.Sprintf(`SELECT %s
	FROM %s
	WHERE NOT EXISTS (
		SELECT 1
		FROM %s
		WHERE %s = %s
	)
	%s`,
		querygen.Qualify(DeliveriesTable, querygen.IDColumn),
		DeliveriesTable,
		DispatchesTable,
		querygen.Qualify(DispatchesTable, DeliveryIDColumn),
		querygen.Qualify(DeliveriesTable, querygen.IDColumn),
		g.LimitClause(),
	)

	return boundedDelete(g, "ReapDeliveries", DeliveriesTable, doomed)
}

// boundedDelete wraps a bounded selection of doomed ids in the DELETE that
// removes them, in the spelling the dialect takes.
//
// MySQL refuses a subquery that reads the table being deleted from
// (ER_UPDATE_TABLE_USED) but accepts the same subquery once it has been
// materialized through a derived table, which is the whole of the difference.
// Rendering it in one place is what keeps the three reaps from disagreeing
// about which dialect needs it.
//
// The deleted table's id is qualified where querygen's own delete leaves it
// bare, for the reason the scoped archive qualifies its key: the statement
// names the table twice, and SQLite reports a bare id across the subquery
// boundary as ambiguous.
func boundedDelete(g *querygen.Generator, name, table, doomed string) *querygen.Query {
	if g.Dialect() == dialect.MySQL {
		doomed = fmt.Sprintf("SELECT %s\n\tFROM (\n\t%s\n\t) AS doomed", querygen.IDColumn, doomed)
	}

	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: name, Type: querygen.ExecRowsType},
		Content: fmt.Sprintf("DELETE FROM %s\nWHERE %s IN (\n\t%s\n);",
			table, querygen.Qualify(table, querygen.IDColumn), doomed,
		),
	}
}

// attemptQueries is the delivery log: the append, its paged read, and the reap
// that clears what a removed delivery left behind.
//
// The scope is reached through the delivery rather than stored on the attempt.
// An attempt is a log line about a delivery, so its owner is the delivery's, and
// a second copy of that fact on every attempt row is a copy that can disagree
// with the first. The join is to a primary key.
//
// One consequence, and it is bounded: past the retention window the reaper
// removes an attempt's delivery, and the attempts that outlive it for a cycle
// stop being listable. They are already doomed — the next reap deletes them —
// and neither is readable by anybody once the delivery is gone.
func attemptQueries(g *querygen.Generator) []*querygen.Query {
	list := g.JunctionListQueries("ListAttempts", AttemptsTable, Attempts.Columns,
		&querygen.Junction{
			Table:    DeliveriesTable,
			Column:   querygen.IDColumn,
			OnColumn: DeliveryIDColumn,
			Columns:  Deliveries.ColumnsExcept(querygen.ArchivedAtColumn),
			Matches:  []querygen.Match{{Column: ScopeColumn}},
		},
		querygen.Match{Column: DeliveryIDColumn},
	)

	return append([]*querygen.Query{insertAttempt()}, append(list, reapAttempts(g))...)
}

// insertAttempt appends one row to the delivery log.
//
// It is authored for the same column insertDispatch is, and the reason is
// sharper here. An attempt's created_at is when the request was issued, which
// the worker measures — it is the same clock the recorded duration is measured
// against, and the row is written after the response came back or the deadline
// elapsed. A database default would stamp the moment the row landed, which is
// later than the instant the log line is about by however long the request took
// to fail.
func insertAttempt() *querygen.Query {
	columns := append(querygen.ForInsert(Attempts.Columns), querygen.CreatedAtColumn)

	values := make([]string, 0, len(columns))
	for _, column := range columns {
		values = append(values, binding(column, Attempts.Nullable))
	}

	return &querygen.Query{
		Annotation: querygen.QueryAnnotation{Name: "InsertAttempt", Type: querygen.ExecType},
		Content: fmt.Sprintf("INSERT INTO %s (\n\t%s\n) VALUES (\n\t%s\n);",
			AttemptsTable,
			strings.Join(columns, ",\n\t"),
			strings.Join(values, ",\n\t"),
		),
	}
}

// arg renders a bound argument reference.
func arg(name string) string {
	return fmt.Sprintf("sqlc.arg(%s)", name)
}

// binding renders a column's argument reference, nullable where the table says
// the column is. It is querygen's own rule, restated for the two authored
// inserts: a NOT NULL column bound through sqlc.narg yields a parameter that can
// express a NULL the server will reject.
func binding(column string, nullable []string) string {
	if slices.Contains(nullable, column) {
		return fmt.Sprintf("sqlc.narg(%s)", column)
	}

	return arg(column)
}

// aliased renders a joined table's columns under a prefix, so that two tables
// sharing most of their column names project into one row without colliding.
//
// It is querygen.Junction.Prefix's rendering, restated for the one projection
// here that is assembled rather than emitted — the claimed dispatch, which
// spans three tables where a junction spans two.
func aliased(table, prefix string, columns []string) []string {
	projected := make([]string, 0, len(columns))
	for _, column := range columns {
		projected = append(projected, fmt.Sprintf("%s AS %s_%s", querygen.Qualify(table, column), prefix, column))
	}

	return projected
}

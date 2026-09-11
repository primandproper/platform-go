package webhooks

import (
	"context"
	"time"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/tenancy"
)

// Dispatch is one endpoint's copy of one delivery: the unit the worker actually
// retries, backs off, and gives up on.
//
// It exists as a distinct row from the Delivery because per-endpoint state is
// the whole point. A delivery that fanned out to five subscribers is not
// "failed" or "delivered" — four may have accepted it on the first attempt while
// the fifth is on its sixth retry, and a single status on the delivery cannot
// express that. Retrying at the delivery level would also redeliver to the four
// that already accepted it.
type Dispatch struct {
	// NextAttempt is when this dispatch next becomes claimable.
	NextAttempt time.Time `json:"nextAttempt"`
	// ID identifies the dispatch.
	ID string `json:"id"`
	// DeliveryID is the delivery being sent.
	DeliveryID string `json:"deliveryID"`
	// EndpointID is the subscriber it is being sent to.
	EndpointID string `json:"endpointID"`
	// OrderingKey is denormalized from the delivery so the claim predicate can
	// enforce ordering without joining. See the claim in
	// webhooks/internal/queries, which is where the guarantee is written down.
	OrderingKey string `json:"orderingKey,omitempty"`
	// LastError is the most recent failure, rendered.
	LastError string `json:"lastError,omitempty"`
	// Attempts is how many attempts have been made.
	Attempts int `json:"attempts"`
	// Dead marks a dispatch that exhausted its attempts. It is skipped by every
	// future claim and is what an operator replays.
	Dead bool `json:"dead"`
}

// ClaimedDispatch is a Dispatch the worker has leased, joined with everything
// needed to actually issue the request. It is assembled by the Store so the
// worker makes one round trip per batch rather than one per dispatch.
type ClaimedDispatch struct {
	// Endpoint is the subscriber, resolved at claim time rather than at
	// dispatch time — so a secret rotated between the event and its delivery
	// signs with the current key, and an endpoint disabled in between is not
	// delivered to at all.
	Endpoint *Endpoint `json:"endpoint"`
	// Payload is the delivery body, verbatim as dispatched.
	Payload []byte `json:"payload"`
	// Scope is the delivery's scope, read back from the delivery row.
	//
	// The worker does not filter on it — it delivers whatever it claimed, to the
	// endpoint the dispatch names, and the fan-out that produced the row already
	// resolved subscribers within this scope. It is here so that a delivery
	// failure, a dead dispatch, and a slow subscriber are attributable to a
	// tenant in the worker's logs and spans, which is otherwise the one place in
	// the pipeline where whose event it was has been forgotten.
	Scope tenancy.Scope `json:"scope"`
	// EventType is the delivery's event type.
	EventType EventType `json:"eventType"`

	Dispatch
}

// Store is the persistence seam.
//
// This package ships a SQL implementation (NewSQLStore) together with the DDL
// it needs (webhooks/migrations), so adopting webhooks does not mean writing
// this. The interface exists because delivery mechanics and persistence are
// genuinely separable, and an application with its own schema conventions —
// or one storing endpoints somewhere that is not a SQL database — should not
// have to fork the package to keep them.
//
// # A consumer write takes a Tx, a consumer read takes an executor
//
// Every method an application calls on its own behalf reads
// (ctx, tx database.Tx, scope tenancy.Scope, ...) if it writes, and
// (ctx, q database.SQLQueryExecutor, scope tenancy.Scope, ...) if it reads. A
// database.Tx is producible only by database.RunInTransaction, so a write's
// signature is a compile-time claim that the caller is already inside a
// transaction — which is the point, because a consumer's write almost never
// travels alone: registering an endpoint and the audit entry that records who
// registered it are one fact, and a store that opened its own transaction would
// commit the first while the second was still refusable.
//
// Enqueue is the proof that the shape works rather than an exception to it. An
// outbound event enqueued in the same transaction as the row that caused it is
// the entire reason that method takes a Tx, and it has taken one since this
// package shipped; the four endpoint and subscription writes now say the same
// thing about the transaction they belong to.
//
// The read takes the wider type deliberately. EndpointsForEvent is the
// precedent: a Tx satisfies database.SQLQueryExecutor, so one signature serves
// both a caller holding Client.Reader() and a caller inside a transaction, and
// the second sees that transaction's own uncommitted writes — an endpoint
// registered and then listed back within one transaction is there. A read
// narrowed to a reader would be reading a database that does not yet contain
// the row its caller just wrote.
//
// # The delivery machinery takes neither
//
// Claim, MarkDelivered, RecordFailure, RecordAttempt, Requeue, Backlog and Reap
// take no executor at all, and run on the handle the implementation was built
// with. They are the component servicing itself: there is no consumer
// transaction for them to join, and the queue protocol's correctness is that a
// claim commits before the request goes out — a caller supplying a transaction
// would be choosing when that commit happens, which is the one thing the
// protocol cannot let them choose. A lease held open for the duration of
// somebody else's transaction is a dispatch nobody else may claim and this
// worker may never send.
//
// That is the same decision metering took for its flush protocol, and the two
// are the same case: a worker draining a queue on a timer, not a request being
// served.
//
// # Nine of these are on the wire and nine are not
//
// webhooks/grpc serves endpoint management and delivery history: SaveEndpoint,
// GetEndpoint, ListEndpoints, ArchiveEndpoint, AddSubscription,
// GetSubscription, ListSubscriptions, ArchiveSubscription and ListAttempts.
// They are the half of this package that is a resource rather than a protocol,
// and the only half a person ever touches.
//
// The seven above are the first group that stays off it, and the reason is the
// paragraph they are already documented by: a caller supplying a transaction is
// exactly what an RPC is, and it is the one thing the queue protocol cannot let
// them supply. EndpointsForEvent and Enqueue are the other two, and each says so
// on its own method — Enqueue is the sharp one, because it is the only method
// here that is consumer-facing and still must not be reachable over a wire.
//
// # The scope is an argument, on every consumer method
//
// Every method reaching an endpoint, a subscription, or a delivery on a
// consumer's behalf takes a tenancy.Scope, and none of them offers a variant
// that omits it — an implementation must filter on it rather than treat it as a
// hint. A subscription carries no scope of its own; its owner is its endpoint's,
// and the scope is reached through that, the same way the delivery log reaches
// it through the delivery.
//
// That includes SaveEndpoint, which takes a whole Endpoint that already carries
// one. It binds the argument rather than Endpoint.Scope, because a scope derived
// from a field is the derivation the column rule exists to rule out: it makes
// "which tenant is this write for" answerable only by reading a struct the
// caller assembled somewhere else. An endpoint whose scope disagrees with the
// argument is ErrScopeMismatch rather than either value quietly winning; one
// that names none adopts the argument.
//
// The exceptions are the machinery above. Claim, Backlog and Reap deliberately
// span every scope, because one worker drains one queue for the whole
// deployment, and MarkDelivered, RecordFailure, RecordAttempt and Requeue
// address a dispatch the worker or an operator is already holding.
type Store interface {
	// SaveEndpoint creates or replaces an endpoint in scope, through the caller's
	// transaction, and reconciles its subscriptions against the set it names.
	//
	// Reconciles rather than replaces: a subscription the endpoint already has is
	// kept, with its identity and its creation time; one it names for the first
	// time is created; one it no longer names is archived rather than deleted, so
	// that a subscription an endpoint has ended is still something the delivery
	// log can be read against. It fills the endpoint's Subscriptions with the rows
	// that are live afterwards, IDs included.
	//
	// The endpoint adopts scope where it names none, and an endpoint naming a
	// different one is ErrScopeMismatch. A nil tx is an error wrapping
	// ErrNilExecutor.
	SaveEndpoint(ctx context.Context, tx database.Tx, scope tenancy.Scope, endpoint *Endpoint) error
	// GetEndpoint reads one of scope's endpoints, secrets included, on the
	// caller's executor. It returns an error wrapping database/sql.ErrNoRows when
	// the endpoint does not exist — including when it exists in another scope,
	// which is the same answer as far as this scope is concerned.
	GetEndpoint(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, endpointID string) (*Endpoint, error)
	// ListEndpoints pages through the endpoints registered in scope, on the
	// caller's executor.
	ListEndpoints(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Endpoint], error)
	// ArchiveEndpoint retires one of scope's endpoints, through the caller's
	// transaction. Its delivery history is retained: the attempts log outlives the
	// endpoint, because "what did we send them" is asked most often after someone
	// has been removed.
	//
	// Its subscriptions are left as they are. An archived endpoint is excluded
	// from fan-out by its own archived_at, so archiving them too would buy
	// nothing and would lose which event types it was subscribed to if it is ever
	// re-registered.
	ArchiveEndpoint(ctx context.Context, tx database.Tx, scope tenancy.Scope, endpointID string) error

	// AddSubscription subscribes one of scope's endpoints to eventType, through
	// the caller's transaction, and returns the resulting row.
	//
	// It is idempotent on the (endpoint, event type) pair: subscribing to
	// something the endpoint already subscribes to returns the existing row, and
	// re-subscribing to something it archived revives that row rather than
	// minting a second one for the same pair. It returns an error wrapping
	// database/sql.ErrNoRows when the endpoint does not exist in scope.
	//
	// The catalog is not checked here — a Store has none. StoreDispatcher.Subscribe
	// is the entry point that gates on it, and it is the one an application should
	// call, for the reason Register exists rather than SaveEndpoint being public
	// API: an accepted subscription to an event type nothing publishes is an
	// endpoint that never fires and no signal explaining why.
	AddSubscription(ctx context.Context, tx database.Tx, scope tenancy.Scope, endpointID string, eventType EventType) (*Subscription, error)
	// GetSubscription reads one of scope's subscriptions on the caller's executor,
	// archived ones included — "when did they stop receiving this" is a question
	// about an archived row. It returns an error wrapping database/sql.ErrNoRows
	// when the subscription does not exist under one of scope's endpoints.
	GetSubscription(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, subscriptionID string) (*Subscription, error)
	// ListSubscriptions pages the live subscriptions of one of scope's endpoints,
	// on the caller's executor.
	ListSubscriptions(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, endpointID string, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Subscription], error)
	// ArchiveSubscription retires one of scope's subscriptions, through the
	// caller's transaction, so the endpoint stops receiving that event type
	// without its other subscriptions, its delivery history, or its identity being
	// touched.
	//
	// This is the method a flat event list cannot offer. Against one, "stop
	// sending me order.created" can only be expressed as a rewrite of the whole
	// set, which loses when it happened and races any concurrent edit of the same
	// endpoint.
	ArchiveSubscription(ctx context.Context, tx database.Tx, scope tenancy.Scope, subscriptionID string) error

	// EndpointsForEvent returns the enabled, unarchived endpoints in scope that
	// are subscribed to eventType, using the caller's executor.
	//
	// The scope is a parameter and not an option: this is the query whose missing
	// filter delivers one account's event to every other account's subscribers.
	//
	// It is off the wire, and the sentence above is why. This is the internal
	// fan-out — the dispatcher asking itself who is subscribed on the way to its
	// own work — so the caller who would reach for it over an RPC is not a
	// caller at all. A console that wants to show which endpoints a given event
	// reaches reads their subscriptions, which is what ListSubscriptions is for
	// and is the same answer arrived at from the side that is scoped by
	// construction.
	EndpointsForEvent(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, eventType EventType) ([]*Endpoint, error)
	// Enqueue writes a delivery and one dispatch per endpoint, in the caller's
	// transaction, so both commit with whatever else that transaction did.
	// The delivery's scope is stored with it.
	//
	// It is off the wire too, and it is the one absence here worth arguing about,
	// because unlike the other eight it is consumer-facing: fanning an event out
	// is something an application does on purpose. What it is not is separable
	// from the transaction it is called in. That clause above — both commit with
	// whatever else that transaction did — is the entire property, and an RPC
	// moves the write into a transaction of its own, on the far side of a
	// network, at a moment the caller does not choose. What you get back is a
	// delivery for a row that rolled back, or a committed row nobody was ever
	// told about, and no amount of retrying repairs either after the fact. It is
	// the same fact audit.Recorder states about an audit entry and the reason
	// that method takes a Tx as well.
	//
	// A process that wants to publish an event it did not cause is describing an
	// RPC of its own — the one whose handler owns the transaction the write
	// belongs in — with Dispatcher.Dispatch called inside it.
	Enqueue(ctx context.Context, tx database.Tx, delivery *Delivery, endpointIDs []string, now time.Time) error

	// Claim leases the next batch of due dispatches, incrementing their attempt
	// counts, and returns them ready to send.
	//
	// It takes no executor: the lease has to be committed before the request goes
	// out, or a second worker claims the same row while the first is mid-flight,
	// and a caller supplying a transaction would be choosing when that commit
	// happens.
	//
	// It spans every scope, deliberately: a worker delivers the whole
	// deployment's backlog, and a per-scope claim would need a list of scopes
	// nothing maintains. What it returns is scoped — each ClaimedDispatch says
	// which scope it came from.
	Claim(ctx context.Context, now time.Time, limit int, leaseUntil time.Time) ([]ClaimedDispatch, error)
	// MarkDelivered retires a dispatch that was accepted. Like Claim it takes no
	// executor: it releases a lease Claim committed, and the request it is
	// reporting on has already happened.
	MarkDelivered(ctx context.Context, dispatchID string, at time.Time) error
	// RecordFailure releases the lease, schedules the retry, and sets dead once
	// the dispatch has exhausted its attempts. Like MarkDelivered it takes no
	// executor.
	//
	// attempts is persisted as given rather than left as Claim incremented it,
	// so the caller can decline to charge an attempt for a failure the
	// subscriber never saw — an open circuit being the case that matters.
	RecordFailure(ctx context.Context, dispatchID string, attempts int, nextAttempt time.Time, lastErr string, dead bool) error
	// RecordAttempt appends to the delivery log. It takes no executor for the same
	// reason: the attempt it records is one the worker has already made, and a log
	// line that rolls back with somebody else's transaction is a delivery nothing
	// remembers.
	RecordAttempt(ctx context.Context, attempt *Attempt) error
	// ListAttempts pages through the attempts recorded for one of scope's
	// deliveries, on the caller's executor. A delivery in another scope reads as
	// one with no attempts, which is what it is from here.
	ListAttempts(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, deliveryID string, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[Attempt], error)

	// Requeue makes a delivery/endpoint pair claimable again, clearing its dead
	// flag and attempt count. It returns an error wrapping ErrDeliveryNotFound
	// if the pair was never dispatched or has been reaped.
	//
	// It takes no scope because the pair is already one: a dispatch exists only
	// where a delivery fanned out to an endpoint, and Dispatch resolves those
	// within one scope. StoreDispatcher.Replay is the scoped entry point, and it
	// establishes the scope by reading the endpoint in it first.
	//
	// It takes no executor because it writes the same column Claim and
	// RecordFailure write, and a row made claimable inside a caller's transaction
	// is a row the worker cannot see until that transaction ends. An operator
	// replaying a delivery is asking the queue to move, not asking for a row that
	// commits with something else of theirs.
	Requeue(ctx context.Context, deliveryID, endpointID string, at time.Time) error

	// Backlog reports how many dispatches are waiting and when the oldest was
	// created, for the worker's health gauges. Like Claim, it spans every scope
	// and takes no executor.
	Backlog(ctx context.Context) (depth int64, oldest time.Time, err error)
	// Reap deletes delivered dispatches, their deliveries, and their attempts
	// once they age past the retention window, up to limit rows. Like Claim it
	// runs on the implementation's own handle: it answers no request.
	Reap(ctx context.Context, before time.Time, limit int) (int64, error)
}

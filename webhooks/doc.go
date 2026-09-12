/*
Package webhooks delivers outbound webhooks: signed, retried, ordered, and
replayable.

Its mirror is webhooks/inbound, which receives them: verify the provider's
signature, publish, ack.

Everything here is a guarantee rather than an opinion. What an event means, when
it fires, and what its payload contains are the application's; that a subscriber
can authenticate the payload, that a delivery is not lost when the process dies,
that a dead subscriber cannot starve a healthy one, and that resource.updated
cannot overtake resource.created are this package's.

# The two halves

Dispatcher is the write side. Dispatch takes the caller's transaction executor,
resolves who is subscribed, and writes one dispatch row per subscriber inside
that transaction:

	err := client.WithTransaction(ctx, func(tx database.Tx) error {
		if err := updateOrder(ctx, tx, order); err != nil {
			return err
		}

		return dispatcher.Dispatch(ctx, tx, tenancy.Of(order.AccountID), &webhooks.Delivery{
			EventType:   OrderUpdated,
			OrderingKey: order.ID,
			Payload:     body,
		})
	})

The deliveries commit with the state change that caused them, or not at all.
There is no way to dispatch outside a transaction by accident: holding a
database.Tx from WithTransaction means you are already in one. This is the same
seam outbox.Enqueue uses, for the same reason.

Every other consumer write takes that same transaction, and every consumer read
takes an executor — a database.Tx for a write, a database.SQLQueryExecutor for a
read, always as the argument after the context. Registering an endpoint is not
usually the only thing an application writes when a subscriber signs up, and the
audit entry recording who did it belongs in the same commit as the endpoint. The
delivery machinery is the deliberate exception: Claim, MarkDelivered,
RecordFailure, RecordAttempt, Requeue, Backlog and Reap take no executor at all,
because a worker draining a queue has no caller's transaction to join and its
lease has to be committed before the request goes out. See webhooks.Store, where
the split is written down.

Worker is the delivery side. It claims due dispatches, signs and sends them,
records every attempt, and schedules retries. It runs in its own process or
goroutine and is started by Run, stopped by Close.

# Why dispatches are rows and not queue messages

The unit of retry is one endpoint's copy of one event, and no broker can express
that. A delivery that fanned out to five subscribers is not "failed" or
"delivered" — four may have accepted it on the first attempt while the fifth is
on its sixth retry. Retrying at the message level redelivers to the four that
already succeeded; tracking state at the message level cannot represent the
fifth's attempt count at all.

So per-endpoint state lives in a row: attempts, next_attempt, last_error, and a
terminal dead flag, per (delivery, endpoint). Retry is a scheduled timestamp
rather than a nack, which means it survives a worker restart, and "give up" is a
state a row is in rather than a message that stopped being redelivered.

# Signing

	X-Platform-Signature: v1,t=1753900000,s=<hex(HMAC-SHA256(secret, "v1." + t + "." + body))>

The scheme is cryptography/requestsigning, and this package is one of its
callers rather than its owner. Secret is requestsigning.Keyring, deliveries are
signed with requestsigning.Sign, and a subscriber verifies them with
requestsigning.Verify — the same functions guarding first-party
service-to-service calls, over the same wire format. Read that package for why
both the version and the timestamp are inside the signed material, and for what
the Current/Previous pair buys.

What is worth repeating here is the receiving end. Verification is where these
schemes are actually got wrong: subscribers compare with ==, skip the timestamp
check, or verify a re-serialized body rather than the received bytes. Point
subscribers at requestsigning.Verify rather than at the paragraph above — a
scheme described in prose is a scheme reimplemented, and reimplemented
verification is where the timing leak goes.

	body, err := io.ReadAll(req.Body)   // the exact bytes, before any decoding
	if err != nil {
		return err
	}

	err = requestsigning.Verify(secret, body, req.Header.Get(requestsigning.SignatureHeader))

A subscriber that is itself a platform service can skip even that and install
requestsigning/http's middleware on the callback route.

# Tenancy

An Endpoint belongs to somebody and a Delivery is somebody's event, and both say
so with a tenancy.Scope. Fan-out is bounded by it: Dispatch resolves subscribers
within the delivery's scope, so an endpoint registered by one account never
receives another account's copy of the same event type.

	var registered *webhooks.Endpoint

	err := client.WithTransaction(ctx, func(tx database.Tx) error {
		var registerErr error

		registered, registerErr = dispatcher.Register(ctx, tx, tenancy.Of(accountID), &webhooks.Endpoint{
			URL:           "https://subscriber.example/hooks",
			Secret:        webhooks.Secret{Current: key},
			Subscriptions: webhooks.SubscribeTo(OrderUpdated),
		})

		return registerErr
	})

The scope is the argument rather than Endpoint.Scope, so what the statement binds
is what the call named. An endpoint that names none adopts it; one naming a
different tenant is webhooks.ErrScopeMismatch rather than either value quietly
winning. Dispatch reads the same way about Delivery.Scope, which is why the
example above fills in neither.

An application whose events are global says tenancy.Global() in both places and
gets what this package did before the dimension existed — Global is a scope like
any other, matching only itself, and it is stored as the empty identifier that
the scope columns default to.

There is no unscoped read. Every Store method that reaches an endpoint, a
subscription, or a delivery on a consumer's behalf takes a scope as an argument,
the zero tenancy.Scope is not a scope, and a query that lost one fails at the
driver rather than widening. The exceptions are the worker's own machinery —
Claim, Backlog, and Reap span every scope, because one worker drains one queue
for the whole deployment — and they say so.

What a scope is not is permission. Passing tenancy.Of(accountID) says these rows
are that account's; whether the caller may act for that account is
authorization's question, asked before this one.

# Endpoints and subscriptions

An endpoint is a subscriber, and a subscription is one of the event types it
wants. Subscriptions are rows of their own — identified, timestamped, and
archivable one at a time — rather than a list of strings on the endpoint.

That is not bookkeeping. An application's own API is asked to retire one
subscription: a user unticks order.created and leaves the other four alone.
Against a flat list the only available answer is to rewrite the whole set, which
cannot say when the subscription ended, has no identifier for the request that
asked to name, and silently reverts whatever a concurrent edit of the same
endpoint did. Against rows it is Unsubscribe on one ID:

	sub, err := dispatcher.Subscribe(ctx, tx, scope, endpointID, OrderShipped)
	// ...
	retired, err := dispatcher.Unsubscribe(ctx, tx, scope, sub.ID)

retired is the row as the archive left it, so when the subscription ended is
readable without asking again — which is what every consumer write here answers
with. Register hands back the endpoint the store holds, Subscribe the
subscription, and the two archives the row they moved; none of them writes onto
the value it was handed, and an archive whose ID named nothing in scope answers
a nil row and no error, because that is the state the caller asked for and there
is nothing to describe it with.

Register and SaveEndpoint still take the whole set, because registration names
event types rather than subscription IDs — there are none yet — and SubscribeTo
builds it. What a save does to the stored rows is reconcile rather than replace:
one the endpoint already has keeps its identity and its creation time, one it no
longer names is archived rather than deleted, and the returned endpoint's
Subscriptions are what is live.

Endpoint.EventTypes derives the flat list where one is wanted — a catalog check,
a subscription UI's checkboxes. It is derived and not stored, so there is no
second copy of the set to keep in step with the rows.

An endpoint also carries a Name, a CreatedBy, and the convention triple's
timestamps. CreatedBy is a tenancy.Scope rather than a user ID string: "whose is
this" already has a type here, and the principal who registered an endpoint is a
finer-grained answer to it rather than a different question. Unlike Scope it is
optional — an application that does not attribute endpoints to a person leaves
it unset — and it is written once, with the row, because an endpoint does not
change hands.

# Ordering

Deliveries sharing an OrderingKey reach a given endpoint in dispatch order. The
guarantee lives in the claim predicate: a keyed dispatch is claimable only when
no earlier undelivered dispatch shares its key *and its endpoint*, so at most
one is ever in flight per (endpoint, key) across the whole fleet.

The endpoint is in that tuple deliberately. Keyed on the ordering key alone, one
subscriber timing out on resource-42.updated would hold back every other
subscriber's copy of the same event — a dead endpoint stalling healthy ones,
which is the failure per-endpoint circuit breaking exists to prevent,
reintroduced in the claim predicate.

Deliveries with no ordering key are unordered and claim freely.

# Delivery semantics

At-least-once, and it cannot be otherwise: the subscriber and the database have
no shared commit, so a crash between a 200 response and the row update
redelivers on restart. Subscribers must tolerate duplicates, and
DeliveryIDHeader is the key to deduplicate on — it is stable across every
attempt and every replay of one delivery.

Failures back off exponentially with full jitter via retrycfg.DelayFor, persisted
as a timestamp so the schedule survives a restart. Past Backoff.MaxAttempts the
dispatch is marked dead: skipped by every future claim, counted, and left for an
operator to replay. Without that terminal state one permanently broken
subscriber blocks its ordering key forever.

Some failures skip the budget entirely. A 4xx other than 408 or 429 means the
subscriber understood and refused, and a URL that no longer passes
CheckEndpointURL will not start passing; both are marked retry.Unretryable and
go straight to dead rather than spending twenty-five attempts proving it.

Circuit breaking is the other direction. A short-circuited delivery is a failure
but is explicitly *not* charged an attempt — an endpoint down for an hour would
otherwise exhaust the budget of every delivery queued behind it, and they would
all need replaying by hand once it recovered.

# SSRF

An endpoint URL is attacker-supplied and this package makes authenticated
requests to it, which is textbook server-side request forgery: point it at
169.254.169.254 and the worker fetches cloud credentials on the attacker's
behalf. CheckEndpointURL enforces https and refuses any host resolving into
loopback, link-local, private, or non-global space — at registration, where the
rejection can be reported to whoever submitted it, and again at delivery,
because DNS is mutable.

Redirects are refused rather than followed. Following one would deliver a signed
payload to a host that was never registered and never checked, turning an open
redirect on any subscriber's domain into a way to point the worker anywhere.

# Persistence

Store is the seam. This package ships a SQL implementation (NewSQLStore) and the
DDL it needs (webhooks/migrations), so adopting webhooks does not mean writing
one — but an application with its own schema conventions can implement the
interface instead of forking the package.

A deployment whose tables predate subscriptions being rows migrates with
migrations.UpgradeSQL, which adds the columns and backfills the existing flat
subscription set. Read that package's Upgrading section first: unlike the
create-only DDL, it is one-shot.

The five tables are rendered from one prefix rather than five names. They
reference each other by foreign key and the queries join across them, so a
consumer who could name them independently could also name them inconsistently,
and nothing would catch it until the first dispatch.

# Where the SQL comes from

Every statement the SQL store executes is in a canonical .sql that sqlc checks
against this package's own schema, on all three dialects, and is executed
through the querier sqlc-gen-unison emits from it. A column renamed in a
migration is a failed `make generate` with no database running, where it used to
be a scan error on whichever dialect a container test happened to reach first.

The schema's facts — the five table names, each table's columns in projection
order, the two subsets a write may assign — are spelled once, in
webhooks/internal/queries. `make generate` renders most of the corpus from them
through database/querygen; eleven statements are written out there in full,
because they are the shapes querygen deliberately does not render — a self-join,
a three-table projection, a bounded delete, an incrementing counter, an
aggregate, a scope reached through another table, and the two creation instants
that are the emitting transaction's rather than the row's. That package's
comment says which is which and why, and its tests pin the committed .sql byte
for byte against the renderer, so "the SQL sqlc checks is the SQL the store
runs" is a fact a test states rather than a property of a pipeline taken on
trust.

One consequence reaches a caller. The store holds no clock: the convention
columns are stamped by the statements themselves, from the database server, and
the instants that are a caller's to choose — when a delivery was enqueued, how
long a lease runs, how far back a reap goes — are arguments as they always were.
A row's created_at and the filter window compared against it now come from one
clock rather than from however many application instances are writing.

# The catalog

Subscribable event types are supplied at construction via WithCatalog, not
stored. What an event means is an application opinion and this package has none.

Both Register and Dispatch reject a type outside the catalog. That matters
because an event type is a string underneath and string literals are typo-prone:
a subscription to "reciped.created" accepted silently produces an endpoint that
never fires, and diagnosing it means noticing an absence.

Declare the event types as EventType constants and key the catalog by them:

	const (
	    OrderCreated webhooks.EventType = "order.created"
	    OrderUpdated webhooks.EventType = "order.updated"
	)

	webhooks.WithCatalog(webhooks.Catalog{
	    OrderCreated: {Description: "an order was created"},
	    OrderUpdated: {Description: "an order was updated"},
	})

EventType is a defined type rather than a string so that this form is available,
and it is a defined type rather than an alias because an alias would be
indistinguishable from string to a type checker, which is the whole point of
having one.

The point is the catalog's second copy. The catalog must list every event type
the application publishes — a missing entry fails the dispatch gate, and where
Dispatch runs inside the write transaction that is a failed write rather than a
missing webhook — so an application of any size ends up deriving it rather than
maintaining it beside the constants that are its source of truth.

Deriving it means answering "which of these constants are event types", and
there are two ways to answer. By name, matching a suffix: a convention nothing
enforces, where a constant spelled differently is silently not an event type and
the miss surfaces as a failed dispatch rather than a failed build. Or by declared
type, which the compiler already knows, cannot be spelled wrong, and does not
care which package or which directory the constant was declared in.

This package ships no generator. Which constants exist, where they live, and
where the descriptions come from are the application's business, and a scan over
them is a few dozen lines once the type makes the question answerable. What
webhooks owes that scan is a declaration form it can rely on, which is this one.

Nothing here constrains the string itself. Dots, colons, and underscores are all
fine, and the catalog remains the authority on which values exist.

# Watching it

The two that matter most are webhooks_backlog_depth and
webhooks_backlog_age_seconds, sampled on the reap tick. Every other instrument
is a rate or a latency, and none of them separates "delivering steadily" from
"delivering steadily while falling further behind" — only the age does.

The rest: webhooks_deliveries_dispatched against webhooks_deliveries_sent (the
gap is the rollback rate), webhooks_deliveries_failed,
webhooks_deliveries_short_circuited, and webhooks_deliveries_dead — alert on any
increase in the last, since a dead dispatch is an event a subscriber will never
see — plus webhooks_claim_errors, webhooks_dispatches_reaped, and the
webhooks_delivery_latency_ms, webhooks_cycle_latency_ms, and
webhooks_claimed_batch_size distributions.

Per-delivery measurements carry an endpoint attribute, because one worker serves
every subscriber and a single broken one is invisible in the total. That
attribute's cardinality grows with the endpoints table; an operator with enough
subscribers to care should drop it in their collector rather than lose the
distinction at the source.

Spans cover Dispatch, each claim, and each delivery. A cycle that claims nothing
is not traced: a root span every poll interval is noise.

# Where this package stops

Nine of the eighteen store methods are served over gRPC by webhooks/grpc: the
endpoint CRUD, the subscription CRUD and the delivery log. That is the half of
this package that is a resource rather than a protocol — an operator adds a URL,
picks the event types it wants, rotates its keys and then asks whether a
delivery got through and what came back — and it is the only half a person ever
touches.

Rotation is not a call of its own. Keys travel toward the store on exactly one
message and are required on every [Store.SaveEndpoint], so rotating is saving
the endpoint with a new keyring and a save is always a full re-registration.
They do not travel back: the proto's endpoint message has no field to put them
in, which is what makes "an API that hands out signing keys" unrepresentable
rather than merely unwritten. [Secret] is why that is affordable — the pair is
what lets one subscriber move without the rest of them breaking.

The nine that stay behind each say so on themselves. Seven are the delivery
machinery [Store] groups under "The delivery machinery takes neither" —
[Store.Claim], [Store.MarkDelivered], [Store.RecordFailure],
[Store.RecordAttempt], [Store.Requeue], [Store.Backlog] and [Store.Reap] —
whose correctness is that a claim commits before the request goes out, so a
caller choosing when that commit happens is the one thing they cannot allow.
[Store.EndpointsForEvent] is the internal fan-out, the dispatcher asking itself
who is subscribed. [Store.Enqueue] is the one worth arguing about, and it is
absent for the reason its own documentation gives: it writes a delivery and its
dispatches in the caller's transaction so they commit with whatever else that
transaction did, and over a wire that becomes a delivery for a row that rolled
back, or a committed row nobody was told about. A consumer publishing an event
from another process is describing an RPC of their own, with
[Dispatcher.Dispatch] called inside its transaction.

What is still an application's own is the policy — who is calling, and what each
method requires — and webhooks/grpc's own documentation is where the surface's
rulings are recorded. Receiving somebody else's callbacks is the other case
entirely, and webhooks/inbound does ship an http.Handler for it — the shape of a
Stripe or GitHub payload is Stripe's or GitHub's, and no application has a say in
it. The module README's "Stores and Transports" section is where that
distinction is drawn for the module as a whole.
*/
package webhooks

//go:generate go run ./internal/queriesgen

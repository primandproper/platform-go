/*
Package outbox makes "write a row and publish an event" atomic.

Without it those are two independent operations against two systems that share
no commit: the transaction succeeds, the publish fails, and durable state and
the event stream diverge permanently with nothing to detect it. The outbox
closes that gap by writing the event into the same transaction as the state
change, and moving it to the broker afterwards from a separate loop.

# The seam

database.Client.WithTransaction hands its callback an executor and nothing
else — it cannot commit or roll back. Enqueue takes that executor, so the
event is just another statement in the caller's transaction and lives or dies
with it:

	err := client.WithTransaction(ctx, func(q database.Tx) error {
		if err := insertOrder(ctx, q, order); err != nil {
			return err
		}

		return writer.Enqueue(ctx, q, outbox.Message{Topic: "orders", Payload: order})
	})

There is no way to enqueue outside a transaction by accident, and that is now a
fact about the types rather than about the reader. Enqueue takes a database.Tx,
which only WithTransaction produces — so an enqueue through a Writer does not
compile. Before, the parameter was the same database.SQLQueryExecutor that
Writer returns, and this paragraph was advice.

# Which events belong at the call site, and which belong in a registration

Enqueue guarantees that the events a caller named live or die with the row
change. It has nothing to say about the events the caller did not name, and that
absence is the one gap in this design the outbox does not otherwise close: an
index event a new repository method never enqueued is an event nothing
downstream is waiting for, so the index is simply wrong until the next rebuild
and no instrument separates that from a quiet period.

A side effect registered at construction closes it, because the call site is not
asked:

	writer, err := outbox.NewWriter(client.Dialect(),
	    outbox.WithWriterSideEffect("orders-index", indexEventsFor))

It runs inside every Enqueue on the caller's executor, so rows it writes commit
with the row change and messages it returns are written by the same transaction
as the caller's — a transaction owing three events commits four rows or none. An
error from one aborts the enqueue and comes back to the caller, whose
transaction rolls back with it.

The line between the two is whether a call site could correctly decline. An
event some writes emit and others do not is the call site's, and passing it to
Enqueue is how that choice gets made: a targeted reindex after a manual repair,
a delete event one branch of one method emits. An event that every write to this
outbox owes — the search index event, a webhook dispatch row per subscribed
endpoint, an audit row — is not being chosen by anybody, and leaving it a
parameter means a method that omits it compiles, reviews clean, and is wrong.
Register those.

Side effects see the messages the caller passed and never what another side
effect derived, so registration order fixes what runs when and nothing more.
Registrations are refused at construction — unnamed, duplicated, or nil — rather
than dropped, because a registration that silently vanishes is the forgotten
event one level up. An Enqueue with no messages runs none of them: an effect
derives from what the caller asked for, and a caller that asked for nothing
changed nothing.

The outbox never looks inside a Payload, so what an effect can derive from is
the application's own message type, which the application asserts back out.
searchsync's documentation works the index event through end to end.

# Where the SQL comes from

Nothing in this package composes a statement. The queries live in
outbox/internal/queries as a rendered, committed corpus — six of the nine
statements written out there rather than emitted by database/querygen, for the
reasons that package's comment gives — sqlc checks that corpus against the
schema outbox/migrations renders, on all three dialects, with no database
running, and what the Writer and the Relay execute is the querier
sqlc-gen-unison generated from it, in outbox/internal/outboxdb. A column renamed
in a migration is a failed `make unison` rather than a scan error in production.

One consequence reaches the API above. An Enqueue of several messages executes
one INSERT per message rather than one multi-row statement: a VALUES list whose
length is the batch's makes the statement's text a function of its argument
count, which is exactly what a checked corpus cannot hold. They still run inside
the caller's transaction, so the atomicity the package exists for is unchanged.

The claim mode reaches it too. FOR UPDATE SKIP LOCKED is statement text rather
than a bound value, so the corpus carries the claim twice — once locked, once not
— and a relay picks the method its mode names, the way a paged read picks
between its two directions.

The one line of SQL this package still names is database/dialect's NOTIFY, which
is addressed to a channel rather than to a table and belongs to no schema sqlc
could check it against.

# Creating the table

outbox/migrations renders the DDL for a dialect and table name. If you already
run database/migrate, pass migrations.SQL to WithGeneratedMigration and the
table is created by your normal migration run at a version you choose — no DDL
copied into your repository. Statements returns the same DDL pre-split for
callers using something else.

# Wire compatibility

Payload is marshaled at enqueue with encoding.EncodeJSON and republished as
json.RawMessage, which marshals as its own bytes. The message the broker
receives is therefore byte-identical to what a direct messagequeue.Publish of
the same value would have produced, so adopting the outbox needs no consumer
change.

The encoding is pinned to JSON rather than taken from the caller. Both halves
of the identity above depend on it: the Relay hands the stored bytes to the
publisher inside a json.RawMessage, so bytes in any other encoding would be
spliced verbatim into a JSON message rather than encoded into one. A publisher
built with a non-JSON ClientEncoder would break it from the other side by
double-encoding, so the Relay is JSON-only by contract.

That contract is not enforced at runtime. messagequeue.Publisher takes its
payload as an any and offers no way to ask what encoding it speaks, so the Relay
cannot check the publisher it is handed. It holds today because every publisher in this module —
kafka, redis, sqs, pubsub — constructs its encoder with encoding.ContentTypeJSON.

# Delivery semantics

At-least-once, and it cannot be otherwise: the broker and the database have no
shared commit, so a crash between a successful publish and the row update
redelivers on restart. Consumers must tolerate duplicates. messagequeue.Publisher
does carry a per-message deduplication key, but the Relay does not set one, so
callers who need dedupe still put their own idempotency key inside the payload.

Ordering depends on the claim mode and on Message.Key. ClaimSkipLocked lets
several relays run concurrently and interleave batches, which gives up global
ordering. Per-key ordering survives regardless, because the claim predicate
admits a keyed message only when no older message with that key is still
pending: at most one message per key is ever in flight across the whole fleet,
so its successor cannot be published until it lands. Unkeyed messages skip that
check and claim freely. ClaimLease serializes on a single relay and preserves
global order. Postgres and MySQL support both modes; SQLite has no SKIP LOCKED
and always uses ClaimLease.

All of that holds up to the publish call and not past it. The Relay does not
forward Message.Key as the publisher's ordering key, so what a broker does with
two messages sharing a key is the broker's business, not the outbox's.

# Waking the relay

The relay polls, at PollInterval, which puts a floor under how long a committed
event waits and a floor under how many claim transactions an idle relay runs.
Both floors come off with a wakeup:

	listener, err := pgnotify.NewListener(ctx, &pgnotify.Config{
		ConnectionString: dsn,
		Channel:          "outbox",
	})
	// ...
	go listener.Run()

	relay, err := outbox.NewRelay(ctx, cfg, client, provider,
		outbox.WithRelayWakeup(listener.Signal()))

with the producing half configured to notify that channel, via
WithWriterNotifyChannel or RelayConfig.NotifyChannel. Enqueue then emits a
payload-free pg_notify inside the caller's transaction, which Postgres delivers
at commit — so a woken relay can never look for a row before it is visible.

Nothing about the durable path changes. The notification carries no information,
the poll ticker stays exactly as it was, and a wake that is never delivered
costs latency and nothing else. That last point is not a nicety: NOTIFY is
at-most-once and connection-scoped, so a listener that reconnects misses
everything sent while it was away. It is safe here only because it is a hint
about a table the relay knows how to read for itself.

RelayConfig.MinWakeInterval floors the rate of wake-driven cycles so a table
taking thousands of inserts a second cannot drive thousands of cycles a second;
a burst becomes one extra cycle. Wakeups are Postgres-only on the producing
side, but WithRelayWakeup is a bare channel — the relay stays dialect-generic,
knows nothing about LISTEN, and is tested against a channel rather than a
container.

# Watching it

Nothing here fails loudly — publish errors are logged and retried, never
surfaced — so the instruments are how you learn the outbox has stopped working.
Pass WithRelayMetricsProvider and WithWriterMetricsProvider.

The two that matter most are outbox_backlog_depth and outbox_backlog_age_seconds,
sampled on the reap tick. Every other instrument is a rate or a latency, and none
of them separates "publishing steadily" from "publishing steadily while falling
further behind" — only the age does. A depth of 40,000 is unremarkable if the
oldest message is four seconds old and an incident if it is four hours old.
Quarantined rows are excluded from both, so a permanently broken message does not
read as a permanently growing backlog.

outbox_enqueue_fanout is how the section above is watched rather than trusted.
It records how many messages each Enqueue wrote, sampled once per distinct topic
in that enqueue, so a call site that has stopped emitting its index event shows
up as the data-change topic's distribution shifting down by one. A distribution
is what makes that visible: a rate falling is indistinguishable from a quiet
period, and the events nobody enqueued are precisely the ones no consumer will
report missing.

The rest: outbox_messages_enqueued against outbox_messages_published (the gap is
the rollback rate), outbox_messages_failed, outbox_messages_quarantined — alert on
any increase, since a quarantined message is a dropped event — outbox_claim_errors,
outbox_messages_reaped, and the outbox_publish_latency_ms, outbox_cycle_latency_ms
and outbox_claimed_batch_size distributions. Everything per-message carries a topic
attribute, because one Relay serves every topic and a single broken publisher is
invisible in the total.

Spans cover Enqueue, each claim, each publish, and each reap, and an Enqueue
names the side effects that ran — including one that failed, since a trace that
omits it describes an enqueue that did not happen. A cycle that claims nothing is
not traced: a root span every poll interval is noise.

# Failure

A message that cannot be published has its attempt count incremented and its
next attempt pushed out by exponential backoff. Past MaxAttempts it is
quarantined: skipped by every future claim and counted by
outbox_messages_quarantined. Without that terminal state a single
permanently-failing message blocks the head of the queue forever, which is the
failure this package is most likely to actually meet.

Published rows are marked rather than deleted, so a duplicate or a gap can be
investigated after the fact, and a reaper deletes them once they age past
Retention.
*/
package outbox

//go:generate go run ./internal/queriesgen

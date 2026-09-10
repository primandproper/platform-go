/*
Package searchsync keeps a search index in step with the database it is derived
from.

Write the row, update the index: two systems, no shared commit, and when the
second step fails they diverge permanently with nothing to detect it. That is
the dual-write problem the outbox package closes for messaging, and it is the
same problem here — so this package closes it the same way, by making the index
event part of the transaction that changed the row and applying it afterwards
from a consumer.

The workaround this replaces is polling: walk a sample of rows on a timer and
re-upsert them. It is expensive in proportion to the table rather than to the
change rate, and it is only ever probabilistically correct — a row the sampler
has not reached is a row the index is wrong about, and no amount of running it
turns that into a guarantee. The v10 removal of search/text/indexing is that
sampler.

The application owns what a document looks like. This package owns that the
index converges on it.

# The three parts

	```mermaid
	flowchart LR
	    subgraph txn["one transaction"]
	        row["row change"]
	        event["outbox Event"]
	    end

	    row --- event
	    event --> relay["outbox.Relay"]
	    relay --> pool["jobs.Pool"]
	    pool --> handle["Syncer.Handle"]
	    handle --> fetcher["Fetcher"]
	    fetcher --> target["Target<br/>(text or vector index)"]

	    reindexer["Reindexer<br/>(jobs.Scheduler)"] --> scanner["Scanner"]
	    scanner --> target
	```

Enqueue an Event in the transaction that changed the row. The outbox Relay
publishes it. A jobs.Pool consumes it and hands it to Syncer.Handle, which reads
the document back from the application's Fetcher and writes it to the Target.
Separately, a Reindexer walks the whole source on a schedule and rebuilds.

# Writing the event

	err := client.WithTransaction(ctx, func(q database.Tx) error {
	    if err := updateOrder(ctx, q, order); err != nil {
	        return err
	    }

	    return writer.Enqueue(ctx, q,
	        searchsync.NewEvent(searchsync.OpUpsert, order.ID).Message("orders-index"))
	})

The event lives or dies with the row change, because outbox.Writer.Enqueue takes
the caller's executor and there is no way to hold one outside a transaction.
Event.Message keys the outbox message by document ID, which is what buys
per-document ordering: the outbox admits a keyed message only when no older
message with that key is still pending, so at most one event per document is in
flight across the whole relay fleet, however many relays are running. Events for
different documents stay free to interleave.

# Registering the event rather than writing it

The call above is correct and forgettable. Nothing about a repository method
that writes an order says the write owes an index event, so the next one enqueues
its data-change message alone, compiles, and passes review — and the index is
wrong from then until the next rebuild, with nothing in between able to notice,
because the event that went missing is one no consumer was waiting for.

Register it on the Writer instead and the call site is never asked:

	writer, err := outbox.NewWriter(client.Dialect(),
	    outbox.WithWriterSideEffect("orders-index",
	        func(_ context.Context, _ database.SQLQueryExecutor, msgs []outbox.Message) ([]outbox.Message, error) {
	            events := make([]outbox.Message, 0, len(msgs))

	            for _, msg := range msgs {
	                changed, ok := msg.Payload.(OrderChanged)
	                if !ok {
	                    continue
	                }

	                events = append(events,
	                    searchsync.NewEvent(searchsync.OpUpsert, changed.OrderID).Message("orders-index"))
	            }

	            return events, nil
	        }))

Every transaction that enqueues an order data-change now writes the index event
too, by the same statement, whether or not whoever wrote it was thinking about
the index. The derivation is the application's because the payload is: outbox
never looks inside a Message.Payload, which is also why the dependency runs only
one way — searchsync imports outbox, and outbox knows nothing of searchsync.

Writing the event at the call site stays right where the call site is genuinely
choosing: a targeted re-index after a manual repair, or an OpDelete one branch of
one method emits. outbox's own documentation draws that line in full.

# The event names a document; it does not carry one

Every applied event reads the row back through the Fetcher. That is one source
read per event, and it is what the correctness rests on.

An event that carries the document body has to be applied in the order it was
written, or the index ends up holding an older body than the one it already had.
An event that carries only an ID cannot: whenever it is applied, and however
many times, it indexes the row's current state. Out-of-order delivery converges.
Redelivery converges — and redelivery is normal operation, not an error case,
since the outbox is at-least-once by construction and cannot be otherwise.

The same indirection settles the awkward case honestly. An upsert whose row has
since been deleted finds nothing, and is applied as a delete: the source is what
the index converges toward, and the source says the document is gone. Leaving it
would strand a document in the index that no later event will ever mention
again.

The cost is real — a read per event, where a fat event would have needed none —
and it is the price of not making delivery order load-bearing. It also pays for
itself once: the Fetcher that serves the change feed and the Scanner that serves
the reindex are the same transform from row to document, so there is one
definition of what a document is rather than two that can drift.

# Implementing the two seams

Fetcher and Scanner are two interfaces with a correctness relationship neither
one states: both must produce the same document for the same row, or a reindex
overwrites what the change feed wrote with a differently-shaped copy and the
index holds two generations of schema at once with nothing to detect it.
Implementing them separately, once per entity, makes that relationship invisible
in exactly the places it has to hold.

searchsync/source is the way not to. It builds both from the three functions
they actually differ in — read one row, page over IDs, turn a row into the
indexed subset — and implements the scan in terms of the fetch, so the two
cannot disagree. It also handles the three things a from-scratch pair gets
wrong: a row deleted between the event and its handling is omitted rather than
failing the batch, a page that omission shortened is refilled rather than ending
the walk, and the scanned IDs are checked against the byte ordering below rather
than sorted into looking like it.

An application whose documents come from somewhere other than a repository —
several joined queries, an API, a computed embedding — implements the two
interfaces directly, and owes them that agreement itself.

# Consuming

The Syncer owns no goroutine and reads from no queue. Handle is a jobs.Handler:

	syncer, err := searchsync.NewSyncer("orders", orderSource, target,
	    searchsync.WithSyncerLogger(logger),
	    searchsync.WithSyncerTracerProvider(tracerProvider),
	    searchsync.WithSyncerMetricsProvider(metricsProvider))
	if err != nil {
	    return err
	}

	pool, err := jobs.NewPool(ctx, &jobs.PoolConfig{
	    Topic:       "orders-index",
	    Concurrency: 8,
	}, consumerProvider, syncer.Handle, jobs.WithPoolDeadLetter(deadLetter))
	if err != nil {
	    return err
	}

	go pool.Run()
	defer func() { _ = pool.Close(shutdownCtx) }()

Concurrency, retry with backoff, dead-lettering, panic containment and draining
shutdown are all the Pool's, which does them carefully already. Nothing here
reimplements them.

A payload that will not decode, or an event with no document ID, comes back
wrapped in retry.Unretryable so the Pool dead-letters it immediately rather than
failing the same way three more times while healthy events wait behind it.

# Recording what the index holds

querygen treats last_indexed_at as the column that marks a table as one this
package mirrors: its presence is what makes it emit the reindex scan, and it is
database-owned, so no create or update a caller writes may supply it. Until
there was a writer, that was a column the conventions reserved and nothing
filled in.

WithSyncerStamper is the writer. Give a Syncer a Stamper and it records every
document the index accepted:

	stamps, err := searchsync.NewStampBuffer(func(ctx context.Context, ids []string) error {
	    _, err := queries.MarkOrdersAsIndexed(ctx, db, ids)

	    return err
	}, batching.WithLogger(logger), batching.WithMetricsProvider(metricsProvider))
	if err != nil {
	    return err
	}

	defer func() { _ = stamps.Close(shutdownCtx) }()

	syncer, err := searchsync.NewSyncer("orders", orderSource, target,
	    searchsync.WithSyncerStamper(stamps))

MarkOrdersAsIndexed is querygen's, emitted from the same column list as the scan
that reads the column back — one UPDATE over WHERE id = ANY, which is the shape
the flush is holding.

Accepted is the operative word. A delete stamps nothing, because there is no
document left to have indexed. An upsert whose row has since vanished is applied
as a delete, and stamps nothing either. A failed write stamps nothing. The
column says what the index holds, not what was attempted, which is the only
reading that makes the reindex scan over it mean anything.

The write is buffered, and that is not throughput tuning. One UPDATE per applied
document, issued from all eight workers of a Pool at once, is concurrent
statements taking row locks on the same rows in whatever order each one built
them — Postgres 40P01, holding a pool connection while it deadlocks. A
batching.Buffer collapses the repeats, flushes from a single goroutine on an
interval, and emits in id order: one stamping write in flight, one lock order.
Nothing reads the column back in the same breath, which is what makes a Buffer
right here rather than a GroupCommit. Its own instruments — batching_buffer_* —
are how a failing stamp is seen, since by the time a flush runs there is no
caller left to hand an error to.

The Buffer is the caller's to build and to Close, because it owns a goroutine
and a Syncer does not. NewStampBuffer exists so the one part that is
load-bearing rather than tunable — the id ordering — is not something each
wiring site has to remember.

A Reindexer has no counterpart and should not: it writes every document there
is, so stamping it would make the column a record of when the last rebuild ran,
the same value on every row, rather than of how current each document is.

# Rebuilding

	reindexer, err := searchsync.NewReindexer("orders", orderSource, target,
	    searchsync.WithReindexPruner(indexIDs))
	if err != nil {
	    return err
	}

	if err = scheduler.Register(reindexer.Job(jobs.MustCron("0 4 * * *"), time.Hour)); err != nil {
	    return err
	}

Like retention.Sweeper, the Reindexer owns no ticker: it is registered with a
jobs.Scheduler, whose distributed lock is what makes the rebuild happen once
across a fleet rather than once per replica. Ten replicas each doing a full scan
of the same table is ten times the load on the source at the one moment the
source is already under a full scan.

# What a rebuild can and cannot repair

The upsert half is straightforward: walk the source in batches, write everything.
That covers a bootstrap into an empty index, a mapping change, and any drift on
the write side.

The delete half — removing documents whose rows are gone — needs to know what
the index currently holds, and nothing here can ask. textsearch.Index and
vectorsearch.Index model upsert, delete, and query; enumeration is a different
operation on every backend (Algolia browses, Elasticsearch scrolls with a
point-in-time, pgvector selects) and none of those narrow interfaces model it.
So it comes from outside, as an Enumerator passed to WithReindexPruner.

Without one, a rebuild is upsert-only. That is not a degraded mode with a
caveat, it is the other mode: it is exactly right for a bootstrap and a mapping
change, and it is not drift repair. A Reindexer reports which one it is on every
span.

With one, the rebuild merges two streams ordered by document ID — the source's
and the index's. A source ID the index has not reached is written; an index ID
the source has passed is deleted. One walk of each side, bounded memory, no
generation column.

# Both streams must agree on what ascending means

Scanner.Scan and Enumerator.Scan promise ascending *byte* order, as Go's <
compares strings. Not "sorted" — sorted by that comparison specifically.

This is not pedantry. Postgres's default en_US.UTF-8 collation sorts
case-insensitively and ignores punctuation; byte order does not. If the source
walks in one order and the index walks in another, the merge's second inference
— this index ID is behind the source, so its row must be gone — is false, and
the rebuild deletes documents that are perfectly alive. A keyset walk over
Postgres wants ORDER BY id COLLATE "C".

So the Reindexer checks rather than trusts. Every page is verified strictly
ascending and free of empty IDs before any of it is applied, and a violation
aborts with ErrUnsortedScan. A stream in a locale collation trips it, because
that stream is not in byte order either. The check costs a comparison per
document and buys the one failure in this package that would silently destroy
data.

Nothing enforces it on the change-feed path, which needs no ordering at all —
this is the reindex's constraint alone.

# Two targets, and the embedding

TextTarget adapts a textsearch.IndexManager; VectorTarget adapts a
vectorsearch.IndexWriter. Anything else is two methods.

A vector index needs an embedding, so Document carries one, and a text target
ignores it. Computing it here would mean this package holding an embeddings
client and deciding when to spend on it — which model, on which fields, at what
cost per rebuild — all of which is the application's call. The Source produces
the embedding alongside the body, in the one place that already knows what the
document is.

# Watching it

Nothing here fails loudly. The Pool swallows handler errors by design and the
Scheduler swallows job errors, so the instruments are how you learn the index
has stopped tracking the database. Pass the metrics provider.

search_sync_lag_ms is the one that matters, and it is the reason Event carries
OccurredAt at all: the distance between when a row changed and when the index
agreed. Every other instrument here is a rate or a count, and none of them
separates "applying events steadily" from "applying events steadily while
falling further behind". A p99 of four seconds is a search index; a p99 of four
hours is a search index nobody should be reading. Pair it with the outbox's
outbox_backlog_age_seconds, which answers the same question one hop upstream —
between them, a lag that is climbing is attributable to the relay or to this
consumer rather than to "search is slow".

It is measured across a process boundary, so it inherits whatever clock
agreement the fleet has. A consumer whose clock runs behind the writer's records
zero rather than a negative lag, and an event with no OccurredAt records nothing
rather than a lag measured from the epoch.

The rest: search_sync_events_applied against search_sync_events_failed, each
carrying the index name and the op; search_sync_documents_vanished, which counts
upserts that turned into deletes because the row was already gone — a small
steady rate is ordinary, a spike is a bulk delete working its way through;
search_sync_apply_latency_ms; and for rebuilds
search_sync_reindex_documents, search_sync_reindex_pruned,
search_sync_reindex_batches, search_sync_reindex_failures and
search_sync_reindex_latency_ms.

Spans cover each applied event and each rebuild, carrying the document ID, the
op, the lag, and — on a rebuild — what it scanned, wrote and pruned.

# There is no config subpackage

Every other assembly seam in this module has one because there is something to
select from the environment: a provider, a connection string, a credential.
There is nothing of the kind here. The Source and the Target are application
code, the index name is a constant in that code, and the only number worth
tuning is the reindex batch size.

What does come from the environment already has a home: the topic, concurrency
and retry policy belong to jobscfg.PoolConfig, the search backend to
textsearchcfg or vectorsearchcfg, and the outbox to outboxcfg. A config package
here would only wrap those and add a fourth name for the same knobs.

# Non-goals

No index schema management: creating an index, its mappings, its dimension and
distance metric are the backends' construction-time concerns, and they disagree
about all of them.

No fan-out. One Syncer serves one index from one topic. An application indexing
five kinds of document runs five, which costs five registrations and buys five
independent lag readings, five retry budgets, and five failure domains — where a
single multiplexed consumer with a type discriminator buys one of each and makes
the slowest index everyone's problem.
*/
package searchsync

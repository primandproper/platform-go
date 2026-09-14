/*
Package workqueue is a leased work queue over Postgres: the
SELECT … FOR UPDATE SKIP LOCKED claim/complete/expire pattern, generic over the
key that names a unit of work.

distributedlock scopes job queues out, and rightly — a lock is not a queue. This
is the queue. It is the piece every distributed-systems consumer writes next, and
the piece they usually write badly, because the two things that make it survive
production are not the parts that look hard.

# What it is, and is not

An item is a key and nothing else. There is no payload column: the consumer
already knows how to turn a key into work, and a queue that also stores the work
has to answer questions about encoding, size, and schema evolution that a key
does not raise. If you want to move payloads to a broker, that is outbox.

Scheduling policy stays with the consumer too. This package knows how to hand out
leases fairly and take them back when they lapse; it does not know what "stale"
means for your domain, or which work is urgent. You express both by enqueueing:
Entry.Delay says "not before", Entry.Priority says "ahead of the rest".

# The clock

The database's now() is the only clock. Every timestamp that governs scheduling —
lease expiry, availability, completion, retention — is written and compared
server-side, and no timestamp is ever bound from or returned to a caller's
process. Durations cross the seam instead: a lease is "this many microseconds
from now()", an age comes back as a duration measured against now().

That is why this package has no clock.Clock option, alone among the platform's
scheduling components. Process clocks never have to agree, which is the whole
reason a fleet can coordinate through one table.

# Failure recovery is expiry; exclusivity is a name

There are no heartbeats. A worker that dies simply lets its lease lapse, and the
item is handed to somebody else. Nothing detects the death; nothing has to.

The price of that simplicity is that work must be idempotent. Two workers can
briefly hold the same key — a lease lapses while its holder is merely slow, not
dead — so the item gets done twice. Item.Reclaimed marks a claim that took over a
lapsed lease, so that window is visible.

What is not a price is the item being recorded wrong. A claim stamps a name on
every row it takes and hands it back on Item.LeasedBy, and Complete and Release
address an item by its key and that name together. So the straggler's late
Complete matches nothing instead of retiring an item the second worker is still
working on — which would have excluded that worker's own Release as
already-completed and left the item done with the work never performed. Its late
Release matches nothing for the same reason, rather than dropping a lease
somebody is holding and putting the item in front of a third worker.

The name is the fence, not the lease's liveness, and the difference shows up in
one case: a worker whose lease lapsed with nobody else claiming still holds the
item, and its Complete still lands. Refusing it would only mean doing the work
again. Matching nothing is not an error — an item the queue never held, one an
operator removed, and one somebody else now holds are the same answer to a caller
— so what a short match gets is a counter and a log line.

One consequence reaches a caller: Complete and Release report on a claim, so
they take the Items that Claim handed out. There is no way to retire an item
without having held it; Remove is how a queue drops work nobody claimed.

# The two details that cost real incidents

Both of these came out of a production system running this pattern, and neither
is obvious until it bites. They are the reason this package exists rather than
the twenty lines of SQL underneath it.

*Every writer takes its row locks in primary-key order.* Enqueue binds its rows
in key order and the statement orders them again, and Complete, Release, and
Remove reach their rows through a CTE that orders and locks them explicitly. With
one total order, contention between concurrent batch writers degrades into a
queue; without it, two batches that overlap in opposite orders deadlock
(SQLSTATE 40P01) the moment they meet. Claim is exempt and safe: SKIP LOCKED
never waits, and a writer that never waits cannot be in a lock cycle.

*Enqueue group-commits.* One statement per caller does not survive contact with a
read path. Every in-flight Enqueue on a process is merged into a single upsert,
so however many callers are enqueueing, exactly one statement is ever in flight —
and overlapping key sets collapse into one row apiece instead of contending for
the same row. Callers still block until their own keys have landed, so
read-your-write holds: enqueue, then claim, and the key you enqueued is there.
The unmerged version of this once wedged a service by parking thirty pool
connections in deadlocking upserts, starving every unrelated endpoint of a
connection.

The batcher owns a goroutine, which is why a Queue has to be Closed.

# Driving it

Claim, work, Complete. Release hands work back early with a delay and a reason;
otherwise the lease lapses on its own and the item returns anyway.

	items, err := queue.Claim(ctx, 100, 30*time.Second)
	// ...
	for _, item := range items {
	    if err := do(ctx, item.Key); err != nil {
	        _ = queue.Release(ctx, time.Minute, err, item)

	        continue
	    }

	    done = append(done, item)
	}

	err = queue.Complete(ctx, done...)

Hand back the Item rather than its key. An item is addressed by its key and the
claim holding it together, and passing the value Claim gave you is what applies
that fence without anybody having to think about it.

A claim's limit counts the items it actually leased, not the rows it looked at:
Postgres applies the LIMIT above the lock, so rows a concurrent claimer holds are
skipped and replaced rather than subtracted. A fleet of claimers all get full
batches while work remains, and a short batch means the queue really is nearly
drained. That depends on the shape of the claim statement — a LIMIT pushed into a
subquery below the lock would silently start returning short batches — so there
is a test pinning it.

The loop around that is yours, and Wait is the only part of it this package
supplies: it blocks until a wakeup arrives, until the poll elapses, or until the
context is done. Given a wakeup it turns an idle worker from one claim query per
tick into none, and turns the latency of a fresh enqueue from a poll interval
into a millisecond:

	listener, err := pgnotify.NewListener(ctx, &pgnotify.Config{
		ConnectionString: dsn,
		Channel:          "work",
	})
	// ...
	go listener.Run()

	queue, err := workqueue.New[string](ctx, cfg, client,
		workqueue.WithWakeup(listener.Signal()))

with Config.NotifyChannel set to the same channel on whatever enqueues, so
Enqueue emits a payload-free pg_notify once the rows have landed.

None of the queue's guarantees rest on that. The notification carries no
information, the poll stays exactly as it was, and a wake that is never
delivered costs latency and nothing else — which matters, because NOTIFY is
at-most-once and connection-scoped, so a reconnecting listener misses everything
sent while it was away. Config.MinWakeInterval floors how often a wake can
return, so a burst of enqueues costs one extra claim rather than one per
enqueue.

Reap and Stats are methods rather than a loop this package runs, because you
already have a scheduler — see the jobs package. Reap deletes completed items
past their retention; Stats is the health read. Nothing here fails loudly, so
Stats.OldestReadyAge is the number that tells you the fleet has stopped draining:
depth alone cannot distinguish a queue that is deep and moving from one that is
deep and stuck.

# Keys

K is comparable, which is most of what makes an encoding safe: maps and slices
are already excluded, so the JSON rendering of a struct key is stable across
processes and releases as long as its field order is. Strings and string-like
types are stored as themselves rather than JSON-quoted, so the table stays
legible. Anything else — a key that has to sort a particular way, or that already
has a canonical string form — supplies WithKeyCodec.

The encoded key is the table's primary key and is bounded by MaxKeyLength; an
over-long key is rejected at Enqueue rather than silently truncated.

# Where the SQL comes from

Nothing in this package composes a statement. The queries live in
workqueue/internal/queries as a rendered, committed corpus — written out there
rather than emitted by database/querygen, for the reason that package's comment
gives — sqlc checks that corpus against the schema workqueue/migrations renders
with no database running, and what the queue executes is the querier
sqlc-gen-unison generated from it, in workqueue/internal/workqueuedb. A column
renamed in a migration is a failed `make unison` rather than a scan error in
production.

A batch reaches those statements as one bound array per column rather than as a
tuple or a placeholder run, so the text of a statement does not depend on how
many items are in the call. Enqueue splits its merged batch into three parallel
arrays — key, priority, delay — Complete and Release bind two, the key and the
claim holding it, and Remove binds one. All of them are in primary-key order,
which is where the lock-ordering discipline above is applied.

# Creating the table

workqueue/migrations renders the DDL for a table prefix. If you already run
database/migrate, hand migrations.SQL to WithGeneratedMigration and the table is
created by your normal migration run at a version you choose.

One table serves any number of logical queues: Config.Name partitions it, and is
the leading column of the primary key. Two Queue values with different names
share nothing but storage.

# Postgres only

Deliberately. The contract above is "the database's now() is the only clock, and
SKIP LOCKED is the arbiter", and the SQL that delivers it — a lock-ordering CTE,
a single-statement claim with RETURNING, interval arithmetic on the server — is
written against Postgres rather than reduced to a portable subset.

SKIP LOCKED is not the part that binds. MySQL 8.0 has it, and CTEs too; what it
has no form of is RETURNING. The claim is one statement that selects due rows,
locks them, increments attempts, extends the lease, and hands back the keys, and
without RETURNING those become a SELECT … FOR UPDATE SKIP LOCKED and a separate
UPDATE inside a transaction held across both round trips. That is a different
concurrency shape with a different failure model — a second implementation
rather than a dialect switch. SQLite is a harder no: it is single-writer, with no
row-level locking to skip.

So New returns dialect.ErrUnsupported for anything but Postgres, rather than
degrading to a lease-only claim that would look like it worked. If a second
backend is ever wanted, the shape to reach for is this package as the interface
with a workqueue/postgres beneath it, the way cache and cache/redis sit — nothing
here forecloses that.

The corpus reflects that decision rather than working around it. There is no
MySQL rendering to reconcile, so the RETURNING split a portable corpus would
have owed is not a shape this package has: sqlc's Postgres engine parses the
lock-ordering CTE, the SKIP LOCKED claim, the interval arithmetic and the
multi-column RETURNING exactly as they are written.

This is not the only place that answer is kept. The module README's
"SQL Dialect Support" section carries the matrix for every package in this
module that stores anything through database, so a consumer choosing between
Postgres and MySQL reads one table rather than discovering this package's answer
here after having already chosen it. That table is generated rather than typed:
internal/cmd/readmegen emits it from the DDL and the queriers each package
actually ships, and reads the narrowing directive below for the reason this one
is short.
*/
package workqueue

//platform:narrowing the claim is the package: `SKIP LOCKED` to take due rows and `RETURNING` to hand the keys back, in one round trip

//go:generate go run ./internal/queriesgen

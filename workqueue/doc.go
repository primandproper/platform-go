/*
Package workqueue is a leased work queue over a SQL table: the
SELECT … FOR UPDATE SKIP LOCKED claim/complete/expire pattern, generic over the
key that names a unit of work, on Postgres, MySQL and SQLite.

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

The server's clock is read at the finest grain it has, which is not the same
grain everywhere. Postgres and MySQL read microseconds. SQLite reads
milliseconds — strftime's %f is the only clock it has finer than a second — so
there every duration is rounded to a millisecond, and the direction is chosen
per duration rather than left to the arithmetic. A lease is rounded up and
padded by one millisecond more, because SQLite truncates its own now before
anything is added to it, and a lease that ended even a millisecond early is a
lease somebody else takes over while its holder is still working. A delay and a
retention window are rounded up without the pad: an item due a millisecond
early, or reaped a millisecond late, is merely early or late. The rounding is
pinned by a test that measures every lease against a clock read taken before
the claim, on all three engines.

# Failure recovery is expiry; exclusivity is a name

Nothing detects a worker's death, and nothing has to: a worker that dies stops
extending its leases, they lapse, and the items are handed to somebody else.

What a worker still running has is Extend, which pushes its claims' horizons out
for as long as it is working — Runner calls it on a timer, so a consumer using
that loop gets it without asking. The lease is therefore sized for a missed
heartbeat rather than for the slowest item the queue will ever hold, which is
what lets it stay short: a short lease is how quickly a dead worker's items come
back.

The price that remains is that work must be idempotent. Two workers can briefly
hold the same key — a worker that is paused or partitioned stops extending
without knowing it has, and its lease lapses while it is merely slow rather than
dead — so the item gets done twice. Item.Reclaimed marks a claim that took over
a lapsed lease, so that window is visible.

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

One consequence reaches a caller: Complete, Release and Extend report on a
claim, so they take the Items that Claim handed out. There is no way to retire
an item, or to keep hold of one, without having held it; Remove is how a queue
drops work nobody claimed.

# The two details that cost real incidents

Both of these came out of a production system running this pattern, and neither
is obvious until it bites. They are the reason this package exists rather than
the twenty lines of SQL underneath it.

*Every writer takes its row locks in primary-key order.* Enqueue binds its rows
in key order and the statement orders them again, and Complete, Release, Extend
and Remove reach their rows through a CTE that orders and locks them explicitly
— on MySQL, through the ORDER BY a single-table UPDATE or DELETE takes, which
acquires its locks in that order. With one total order, contention between
concurrent batch writers degrades into a queue; without it, two batches that
overlap in opposite orders deadlock (SQLSTATE 40P01) the moment they meet.
Claim is exempt and safe: SKIP LOCKED never waits, and a writer that never
waits cannot be in a lock cycle. SQLite has one writer, so there is no second
party to a cycle there at all.

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

# Enqueue is not in your transaction

The group commit above is also the reason an enqueue cannot join the caller's
database work. The batch belongs to the process, so its upsert commits on the
batcher's connection at an instant none of its waiters picked; there is no
Enqueue taking a database.Tx, and a batch shared across callers could not be
handed to one of their transactions anyway.

That is a position rather than an oversight, and it has consequences in both
directions. A key enqueued inside client.WithTransaction survives that
transaction's rollback, so a worker can claim a name whose row was never
committed — or claim it before the commit, which looks the same to the worker.
An enqueue that fails after the row committed loses the other half: durable
state with nothing coming for it, which no amount of retrying the enqueue will
notice once the process that was doing it is gone.

Two ways to close each. Enqueue after the transaction commits and add a sweep
over the rows that should have been enqueued — operations.Recover is that sweep,
re-offering operations whose process died between the two writes. Or write the
intent into the transaction and let something else enqueue from it: that is what
outbox is for, and it is the one to reach for when the work must not be lost,
because there the message lives or dies with the row and the enqueue is retried
until it lands.

Neither removes the last obligation, and it is the one to design for rather than
to defend against: a worker must tolerate a key whose subject does not exist. The
queue stores a name, the thing named lives in a table this write neither waits
for nor rolls back with, and a claim that finds nothing behind the key is an
ordinary outcome — Complete it. operations.Worker does exactly that on
ErrOperationNotFound, and completing rather than failing is what keeps the item
from being claimed again on the next pass to reach the same conclusion.

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
Postgres and MySQL apply the LIMIT above the lock, so rows a concurrent claimer
holds are skipped and replaced rather than subtracted. A fleet of claimers all get full
batches while work remains, and a short batch means the queue really is nearly
drained. That depends on the shape of the claim statement — a LIMIT pushed into a
subquery below the lock would silently start returning short batches, and on
MySQL a read that sorted rather than walking the claim index would lock every
candidate before the limit applied — so there is a test pinning it on both.

The loop around that is Runner's, or yours. Runner is the one every consumer was
writing — claim, work, batched complete, batched release with cause, until the
queue is empty or the context is done — with the two parts of it that are easy
to get wrong built in: the leases on running handlers are extended while they
run, and a shutdown drains the batch it claimed rather than abandoning it. The
handler is the only seam:

	runner, err := workqueue.NewRunner(ctx, &workqueue.RunnerConfig{}, queue,
	    func(ctx context.Context, item workqueue.Item[string]) error {
	        return do(ctx, item.Key)
	    })
	// ...
	err = runner.Run(ctx) // blocks until ctx is done

Wait is what that loop sleeps on, and what a hand-written one should sleep on:
it blocks until a wakeup arrives, until the poll elapses, or until the context is
done. Given a wakeup it turns an idle worker from one claim query per
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
Enqueue emits a payload-free pg_notify once the rows have landed. That half is
Postgres's alone — MySQL and SQLite have no NOTIFY, and New refuses a channel on
either with ErrNotifyUnsupported rather than dropping it — so on those two a
worker runs on its poll, which is what Wait was always going to fall back to.

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

Requeue is the operator's write. Under Config.MaxAttempts an item that keeps
failing eventually stalls: it is excluded from every claim and left in the table,
counted by Stats.Stalled, so that somebody can see what it died of. Requeue is
how it comes back once the cause is fixed — it zeroes the attempt counter on the
keys you name and makes them claimable now, keeping the priority, the original
enqueue time and the last error, and reports how many it actually revived.
Enqueue will not do it: a re-enqueue of an outstanding item merges with what is
there rather than restarting it, which is what keeps the ceiling a ceiling.

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
sqlc-gen-unison generated from it. A column renamed in a migration is a failed
`make unison` rather than a scan error in production.

There are two queriers, because there are two statement sets:
workqueue/internal/workqueuedb for Postgres and
workqueue/internal/workqueuesplitdb for MySQL and SQLite. unison converges a
query onto one shape across the dialects it generates for and refuses one whose
shape differs, and these differ in shape rather than in spelling — see the
per-dialect section below.

On Postgres a batch reaches its statement as one bound array per column rather
than as a tuple or a placeholder run, so the text of a statement does not depend
on how many items are in the call. Enqueue splits its merged batch into three
parallel arrays — key, priority, delay — Complete, Release and Extend bind two,
the key and the claim holding it, and Remove and Requeue bind one. MySQL and
SQLite have no array type, and bind a set as an IN list that sqlc expands per
call, which carries one column: so there Remove and Requeue bind their keys,
Complete, Release and Extend bind the claim's name once and its keys as the
list, and Enqueue is a statement per row. All of them are in primary-key order,
which is where the lock-ordering discipline above is applied.

# Creating the table

workqueue/migrations renders the DDL for a table prefix. If you already run
database/migrate, hand migrations.SQL to WithGeneratedMigration and the table is
created by your normal migration run at a version you choose.

One table serves any number of logical queues: Config.Name partitions it, and is
the leading column of the primary key. Two Queue values with different names
share nothing but storage.

# Three dialects, and what each costs

Postgres, MySQL and SQLite all run the whole contract above — the fence, the
merge rule, the lease that only moves forward, the full batch, the one clock —
and one suite of tests runs against all three. What differs is how many round
trips each operation takes, and that difference is stated here rather than
discovered.

On Postgres every operation is one statement. The claim selects due rows, locks
them, increments their attempts, stamps the lease and the claim's name, and
hands the rows back through RETURNING, so nothing is ever selected without
also being leased. A batch of any size is one statement, because it is bound as
arrays.

MySQL has SKIP LOCKED and no RETURNING; SQLite has neither, and no row locks at
all. On both the claim is three statements held in one transaction: a read that
selects the due rows (and on MySQL locks them, skipping what another claimer
holds), an update that leases them — repeating every test the read made, so it
takes nothing the rows stopped being — and a read-back by the name the lease
stamped. The transaction is what makes that one claim: the read's locks last
until it commits, so no other claimer can select those rows in between, and on
SQLite the transaction is the only writer. The price is a transaction held
across three round trips per claim, and a claimer that dies between them rolls
the whole claim back rather than leaving half of one.

The rest of the costs follow from there being no arrays to bind:

  - Enqueue is a statement per item in the merged batch, in one transaction,
    rather than one statement for the batch. The group commit still merges every
    caller on a process into that one transaction.
  - Complete, Release and Extend are a statement per distinct claim the call
    names — which is one statement when the call hands back what one Claim
    handed out, and it almost always does. Extend adds a count in the same
    transaction, because MySQL reports rows changed rather than matched and an
    extension that lands under a longer lease changes nothing.
  - Reap is two statements in a transaction, a locking read and a delete, for
    the claim's reason.

SQLite's single writer also means its claimers take turns rather than running
side by side. That is SQLite's answer to concurrency rather than this package's,
and the queue is correct under it; it is simply not a fleet.

The module README's "SQL Dialect Support" section carries the matrix for every
package in this module that stores anything through database, and it is
generated rather than typed: internal/cmd/readmegen emits it from the DDL each
package ships.
*/
package workqueue

//go:generate go run ./internal/queriesgen

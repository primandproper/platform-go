/*
Package operations runs work that outlives the request that asked for it, and
gives the client something to watch while it does.

An export, a bulk import, a reindex: the handler cannot finish these inside a
request, so it accepts them, hands back an ID, and the client comes looking
later. Every service invents that contract, and most invent it without
durability — the job dies with the process — or without progress, which is a
spinner backed by nothing.

The pattern is Google's long-running operation, with one deliberate departure
noted under "Progress" below. A row is the operation: it says what was asked
for, how far along it is, and how it ended. Everything else here — the queue, the
worker, the watcher, the HTTP surface — exists to move that row and to read it.

# The shape

	registry := operations.NewRegistry()

	err := operations.Register(registry, operations.Definition[ExportRequest]{
		Kind:       "dataprivacy.export",
		CountLabel: "records",
		Run: func(ctx context.Context, req ExportRequest, rep operations.Reporter) (*operations.Result, error) {
			domains := collectors.Names()
			rep.SetUnits(len(domains))

			for _, domain := range domains {
				select {
				case <-rep.Cancelled():
					return nil, operations.Unretryable(operations.Fail("cancelled", "stopped after %d domains", done))
				default:
				}

				rep.StartUnit(domain)

				for batch := range collect(ctx, domain, req.SubjectID) {
					rep.Advance(int64(len(batch)))
				}

				rep.FinishUnit()
			}

			return &operations.Result{URI: key}, nil
		},
	})

A handler starts one and returns immediately:

	op, err := svc.Start(ctx, "dataprivacy.export", req, operations.WithOwner(tenancy.Of(userID)))
	// ... 202, with op.ID

A worker runs it, and a watcher streams it. Both are ordinary background loops;
see Worker and Watcher.

# Whose operation it is

Every operation belongs to a tenancy.Scope, and every read a consumer can make
binds it: Store.Get, Store.GetMany and Store.List each take one, Service.Get and
Service.List pass it through, and Watcher.Watch establishes a subscription under
it. There is no read here that omits it, because what an operation holds is the
status of somebody's export, and the caller who reaches for an unscoped read is
the caller who has not thought about which somebody.

The consequence is that a row belonging to another tenant is a row the query does
not return — reported as ErrOperationNotFound, the same answer an operation that
does not exist gets. That used to be a comparison in operations/http after the
read. It is gone: one surface remembering to make it is one surface, and a
consumer's own handler is the one that would not have.

An operation started without WithOwner belongs to tenancy.Global(), which is a
scope like any other and matches only itself. A single-tenant deployment names no
owner anywhere, pairs it with operations/http.GlobalOwner, and behaves exactly as
it did before the dimension existed.

Seven Store methods take no scope, and each says why on itself. They are the
worker's own: the claim, the flush, the two outcomes, the cancellation write, the
recovery sweep and the reap. A sweep bounded by tenant would leave every other
tenant's operations stranded, which is the shape of the carve-out rather than an
exception somebody wanted.

# The row is the only source of truth

State, progress, and outcome live in one row, and every read path — the poll, the
subscription, an operator with psql — reads that row. Nothing is cached in a
process, broadcast between processes, or held in a channel that a restart loses.

That is what makes the fleet uniform. Any replica can serve a status request or a
subscription for any operation, because any replica can read the row; there is no
affinity to arrange, no sticky sessions, and no fan-out bus to run. It is also
what makes the guarantees testable: there is one place to look.

# Two writes, and the gap between them

Start does two things — insert the row, then enqueue the ID on workqueue — and
they cannot be one transaction. The queue's Enqueue merges every in-flight call
on the process into a single upsert, which is what makes it cheap enough to call
from a handler, and a batch shared between callers cannot join any one caller's
transaction.

So the row lands first and the enqueue follows. A process that dies in between
leaves an operation that is recorded, readable, pending, and queued nowhere.

This is stated rather than hidden because the fix is a thing you have to run.
Service.Recover finds operations that have been pending longer than
Config.RecoverAfter — and running ones whose lease lapsed — and re-offers them.
It belongs on the jobs scheduler beside Reap:

	scheduler.Register(jobs.NewJob("operations-recover", jobs.MustCron("* * * * *"), func(ctx context.Context) error {
		_, err := svc.Recover(ctx)

		return err
	}))

Re-enqueueing something already queued is harmless — the upsert merges on the
key, and a worker that claims an operation somebody else is running is refused by
the guarded transition below — so the sweep does not have to be clever about
whether an operation is really lost. It could not be: the queue and the row are
two tables, and no read spans both consistently.

A deployment that runs the worker and not the recovery sweep will strand an
operation every time a process dies at the wrong moment. That is the failure this
design is most anxious about, which is why the sweep is a named method that
returns a count and logs what it did, rather than a flag.

# The lease is the row, and progress extends it

Two leases are involved and only one of them decides anything.

The work queue leases the *key*: it says which worker was handed the dispatch.
That lease is fixed at claim time and cannot be extended, which makes it useless
as a bound on work whose whole premise is that its length is unknown.

The operation row's claimed_until is the real lease, and Store.Begin is the
guarded transition that hands it out — pending, or running-with-a-lapsed-lease,
becomes running under a new lease, in one conditional UPDATE. Exactly one worker
matches. A queue lease that lapses early therefore costs a wasted claim and a
refused transition, not a second execution.

What makes that lease fit long work is that every progress flush extends it. The
flush is one statement that writes where the Runner has got to, pushes
claimed_until out, and returns whether a cancellation has been requested — so a
Runner that reports progress is, by that fact alone, holding its lease and
observing cancellations, with nothing extra to call and no second round trip.

The corollary is worth being blunt about: a Runner that reports no progress at
all is bounded by WorkerConfig.Lease and nothing else, and will be reclaimed and
run a second time if it takes longer. It also cannot be cancelled, because
nothing is asking. Both are fixed by the same thing.

# Progress, in two tiers, neither required

Work that fans out over a known set of units — dataprivacy's registered data
domains, a reindex's shards — has a free denominator, and "3 of 9 domains
complete" is the answer people want. Work inside a unit usually cannot say how
much there is without a counting pass first, which is a second full scan run to
make a progress bar prettier. So:

  - The outer tier is units: SetUnits declares the denominator, StartUnit and
    FinishUnit move the numerator. A Runner that never calls SetUnits reports no
    denominator, and Progress.Fraction says so rather than dividing into a bar
    that sits at 100% from the first tick.

  - The inner tier is a monotonic count with no total: Advance(n), rendered with
    the noun the kind registered. "4,300 records collected." A flow that fetches
    everything without counting first ports as it stands.

The count does not reset at a unit boundary, which is the one place the reading
of "within a unit" is settled by decision rather than by wording. It is a
spinner's number, and a client that was showing 4,300 suddenly showing 12 reads
as a fault rather than as progress; the per-unit structure is already carried by
the tier above.

Everything on Reporter is buffered and in-memory, so Advance in a tight loop is
an integer add. The buffer is flushed on WorkerConfig.ProgressInterval, at every
unit boundary, and once more when the Runner returns. Nothing on Reporter returns
an error, because progress is advisory: an update that does not land costs a
watching client a couple of seconds and costs the work nothing, and a Runner
forced to handle that error would ignore it.

# Watching: snapshots, not deltas

Watcher.Watch hands back a channel of Operation values, ending with the terminal
one, and every value on it is the whole operation as the row stood when it was
read.

That single decision is what makes the rest cheap. A slow subscriber does not
need the states it missed, because the newest snapshot says everything they would
have said — so the channel holds one value, latest wins, and nothing has to be
buffered or replayed. A delta stream would have had to guarantee delivery of
every step, which over a connection that can drop means sequence numbers, a
replay buffer, and a retention policy for it.

Underneath, a payload-free pg_notify on every write wakes the loop, which
re-reads every subscribed operation in one statement and compares revisions. One
query per wake, however many subscribers. The notification carries nothing and
nothing depends on it arriving — see database/postgres/pgnotify on why that is
the only safe way to use LISTEN/NOTIFY — so a watcher with no wakeup wired polls
at WatcherConfig.Poll and is exactly as correct, just later.

	listener, err := pgnotify.NewListener(ctx, &pgnotify.Config{
		ConnectionString: dsn,
		Channel:          "operations",
	})
	// ...
	go listener.Run()

	watcher, err := operations.NewWatcher(ctx, cfg, store,
		operations.WithWatcherWakeup(listener.Signal()))
	// ...
	go watcher.Run(ctx)

with operations.WithStoreNotifyChannel("operations") on the writing side.

# Cancellation is a request, not a kill

Cancel sets a flag. An operation that has not started is cancelled outright,
because nothing has begun and there is nothing to unwind. A running one keeps
running until its Runner notices, through Reporter.Cancelled, and stops at a
point it can describe — between units, not between two halves of a write. Only
the Runner knows what a half-finished unit of its work has left behind.

A Runner that never consults Cancelled runs to completion and the operation
succeeds. That is the honest outcome: the work was in fact done.

Cancellation beats both success and failure when it is recorded, and that is
deliberate. A Runner that stopped early because it was asked to may return
cleanly or may return an error, and recording either at face value would report a
partial export as complete, or report as failed something where nothing went
wrong. StateCancelled is also kept distinct from StateFailed for the same reason
in reverse: a dashboard that counts cancellations beside genuine failures reports
an error rate that is a measure of user behavior.

# Every operation reaches a terminal state

This is the promise, and every other decision defers to it.

An operation whose Runner errors is retried until WorkerConfig.MaxAttempts (or
the kind's own) is spent, then failed with CodeAttemptsExhausted carrying the
last symptom. An operation whose kind no build registers is failed at once with
CodeUnknownKind, rather than retried against a name nothing will ever answer to.
A Runner that panics has it contained, and fails that operation rather than the
batch. A worker that dies has its lease lapse, and the operation comes back.

It is why MaxAttempts cannot be unlimited here, unlike in a work queue: unlimited
is precisely the case where an operation never terminates, and a client polling
something that will be retried forever is worse served than one told it failed.

# What the client is obliged to understand

One field: done. False while the operation may still change, true once it will
not. Everything else — the progress tiers, the result pointer, the structured
error — is there to be used and safe to ignore, which is the property that lets a
client written against one kind of operation work against every other.

# Duplicate execution is possible, and Runners must be idempotent

A lease lapses while its holder is merely slow, not dead, and the operation is
handed to somebody else; both run. That is inherent to lease-based recovery and
this package does not pretend otherwise — the alternative is fencing tokens and
heartbeats, and the same trade-off is discussed at more length in workqueue.

The cost is bounded and the tools are there. Reporter.Attempt hands the Runner
the operation ID — stable across attempts, and so a natural idempotency key —
along with which attempt this is and whether it is the last one. And
operations_worker_leases_lost counts the times a Runner was still working when
the row was taken away, which is the number that says the lease is mis-sized
rather than the work being unlucky.

Attempt.Final is there for work that owes somebody an answer either way. The
worker records a permanent failure on the row and stops; it has no notion of who
was waiting. A Runner that has to send that person a message — dataprivacy's
statutory deadlines are the case this was added for — needs to know which attempt
is the last one, and cannot work it out: the ceiling is WorkerConfig.MaxAttempts
unless its own Definition overrode it, and neither is visible from inside Run.

# Postgres only

Deliberately, and the reason is one rather than the three it looks like from the
statements.

The queue underneath is workqueue, which is Postgres-only for its own reasons —
the single-statement RETURNING claim is its concurrency contract, MySQL's
split is a different failure model, and SQLite has no row locks to skip. This
package inherits whatever workqueue supports, so a multi-dialect operations is a
decision about workqueue rather than about anything here.

The other two only look like constraints. The guarded transitions are UPDATE …
RETURNING, which is legal because this package renders a single-dialect corpus
and a roster of one cannot diverge — see operations/internal/queries. And the
watch path's push half is LISTEN/NOTIFY, which is an optimization over a poll
that is documented above and always available: a watcher with no wakeup wired is
exactly as correct, just later.

So the constructors return dialect.ErrUnsupported for anything else, rather than
degrading to something that looks like it worked.

The module README's "SQL Dialect Support" section is where that narrowing is
spoken module-wide, beside the roster of every other package that stores
anything through database — the table to read before choosing a dialect, rather
than after choosing this package.

# Where the SQL comes from

Nothing in this package composes a statement. The queries live in
operations/internal/queries as a rendered, committed corpus — some of it emitted
by database/querygen, the rest written out there for the reason that file gives
— sqlc checks that corpus against the schema operations/migrations renders with
no database running, and what the store executes is the querier sqlc-gen-unison
generated from it, in operations/internal/operationsdb. A column renamed in a
migration is a failed `make unison` rather than a scan error in production.

# Creating the table

operations/migrations renders the DDL for a table prefix. If you already run
database/migrate, hand migrations.SQL to WithGeneratedMigration and the table is
created by your normal migration run at a version you choose.
*/
package operations

//platform:narrowing runs on `workqueue`, so its roster is `workqueue`'s

//go:generate go run ./internal/queriesgen

/*
Package timers is durable one-shot scheduling over Postgres: run this once at
instant T, exactly once across the fleet, surviving restarts.

jobs covers periodic work — cron across a fleet — and workqueue covers work that
should happen as soon as somebody is free. Neither covers the third thing every
product needs: a trial that expires on the 14th, an email that goes out in three
days, a reminder, an escalation, a hold that releases at close of business.

# Why not a delayed function call

The tempting shortcut is time.AfterFunc, or a delayed-write channel fed by a
notification. Both work in a demo and neither is a scheduler, because both keep
the schedule in memory: the process restarts and every pending trial expiry is
gone, with nothing to reconcile against and no error anywhere to say so. That is
not an edge case — a deploy is a restart, and a service deploys more often than
a fourteen-day trial expires.

So the schedule is a row before Schedule returns. A notification is only ever
the news that a row exists; losing one costs latency and nothing else, which is
what makes it safe to build on at all.

# The clock, and the one place a caller's clock counts

A timer names an absolute instant. That is the whole distinction from a work
queue, which takes an offset from the database's now() precisely so that no
caller's clock enters the schedule — an offset is anchored to whichever process
evaluated it, and "in three days", stored as an offset, means three days from
whenever that process thought it was.

An instant does not have that problem. "2026-08-21T09:00:00Z" means the same
thing to every process that reads it, forever, including the ones that restart in
between. So the instant is bound absolutely and stored as it was given.

Whether a stored instant has arrived is Postgres's now() to answer, always, and
that is what makes a fleet agree. Every other timestamp that governs
scheduling — lease expiry, retry delays, lateness, retention — is written and
compared server-side, and crosses the seam as a duration.

The clock.Clock this package holds therefore has exactly two jobs, and neither
of them decides when a timer fires: ScheduleIn turns "in three days" into the
instant you meant at the moment you said it, and Wait paces this process's own
sleeping. Inside a testing/synctest bubble the default clock reads bubble time,
so a fourteen-day timer fires instantly and deterministically without a test
double.

# Exactly once, which means at least once plus idempotence

The lease is the arbiter. A claim selects and leases due timers in one
SELECT … FOR UPDATE SKIP LOCKED statement, so two claimants can never hold the
same firing, and a firing is retired by marking the row rather than by anything
in the worker's memory.

There are no heartbeats. A worker that dies simply lets its lease lapse and the
timer is handed to somebody else; nothing detects the death, and nothing has to.
The price is that a lease which lapses while its holder is merely slow produces
two firings, so handlers must be idempotent. Due.Reclaimed marks a firing that
took over a lapsed lease, so that window is visible.

What is not a price is the firing being recorded wrong. A claim stamps a name on
every row it takes and hands it back on Due.LeasedBy, and Complete and Release
match on it — so the slow worker's eventual Complete retires nothing, rather
than marking fired a firing the second worker is still running and then excluding
that worker's own Release as already-fired, which is how a firing would be
recorded done having never succeeded.

The name is the fence, not the lease's liveness, and the difference is
load-bearing: a worker whose lease lapsed with nobody else claiming still holds
the firing, and its Complete still lands. Worker.pass depends on exactly that —
it completes on a context detached from the worker's, with a full lease of
grace, so a handler that overran still records what it did. Matching nothing is
not an error; a short match gets a counter and a log line.

# Rescheduling, and the second fence that makes it safe

Scheduling a key that already has a timer moves it, and the new instant wins
outright — later as readily as earlier, because "the trial was extended" is the
ordinary case and a merge rule that only ever moved things earlier could not
express it.

That raises a race a work queue does not have: a timer rescheduled during the
seconds it is being fired. Claim hands back Due.RunAt, and Complete and Release
match on it as well as on the key and the claim, so a worker holding a stale
instant marks nothing and the new schedule stands. Pass the Due value back rather
than its key and both fences apply without anybody having to think about it.

The two answer different races and neither covers the other. The instant catches
a schedule that moved; the claim catches a lease that was taken over, which the
instant cannot see because a claim leaves run_at exactly where it was. The case
that needs both is a reschedule to the instant a timer already has, below: the
row's schedule is unchanged, its lease may have lapsed and been reclaimed, and
the name is the only thing that says so.

A move drops the lease and the name on it along with the schedule, so the new
schedule is claimable at once rather than waiting out a lease nothing can still
discharge — and so that the worker holding the old one cannot come back and
report on the new. Rescheduling to the instant a timer already has is not a move
and leaves both alone — that is what an at-least-once upstream redelivering
"start trial" looks like, and treating it as a move would free a row somebody is
firing and let a second worker fire it too.

What cannot be undone is a handler already running. A timer moved during its own
firing may still have fired once — nothing can reach into a goroutine and stop
it — so if that matters, cancel and schedule under a new key.

# Sleeping until the instant, not through it

A work queue polls because it cannot know when work will arrive. A timer set
knows exactly when its next firing is owed, so NextDue answers that in one round
trip and Wait sleeps for it:

	for {
		due, err := set.Claim(ctx, 20, time.Minute)
		// ...
		if len(due) == 0 {
			if err = set.Wait(ctx, time.Minute); err != nil {
				return err
			}
		}
	}

The poll interval is a backstop rather than a tuning compromise, which is why it
is a minute here and a second in a work queue. Pair it with a wakeup for the one
case the next-due read cannot cover on its own — a timer scheduled for thirty
seconds from now, landing just after a poller went to sleep for an hour:

	listener, err := pgnotify.NewListener(ctx, &pgnotify.Config{
		ConnectionString: dsn,
		Channel:          "timers",
	})
	// ...
	go listener.Run()

	set, err := timers.New[TrialID](ctx, cfg, client, timers.WithWakeup(listener.Signal()))

with Config.NotifyChannel set to the same channel on whatever schedules, so
Schedule emits a payload-free pg_notify once the rows have landed. None of the
set's guarantees rest on it: NOTIFY is at-most-once and connection-scoped, so a
reconnecting listener misses everything sent while it was away, and the poll is
what makes that survivable.

# Driving it

Worker is the loop above, with a handler:

	worker, err := timers.NewWorker(ctx, &timers.WorkerConfig{}, set,
		func(ctx context.Context, due timers.Due[TrialID]) error {
			return expireTrial(ctx, due.Key)
		})
	// ...
	go func() { _ = worker.Run(ctx) }()

It exists where workqueue's equivalent deliberately does not, because sleeping
until the next instant rather than through it is the entire behavior being asked
for and there is only one right way to write it. Callers who want their own loop
have Claim, Complete, Release, and Wait, which is what Worker is built from.

Reap and Stats are methods rather than loops this package runs, because you
already have a scheduler — see the jobs package. Reap deletes fired timers past
their retention; Stats is the health read. Nothing here fails loudly, so
Stats.OldestDueLateness is the number that says the fleet has stopped firing:
a count of outstanding timers cannot distinguish a set with a lot scheduled from
one where nothing is going off.

# Keys and payloads

K is comparable, which is most of what makes an encoding safe. Strings and
string-like types are stored as themselves rather than JSON-quoted, so the table
stays legible; anything else is JSON, and a key that needs a specific rendering
supplies WithKeyCodec. The encoded key is the table's primary key and is bounded
by MaxKeyLength.

A timer may carry an opaque payload, which a work queue's item may not. The
reasoning that keeps payloads out of a queue — the consumer already knows how to
turn a key into work — holds less often for a one-shot timer, because the thing
it fires about frequently has no durable row to key into: an abandoned checkout,
an unaccepted invitation, an escalation that exists only as a decision somebody
made. Without a payload the caller has to invent a table to hold that context,
and that table is this one. MaxPayloadSize bounds it; if the context is large,
store it somewhere addressable and let the payload carry the address.

# Two details that cost real incidents

Both are inherited from workqueue, whose documentation explains them at length,
and both are load-bearing here for the same reasons.

Every writer takes its row locks in primary-key order. Schedule sorts its rows
before binding them and the statement orders them again, and Complete, Release,
and Cancel reach theirs through a CTE that orders and locks them explicitly. Claim is exempt and safe:
SKIP LOCKED never waits, and a statement that never waits cannot be half of a
deadlock.

The claim's LIMIT sits above the lock rather than below it, so a claimant gets a
full batch whenever that many timers are due however many competitors are
running. A LIMIT pushed into a subquery beneath the lock would still be correct
and would quietly halve throughput under contention, so there is a test pinning
it against a real server.

Schedule does not group-commit, and workqueue's Enqueue does. That is not an
omission: scheduling is not a per-request write path — one row is created when a
trial starts, not on every read of it — so the contention that makes merging
worth its complexity does not arise. If you find yourself scheduling on every
request, you want a work queue.

# Where the SQL comes from

Nothing in this package composes a statement. The queries live in
timers/internal/queries as a rendered, committed corpus — written out there
rather than emitted by database/querygen, for the reason that package's comment
gives — sqlc checks that corpus against the schema timers/migrations renders with
no database running, and what the set executes is the querier sqlc-gen-unison
generated from it, in timers/internal/timersdb. A column renamed in a migration
is a failed `make unison` rather than a scan error in production.

A batch reaches those statements as one bound array per column rather than as a
tuple per row, so the text of a statement does not depend on how many timers are
in the call. Schedule, Complete, Release, and Cancel each split their batch into
parallel arrays, in primary-key order, which is where the lock-ordering
discipline below is applied.

# Creating the table

timers/migrations renders the DDL for a table prefix. If you already run
database/migrate, hand migrations.SQL to WithGeneratedMigration and the table is
created by your normal migration run at a version you choose.

One table serves any number of logical sets: Config.Name partitions it, and is
the leading column of the primary key. Two Timers values with different names
share nothing but storage.

# Postgres only

Deliberately, and for the same reason workqueue is — see its package
documentation for which construct binds. The claim is one statement that selects
due rows, locks them, increments attempts, extends the lease, and hands back the
keys with their payloads; without RETURNING that becomes a
SELECT … FOR UPDATE SKIP LOCKED and a separate UPDATE inside a transaction held
across both round trips, which is a different concurrency shape rather than a
dialect switch. New returns dialect.ErrUnsupported for anything else.

The module README's "SQL Dialect Support" section is where that narrowing is
spoken module-wide, beside the roster of every other package that stores
anything through database — the table to read before choosing a dialect, rather
than after choosing this package.
*/
package timers

//platform:narrowing claims a due timer in the one statement, and would owe the same split anywhere else

//go:generate go run ./internal/queriesgen

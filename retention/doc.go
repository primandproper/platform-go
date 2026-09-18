/*
Package retention deletes data on a clock, from policies given as data.

Every application accumulates rows with a lifetime — expired OAuth2 tokens,
stale sessions, delivered webhook records, request captures holding PII — and
every application ends up cleaning them out with a script per table, if at all.
The scripts are all the same shape and all differ in the two ways that matter:
whether they batch, and whether anyone can prove they ran.

This is the proactive mirror of the dataprivacy package. That one erases data
because a subject asked; this one deletes data because a policy says its time is
up. What "expired" means for a table is the application's business. That the
policy runs, runs in bounded batches, and leaves evidence behind is this
package's.

# A policy is data

	policies := []retention.Policy{
	    {
	        Name:  "expired-oauth2-tokens",
	        Age:   24 * time.Hour,
	        Target: retention.Table{Name: "oauth2_client_tokens", Column: "expires_at"},
	        Basis: "access tokens are useless once expired; kept a day for support",
	    },
	    {
	        Name:  "delivered-webhook-attempts",
	        Age:   30 * 24 * time.Hour,
	        Target: retention.Table{Name: "webhook_delivery_attempts", Column: "created_at"},
	    },
	}

Age is measured back from the column the Target names, which means the same
field carries two readings depending on what that column is. Against a
created_at it is a retention window: keep for thirty days. Against an expires_at
it is a grace period: the row was already dead at the instant in the column, and
Age is how long after that it is kept around for support and forensics — zero is
a legitimate value there and the only place it is.

A Table target is a table, the timestamp column age is measured from, and the
key column a batch is bounded by. There is no predicate field, on purpose: a
free-text SQL fragment reaching a query through configuration is the one thing
in this package that could not be vetted at startup, and a policy that needs a
predicate needs Go, not a string. Implement Target instead — it is four methods,
and Table is the reference implementation.

# The audit log is the second implementation

audit.PruneTarget is what Target being an interface was for. The audit log
cannot be swept by a predicate: its entries chain per scope, and a DELETE taking
whatever a timestamp selected would punch a hole in the middle of a chain, which
is indistinguishable from tampering. So it prunes each scope as a prefix and
writes the removed entry's hash back as that scope's watermark — behavior no
declarative target could express, in a policy that is otherwise ordinary:

	policy, err := auditcfg.NewRetentionPolicy(ctx, auditConfig)

The dependency runs one way only. This package imports audit for two things —
to record the entry accounting for each sweep, and to recognize the tables a
Table must never be pointed at; audit does not import this one, and satisfies
Target structurally instead. Which means the sweep that prunes the audit log
writes its own accounting entry into the log it just pruned.

Because a Table pointed at those tables would work, it is refused. The DELETE
this package renders against audit_log_entries is legal SQL and would run every
night without complaint; the damage only surfaces later, in an
audit.Reader.Verify that finds a chain with a hole in it and no prune watermark
to say who made it. So Table.Validate recognizes the audit tables — through
audit.IsAuditTable, at any prefix and in any schema — and fails construction
with ErrChainedTable, naming audit.PruneTarget. It is the one table name this
package knows anything about, and it is there because the alternative to
knowing is a policy set that looks correct and quietly destroys the evidence
the other policies are accounted for in.

# A policy set can be previewed

	report, err := sweeper.Report(ctx)

Sweeper.Report samples every policy's cutoff and backlog and writes nothing —
no DELETE, no transaction, no audit entry, and no instrument. It answers the
question the declaration cannot: Age is a duration written against a column
whose meaning somebody had to remember, and the cutoff is that arithmetic
carried out, with the number of rows currently sitting behind it. A policy
meant as a thirty-day window and typed as a thirty-minute one is invisible in
the source and unmistakable in a report taken before the job is registered.

Disabled policies are reported, where a sweep leaves them out of its result
entirely. A sweep accounts for what it did and a policy that did not run did
nothing to account for; a report answers what a policy would do, and the policy
that most needs the answer is the one somebody is deciding whether to turn on.

# The sweep is scheduled, not looped

	sweeper, err := retentioncfg.NewSweeper(ctx, cfg, client, policies,
	    retentioncfg.WithPillars(pillars),
	    retentioncfg.WithSweeperOptions(retention.WithSweeperAuditRecorder(recorder)),
	)
	if err != nil {
	    return err
	}

	if err = scheduler.Register(sweeper.Job(jobs.MustCron("0 4 * * *"), 30*time.Minute)); err != nil {
	    return err
	}

The Sweeper owns no goroutine and no ticker. It is registered with a
jobs.Scheduler, whose distributed lock is what makes the sweep run once across a
fleet rather than once per replica — ten replicas each issuing the same DELETE
is ten times the lock contention on a table people are still reading.

Within a run, each policy is drained in bounded batches with a pause between
them. That is the whole reason this is a package and not a DELETE: a table with
four million expired rows and no batching is one statement holding locks for
minutes, and the first time anyone finds out is when it happens in production
against a table that is also serving traffic. A batch is its own transaction, a
policy stops after MaxBatches whether or not it drained, and what it could not
reach this run is reported as backlog rather than pursued.

# Accounting is the point

A sweep records what it removed in the audit log, one entry per policy per run,
carrying the cutoff it computed, the number of rows, and the policy's stated
basis. That entry is the difference between having a retention policy and
claiming one: without it, "we delete tokens after a day" is an assertion about a
cron job nobody has read.

The entry is written in its own transaction after the policy's batches, not
inside them. An entry per batch would put an audit row into the log for every
thousand rows deleted — into a table whose own retention default is seven years
— and the audit log is not a debug log. The cost of the separation is that a
crash between the last batch and the entry loses that run's record, which is why
the rows removed are also a counter and a log line.

Nothing here writes an entry for a policy that removed nothing. A sweep that
found an empty table is not an event, and a nightly entry saying so for every
policy would bury the ones that matter.

# The SQL here is composed in Go, and that is a ruling rather than an oversight

Every other package in this module that owns SQL renders it into a canonical
.sql, has sqlc check it against a committed schema, and executes the querier
sqlc-gen-unison generates — so a renamed column is a failed generate rather than
a runtime error. This package cannot be on that tier, and the reason is the same
thing that makes it useful: the table a pass deletes from, the column its age is
measured from, and the key its batches are bounded by are values a Policy
carries, written by an application, against tables this module ships no
migrations for. There is no schema for sqlc to read and no statement it could
check, because the statement does not exist until a Policy names one.

What the two statements owe in exchange is written down rather than assumed.
Every identifier that reaches query text goes through dialect.ValidIdentifier at
construction, which is why Table has no predicate field — an identifier can be
vetted and a SQL fragment cannot, and configuration is exactly the path by which
an unreviewed one would arrive. The values are bound, never interpolated. The
shape follows database/querygen's bounded prune: ordered, so successive batches
make monotonic progress through a backlog; capped, so no pass can become the
unbounded DELETE this package exists to avoid; and rendered as the native
bounded write on the one dialect whose grammar has one, which is
dialect.SupportsWriteLimit rather than a second copy of that answer. And what a
corpus would have bought — somebody other than the author agreeing the statement
is legal — is bought instead by a container suite that runs both statements
against a real Postgres, a real MySQL, and SQLite.

database/querygen's doc carries the long form, under "The packages that are not
on this tier"; internal/sqltier carries the ruling in the form that fails a build
if this package ever grows a statement nobody has thought about.

# What this does not do

It does not soft-delete, archive, or export before deleting. A policy that has
to move rows somewhere first is two steps, and the first one is the
application's.

It does not know about foreign keys. A DELETE that violates one fails, the
policy reports the error, the other policies still run, and the next sweep tries
again — which is the correct behavior for a constraint the database is enforcing
and the wrong place to work around it. Order the policies so children go first,
or give the constraint ON DELETE CASCADE.
*/
package retention

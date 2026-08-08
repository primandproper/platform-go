/*
Package audit is the durable, queryable, tamper-evident record of who did what
to which resource.

It is not logging. A log is a best-effort account written beside the work; this
is a record written inside it, in a table nothing can edit, chained so that
removing or altering an entry is detectable after the fact. The difference
matters exactly when it is expensive to be wrong — a dispute, an incident, an
audit — which is also when a best-effort account turns out to be missing the one
line that mattered.

Retrofitting it is miserable, because the write sites are every mutation in the
codebase. That is the argument for it living here rather than being written once
per application.

# The seam

database.Client.WithTransaction hands its callback an executor and nothing else
— it cannot commit or roll back. Record takes that executor, so the audit entry
is just another statement in the caller's transaction and lives or dies with it:

	err := client.WithTransaction(ctx, func(q database.SQLQueryExecutor) error {
		if err := updateRecipe(ctx, q, after); err != nil {
			return err
		}

		changes, err := audit.Diff(before, after)
		if err != nil {
			return err
		}

		return recorder.Record(ctx, q, &audit.Entry{
			EventType:    audit.EventUpdated,
			ResourceType: "recipe",
			ResourceID:   after.ID,
			Scope:        accountID,
			Actor:        audit.Actor{ID: userID, Type: audit.ActorUser, IP: remoteIP},
			Changes:      changes,
		})
	})

That is the single most important thing in this package, and it is the reason
Record takes a querier rather than owning a handle. An audit log that can
disagree with the data it describes is worse than none: it is a record that
looks authoritative and is not. Everything genuinely asynchronous — fan-out to a
warehouse, notification, retention — happens after the commit, never instead of
it.

There is no way to record outside a transaction by accident: holding a
SQLQueryExecutor from WithTransaction means you are already in one.

# What is guaranteed, and what is not

Entries chain per scope. Each carries the hash of its predecessor and its own
hash over that plus its canonical image, so any edit to a past entry, any
removal, and any reordering breaks the chain at a position Verify will name.

The chain is partitioned by scope so that tenants do not serialize against each
other, and positions within a scope are unique in the database rather than by
convention — so a fork is not something Verify has to detect, it is something
the table cannot hold. Concurrent writers to one scope serialize on that scope's
chain row for the length of the caller's transaction.

Stated precisely, a clean Verify proves that nobody edited, removed, or
reordered an entry without also rewriting every entry after it. It does not
prove the table was not replaced wholesale by a consistent forgery — nothing
self-contained can. The answer to that is to publish head hashes somewhere the
database's owner does not control; Record writes each entry's Hash back into the
value you passed, which is what you would publish.

Append-only enforcement is available but optional, because it is separately
privileged: migrations.AppendOnlyStatements renders triggers that make the
database refuse an UPDATE outright. Deletion is deliberately left possible,
because retention has to delete and no trigger can tell that sweep apart from an
attacker; deletion is covered by the chain instead. If your deployment can
revoke UPDATE and DELETE from the application role, do that as well — it is
strictly stronger than either.

# Redaction

A password hash or a bearer token that reaches this table is in the one table
designed to be immutable and retained for years. Register a Redaction per
resource type — or under the empty resource type, for a rule about a field name
wherever it appears — and the value is dropped or replaced by a digest before it
is ever written. Filtering at query time is not the same thing and does not
help.

The static counterpart is the audit:"-" struct tag, which keeps a field out of
every Diff. Use the tag for a field that must never be audited anywhere, and a
Redaction for a policy that belongs to a deployment.

# Retention

PruneTarget removes entries past the retention window, and takes two precautions
that an ordinary reaper would not. It removes only a prefix of a scope's chain,
never a row from the middle, so the survivors stay contiguous and verifiable
against each other. And it records the hash of the last entry it removed as that
scope's prune watermark, so the oldest surviving entry still links to something
and Verify can tell retention's gap from a deletion. Both happen in the same
transaction: a deletion whose watermark did not land would read as tampering.

This package owns no sweep loop. PruneTarget satisfies retention.Target, so the
pruning is a retention.Policy the application registers alongside every other
one it enforces — scheduled by a jobs.Scheduler, holding that scheduler's
distributed lock so a fleet sweeps once rather than once per replica, reporting
a backlog, and accounted for by an audit entry per run:

	policy, err := auditcfg.NewRetentionPolicy(ctx, cfg)

There is no import of retention here and there cannot be one — retention imports
this package, to write that entry. Go's interfaces are structural, so the target
satisfies it anyway; the compile-time assertion saying so lives in this
package's external test.

That accounting entry is written into the log the sweep just pruned, which is
the intended reading: until it existed, the one deletion this module performed
against the audit log was the one deletion nothing recorded.

The default window is seven years, which is long. A default that quietly deleted
evidence somebody was required to keep would be the worse failure — and the
one-hour floor RetentionConfig enforces is there for the same reason, since
retention.Policy itself permits a zero age.

# Reading it

Reader.List pages with filtering.QueryFilter, so the cursor, limit, and time
window an HTTP caller already knows how to send work here unchanged. Query
selects by scope, actor, resource, and event type. Note that Query.Scope is a
pointer: the empty string is a real scope — the one platform-level events belong
to — so a plain string could not distinguish "only platform events" from "every
tenant's events", and in a multi-tenant read path that distinction is a
disclosure rather than a wrong answer.

# Creating the tables

audit/migrations renders the DDL for a dialect and table prefix. If you already
run database/migrate, pass migrations.SQL to WithGeneratedMigration and the
tables are created by your normal migration run at a version you choose — no DDL
copied into your repository. Statements returns the same DDL pre-split for
callers using something else.

The library owns the schema rather than defining a repository interface for the
application to implement, and the hash chain is why: the uniqueness constraint
that makes a fork unrepresentable and the chain row that serializes writers are
not incidental storage details, they are the guarantee.

# Relationship to eventcapture

They resemble each other and are not the same concern. eventcapture is
best-effort and off the hot path: a full buffer drops events and counts them,
because analytics that slows a request is worse than analytics with a gap. This
package is the opposite trade in every respect — synchronous, transactional, and
willing to fail a caller's write rather than lose a record. An entry here is not
a specialization of a captured event; it is the thing you keep when the captured
event turns out not to have been kept.

# Watching it

Pass the metrics providers. audit_chain_breaks is the one to alert on:
everything else here describes throughput, but a non-zero break count means the
log has stopped being evidence. The rest are audit_entries_recorded,
audit_record_latency_ms (Record runs inside somebody's transaction, so its cost
is lock hold time on their rows), and audit_verifications.

Pruning is instrumented by retention rather than here, under the policy's name
— retention_rows_removed, retention_backlog, retention_batches,
retention_sweep_errors, and retention_sweep_latency_ms. The backlog gauge is the
one worth alerting on, because it is what separates a log that is clean from one
whose sweep is stuck.

Spans cover Record, each read, and each verification. No span or log line
carries a value from Changes or Metadata —
those hold exactly what Redaction exists to keep out of durable storage, and a
span exporter is durable storage.
*/
package audit

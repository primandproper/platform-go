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

	err := client.WithTransaction(ctx, func(q database.Tx) error {
		if err := updateRecipe(ctx, q, after); err != nil {
			return err
		}

		changes, err := audit.Diff(before, after)
		if err != nil {
			return err
		}

		return recorder.Record(ctx, q, tenancy.Of(accountID), &audit.Entry{
			EventType:    audit.EventUpdated,
			ResourceType: "recipe",
			ResourceID:   after.ID,
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

# Verification is paged, and one call is bounded

A verification reads every entry in its range in full — change-set and metadata
blobs included, because the hash is taken over the bytes as stored — so the
range a caller is entitled to ask for is a cost this package has to bound rather
than assume. It walks in pages over the (scope, seq) index, carrying the
predecessor's hash and the expected position across each page boundary, so the
seam between two pages is checked like every other link and the process holds
one page at a time however long the chain is.

One call is bounded twice over. WithVerificationPageSize is how much is in
memory at once; WithVerificationCeiling is how much one call walks before it
stops and says where. A result carries LastSeq and Complete for that: a caller
walking a chain longer than the ceiling loops while the result is intact and
incomplete, passing LastSeq back as the next call's afterSeq, and ChainStart is
what the first call passes. Complete is the field that keeps "this chain is
intact" and "this chain is intact as far as I looked" apart, which matters most
on the wire, where audit/grpc exposes the walk with both ends of the window
optional.

# The scope is one thing, read two ways

An entry's scope is the chain's partition and it is a tenancy.Scope, and those
are not two facts to reconcile. Both are one opaque owner identifier: the chain
key is whatever partition integrity is wanted over, and tenancy.Scope names
whose data a row is, with no depth model in it at all. An application whose
chains are per user passes each user's scope and gets a chain per user, because
a user is an owner.

What the type adds over the identifier is one bit. tenancy.Global is the chain
platform-level events are recorded in, stored as the empty identifier; the zero
Scope is a caller who never decided, and Record refuses it rather than filing
the event under the platform's own chain. The distinction is worth a type
because it is the one a string cannot hold, and both this package and
comments/privacy had reimplemented it — a pointer here, a sentinel there —
before they shared one.

It is named once per write, as Record's own argument, and one call appends to
one chain. Entry.Scope is not a second place to say it: an entry that names
none adopts the argument, and one that names a different tenant is
ErrScopeMismatch rather than either value quietly winning. That is the rule
every write in this module follows — a scope goes into the statement bound as a
tenancy.Scope rather than derived from a field on something the caller
assembled elsewhere — and here it buys more than a correctly labeled row.
Because the scope is also the partition, an entry carrying the wrong one would
not mislabel anything: it would append to another tenant's chain, and a chain
is the one structure here whose whole value is that it cannot be appended to
incorrectly.

What that costs is the batch that spanned tenants, which one call used to allow
because the scope was read off each entry. A transaction spans one tenant's
work, so a caller with entries for two makes two calls, into one transaction
and two chains.

The reading is a *tenancy.Scope wherever a read may legitimately decline to
narrow — Query.Scope and Reader.Get — because there a third answer exists that a
Scope cannot hold: "do not narrow at all". See Reading it.

Append-only enforcement is available but optional, because it is separately
privileged: migrations.AppendOnlyStatements renders triggers that make the
database refuse an UPDATE outright. Deletion is deliberately left possible,
because retention has to delete and no trigger can tell that sweep apart from an
attacker; deletion is covered by the chain instead. If your deployment can
revoke UPDATE and DELETE from the application role, do that as well — it is
strictly stronger than either.

# Two levels, and neither derives the other

An application whose model is a user acting inside an account records both, and
the mapping is the obvious one: the scope is the account, the actor is the user.
Nothing here collapses them, and nothing recovers one from the other.

There is no projection helper for that pair, deliberately. One would have to
assume a consumer has exactly two levels, assume which of them is the tenant,
and assume the account is recoverable from the scope — three assumptions about a
consumer's own directory, which is the one shape this package has no opinion on.
The failure such a helper would license is already written in the wild: a
conversion that infers the account back by testing whether an entry's scope
differs from its actor's ID is recovering a fact it never stored, and is right
only as often as that coincidence holds.

A consumer wanting the pair back reads both fields off the entry. Both are
there, both were written by the caller who knew them, and reading two fields is
cheaper than deriving one from the other and safer than assuming it can be.

Where a write's actor genuinely is not known — a repository method that takes an
ID and nothing else, with no principal anywhere on the path — ActorUnattributed
names that absence, and spells both halves of the Actor. It is not ActorSystem,
which is a claim that the application acted deliberately. An entry that simply
leaves the actor out is still refused; see ErrEmptyActor.

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

Every read takes the caller's executor, the way Record takes the caller's
transaction. Hand it Client.Reader() from a console and it runs on the replica;
hand it the database.Tx you are already inside and it sees the entry you have
just recorded and not yet committed. That second case is the ordinary one and
this package used to make it impossible: the reader bound a handle of its own,
so a caller could not read back what they had just written.

Reader.List pages with filtering.QueryFilter, so the cursor, limit, and time
window an HTTP caller already knows how to send work here unchanged — the window
maps onto recorded_at, which is when the event happened. Query selects by scope,
actor, resource, and event type, one value each.

Reader.Get takes a *tenancy.Scope, and so does Query.Scope, where an Entry's is
a tenancy.Scope. A narrowing has a third reading a scope does not: nil is "do
not narrow at all", which is what an operator console asks for and what nothing
a tenant can reach should. A scope confines the read to one chain — tenancy.Global
included, which reads the platform's own events and nobody else's — and a
non-nil pointer at the zero Scope is a caller whose lookup came back empty,
refused with tenancy.ErrNoScope rather than widened. In a multi-tenant read path
telling the first of those from the second is a disclosure rather than a wrong
answer, which is why the distinction is a type rather than a convention.

An entry that exists but sits outside a named scope reads as ErrEntryNotFound,
the same answer an id that was never written gets. Telling them apart would make
a Get an oracle for which entry ids exist in another tenant's log.

# On the wire

audit/grpc serves the Reader over gRPC — one entry, a page of them, and a
verification — and it is strictly narrower than that interface on purpose.
Record is not on it, for the reason Recorder gives. The scope is not settable
through it either: it binds off the connection into every call, and the schema
reserves the field name so that a request has nothing to carry one in. What
makes the crossing worth making is Verify, which is the capability nobody writes
for themselves and the one worth running on a schedule from somewhere else.

# Where the SQL comes from

Every statement this package executes is rendered by audit/internal/queries into
a committed .sql per dialect, checked there by sqlc against the DDL
audit/migrations renders, and run through the querier sqlc-gen-unison generates
from it. Nothing here composes a query. A column renamed in a migration is a
failed `make unison` on every dialect rather than a scan error at run time, and
the projection a read lists and the fields a conversion reads are one generated
struct rather than two lists somebody keeps in step by eye.

That corpus also holds the three statements dataprivacy/auditerasure runs, since
that package owns no table of its own; Erasure is the seam it reaches them
through.

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

//go:generate go run ./internal/queriesgen

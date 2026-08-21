/*
Package dataprivacy fulfills GDPR and CCPA subject access and erasure requests
as durable, auditable operations.

Subject access requests and erasure requests are legally mandatory, tedious, and
structurally identical across applications: fan out over every domain that holds
data about a subject, aggregate it, package it, deliver it safely, and record
that you did. The application owns what its data is. This package owns doing it
exactly once, durably, with an expiring artifact and an auditable result.

# The registry, and the type that is not here

Adding a domain is a registration:

	registry := dataprivacy.NewRegistry()

	if err := registry.RegisterCollector("identity", identityCollector); err != nil {
		return err
	}
	if err := registry.RegisterEraser("identity", identityEraser); err != nil {
		return err
	}

A Collector returns already-encoded JSON — an opaque fragment the library never
looks inside — and the library composes the fragments into a document by key.

That is the one place this deliberately departs from the prior art it
generalizes. There, every domain wrote into a single shared aggregate struct, so
adding a domain meant editing a central type that imported every domain package:

	// what this replaces
	type UserDataCollection struct {
	    Identity     identity.UserDataCollection
	    MealPlanning mealplanning.UserDataCollection
	    Webhooks     webhooks.UserDataCollection
	    // ...eight more
	}

A library cannot have that type — it would have to import its own consumers —
and it turns out not to need one. The cost that type imposed was not
hypothetical: it gained two fields in a single month, each an edit to the file
most likely to conflict. It also meant one domain returning an error aborted the
whole aggregate, so a subject's entire export failed because one unrelated table
was slow. Fragments keyed by domain fix both: registration is local, and a
failure is recorded against its key.

# Writing a collector

Two of the things every collector does are this package's semantics rather than
the domain's, and both are quiet when they are wrong. A paged read has to be
walked to its end — a collector that reads one page and stops returns a
truncated subject access request, which is a compliance defect that looks
exactly like a correct one. And "this domain holds nothing" has to be said as a
nil fragment, so the section is omitted from the artifact rather than written as
null; an artifact padded with empty objects for every domain in the application
reads as a form rather than an answer.

Both are here rather than inferred per consumer:

	registry.RegisterCollector("webhooks", dataprivacy.CollectorFor(
	    func(ctx context.Context, subject dataprivacy.Subject, filter *filtering.QueryFilter) (*filtering.QueryFilteredResult[webhooks.Webhook], error) {
	        return repo.ListWebhooksForUser(ctx, subject.ID, filter)
	    },
	))

That is what most collectors are once the observability preamble is removed. One
that wants the preamble — its own span, its own logger — writes it and calls
CollectAll and Fragment itself, and a domain whose "nothing held" is something
other than an empty list calls Fragment with its own answer. What stays with the
consumer either way is the span, the logger, and the repository call: the read
is the domain's and always will be.

# Collect and erase are separate interfaces

Erasure is not the inverse of export. Some data must be retained — financial
records under tax law, audit entries under legitimate interest — and some must
be anonymized in place rather than deleted, because a foreign key still points
at it. Only the domain knows which of the three applies to each of its tables, so
Eraser is registered separately and reports what it kept:

	func (e identityEraser) Erase(ctx context.Context, q database.SQLQueryExecutor, s dataprivacy.Subject) (dataprivacy.ErasureOutcome, error) {
	    // ...
	    return dataprivacy.ErasureOutcome{
	        Deleted:    rows,
	        Anonymized: anonymized,
	        Retained: map[string]string{
	            "invoices": "financial records, retained 7 years under [statute]",
	        },
	    }, nil
	}

# Partial exports are delivered; partial erasures are not

The two halves fail differently, and the asymmetry is deliberate.

Collection is isolated per key. A collector that errors or times out costs its
own section: the artifact is still written, its manifest names the missing
sections and why, and Request.Failures carries the same information. A subject
with thirty days to complain is better served by most of their data plus an
honest account of the gap than by nothing. An export in which *every* collector
failed is a hard failure — a document asserting that nothing is held about a
person is the one wrong answer available here.

A partial export is a successful operation, and that is worth being explicit
about, because it is the one place this package's idea of success and the
operations package's could have been made to disagree. The operation succeeded:
it produced an artifact and a pointer to it. Which sections are missing is in
ExportSummary, in the operation's Result.Detail, and in the artifact's own
manifest — a fact about the answer rather than about whether there is one.

Erasure is atomic. Every registered Eraser shares one transaction with the
request's own bookkeeping, so a subject is never left deleted from eight domains
and present in three. A partial erasure has no coherent meaning and no status
could describe it, so an eraser's error rolls the whole thing back and the
request is retried intact.

# The two operation kinds

Both halves run as operations. This package supplies two Runners and registers
them; everything around them — the queue, the worker, the lease, the retry
budget, the progress a client watches, the status endpoint — is the operations
package's, shared with every other long-running thing in the application.

	KindExport   = "dataprivacy.export"
	KindErasure  = "dataprivacy.erasure"

The registered domains are the progress denominator for free, because the
registry already enumerates them: an export reports "3 of 9 domains complete"
without a counting pass, and within a domain reports bytes collected, which is
the untotalled counter a fan-out over opaque fragments can honestly offer. The
artifact is the result pointer — Result.URI holds the uploads key and
Result.Detail an ExportSummary.

# What is left of this package's own state machine

	```mermaid
	stateDiagram-v2
	    [*] --> in_progress: Submit(export)
	    [*] --> in_progress: Submit(erasure), no confirmation window
	    [*] --> awaiting_confirmation: Submit(erasure), window > 0

	    awaiting_confirmation --> in_progress: Confirm
	    awaiting_confirmation --> cancelled: Cancel
	    awaiting_confirmation --> cancelled: window lapses

	    in_progress --> completed: fulfilled
	    in_progress --> failed: the operation gave up
	    in_progress --> cancelled: stopped mid-flight

	    completed --> expired: artifact deleted
	```

There is no cycle in it any more, and that is the shape of what the port
removed. pending → processing → pending was a retry, and retries are the
operation's now, along with the lease, the attempt counter, the backoff
schedule, and the poll loop that drove all three. What is left are the states
that were never the operation's to hold:

  - awaiting_confirmation, because a request nobody has confirmed has no
    operation at all. Folding it in would have meant an operation that exists in
    order to not run, which is a queue entry pretending to be a consent record.
  - cancelled by a lapsed window, for the same reason.
  - expired, because the artifact outlives the operation that produced it and is
    swept on its own schedule. An operation is done when the export is
    delivered; the file in the bucket is a separate obligation with a separate
    clock.

And three that look like the operation's and are not. in_progress is coarser
than pending-or-running on purpose — this row does not know whether a worker has
picked the work up, and Request.OperationID points at the thing that does.
completed is written by the runner in the same transaction as the artifact
reference and the audit entry, because those three facts have to commit
together. failed is written on the operation's final attempt, which is the only
moment at which "nobody is getting an answer" is a true thing to record; see
operations.Attempt, which exists because of this.

The retention windows differ, and that is the practical reason for two records
rather than one. An operation is reaped in weeks; this row is kept for years,
because it is the statutory record that a named person asked and when.

Two-phase confirmation is opt-in: a ServiceConfig.ConfirmationWindow of zero —
the default — starts an erasure on submission and Confirm is never needed.
Turning it on is the difference between an accidental erasure being a support
ticket and being unrecoverable, and regulation generally permits a verification
step.

# Assembly

The fulfiller supplies the runners and registers them; an operations Worker over
the same registry runs them.

	fulfiller, err := dataprivacy.NewFulfiller(ctx, &dataprivacy.FulfillerConfig{}, store, registry,
	    dataprivacy.WithFulfillerUploadManager(uploader),
	    dataprivacy.WithFulfillerCompressor(compressor),
	    dataprivacy.WithFulfillerAuditRecorder(recorder),
	    dataprivacy.WithFulfillerNotifier(notifier),
	    dataprivacy.WithFulfillerURLSigner(
	        dataprivacy.NewArtifactURLSigner(uploader, 15*time.Minute, false),
	    ),
	)
	if err != nil {
	    return err
	}

	if err = fulfiller.Register(operationsRegistry); err != nil {
	    return err
	}

	svc, err := dataprivacy.NewService(ctx, &dataprivacy.ServiceConfig{}, store, operationsService)

Every process that submits registers the kinds too, not only the ones that run
them: operations resolves a kind at Start so that an unrunnable operation is
refused at submission rather than discovered in a worker an hour later. In
practice the API process builds a Fulfiller as well and simply never runs a
worker.

The signer is what puts a working link in the completion mail. Without it the
notification still goes out, saying the export is ready and to sign in for it —
which is the right message when a link cannot be handed out, and the wrong one
when it merely was not wired.

The Sweeper belongs on the jobs scheduler rather than on a ticker of its own, so
the sweep runs once across a fleet:

	sweeper, err := dataprivacy.NewSweeper(ctx, &dataprivacy.SweeperConfig{}, store,
	    dataprivacy.WithSweeperUploadManager(uploader),
	)
	if err != nil {
	    return err
	}

	if err = scheduler.Register(sweeper.Job(jobs.MustCron("0 * * * *"), 30*time.Minute)); err != nil {
	    return err
	}

A deployment that runs the operations worker and not the Sweeper accumulates
artifacts forever. That is the failure this package's design is most anxious
about, which is why the sweep is a named, schedulable thing rather than a flag.
Schedule operations.Service.Recover beside it for the reason operations gives:
without it, a request whose enqueue was lost waits for nothing.

One sizing constraint comes with the port, and it is the kind that presents as a
hang rather than an error. A runner holds a database transaction for the whole
of its work — every eraser shares one, and an export's completion is one — while
the operation's progress flush writes to the operations table beside it. Both
draw from the same connection pool, so a pool without spare capacity deadlocks:
size it for the operations worker's concurrency plus one connection per running
operation, not for its concurrency alone.

# Asking after a request

There are two reads and they answer different questions.

Service.Get returns the request: what was asked, by whom, when it is due,
whether the artifact still exists. operations/http, mounted against
Request.OperationID, returns how it is going — the state, the domains completed,
the bytes collected, the structured error, and a server-sent event stream of the
same. There is no status endpoint in this package, because there is no version
of one that would be better than the one every other long-running thing in the
application already has.

The operation's owner is the subject's ID, which is what lets that endpoint
scope a read to the person it is about.

# Delivering the artifact

The artifact is canonical JSON, then compressed, then optionally encrypted. Two
delivery paths exist and they are not interchangeable:

  - Service.Download mints an expiring signed URL straight to storage. The bytes
    never pass through the application.
  - Service.Open streams the artifact, reversing compression and encryption. It
    works with every provider, at the cost of proxying.

Configuring an encryptor disables Download, and that is enforced rather than
documented — see ErrArtifactEncrypted. A signed URL hands the client the stored
object, and the stored object under encryption is base64 ciphertext. A subject
who followed that link would get a file they cannot open, and would find out
some days into a statutory window.

# Encryption and the audit log

Two things this package touches are cryptographically load-bearing, and both are
worth knowing about before wiring it up.

An artifact encrypted at rest is only as recoverable as the key. Losing the key
turns every unexpired artifact into garbage, and the subjects waiting on them
have a deadline. Encryption is therefore off unless configured.

# Erasure and backups

Deleting a row erases it from the live database and from nowhere else. With any
real retention window, an erasure completed today leaves the subject present in
every snapshot taken before it — for the length of that window — and no amount
of DELETE reaches a snapshot, because the media is not writable.

WithFulfillerShredder closes that, by destroying the subject's data key rather
than only their rows: every column encrypted under that key becomes noise at
once, in the live database and in every backup that already shipped. See
cryptography/shredding, and read what it says about where the keys table lives
before wiring it, because a keys table backed up alongside the data it protects
hands everything back on the first restore.

The shred runs before the erasers and outside their transaction. Both are
deliberate and both are explained at Fulfiller.erase. A scoped request does not
shred at all — a data key spans every scope its subject appears in — and says so
in Request.Retained rather than quietly doing less than it was asked.

The audit log is a hash chain, which means audit entries about a subject cannot
simply be deleted or anonymized — either would make audit.Reader.Verify report
tampering, for the rest of that scope's history. dataprivacy/auditerasure exists
to do the part that is sound: delete whole audit scopes belonging to the
subject, and report the rest as retained with a stated basis. It is registered
explicitly, and an operator who wants the audit log left entirely alone simply
does not register it.

# What is recorded

Every submission, confirmation, cancellation, completion, and artifact access is
written to the audit log when a Recorder is configured, in the same transaction
as the state change it describes. "Who exported this person's data" is itself
sensitive, and a system that can produce an export without leaving a record of
who asked has a data exfiltration path with no alarm on it.

The audit entries carry the subject's ID and nothing else about them. An audit
log is durable by design, and copying a person's data into the log that records
the request to export it would defeat both. The operation carries even less: its
request is a Job holding a request ID, and the runner reads the rest from the
row.

# Deadlines

Request.DueAt is stamped at submission from the configured response window —
thirty days by default, GDPR's figure rather than CCPA's forty-five, because a
deadline that is too early produces a gauge somebody looks at and one that is
too late produces a fine. The Sweeper samples dataprivacy_requests_overdue from
it.

Alerting on that gauge is left to the operator. The number is a fact; what
counts as an incident is a policy, and this package has no business holding an
opinion about which of a consumer's jurisdictions applies.

# Upgrading

There is no migration from a dataprivacy_requests row that was mid-processing
under the old worker to an operation, and none is offered. The old row carries an
attempt count and a lease against a state machine that no longer exists, and
inventing an operation for it would either re-run work that was half done or
record a completion nothing performed. A subject with a statutory deadline is the
wrong person to be approximately right about.

So drain before deploying: stop accepting requests, let the old worker finish
what it claimed, and deploy once the table holds nothing in pending or
processing. A deployment that cannot drain can run both releases for one
release's overlap, with the old worker fulfilling the old rows while the new one
starts operations for everything submitted after.

The schema changes with it. next_attempt, claimed_until, and attempts are gone,
operation_id is new, and the pending and processing statuses are replaced by
in_progress. Render the DDL from dataprivacy/migrations as usual; a table that
already exists needs the columns added and dropped by hand, because this package
ships no numbered migrations — see that package for why.
*/
package dataprivacy

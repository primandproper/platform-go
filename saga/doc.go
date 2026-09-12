/*
Package saga runs a linear sequence of steps durably, and unwinds it when a step
fails.

A saga is a multi-step process where each step can fail and earlier steps need
undoing: place an order, charge a card, reserve inventory, notify a partner. The
guarantees — durable state between steps, resumption after a crash,
compensation in reverse order — are pure infrastructure. The steps themselves
are pure application, and this package never looks inside one.

# If you need any of this, use Temporal

This is a linear saga runner. It does not do, and will not do:

  - Branching, conditionals, or dynamic step graphs.
  - Versioning of in-flight definitions — the problem that makes durable
    execution genuinely hard, and the reason the products that solve it are
    products.
  - Long-lived signals, queries, or external events mid-saga.
  - Child workflows, cancellation trees, or heartbeating.
  - Parallel fan-out or sub-sagas.

If a process needs any of those, the answer is Temporal, not this package. A
linear saga runner is a few hundred lines over machinery this module already
has; a workflow engine is a product, and a workflow engine grown accidentally
inside a shared library is the worst of both.

What this is for is the ordinary case that keeps getting written by hand: four
or five steps, each of which talks to something that cannot join your
transaction, where the third one failing means the first two have to be taken
back.

# Defining a saga

A definition is a name and an ordered list of steps. Each step has a Do, an
optional Undo, and an optional delay before it runs:

	type Booking struct {
		OrderID   string
		ChargeID  string
		Reserved  bool
	}

	registry := saga.NewRegistry()

	err := saga.Register(registry, saga.Definition[Booking]{
		Name: "place_order",
		Steps: []saga.Step[Booking]{
			{
				Name: "charge_card",
				Do: func(ctx context.Context, s *Booking) error {
					id, err := payments.Charge(ctx, s.OrderID)
					s.ChargeID = id

					return err
				},
				Undo: func(ctx context.Context, s *Booking) error {
					return payments.Refund(ctx, s.ChargeID)
				},
			},
			{
				Name: "reserve_inventory",
				Do:   func(ctx context.Context, s *Booking) error { ... },
				Undo: func(ctx context.Context, s *Booking) error { ... },
			},
			{
				Name: "notify_partner",
				Do:   func(ctx context.Context, s *Booking) error { ... },
			},
		},
	})

State is mutated in place and persisted after every step. A step that runs after
a crash sees exactly what was persisted, which is what makes resumption correct
rather than approximately correct.

# Starting and advancing

Start writes a row; a Worker advances it:

	runner, err := saga.NewRunner[Booking](client, store, registry)
	instance, err := runner.Start(ctx, "place_order", Booking{OrderID: id})

The client is there because Start opens a transaction of its own, and a Store is
not where a transaction comes from — see the Store documentation.

Reach for StartInTransaction where you can. A saga started in its own
transaction, after the caller's has already committed, does not exist if the
process dies in between — and whatever the caller wrote to decide to start it
has already happened.

	err := client.WithTransaction(ctx, func(q database.Tx) error {
		if err := orders.Save(ctx, q, order); err != nil {
			return err
		}

		_, err := runner.StartInTransaction(ctx, q, "place_order", Booking{OrderID: order.ID})

		return err
	})

The Worker polls for instances that are due, takes a per-instance distributed
lock, and runs as many steps as it can before handing the instance back. One
worker pool advances every definition in the process; it is not generic, and
does not need to be — see the type-erasure note below.

# The state machine

	stateDiagram-v2
	    [*] --> running
	    running --> completed: every step's Do succeeded
	    running --> compensating: a Do exhausts its attempts
	    compensating --> compensated: every Undo succeeded
	    compensating --> stuck: an Undo exhausts its attempts
	    stuck --> running: Resume()
	    stuck --> compensating: Resume()
	    completed --> [*]
	    compensated --> [*]

Resume() returns an instance to whichever of running or compensating it left.

Five statuses, and there will not be more. Every additional status in a durable
execution engine is another pair of edges the compensation logic has to be
correct about.

# Compensation includes the step that failed

Compensation starts at the failed step, not at the one before it, and this is
the one place the package departs from the textbook description of a saga.

A Do that returned an error may still have posted the charge, written the
object, or sent the message before it failed. A compensation that began at the
previous step would leave exactly that half-applied effect behind — which is the
effect a saga exists to take back. An Undo with nothing to undo is a no-op, and
Step.Undo's contract already requires it to be one, so the inclusive boundary
costs a redundant call in the case where the step did nothing and saves a
stranded side effect in the case where it did.

# StatusStuck

StatusStuck is a first-class terminal state, not a flavor of failure. It means a
compensation itself failed past its retry budget: something is half-done, and
this process has run out of ways to undo it.

It is never resolved automatically, because nothing inside this process can
resolve it — the fact that needs to change is in the outside world. Alert on
saga_instances_stuck, fix whatever broke, and call Runner.Resume. The
compensation budget is deliberately larger than the forward budget
(DefaultCompensationAttempts is ten against three): giving up on a Do costs a
compensation, and giving up on an Undo costs somebody's evening.

Most homegrown saga implementations swallow this case. It is how money goes
missing.

# Exactly-once, honestly

The library supplies a deterministic idempotency key per (instance, step,
phase) and, when a Manager is configured, runs every step under it:

	manager, err := idempotency.NewManager[saga.StepResult](recordCache, locker)
	worker, err := saga.NewWorker(ctx, cfg, client, store, registry, locker,
		saga.WithWorkerIdempotency(manager))

The key deliberately excludes the attempt number. A crash-and-resume becomes
attempt two, and a fresh key per attempt would re-execute exactly the billable
work the key exists to suppress. Dropping it is safe because Manager.Do only
commits successful results: a genuinely failed step releases its claim, so the
next attempt re-executes under the same key. Retry-after-error and
replay-after-crash are distinguished by the store, not by the key.

What that buys, precisely: a crash after the step succeeded and its result was
recorded, but before this package's own row said so, replays instead of
re-executing.

What it does not buy: a crash between "the effect landed at the third party" and
"the idempotency store committed" still re-executes. No library that does not
share a transaction with the step can close that window, and this package
deliberately does not share one — see below. A step whose effect is billable
should also carry the instance ID into whatever idempotency the provider itself
offers. Stripe's Idempotency-Key header exists for the same reason this does.

Without a Manager, every step re-executes on every resumption. That is correct
for a step that only reads or writes an idempotent upsert, and it is a duplicate
charge for anything else.

# Do does not receive a querier

Step.Do gets the state and a context, not a database handle, and the state
persistence is this package's transaction rather than a shared one.

In the flows a saga is actually for, the steps that need compensating are the
ones that cannot join a transaction: an LLM call, an object-storage write, a
third-party API post. The one step that could share a transaction is typically
the final "record it" write, and nothing after it can fail and force its
compensation — so an idempotent upsert keyed by instance ID is strictly simpler
than threading a querier through every Step.

If a saga has several middle steps writing to the local database, that is a
signal those steps wanted one transaction, not a saga.

# Definition drift

Definitions are code, so a deploy can change the step list while instances are
in flight. Versioning is permanently out of scope, so the answer is blunt and
honest: an instance whose stored step names no longer match the definition this
build registers is marked StatusStuck and left for a human, and Resume refuses
it until the lists agree again.

It never silently runs the wrong compensation. Instance.StepNames records what
the saga started with and Registry.StepNames reports what this build has, so the
difference is readable rather than inferred.

Adding a step to the end of a definition is therefore also a breaking change for
anything in flight. Drain the in-flight instances, or start a new definition
under a new name and leave the old one registered until the last instance of it
finishes.

# Type erasure, and why the worker is not generic

Definition and Instance are generic over the state type. Store, Registry, and
Worker are not.

T is bound exactly once, at Register, where the closures that decode the state,
run the step, and encode it back are built. Everything below that moves
json.RawMessage. That is what lets one non-generic worker pool advance every
saga in the process, and one DI container hold the Store that all of them share.

The alternative — threading T through Store and Worker — forces a worker pool
per state type. That is not a saga engine; it is a saga engine per struct.

Runner[T] is where T comes back. It checks the definition's registered state
type against its own and reports ErrStateTypeMismatch rather than decoding a
saga's state into a struct that merely happens to parse.

# Lifecycle events

An EventPublisher receives started, step-completed, compensating,
step-compensated, completed, compensated, and stuck events, in the same
transaction as the row each describes. NewOutboxPublisher wires that to this
module's outbox, keyed by instance ID so a subscriber never sees "completed"
before the step completion that preceded it.

Events carry no saga state. T is the application's own domain object and a
lifecycle event fans out to every subscriber of a topic; a subscriber that needs
the state has the instance ID and a Runner to read it with.

# Retention

This package does not delete terminal instances, and that is a deliberate gap
rather than an oversight. A completed saga is the record that a multi-step
business process ran and what it did, and how long that is worth keeping is a
question about the application's obligations, not about this table. The rows are
small and there is one per process, not one per step.

When the answer is "not forever", it is one statement against the schema
saga/migrations renders:

	DELETE FROM saga_instances
	WHERE status IN ('completed', 'compensated') AND created_at < $1;

Note what it does not delete: a stuck instance, at any age. That row is the only
record that something is half-done.

# Storage

The package ships a SQL Store (NewSQLStore) and the DDL it needs
(saga/migrations), for Postgres, MySQL, and SQLite. Store is an interface
because the state machine and its storage are genuinely separable; nothing about
adopting this package requires implementing it.

Two of its methods take the caller's transaction and the other six take nothing
at all, which is a deviation from what every other store in this module does and
is a ruling rather than an oversight — the interface's own documentation carries
the argument, method by method. What an implementor owes is worth stating from
this side too: no scope, because a saga belongs to no tenant, and no
WithTransaction, because a Store is not where a transaction comes from.

# Where the SQL comes from

Nowhere in this package. The statements the store runs are rendered into a
committed corpus in saga/internal/queries, one .sql per dialect; sqlc checks
them against the schema saga/migrations renders, at build time, with no database
running; and what executes is the querier sqlc-gen-unison generates from those
same files, in saga/internal/sagadb, with the consumer's table prefix
substituted once at construction.

The reason it is worth the machinery is the failure it removes. A saga instance
is a cursor into a multi-step process, and a projection that has drifted from
the scan beside it strands one mid-step — which surfaces days later, as a
process that neither completed nor unwound. On the corpus, a renamed column is a
failed `make unison` on every statement that names it, in all three dialects,
rather than a scan error under load.

What is written out by hand is which statements the store wants, and — for the
ones no generator shape covers — their text, in that same internal package,
where they are checked exactly as the generated ones are. What is not written
anywhere is SQL assembled at run time.
*/
package saga

//go:generate go run ./internal/queriesgen

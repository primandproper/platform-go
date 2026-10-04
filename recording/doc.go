/*
Package recording writes the companions a store write owes, on the write's own
transaction: the audit entries that say what happened and who did it, and the
one domain event that tells everybody else.

It is the body every Hooks implementation in this module has, written once.
Every store here with a Hooks interface hands each write's rows to a hook on the
caller's database.Tx, and what a consumer does with them is the same thing
thirteen times over: build an audit entry, diff the before and after if there
is one, write the entry through an audit.Recorder, emit an event through a
webhooks.Emitter. The two halves already exist in this module; what did not was
the call that does both, and a consumer wrote that call, and the thirteen types
around it, for itself.

	rec, _ := recording.New(auditRecorder, emitter, principalFromContext)
	hooks, _ := waitlists.NewRecordingHooks(rec)
	store, _ := waitlists.NewSQLStore(db, waitlists.WithHooks(hooks))

A package with a Hooks interface ships a RecordingHooks beside it, built over
this type, that records what platform knows about its own writes. A consumer
that wants exactly that passes it. A consumer that wants one entry shaped
differently embeds it and overrides one method, which inverts the arrangement
this package replaces: the consumer writes the exception, and platform writes
the rule.

# One transaction, two writes, one order

Record takes the caller's Tx, as every write in this module does, and both
halves run on it. The row, its entries and its event commit together or none of
them does, which is the property a hook exists to give and the one a
hand-written version keeps losing: an entry appended after the commit, or an
event emitted outside it, is durable state diverging from the record of it with
nothing able to detect the gap.

The entries are written before the event. Nothing downstream depends on the
order inside one transaction, but a failure does: an audit recorder refusing an
entry fails the write before an event has been enqueued, so a refused entry
never leaves a published event describing a row that was rolled back.

# What the consumer still decides

Three things, each an option or an argument rather than a type to implement.

Who did it is read off the context through the callers.PrincipalExtractor the
Recorder is built with, which is the same extractor every gRPC surface here
already takes. A write that reaches the recorder with no principal on its
context is recorded as audit.ActorUnattributed, by name, so a log can count the
writes nobody has yet decided an actor for; it is never recorded with an empty
actor, which audit refuses.

Where an entry is filed is the write's scope unless a ScopeResolver says
otherwise. The default is right for a store whose rows belong to a tenant. It is
wrong for a global catalog whose entries are about a person, where "what
happened to this subject" has to be answerable from the log after the row no
longer says, and WithScopeResolver is how a deployment files those entries under
the subject instead. Entries in one Record call that resolve to different scopes
are written in separate batches, one per chain, because an audit chain is the
one structure here that must not be appended to across tenants.

Which event types a subscriber may receive is the dispatcher's catalog, as it
is for every event. An event type outside it is published to the outbox and not
dispatched, which is how a deployment keeps a credential event internal; see
webhooks.Emitter.Emit for the gate.

# What belongs elsewhere

The event names and the entries' shape for a store's own writes belong to that
store's package, beside its Hooks, which is where each RecordingHooks lives. A
consumer's own nouns are recorded through Record directly, with event types the
consumer names, exactly as webhooks.Emitter documents. Redaction is the audit
recorder's: a field that must never be audited carries an `audit:"-"` tag on the
row type, and a policy that belongs to one deployment is an audit.WithRedaction
on the recorder this type is handed.

# Which tier this is

The domain's, by the README's rule. An audit entry is about a resource an
application has and an event is the application telling its subscribers about
one; a deployment with nothing to record has nothing to compose here. That it
owns no table is the same shape callers has, and the same answer.
*/
package recording

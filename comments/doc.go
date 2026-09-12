/*
Package comments is the durable half of letting your users talk about your
things: a scoped table of what was said, threaded one level deep, about targets
this package deliberately cannot see.

Every product grows this table. A form somewhere collects some text, a row is
written against whatever it was about, somebody replies to it, and then the same
scoping, paging, editing, archiving and erasing gets written again in each
application. What varies is the catalog of things that can be commented on, and
that is the one part this package refuses to guess at.

# The target, and why the store cannot check it

A comment is about something: a recipe, a meal plan, another user's post. Which
kinds of thing exist is an application fact, and the rows themselves live in
tables this package has never seen — in another schema, sometimes in another
database. There is no foreign key available to it and no join it could make.

So the vocabulary is supplied, not inferred. [WithTargets] takes a [Targets]
catalog: the [TargetType] values this application accepts comments on, each with
a description and, optionally, an existence check the consumer supplies. A write
naming a type outside the catalog is [ErrUnknownTargetType]; a write whose
registered check cannot find the target is [ErrTargetNotFound]. The catalog is
the webhooks event catalog's idea applied to a second problem, for the same
reason: a target type is a string underneath, and a comment written under a
misspelled one is stored, counted, and shown nowhere.

Reads are not gated on the catalog. See [Store] for the argument — briefly, the
catalog stops a comment being written where nothing will list it, and the type
that has been withdrawn from a catalog is exactly the one whose rows an operator
still needs to reach.

# Dangling targets: the ruling

A comment can outlive the thing it is about, and this package cannot stop that.
It is worth saying plainly rather than leaving to be discovered:

Referential integrity between this table and an application's own tables is
impossible by construction. The existence check runs at the write and answers for
that moment only; nothing here observes the consumer's deletes, and no database
constraint can span a schema this package does not know the shape of. A target
that is hard-deleted leaves its comments in place, live, listable, and about
nothing.

What that means in practice:

  - [Store.ListCommentsByTargetType] and [Store.ListRootComments] will return
    comments whose target is gone. They are not corrupt rows and they are not
    filtered out, because filtering them out would mean a read that consults the
    consumer's tables, which is the thing that cannot be done.

  - The fix is the consumer's, and it has one shape:
    [Store.DeleteCommentsForTarget], called from the transaction that removes the
    target. It takes a database.Tx precisely so that it can be — a sweep outside
    that transaction is a window in which the target is gone and its comments are
    not.

  - Where the deletion is a person rather than a thing, the same job belongs to
    an eraser: comments/privacy ships one, and registering it puts a subject's
    comments in the same transaction as the rest of their footprint.

  - A consumer that does neither accumulates dangling comments. They cost storage
    and they surface as a moderation queue full of rows about nothing. Nothing in
    this package will report it, because nothing in this package can see it.

An existence check registered on a target definition narrows the window; it does
not close it. A target deleted between the check and the insert is a comment
about nothing, written by a store that had just been told the target was there.

# Threads are one level deep

A comment has a ParentID, and [RootParentID] — the empty string — is a comment
that replies to nothing. A reply's parent must be a live root in the same scope
and on the same target; a reply to a reply is [ErrNestedReply].

The depth limit is a reading decision rather than a storage one. A parent id
admits any depth; assembling an arbitrarily deep tree does not. That is a
recursive walk, and a recursive walk is neither one statement nor the same
statement on the three engines this package serves — so a store that accepted
depth would be a store whose reads could not return what it stored. One level is
the depth a flat pair of reads can answer: the target's roots, then one root's
replies.

A reply may outlive its parent. Archiving a root does not archive its replies,
and erasing an author's root comment leaves replies parented to a row that is no
longer there. Both are deliberate, and they are one case: a reply is still a
reply, [Store.ListReplies] still finds it by parent id, and "in reply to a
removed comment" is what every discussion UI already renders. A consumer that
wants the subtree gone enumerates the replies and archives them too.

# The transaction is the caller's

Every write runs inside a transaction the caller owns, and none of them opens
one. Each takes a database.Tx rather than reaching for a writer of the store's
own, and the type is what says so — only database.RunInTransaction produces one,
so the obligation is the compiler's rather than a doc comment's. A consumer with
nothing to join writes:

	var written *comments.Comment

	err := client.WithTransaction(ctx, func(tx database.Tx) error {
		var createErr error
		written, createErr = store.CreateComment(ctx, tx, scope, comment)

		return createErr
	})

The reason is that a comment is rarely the only row a consumer writes. An audit
entry naming who said it and a data change event on an outbox somebody fans out
are the ordinary companions, and a companion written after the comment's own
write has committed is a companion that can go missing while the comment stays.
The gap is narrow and it is one-directional — a comment with no event, never an
event naming a comment that was not written — and nothing outside this package
can close it. A write that could still be called without a transaction is a write
that will be, so there is no such call.

Every write that names one comment hands its row back that way, on the caller's
transaction and after the statement: the writing, the edit and the archive. What
the entry beside a write describes is the row the write left, not the one the
caller read a statement earlier — and on the archive there is no earlier read to
fall back on, because the row it hides is the one every keyed read here is
written not to return.

The reads take the wider database.SQLQueryExecutor, which is the asymmetry doing
the work: a caller listing a discussion for a page passes Client.Reader() and a
caller that has just written a comment passes the same Tx it wrote through, and
sees it. Neither needs a second method.

What does not move into that transaction is the target existence hook, which
takes no executor and reads on whatever connection the consumer built it over.
[Store.CreateComment] says what that costs.

# Tenancy

Every read and write takes a tenancy.Scope, and there is no variant of anything
that omits one. A deployment with a single tenant passes tenancy.Global()
everywhere and behaves exactly as it would have without the column.

That includes the two writes that take a whole [Comment]: the scope is the
argument's, not the value's. A Comment whose Scope names a different tenant is
[ErrScopeMismatch] and one that names none adopts the argument — see [Store] for
why the entity's field is not what the statement binds.

There is deliberately no cross-scope listing — see [Store] for what that costs
and why the alternative is worse.

# Personal data

The body is a sentence somebody typed, and nothing can promise a sentence
somebody typed names nobody. So this table meets the dataprivacy seam like any
other store of personal data, and comments/privacy ships the two halves: a
dataprivacy.Collector that returns what a subject wrote, and a dataprivacy.Eraser
that destroys it.

The erasure is a hard delete rather than an anonymization, and the reason is that
there is nothing to anonymize down to. Stripping the author off a comment leaves
the free text, which is the part that identifies people; keeping the text and
losing the author would be a worse outcome than either.

# On the wire

comments/grpc is the gRPC surface over [Store]: eight RPCs, the .proto they are
described by, a typed client, and the permissions each one wants. What stays the
consumer's is what was always theirs — who is calling, what each method
requires, whose comments a caller may touch, and the catalog itself, which is an
opaque string in the schema and never a generated enum.

Two of the ten store methods are deliberately not there, and both are named on
their own method here: [Store.DeleteCommentsForTarget] and
[Store.DeleteCommentsByAuthor] are bulk erasure that exists to commit inside
somebody else's transaction.

# Where the SQL comes from

The store executes no SQL this module has not checked against its own schema.
comments/internal/queries describes the table as data; a generator renders that
into one .sql per dialect; sqlc checks each against the DDL comments/migrations
ships; and sqlc-gen-unison turns the checked statements into the typed querier
the store calls. A column renamed in a migration is a failed generate rather than
a runtime scan error, on all three dialects at once.

	make generate   # re-renders internal/queries/<dialect>_generated.sql
	make unison     # re-renders the schema and the generated querier

# Getting the table

comments/migrations renders the DDL for a dialect and a table prefix. It ships no
numbered migration file, because migration numbers are global per consumer; hand
migrations.SQL to database/migrate's WithGeneratedMigration and the table is
created by your own migration run.
*/
package comments

//go:generate go run ./internal/queriesgen

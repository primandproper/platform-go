/*
Package waitlists stores the queue people join before the thing they are
queueing for exists: named lists with a closing time, and the signups against
them with a lifecycle of their own.

	// Every write runs in a transaction the caller owns, so the row commits with
	// whatever the caller writes beside it — see [Store]. A caller with nothing
	// to commit alongside opens one anyway, and it is one line.
	var list *waitlists.List

	err := client.WithTransaction(ctx, func(tx database.Tx) (err error) {
		list, err = store.CreateList(ctx, tx, tenancy.Global(), &waitlists.List{
			Name:        "Launch",
			Description: "early access to the beta",
			ClosesAt:    launchDay,
		})

		return err
	})

	// On the request path, from a form somebody filled in — beside whatever the
	// consumer records about it, in the transaction that carries both.
	err = client.WithTransaction(ctx, func(tx database.Tx) error {
		signup, joinErr := store.Join(ctx, tx, tenancy.Global(), list.ID, &waitlists.Signup{
			Contact: "Ada@example.com",
		})
		if joinErr != nil {
			return joinErr
		}

		// When it is their turn, and when they ask to come off it, in the
		// transaction that records who did either.
		if err = store.Invite(ctx, tx, tenancy.Global(), list.ID, signup.ID); err != nil {
			return err
		}

		return store.Withdraw(ctx, tx, tenancy.Global(), list.ID, signup.ID)
	})

	// Reads take the wider executor: an ordinary one runs off the client, and
	// one handed a Tx sees that transaction's own uncommitted writes.
	page, err := store.ListOpenLists(ctx, client.Reader(), tenancy.Global(), nil)

# Why this is in the platform at all

A waitlist is the next thing every pre-launch product writes, and it sits in the
[github.com/primandproper/platform-go/v14/authentication/passwordreset] class:
short enough to look like it needs no library, repeated often enough that the
copies drift. What drifts is never the CRUD. It is the three things below.

# The contact is stored twice, and the second copy is the point

A signup holds an address, which means two obligations that pull in opposite
directions. The list exists to write to that address, so it cannot be stored as a
digest — a digest cannot be emailed. And a person who asks to come off the list
has to stay off it, which means remembering them after their address is gone.

So the row carries both. [Signup.Contact] is the address as it was given, and
[Signup.ContactDigest] is [SQLStore.Digest] of [Normalize] of it: the column the
row is found by, the column the uniqueness is on, and the column a withdrawal
leaves behind. [SignupStore.Withdraw] blanks the contact, the notes and the
subject reference, keeps the digest, and marks the row [StatusWithdrawn]. A
later [SignupStore.Join] from the same address finds that row and is refused with
[ErrContactWithdrawn].

That is the whole design, and it is what a hand-rolled signups table gets wrong.
The two ways it is usually written both fail: deleting the row frees the key, so
the next form submission re-subscribes somebody who asked to be left alone, and
keeping the row intact means holding an address you have promised not to use. A
digest is how a table remembers somebody it no longer holds.

The digest is unsalted and the hash is fast, which is deliberate and is a weaker
claim than the one passwordreset makes. What it digests is an address somebody
chose rather than 256 bits from a CSPRNG, so anyone willing to hash a list of
addresses can find out whether one is on a suppression list. It is not there to
make a withdrawal secret. It is there so the suppression does not require the
address, which is the difference between a suppression list and a mailing list
you have promised not to use — see [WithHasher].

Normalization is case folding and a trim, and no more than that. Plus-addressing
and dots in a Gmail local part are each a provider's own policy about which
addresses are the same mailbox, and a library that guessed would merge two
people's signups at some providers and split one person's at others.

# A transition is a guarded write, not a read and a write

[SignupStore.Invite] and [SignupStore.Convert] each run one UPDATE whose WHERE
names the status the row must already hold, and it is the affected-row count —
not a read before it — that decides whether this caller is the one that moved the
signup. Two requests inviting the same person both find them waiting; one of the
updates reports a row and the other is told [ErrWrongStatus], so one email goes
out between them.

Deciding on the read instead leaves a window exactly as wide as whatever the
caller does next, which for an invitation is an email — and a waitlist that
emails the same person twice is the failure everybody has seen.

Because a statement that matched nothing cannot say which of two things went
wrong, the losing path makes one more read to find out: a signup that is gone
reports [ErrSignupNotFound], one in another status reports [ErrWrongStatus]
naming it, and a second withdrawal reports [ErrAlreadyWithdrawn]. That read costs
a round trip nobody is waiting on.

# Archiving is not withdrawing

Both hide a signup, and they are not interchangeable, so the store offers both
under names that say which is which.

[SignupStore.ArchiveSignup] is administrative. It is the soft delete every table
in this module has: the row stops appearing in reads, and nothing about what it
holds changes. The contact is still stored, nothing is suppressed, and the
uniqueness still covers the row — so the next signup from that address gets
[ErrAlreadySignedUp].

[SignupStore.Withdraw] is the person's own request. It erases what the row said
about them and keeps the suppression. Somebody clicking "unsubscribe" wants this
one, and a consumer that reaches for the archive instead has written the bug this
package exists to prevent.

# Every write takes the caller's transaction, and every read an executor

A row in a consumer's schema is rarely written alone. An audit entry naming who
did what and a data change event on an outbox somebody fans out are the ordinary
companions of a signup, and a companion is worth what its atomicity with the row
is worth. So every write here — [ListStore.CreateList] through
[SignupStore.ArchiveSignup] — takes a database.Tx the caller is holding, and
there is no form that opens a transaction of its own. [SignupStore.Invite] and
[SignupStore.Convert] are the moments this matters most: they are exactly when an
application sends an email and writes an audit entry, and a store that committed
on its own behalf would put the two in different transactions.

Every read takes the wider database.SQLQueryExecutor, so one read serves a caller
holding the client's reader and a caller inside a transaction — and the second
sees that transaction's own uncommitted writes. Reading a signup immediately
after joining somebody is therefore a read of the row that was just written,
rather than of a database that does not hold it yet.

A caller with nothing to commit alongside opens a transaction anyway, through
database.Client.WithTransaction, which is one line. See [Store] for the argument
in full, including why the scope is an argument on the writes that take a whole
entity carrying one.

# Erasure is a withdrawal, and it is a separate package

A signup holds an address and, where the person had an account, a reference to
it — which is personal data, and this package does not own the directory people
live in. A consumer cannot close that with a foreign key either: a withdrawal
blanks the subject reference to the empty string, which names no user, so a
cascade on that column would refuse every withdrawal.

[SignupStore.WithdrawSignupsForSubject] is the erasure write. It is a withdrawal
rather than a delete for the reason the digest exists — a delete frees the
unique key, so somebody erased at their own request could be re-subscribed by
the next form submission — and it reaches archived signups, which the single-row
withdrawal cannot, because an archived signup still holds the address it was
made with. It runs in the caller's transaction, like every other write here, so a
subject's signups and the rest of their footprint commit or roll back together.

[github.com/primandproper/platform-go/v14/waitlists/privacy] is the
dataprivacy.Collector and dataprivacy.Eraser built on that write and on
[SignupStore.ListSignupsForSubject]. It is its own package so that a service
with a signup form and no privacy pipeline does not compile one.

# ClosesAt is required, and the column is NOT NULL

A list names the instant it stops taking signups, and there is no way to say
"never". That is the one shape here worth arguing about, so the argument is
written down.

A nullable closing time reads as "this list never closes", and honoring it means
`closes_at IS NULL OR closes_at > now` — a disjunction over the column the read
pages by, on the read this package exists to serve, on three dialects. What the
NOT NULL column buys instead is one comparison against a bound instant, which is
what makes [ListStore.ListOpenLists] a keyset page rather than a filter applied
after the fact.

The state the nullable column was for is still expressible and is the state the
row already had. A list whose end is not yet decided names a far horizon and is
brought in by [ListStore.UpdateList] when the date is known; a list that should
stop taking signups this instant is archived, which is the retirement this schema
already has. See waitlists/migrations.

The comparison is against the store's clock rather than the server's, bound as an
argument. closes_at is stamped by the application, so comparing it against
CURRENT_TIMESTAMP would be two clocks deciding one row — and under a test clock
that only moves when a test moves it, the two are years apart. [List.OpenAt] and
ListOpenLists therefore agree by construction, and [WithClock] moves both.

# Scope

Every method takes a tenancy.Scope, and there is no unscoped read of anything
here. A deployment with one catalog of lists passes tenancy.Global() everywhere
and behaves exactly as it would have without the column.

A list and the signups against it share a scope, and the signup carries its own
copy of it rather than reaching the list's through the reference — a scope
predicate that had to join to find its column is a predicate a read can omit, and
every read here is one that must not.

Every signup-side method also takes the list. The list is half of what addresses
a signup, and a read that omitted it would be a read that could hand one list's
row to a caller holding another list's id.

# What this package does not do

It does not send anything. [SignupStore.Invite] records that somebody was let in;
what reaches them is
[github.com/primandproper/primitives-go/v2/email]'s to deliver, off the
[Signup.StatusChangedAt] this stamps.

It does not number the queue. "You are 4,102nd in line" is a count that changes
under whoever is reading it — every withdrawal ahead of somebody renumbers them —
and the paged reads here already carry the counts a caller needs to render a
position they are willing to stand behind.

It does not decide who may administer a list.
[github.com/primandproper/primitives-go/v2/authorization] is where that lives, and
a store that pretended to know who was calling would be an authorization check in
the wrong layer.

# Where the SQL comes from

Nothing in this package composes SQL. The statements are rendered from the column
lists in waitlists/internal/queries through database/querygen, committed as one
.sql per dialect, checked against the schema by sqlc, and executed through the
querier sqlc-gen-unison generates into waitlists/internal/waitlistsdb. A column
renamed in waitlists/migrations is a failed generate rather than a runtime scan
error, on both tables, in all three dialects.

The tables are waitlists/migrations' to create, at whatever prefix a consumer
chooses. The platform ships no numbered migration file — see that package.
*/
package waitlists

//go:generate go run ./internal/queriesgen

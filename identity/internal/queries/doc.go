/*
Package queries is the identity schema described as data: the canonical table
names, each table's columns in the order every read projects them, and the two
subsets a write may assign.

It exists because those facts have two consumers that must not disagree. The
generator behind `make generate` renders these tables through
[querygen.Generator.StandardCRUD] and the keyed forms beside it into the
canonical .sql files sqlc is run over; the store reads the same table names and
column lists to build its projections and to name what it selects. A column list
spelled in both places could differ in one name, and the symptom would be a
check that passes over SQL nobody executes.

So it is spelled once, here, and both halves read it. The .sql files beside this
file are the generator's output — see [Render] and identity/internal/queriesgen.
Why the rendered .sql is committed at all, when the generated Go beside it in
identity/internal/identitydb carries the same statements in executable form, is
identity's package comment, under "Where the SQL comes from".

# Which queries each table gets

[Table.Options] is where a table says what it is beyond a list of columns, and
three of the four options carry a fact that a column list cannot:

  - Ownership is the scope column, so every emitted statement is keyed on it.
    It is named rather than inferred, because a table whose rows are readable
    across scopes and one whose rows are not look identical from the columns.
  - Nullable names the columns a write may set to NULL, which lives in the
    schema neither this package nor querygen reads.
  - Updatable names the columns the standard update assigns, and everything
    else assignable becomes immutable to it. It is stated positively because
    that is the shorter and the more checkable half: a user has four profile
    columns and ten written only by the method that owns them, and a list of
    the ten is a list somebody adds a column to a table without extending.
    Getting it wrong is not a small thing — querygen assigns every column its
    options leave mutable, and the struct a caller is holding is often a
    [identity.User.Redacted] copy whose credential fields are empty, so a
    password hash left in the update set is blanked on every profile save.

The columns Updatable leaves out are not columns nothing writes — they are the
columns a *named* statement writes, and those statements are emitted too. See
fieldWrites: the password and its stamp, the two-factor secret and its
verification, the email verification token, the account status, the ownership
transfer, the invitation answer. Each names its own SET list rather than the
table's mutable set, and three of them carry a predicate on the value being
replaced, which is what makes a losing concurrent writer report zero rows
instead of overwriting the winner.

The two-factor verification is the fourth guarded one and its guard is not an
equality at all: a secret that exists and has not been proven, which is a
not-empty comparison and an IS NULL. Neither names a value a caller holds, so
neither is an argument, and that is what makes a replayed verification write
nothing rather than move the timestamp forward.

Memberships is the fourth table and gets none of the standard set. Its columns
are textbook and not one of its statements is: the get, the archive and the bulk
archive key on the (belongs_to_user, belongs_to_account) pair rather than on id,
which the standard set has no way to express.

Its write is emitted, though — [querygen.Generator.UpsertQuery] renders it — and
it is the statement whose three dialect files genuinely diverge rather than
merely renumbering their placeholders. A membership has to be written by
converging on the pair: it is unique across live and archived rows alike, so
rejoining an account revives the row that is already there rather than adding a
second, and the id it keeps is what the membership's roles hang off.

Its reads are another matter, and they are the three junction lists [Render]
appends after the standard sets. An account's roster is a page of memberships
with the member's columns projected beside them; a user's account list is a page
of accounts reached through the same table; a user's own membership list is the
unpaged read behind every authorization decision this package answers. All three
were hand-built until querygen learned the shape, and the roster was what kept a
two-entity scan-by-position pairing alive after everything single-table had been
generated.

# The keyed variants

A table's standard queries are not all of what a store runs against it, and the
difference used to be the hand-written half. A read keyed on a reference, a read
of one database-owned column, a read keyed on a natural key the table carries an
id alongside — each of those was written out by hand, so sqlc proved statements
the store did not run while the store ran statements sqlc never saw.

So they are rendered here too, through querygen's keyed Query forms, which are
the standard statements with more predicates rather than a second rendering of
them:

  - the two paged invitation reads, keyed on the sender or the addressee
  - the read-back of created_at, for the one create that still wants a stamp
    rather than the row it wrote — see [Stamped]
  - the two archival read-backs, each keyed on the id and the scope and carrying
    the archived predicate's complement, because the row an archival stamps is
    the one row every other single-row statement here is written not to return
  - the three single-user reads keyed on a username, an email address, or a
    verification token
  - the two collision checks, keyed on a username or an email address and
    excluding the row being updated — see uniquenessChecks, rendered from no
    column list at all because the unique indexes cover archived rows and so
    must the read, which is the same trick the archival read-backs above play
    for the opposite half of its reason
  - the three membership reads, all keyed on the (user, account) pair
  - the four batched reads, each keyed on a whole set of keys at once

The membership ones are why Memberships is declared at all despite emitting no
standard query, and why [Table.KeyedColumns] exists: querygen derives a
statement's id predicate from the column list it is handed, so a read that keys
on the natural key hands over the list without the id while still projecting it.

The batched ones are the same idiom used for a different predicate. Three of
them read a role table — the three tables here that are not declared as
[Table]s, because a role row is two columns hanging off a parent and gets no
standard query at all — and the fourth reads the users a page's rows refer to,
from a column list without archived_at, because a hydration read that hides a
soft-deleted user turns "created by a departed colleague" into "created by
nobody". What none of them can express is the empty batch, which is why the
store answers that before it calls: see [querygen.Generator.SetReadQuery].
*/
package queries

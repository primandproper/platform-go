/*
Package queries is the billing schema described as data: the canonical table
names, each table's columns in the order every read projects them, and the
subsets a write may assign.

It exists because those facts have two consumers that must not disagree. The
generator behind `make generate` renders these tables through
[querygen.Generator.StandardCRUD] and the keyed forms beside it into the
canonical .sql files sqlc is run over; the store reads the same table names to
render its prefix. A column list spelled in both places could differ in one name,
and the symptom would be a check that passes over SQL nobody executes.

So it is spelled once, here, and both halves read it. The .sql files beside this
file are the generator's output — see [Render] and billing/internal/queriesgen.

# Which queries each table gets

All four tables get the standard set, which is unusual enough in this module to
be worth saying rather than leaving to be inferred: every row in this schema is a
resource addressed by its own id within a scope, which is exactly what
[querygen.Generator.StandardCRUD] emits. [Table.Options] is where each says the
three things a column list cannot:

  - Ownership is the scope column, so every emitted statement is keyed on it. It
    is named rather than inferred, because a table whose rows are readable across
    scopes and one whose rows are not look identical from the columns.
  - Updatable names the columns the standard update assigns, and everything else
    assignable becomes immutable to it. [Products] and [Subscriptions] have one;
    [Purchases] and [Transactions] deliberately have none.
  - Omitted drops what a table has no caller for. Only [Products] keeps the
    existence check — writing a subscription or a purchase means naming a
    product, and refusing a bad product id before opening a transaction is
    exactly what that statement is for.

The two tables with no Updatable are the point of this schema rather than an
omission in it. A purchase and a ledger row are records of money that has already
moved: the amount, the currency, the account and the provider's identifier are
facts about a completed attempt, and a standard update is a statement able to
rewrite all four. What legitimately changes about either is one column, and each
gets one guarded statement that assigns it and nothing else.

# The keyed variants

A table's standard queries are not all of what a store runs against it, and the
difference used to be the hand-written half everywhere in this module. Each of
these is rendered here, through querygen's keyed forms, which are the standard
statements with more predicates rather than a second rendering of them:

  - the read-back of created_at on all four tables, which no create carries
    because the database owns the column
  - the read-back each archive answers with, on all four tables — the one shape
    here carrying archived_at IS NOT NULL, because the row an archive moved is
    the row every other single-row statement over its table is written not to
    return; see [archivedReads]
  - the four reads keyed on a provider's own identifier, which is the lookup
    every payment webhook begins with — see [externalIDReads]
  - the three paged histories keyed on the account, beside the scope-wide lists
    the standard set already emits
  - the page of an account's subscriptions whose paid period covers a bound
    instant, which is the read behind every entitlement check — see
    [accountReads] for why the instant is bound rather than the server's clock,
    and why the statement does not filter on the status
  - the two guarded status writes and the guarded completion

# The guards, and why two of them share one argument name

[guardedWrites] holds the three statements whose correctness is the affected-row
count rather than a read the caller made first. Every one of them is written from
a payment provider's webhook, and every payment provider redelivers.

The two status writes name the status column twice — in the SET that assigns it
and in the predicate requiring the row not to already hold it — under a single
argument name, which renders as `SET status = X WHERE status <> X`. That is the
inverted case, and one name is what makes it say what it means: a replayed event
finds the row already in the status it was going to write, touches nothing, and
is reported as the replay it is.

It is worth being explicit that this is the opposite of what [querygen.Match.Arg]
exists for. There the guard requires the row to already hold a value, and under
one name the write would set the column to the value it was requiring — legal SQL
that guards nothing. Here the guard requires the row not to hold it, and the two
ends are genuinely the same value.

CompletePurchase guards on absence instead: completed_at IS NULL is what makes a
purchase complete exactly once, so a second delivery cannot restamp the moment
the money arrived.
*/
package queries

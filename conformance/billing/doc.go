/*
Package billing is the ledger surface's promises, assertable against any subject
that mounts it.

Eighteen RPCs over four nouns, and they answer to two different rules. The
catalog answers to the scope alone: a product is the tenant's, so what is
asserted about it is that a request reaches the caller's catalog and never a
neighbor's. The other three nouns are somebody's own money, and answer to an
account as well — so the assertions are that a named account the caller has no
standing in is refused before anything is read, and that a row belonging to
another account is answered exactly as an absent one is, because a different
answer would tell a caller walking identifiers which of them are real.

# What is here and what stayed behind

billing/grpc keeps its construction and contract tests: what NewServer refuses
to be built from, what the permission roster covers, that no message carries a
scope field, and what the converters do with a nil or a status. It also keeps
everything that varies how the server was built — an authorizer that cannot
decide, a wrapped refusal, a grants extractor that holds or lacks the archive
grant — because a deployed service was built once and cannot be rebuilt by the
thing testing it.

It keeps the purchase and ledger halves of the account rules too, for a reason
that is not about the server. No RPC creates a purchase or a transaction, and
that is the design rather than a gap: both are what a payment provider reports.
A subscription is the same kind of fact, and the Subscribed action is how a
subject brings one about; the assertions about purchases and transactions wait
for actions of their own rather than for a suite that writes rows.

# Both directions, deliberately

Each confinement assertion proves the caller reaches its own row through the
same client before proving it cannot reach a neighbor's. A deployment whose
scoping is comprehensively broken answers "absent" to everything, and would pass
every refusal here on the strength of reaching nothing at all.

# The subject's rule, and why these assertions can hold under any other

What a caller has standing in is the deployment's rule. The assertions assume
only the narrowest thing any rule must say: a caller has standing in the account
they are active on, and none in an account belonging to somebody who shares no
membership with them. A deployment whose rule is wider than that — an operator
who may read every ledger — is one these refusals were not written for, and the
subject says so by minting callers who hold no such role.
*/
package billing

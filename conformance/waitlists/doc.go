/*
Package waitlists is the signup surface's promises, assertable against any
subject that mounts it.

Seventeen RPCs, and two audiences for them. Fourteen are an operator's console
and need a caller; three — ListOpenLists, Join and Withdraw — are a signup page,
the form on it and the unsubscribe link in the mail that follows, and are
reached by people who have not signed in. What a consumer needs verified is
different for each half, so the assertions are split the same way.

The console's promise is the one every surface here makes: the catalog and the
queue a request reads are its caller's tenant's, by identifier, by listing and in
both directions, and a signup is addressed by its list as well as by its own
identifier. A transition happens once, and what it answers with is the row as
stored rather than as sent.

The signup page's promise is what it withholds. Join answers a new address, one
already on the list and one that asked to be left alone with the same empty
message, because nothing on a form establishes that the caller owns the address
they typed. A withdrawal the deployment will not authorize reads exactly as an
identifier nobody minted — NotFound, never PermissionDenied — because the
difference between the two is an oracle over which signups exist. Both are
asserted as the wire carries them, which is the only place the second can be
seen: the handler's choice of code is a default that the encoding interceptor
may overrule, and only a real connection runs it.

# Where a visitor lands

A request with nobody on it takes its tenant from the deployment's scope
resolver rather than from a principal, and no client can learn what that
resolver answers. The assertions about the public half made without a caller
therefore need Seams.VisitorScope, and open their lists there through a caller
minted into it; a subject that names no visitor scope, or cannot mint a caller
into it, skips them with the reason printed. The same promises are asserted
again through a signed-in caller wherever the promise does not depend on
anonymity, because every public RPC is also reachable by somebody who has
signed in.

# What is here and what stayed behind

waitlists/grpc keeps its construction and contract tests: what NewServer refuses
to be built from, the permission roster and the public three's declaration, the
reservations in the proto, the store-method roster, and the options. It keeps
every test that builds the server some particular way — a SignupAuthorizer that
permits, refuses, fails or records what it was handed, a ContactResolver, a
GrantsExtractor that unlocks archived rows, a scope resolver that cannot place a
request — because a deployed service was built once and cannot be rebuilt by the
thing testing it. What a deployed service's grants answer an administrator is
the exception, since an administrator holds the grant that retires a list: an
administrator asking for retired lists receives them, and that is asserted here.

That includes the withdrawals that succeed. Whether a withdrawal is permitted is
the consumer's SignupAuthorizer's answer and has no default, so a suite cannot
assume one; what it asserts instead is what a refused one looks like, and it
reaches a withdrawn row through the operator's erasure RPC, which asks no
authorizer, when the promise under test is about what a withdrawn row does
next.

# Both directions, deliberately

Each confinement assertion proves the caller reaches its own row through the
same client before proving it cannot reach a neighbor's. Without that, a
deployment whose scoping is comprehensively broken passes every "the neighbor's
row is absent" assertion on the strength of reaching nothing at all.
*/
package waitlists

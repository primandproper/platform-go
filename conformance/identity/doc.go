/*
Package identity is the directory surface's promises, assertable against any
subject that mounts it.

Thirty-one RPCs, and what a consumer needs verified about the reads among them
is one sentence: the directory a request reads is the one its caller is in. The
assertions here are that sentence by identifier, by listing, and in both
directions — because a scope dropped from a predicate does not error, it widens,
and a widened answer is indistinguishable from a correct one until a second
tenant exists.

The fourth assertion is a different promise and the one with the worst failure:
a read never renders a credential. It is asserted end to end rather than against
the schema, because the schema already forbids it and that is precisely why an
end-to-end check is the one that can fail — a field the proto does not declare
cannot be rendered by the converter, but a deployment that puts its own
projection in front of this surface can render whatever it likes.

# What is here and what stayed behind

identity/grpc keeps its construction and contract tests, all of which are about
a server rather than about a call: what NewServer refuses to be built from,
what the permission roster covers, whether the proto and the Go struct describe
the same type, and what the converters do with a nil. It also keeps its
authorizer suite, which builds TargetAuthorizers and exercises them directly —
a seam implementation under test, not a promise over a wire.

What moved is the half a consumer is owed and could not check for this module.

# Both directions, deliberately

Each confinement assertion proves the caller reaches its own row through the
same client before proving it cannot reach a neighbor's. Without that, a
deployment whose scoping is comprehensively broken — every caller resolved to
one wrong directory, or to none — passes every "the neighbor's row is absent"
assertion on the strength of reaching nothing at all.

That is not hypothetical. It is what the first draft of the audit suite did,
and a deliberately broken resolver passed two of its five assertions before the
controls went in.

# Who a call is made as

The directory is administered, and a deployment reserves administering it to a
service role. Register, GetUser, ListUsers, SearchUsersByUsername, ListAccounts,
ArchiveUser, UpdateUserAccountStatus, SetUserServiceRoles and
SetUserRequiresPasswordChange are therefore made by operators, as
conformance.OperatorMethods describes, and the directory's confinement is
asserted between an operator in each of two tenants. Register here is the
administered door; the one a person registers themselves through is signin's.

The rest is made as an ordinary caller, and each is a promise: a signed-in
caller reads their own principal through GetPrincipal — which is how every
suite here learns a caller's address, rather than through GetUser — saves their
own profile and agreements, reads the accounts they belong to and their
rosters, and administers an account they own: its members, its invitations and
its ownership.
*/
package identity

/*
Package settings is the settings surface's promises, assertable against any
subject that mounts it.

Thirteen RPCs in two halves. The catalog is an operator's: what settings exist,
of what kind, falling back to what. The values are a person acting on
themselves, and that half is the settings screen every consumer ships. What a
consumer needs verified about it is that a value goes in as the kind it was
declared and comes back as that kind, that a subject who has not chosen is
answered by the definition rather than by a missing row, and that nobody reaches
a catalog or an answer that is not theirs — a neighboring tenant's by the scope,
and a colleague's by the subject authorizer.

# Where the catalog comes from

The catalog is created through the surface, by CreateDefinition, rather than
through an action seam. A definition is something a client can bring about, so
a seam for it would be a backdoor with a nicer name. What a client cannot
assume is that it is allowed to: defining a setting is an administrator's
decision, and a deployment enforcing method grants refuses an ordinary caller.
So the catalog is written by an administrator minted into the caller's tenant
where the subject has one, by the caller itself where it does not, and an
assertion whose definition is refused skips with the reason printed rather than
failing a deployment for being right. Every name is minted per test, because a
deployment declares its own catalog at boot and a suite asserting against a
fixed name would be asserting against theirs.

# What is here and what stayed behind

settings/grpc keeps everything about a server rather than about a call: what
NewServer refuses to be built from, the permission roster, the schema checks,
the converters, and the store-method roster that pins DeleteValuesForSubject's
absence. It also keeps the assertions that vary how the server was built — an
authorizer that cannot decide, a server with no grants extractor — and the ones
that need a caller holding an exact set of grants, which is a principal a
subject cannot describe. That is where include_archived's grant rulings stay.

# Both directions, deliberately

Each confinement assertion proves the caller reaches its own catalog or its own
settings through the same client before proving it cannot reach a neighbor's,
and each refusal of a colleague's settings is preceded by the same call against
the caller's own. A surface that refused everybody, or a scope that resolved to
nothing, would otherwise pass every one of them.
*/
package settings

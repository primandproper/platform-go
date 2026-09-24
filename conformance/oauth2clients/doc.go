/*
Package oauth2clients is the administered half of the OAuth2 client registry's
promises, assertable against any subject that mounts it.

Four RPCs, and the property with the worst failure is the one a request cannot
express: a registration minted here belongs to nobody. An administered
registration is one any subject in the registry may authorize through, so its
owner cannot come off the wire — a request that could name one mints a
credential in somebody else's name — and it must not quietly become the caller
either. The rest is the reach the surface is behind a permission for, asserted
rather than described: the whole of the caller's registry, and none of anybody
else's.

The secret is asserted end to end, in both directions. It is on the wire exactly
once, in the answer to the create that minted it, because a registration whose
secret never reached its creator is one nobody can use; and it is never on a
read afterwards, because the plaintext is returned once and never stored.

# What is here and what stayed behind

authentication/oauth2clients/grpc keeps its construction and contract tests —
what NewServer refuses, what the permission roster covers, that the scope name
is reserved on every message — and everything that varies how the server was
built: a store that fails, an extractor that answers with nobody. It also keeps
the one reach assertion no client can set up, a registration belonging to a
person, since every registration this surface mints is administered and the
self-service half that mints the other kind has no RPC here.

# Both directions, deliberately

Each confinement assertion proves the caller reaches its own registration
through the same client before proving it cannot reach a neighbor's. "Absent" is
also what a deployment that resolves every caller to the wrong registry answers.
*/
package oauth2clients

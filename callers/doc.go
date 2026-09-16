/*
Package callers is who is calling, as every gRPC surface in this module reads
them: the interface a consumer's own authentication interceptor satisfies, the
function that reads one off a request context, and the refusal a surface returns
to a caller who may make this call and may not make it against the row they
named.

It is imported as-is.

	func extractPrincipal(ctx context.Context) (callers.Principal, bool) { ... }

	srv, _ := settingsgrpc.NewServer(store, db, extractPrincipal, subjectAuthorizer)

# Why it is a package of its own

Three names, no table, no transport, and no import of anything else in this
module — which is the whole of the point. [Principal] and [PrincipalExtractor]
were declared in identity/grpc, because the directory was the first surface to
cross onto the wire and an interface gets written where it is first needed. Ten
surfaces name them now and nine of those need nothing else identity has: a
consumer wiring only settings, or only comments, linked identity, its generated
querier, its migrations and its protobuf bindings in order to compile an
interface with three methods on it.

It is a move rather than an alias left behind. An alias compiles, and it also
preserves the build edge for everybody who keeps spelling the old name, which
is the entire thing being removed — so there is one spelling of each of these
three names in this module and it is this one.

# Which tier this is

The domain's, by the rule the README's "Primitives and Domains" section states.
A principal is a user, the directory they are in, and the account their request
is against; those are three of this product's nouns, and an application with no
users has nobody to extract. That it owns no table is the unusual part and not
the deciding one — what it holds is the vocabulary a domain transport shares
with the consumer standing in front of it, which is a thing a product has.

# One notion of a caller

These are one package rather than one declaration per surface because a
deployment has one authentication interceptor and one notion of who is calling.
The alternative — each surface declaring an interface of the same shape — is a
consumer writing one adapter per service that happens to need the same three
facts, and it is two chances to disagree about who is calling in the packages
where a disagreement costs the most.

What each surface does with a principal stays that surface's own, and is
documented there: comments writes the user identifier into a comment's author,
webhooks writes it into an endpoint's provenance, settings hands the whole
principal to its authorizer rather than the one field it would have picked, and
waitlists is the one surface whose extractor reports nobody on requests that are
working exactly as intended.
*/
package callers

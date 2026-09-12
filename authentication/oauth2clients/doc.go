/*
Package oauth2clients is an administered registry of OAuth2 clients: the
registrations an operator or a person creates on purpose, as opposed to the ones
an anonymous caller mints at /register.

It is the client-management half of an OAuth2 deployment.
authentication/oauth2server is the protocol half — it implements OAuth 2.1 and
serves the six endpoints — and it already ships everything a client needs to
*use* a registration. What it does not ship, and deliberately, is a way to
administer one: its own Store models a registration as RFC 7591 dynamic
registration, which is written by an anonymous caller, bounded by an expiry, and
never listed by anybody. Its documentation says so, and names the alternative:

	/register is the one of the six a deployment can turn off, for one whose
	clients are administered somewhere else — created through a
	permission-gated API, seeded by a migration.

This is that API.

# The two arrangements, and why neither is a flag

A registration belongs to somebody, and there are two answers to who, both of
which real deployments need.

In the first, clients are infrastructure. Only an operator mints them, and one
governs how an *application* speaks to the service, on behalf of whichever
person happens to sign in. It belongs to no tenant and no user.

In the second, clients are personal. A person mints their own scoped
credentials, for a script, an integration, or a device, and nobody else may use
or even see them.

Both are expressed by two columns rather than a mode:

	Scope          which registry the row is in; tenancy.Global() is the
	               deployment's own
	BelongsToUser  the person who owns it; the empty string is nobody

So an infrastructural client is Global with no owner; a personal one is a scope
and an owner; a tenant's own service integration is a scope and no owner. Each
field that is filled in adds a predicate to [Client.Admits] and none of them
removes one, which is the property a boolean would have lost: there is no
setting here whose effect is to switch a check off.

# Only one of the two has a transport here

Both arrangements are this package's. Only the first has a shipped gRPC surface.

[Service.CreateClient] takes an owner and [Store.ListClientsForOwner] pages by
one, so a personal credential is minted, read and withdrawn through the Go API
exactly as an infrastructural one is. What authentication/oauth2clients/grpc
serves is the administered four — create, get, list, archive — every one of them
behind a grant.

That is a decision about consumers rather than about the model. The transport
briefly carried a self-service mirror of five operations, reachable behind no
permission at all on the theory that owning the row is the authorization; a diff
against the consumer the package was drawn from found it answered nobody. Five
permissionless RPCs are the surface with the most ways to be wrong, and the ones
here were being maintained and audited for no caller, so they went before
anything consumed the package. Nothing in the schema went with them — the column,
its index and the owner-keyed read are all still here — so the mirror is
re-addable without a migration, and the .proto records the shape it would have to
take.

A deployment that wants a personal-credential surface today writes it over
[Service] and [Store], and decides for itself whether "you may manage your own"
is a grant its policy names.

# Two tables, deliberately

This package's table is oauth2_registered_clients, and
authentication/oauth2serverstore's is oauth2_clients. A deployment runs both
migrations, so they cannot share a name: both are CREATE TABLE IF NOT EXISTS,
and the second would be a silent no-op followed by a store selecting columns
that are not there. dinnerdonebetter hit exactly this and worked around it by
prefixing the authorization server's four tables; a prefix is a consumer's
deployment decision, so the module ships two names instead of requiring one.

The two are joined by a seam rather than by a foreign key — see
[NewAuthorizationServerStore], which is an oauth2server.Store whose client half
reads this registry and whose credential half is the wrapped store's. A
deployment wiring that seam also passes oauth2server.WithDynamicRegistration(false),
so the anonymous endpoint that writes to the *other* table is not served at all.

# The one read with no scope in it, and what pays for it

[Store.ResolveClientID] takes no tenancy.Scope. Every other read here takes one
and there is deliberately no unscoped variant of any of them.

It is not an unscoped read. It is the read that *produces* a scope: client_id is
minted from crypto/rand, is globally unique by the index the schema declares,
and the row it finds is the only thing in the system that knows which registry
the client is in. /authorize names a client and nothing else, before anybody has
signed in, so there is no scope the caller could have passed. That is the
machinery carve-out the tenancy convention makes for a component servicing
itself — the same one webhooks' delivery worker gets — and it is narrow in the
way the convention asks.

What the carve-out costs is that resolving a registration cannot itself be an
authorization decision: it has no subject to decide about. [Client.Admits] is
what buys that back, and it runs in the two seams this package ships over
oauth2server's login step, because those are the only places holding both facts
— the client the request named, and the subject just authenticated. A
registration in one registry cannot authorize a person in another, and one owned
by a person cannot authorize anybody else.

# The secret

A client secret is returned exactly once, on the [IssuedClient] that
[Service.CreateClient] hands back, and stored only as a digest — produced by
oauth2server.Hash, which is the function that package's /token endpoint compares
against, so there is one encoding rather than two that can drift. There is no
read that recovers it, no hook that receives it, and no second call that
reissues it: a caller who loses a secret archives the registration and mints
another.

That the plaintext lives on its own type rather than on a field of [Client] is
the point of the type. A field populated once and empty on every other read is
the field that ends up in a log line, because nothing about its declaration says
it is usually absent.

It is also what keeps the store's writes safe to hand back. Every one of them
answers with the row it moved — see [Store] — and every one of those rows is a
read of the table, so it carries Client.SecretHash and could not carry a
plaintext if a caller wanted it to.

# What this package does not do

It does not authenticate anybody, mint a token, or serve an endpoint. It owns a
table and the operations over it, and the authorization server is a package away.
It also does not model public (secretless) clients: every registration here holds
a secret, and the seam reports AuthMethodClientSecret for all of them.
*/
package oauth2clients

//go:generate go run ./internal/queriesgen

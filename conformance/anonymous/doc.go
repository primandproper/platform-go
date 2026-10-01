/*
Package anonymous asserts what every RPC in this module does with a request
carrying no caller.

It is one suite over every surface rather than a test in each, because the
promise is the same sentence everywhere and the interesting half of it is the
exceptions. Most surfaces require a caller on every method. The rest do not,
and each of those declares which of its methods are the exception in its own
package — waitlists as PublicMethods, signin, passwordreset and passkeys as
AnonymousMethods — so this suite reads those declarations rather than carrying a
list that could disagree with them.

Those declarations are this module's, and a deployment may keep any declared
method to its staff anyway, by naming it in Seams.OperatorMethods. A reserved
method is then not reachable without a caller, and the assertion that it is
skips with the reservation named; that every other method refuses a request
with nobody on it holds either way.

# Why it enumerates rather than names

The methods come from each service's protobuf descriptor, so an RPC added after
this file was written is covered by it without anyone remembering to come back.
That is the same reading identity/grpc's own anonymous-caller test takes, and
the same one waitlists/grpc's permission test takes of its two lists; this is
that reading applied across the module and against a service somebody else
assembled.

# Both directions, and why the second one matters more

A method that requires a caller must refuse one that has none, with
codes.Unauthenticated. That is the direction everybody remembers.

A method that is deliberately anonymous must *not* be refused that way. That is
the direction nothing else checks, and it is the one with a user-visible
failure: the five public waitlists RPCs are a signup form, the catalog it
offers, the confirmation link in the mail that follows it, and the two ways off
the list. A deployment that puts
authentication in front of those has broken the page for exactly the people who
have no account yet, and every authenticated test in the suite still passes.

An anonymous method is asserted only to be reachable, never to succeed. Invoked
with an empty request it will usually fail on its input, which is correct and
uninteresting — what is being asserted is that it failed for a reason other than
who was asking.

# The HTTP half

dataprivacy, mediaregistry, operations and the OAuth 2.1 authorization server
are served over HTTP, and the same sentence is asserted of their routes: a
request with nobody on it is refused as 401. dataprivacy's confirm is not an
exception — it is a link in a mail, and a browser following it still arrives
with its session.

The authorization server is the one surface with exceptions, and it has them by
design: its discovery document and /authorize are where a client and a person
arrive before anybody is signed in, so a request with nobody on it must reach
them. The roster marks those routes public, one at a time, and they are
asserted the other way. /token and /revoke are not among them. They are reached
by a client authenticating as itself, so a request carrying no credential is
refused there as 401 like anywhere else.

The routes are listed rather than enumerated, because there is no registry an
HTTP surface's routes can be read out of: routing.Route is what registration
returns, not something a package can declare without a router. The list is kept
honest the way the gRPC roster is, from the other side:
TestHTTPRosterMatchesWhatEachSurfaceMounts mounts each surface's real handlers
and compares what they registered with the list, in both directions.

A POST carries a well-formed empty body, so a refusal cannot be about a body the
router failed to decode; and a path parameter names nothing, because a request
with no caller must be refused before anything is looked up.

# What a subject must supply

Seams.Anonymous, a connection carrying no caller, and for the HTTP half
Seams.AnonymousHTTP and a Subject.HTTP naming where the routes are. A subject that supplies none
skips, because there is no way to synthesize one: on a deployment that carries
credentials on the connection rather than per call, an anonymous request is a
different connection and not a call made without metadata.
*/
package anonymous

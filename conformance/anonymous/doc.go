/*
Package anonymous asserts what every RPC in this module does with a request
carrying no caller.

It is one suite over all twelve surfaces rather than a test in each, because the
promise is the same sentence everywhere and the interesting half of it is the
exceptions. Nine surfaces require a caller on every method. Three do not, and
each of those declares which of its methods are the exception in its own
package — waitlists as PublicMethods, signin and passwordreset as
AnonymousMethods — so this suite reads those declarations rather than carrying a
list that could disagree with them.

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
failure: the three public waitlists RPCs are a signup form, the link in the mail
that follows it, and the unsubscribe in that same mail. A deployment that puts
authentication in front of those has broken the page for exactly the people who
have no account yet, and every authenticated test in the suite still passes.

An anonymous method is asserted only to be reachable, never to succeed. Invoked
with an empty request it will usually fail on its input, which is correct and
uninteresting — what is being asserted is that it failed for a reason other than
who was asking.

# The HTTP half

dataprivacy, mediaregistry and operations are served over HTTP, and the same
sentence is asserted of their routes: a request with nobody on it is refused as
401. None of the three has an anonymous route — dataprivacy's confirm is a link
in a mail, and a browser following it still arrives with its session — so this
half has one direction.

The routes are listed rather than enumerated, because there is no registry an
HTTP surface's routes can be read out of: routing.Route is what registration
returns, not something a package can declare without a router. The list is kept
honest the way the gRPC roster is, from the other side:
TestHTTPRosterMatchesWhatEachSurfaceMounts mounts each surface's real handlers
and compares what Mount returned with the list, in both directions.

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

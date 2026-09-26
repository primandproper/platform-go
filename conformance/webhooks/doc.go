/*
Package webhooks is the outbound webhook surface's promises, assertable against
any subject that mounts it.

Eleven RPCs, and the consumer's settings page for their integrations is most of
them: register a URL, choose what it hears about, retire what it no longer
should, and roll the key it is signed with. What a consumer needs verified is
that an endpoint is stored as it was registered and comes back that way, that
a subscription set can be added to and retired from one member at a time, that
a signing key goes in and never comes out, and that nobody reaches an
endpoint, a subscription or a key that belongs to another tenant.

# Where the event types and the URL come from

Which events an application publishes is its catalog, and no suite can guess
one. So every event type named here is read from ListEventTypes — which is also
the promise that read makes, that anything it offers a subscription form is
something a subscription may name. A subject whose catalog is empty skips, and
the few assertions about a subscription set of more than one skip where the
catalog has fewer than two, both with the reason printed.

Unless the subject names another, every endpoint is registered at an address
in 192.0.2.0/24, the block RFC 5737 reserves for documentation. It needs no
DNS, passes this module's default URL check, and routes nowhere, so a deployment whose worker delivers to it —
because something else in their run published an event it subscribed to — sends
its request into nothing. Each endpoint is archived when its test ends, so a
deployment is not left delivering there.

A deployment that has replaced the URL check with an allowlist of its own hosts
refuses that address, which is right of it, and names one it accepts in
Seams.WebhookURL instead. A refused registration fails rather than skips, and
that is deliberate: the refusal a strict allowlist gives the documentation
address is the same InvalidArgument a regression refusing every endpoint gives
it, so a skip would turn most of this suite quiet on exactly the deployment it
exists to catch. The subject knows which of the two it is; the suite does not.

# What is here and what stayed behind

webhooks/grpc keeps everything about a server rather than about a call: what
NewServer refuses to be built from, the permission roster, the schema checks
that no response message can carry a keyring, the converters, and the roster
that pins Enqueue's absence. It keeps what varies how the server was built — a
URL checker a test injected — and the assertions that read the store beneath
the surface, because the store is the only vantage point from which a stored
or rotated key is observable at all.

It keeps the delivery log. ListAttempts answers with what the worker recorded
while delivering an event the dispatcher fanned out, and no client can bring
either about: a delivery is written only inside the transaction of the
application change it describes. An action for "publish an event" would still
leave the assertion waiting on a real worker's real delivery attempt, which is
a pacing test rather than a promise about a read. It keeps include_archived as
well, because whether a caller receives retired endpoints depends on grants a
subject does not describe.

# Both directions, deliberately

Each confinement assertion proves the caller reaches its own endpoint through
the same client before proving a neighbor cannot. A scope that resolved to
nothing would otherwise pass every absence here — and two of this surface's
refusals are not refusals at all: archiving somebody else's endpoint or
subscription answers OK, exactly as archiving nothing does, so the only way to
see that it was confined is to read the row afterwards as its owner.
*/
package webhooks

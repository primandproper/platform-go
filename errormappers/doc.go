/*
Package errormappers registers this module's domain tier with the two transport
registries, in one call.

primitives-go's errors/http and errors/grpc are primitives and map primitives,
and cannot import this module to reach any further. Everything above them maps
itself: every package internal/sentinelmatrix's Packages names exports an
HTTPMapper and a GRPCMapper beside its sentinels, and those its
ClientSafePackages names additionally export the refusals whose own wording a
gRPC status may carry. Neither list is written out here. A list in prose is
checked against nothing, and both of those are checked against the packages'
own source in both directions — this paragraph named six of fifteen for as long
as it did precisely because nothing could tell it had stopped being true. None
of them registers itself — a mapper that installs itself into a process-wide
registry by being linked in is a side effect a consumer cannot opt out of — so
something above has to, and before this package that something was either
service.Register or every one of those calls written out by hand.

Writing them out is worth removing because skipping one raises nothing. There
is no error, no log and no panic: ToAPIError falls through to ErrNothingSpecific
and "an error occurred", HTTPStatusForCode answers 500 for a code it does not
recognize, and MapToGRPC returns whatever default the caller passed. A subject
asking after somebody else's export gets a 500 where the read path went to the
trouble of returning a 404, and an expired session gets a 500 where sessions/http
promises a 401. Documentation was the entire mechanism, and the way a consumer
found out was somebody reporting a 500 on a link that had merely expired.

# The rule

The domain tier registers at the composition root. A package that owns sentinels
declares the mapper beside them, and the binary that assembles the service says
which mappers the process answers with — service.Register calls Register for a
service built from a service.Config, and a service assembled by hand calls it
itself, next to its own mappers.

operations/http.New is the one exception in this module, and it stays the only
one. It was made when that package was the only surface here that both answered
through errors/http and belonged to a package in the list, so constructing it is
already the statement that this process serves operation errors on the wire.
dataprivacy/http is a second such surface now and registers nothing: the
exception did not travel with the shape, because a mapper set assembled from
whichever handlers a binary happens to have constructed is not one a consumer can
read off a single call. The other two have nowhere to make the statement at all:
links ships no transport, and sessions/http ships one that never writes an error
response — its middleware logs a load failure and serves the request
anonymously, so the 401 is written by the consumer's handler through the
consumer's own ToAPIResponse call. Both paths registering is harmless, which is
the fourth section.

# Why it is not in service

Importing service to register those packages' mappers means paying for the whole config
tree — every sub-config, and every package each one wires, in both modules. A
consumer assembling three packages by hand should not import all of that to be
told what a link that has expired means on the wire. This package imports those domains and the two
registries and nothing else.

# What it does not do

It takes no injector, returns no error and is not a do registration. The two
registries are process-global — an error is mapped by whatever is linked into the
binary, not by whichever container resolved the handler — so calling it twice
appends a second copy of each mapper, which answers identically and is never
reached, because the registries stop at the first match.

# A consumer that already maps these sentinels

Stopping at the first match is also the rule for two mappers that disagree, and
that is the one a consumer migrating onto this call has to act on. Both
registries are append-only and consulted in registration order, and the first
mapper to claim an error decides the answer. Nothing compares an arriving mapper
against the sentinels already spoken for: a second mapper over
comments.ErrCommentNotFound is not refused, not merged and not warned about — it
is appended behind the first and never reached for that sentinel. First wins,
and first means whichever registration ran earlier rather than whichever module
the mapper came from.

An init function always runs before main does anything, so a consumer that wrote
its own mappers over this module's sentinels and registered them from init keeps
answering with those, whatever this call installs afterwards. That is the shape a
consumer arrives in from a release where this module registered nothing and
writing them out by hand was the only way to have them at all. Where the two
agree the cost is a comparison. Where they disagree the consumer's answer is the
one on the wire, and the disagreement is silent in both directions — the
package's own mapper is never consulted, and nothing reports that it was skipped.

So delete them. A mapper over a sentinel this module owns is now declared beside
that sentinel and moves with it, and a copy kept downstream is a second opinion
about what a refusal means that surfaces only once the two have drifted. What a
consumer keeps is its mappers over its own sentinels: those collide with nothing
here and stay registered wherever they already are, before or after this call.

A mapper over the platformerrors sentinels is a different case with the same
instruction. Both registries consult PlatformMapper ahead of every registered
mapper, so a consumer's opinion about platformerrors.ErrPermissionDenied has
been unreachable for as long as it has been registered — by ordering that
predates this package — and deleting it changes nothing on the wire.

The client-safe lists carry none of this. RegisterClientSafeSentinels builds a
membership test rather than an ordered chain, so a sentinel registered by both
sides costs one more comparison and reaches the client with the same words
either way.

The reasons list is the one place ordering returns, and it is worth knowing
before wiring a consumer's own. RegisterClientSafeReasons keeps the *first*
registration of a sentinel, because a later one reaching back and changing what
a client already branches on is the one thing a stable identifier may not do. So
a consumer who wants their own service name in the ErrorInfo domain, rather than
the package's, registers their list before calling Register rather than after.
Registering reasons also registers their sentinels as client-safe, so a consumer
substituting a list does not lose the messages.
*/
package errormappers

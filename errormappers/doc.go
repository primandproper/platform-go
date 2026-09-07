/*
Package errormappers registers this module's domain tier with the two transport
registries, in one call.

errors/http and errors/grpc are primitives and map primitives. Everything above
them maps itself: dataprivacy, identity, links, operations and sessions each
export an HTTPMapper and a GRPCMapper beside their sentinels, and links
additionally exports the redemption outcomes whose own wording a gRPC status may
carry. None
of them registers itself — a mapper that installs itself into a process-wide
registry by being linked in is a side effect a consumer cannot opt out of — so
something above has to, and before this package that something was either
service.Register or eleven calls written out by hand.

Eleven calls is a number worth removing because skipping one raises nothing. There
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

operations/http.New is the one exception in this module, and it is the only one
that can be. It is the only surface here that both answers through errors/http
and belongs to a package in the list, so constructing it is already the statement
that this process serves operation errors on the wire. The other three have
nowhere to make that statement: dataprivacy and links ship no transport at all,
and sessions/http ships one that never writes an error response — its middleware
logs a load failure and serves the request anonymously, so the 401 is written by
the consumer's handler through the consumer's own ToAPIResponse call. Both paths
registering is harmless, which is the fourth section.

# Why it is not in service

Importing service to register five packages' mappers means paying for the whole config
tree — every sub-config, and every package each one wires. A consumer assembling
three packages by hand should not import seventy to be told what a link that has
expired means on the wire. This package imports the five domains and the two
registries and nothing else.

# What it does not do

It takes no injector, returns no error and is not a do registration. The two
registries are process-global — an error is mapped by whatever is linked into the
binary, not by whichever container resolved the handler — so calling it twice
appends a second copy of each mapper, which answers identically and is never
reached, because the registries stop at the first match.
*/
package errormappers

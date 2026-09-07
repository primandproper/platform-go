/*
Package http translates errors into HTTP responses, in both directions.

The forward direction is a Go error to the response a client sees: ToAPIError
resolves it to an ErrorCode and a safe message, HTTPStatusForCode turns that code
into a status, and ToAPIResponse does both and builds the envelope. The reverse
direction is ErrorForCode, which a typed client uses to turn a code it received
back into the platform sentinel it stands for — so a caller of a remote service
matches ratelimiting.ErrRateLimited with errors.Is exactly as it would inside the
serving process. Only the codes that came from exactly one sentinel invert;
everything else reports nil, because guessing would hand a client an error to
branch on that nobody promised.

# Which direction the imports run

This package imports the packages whose sentinels PlatformMapper maps —
circuitbreaking, database, idempotency, ratelimiting, requestsigning, and the two
search indexes. Every one of them is a primitive, and that is the whole of the
list on purpose: this package is a primitive too, so nothing built on those may
appear in it.

The tier above maps itself. dataprivacy, identity, links, operations and
sessions each export an HTTPMapper holding the cases for their own sentinels, and the import
runs from them to here — they need ErrorCode, this package needs nothing of
theirs. Anything else with a sentinel a client should act on does the same:
declare a mapper beside the sentinel, and register it.

Registration is what makes a mapper reachable. RegisterHTTPErrorMapper appends
one; ToAPIError consults PlatformMapper first, then registered mappers in
registration order. This module's five are one call — errormappers.Register,
which service.Register makes for a service built from a service.Config and a
service assembled by hand makes itself, alongside the mappers it declares for its
own sentinels. There is deliberately no init doing it: a mapper that installs
itself into a process-wide registry by being linked in is a side effect a
consumer cannot opt out of.

# Messages are deliberately uninformative

An unmatched error resolves to the neutral code and "an error occurred" — never
to a domain-specific code, so a failure inside a payment path cannot surface as
one blaming the database. Matched errors get a fixed message that names no limit,
no permission, and no quota: those are useful to an operator and equally useful
to someone probing an endpoint for the shape of the policy behind it. What the
handler already knows it can render itself, next to an upgrade link or in a log,
rather than putting it on the wire.

Unmapped codes resolve to 500. That is the direction to fail in: a code nobody
gave a status keeps its server-side failure looking like one.
*/
package http

//platform:transport mapping: a sentinel to a status code, and back

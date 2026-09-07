/*
Package grpc translates errors into gRPC statuses, and back again on the other
side of the wire.

MapToGRPC resolves a Go error to a codes.Code, consulting PlatformMapper first
and then whatever mappers domains have registered. The interceptors apply that
mapping to whatever a handler returns, and DecodeErrorFromStatus reconstructs the
original error on the client, so errors.Is keeps matching across a service
boundary.

The sentinel set PlatformMapper maps is the same one errors/http's maps,
deliberately. A service exposing both transports would otherwise answer one
failure with a considered status on one and codes.Unknown on the other, and which
the client got would depend on how it happened to connect. Each domain mapper
holds the same property for its own sentinels.

# Which direction the imports run

This package imports the packages whose sentinels PlatformMapper maps —
circuitbreaking, database, idempotency, ratelimiting, requestsigning, and the two
search indexes. Every one of them is a primitive, and that is the whole of the
list on purpose: this package is a primitive too, so nothing built on those may
appear in it.

The tier above maps itself. dataprivacy, identity, links, operations and
sessions each export a GRPCMapper holding the cases for their own sentinels, and the import
runs from them to here. Anything else with a sentinel a client should act on does
the same: declare a mapper beside the sentinel, and register it.

Registration is what makes a mapper reachable. RegisterGRPCErrorMapper appends
one; MapToGRPC consults PlatformMapper first, then registered mappers in
registration order. RegisterClientSafeSentinels is the companion for the other
half of the answer — whether a sentinel's own words reach the client, described
below. This module's four are one call that does both, errormappers.Register,
which service.Register makes for a service built from a service.Config and a
service assembled by hand makes itself, alongside the mappers it declares for its
own sentinels. There is deliberately no init doing it: a mapper that installs
itself into a process-wide registry by being linked in is a side effect a
consumer cannot opt out of.

# What reaches the client, and what that assumes

The status message is derived from the code rather than from the error's text,
which is the whole wrapped chain and can name tables, connection strings, and the
permission that was missing. The exception is a list of platform sentinels
documented as client-safe, whose own wording tells a caller what to do
differently without describing the policy behind the refusal, plus whatever a
domain has added to it with RegisterClientSafeSentinels.

The full error does still cross the wire, encoded in the status details, and that
is what makes the error reconstructable on the far side. It is meant for trusted
service-to-service traffic. A server running these interceptors and reachable by
untrusted clients needs that detail stripped at the edge — otherwise the internal
error text this package took care to keep out of the message is available in the
details of the same response.
*/
package grpc

//platform:transport mapping: a sentinel to a gRPC code, and back

/*
Package grpc serves the issue report queue over gRPC.

It is the gRPC service over
[github.com/primandproper/platform-go/v14/issuereports]'s Store, the converters
between the generated messages and its types, a typed client, the default
permission fragment a consumer composes into its policy, and the seam that says
whose reports a caller may name.

It is imported as issuereportsgrpc.

	srv, _ := issuereportsgrpc.NewServer(store, client, extractPrincipal, targets)
	// []grpcserver.RegistrationFunc{srv.RegisterOn}

The pattern a domain surface here follows is written down once, in
identity/grpc's documentation, and this package follows it rather than restating
it. What is below is what is particular to this one.

# Ten of eleven

issuereports.Store has eleven methods and this service serves ten. The absence is
DeleteReportsByReporter, which destroys every report one person filed, and it is
the shape identity/grpc's pattern names first: a write whose caller is already
inside the process's own transaction is not an RPC. It runs on the caller's
database.Tx so that a subject's reports and the rest of their footprint commit or
roll back together, and an erasure that committed while the rest of the run
failed is a subject who has been told they were forgotten and half was. It is
reached through issuereports/privacy, from the consumer's own dataprivacy run.
roster_test.go is where that ruling lives as a test rather than as a sentence.

# The compare-and-set is the reason this surface exists

[Server.TransitionReport] carries both the status the caller believed the report
held and the one it should move to, and the statement requires the row to still
hold the first. Without it, two triagers resolving the same report both succeed,
the second note overwrites the first, and nothing anywhere says so.

That guard is worth more over a wire than it was in process. The window between
the read a decision was made from and the write that records it is a screen and a
person wide here, so the conflict is the ordinary case rather than the rare one —
and issuereports.ErrStatusConflict is a refusal a client can act on: re-read, and
decide about the status the report is in now. It reaches them as codes.Aborted,
which is gRPC's code for exactly that, and it is registered client-safe so the
sentence travels with it.

# Authorization has two halves, and this table has two audiences

[Permissions] is the first half: a grant on the method, evaluated by
authorization/grpc's interceptor from the full method name and the caller's
grants, before the request body has been looked at.

[ReportAuthorizer] is the second, and it is here because it cannot be there. A
person files a report and reads back what they filed; a triager pages the queue
and works it. Two RPCs take their target from the request or from the row —
GetReport and ListReportsByReporter — and asking whether the caller has any
standing in that person's words means holding the row or the name, which an
interceptor holding the grants has no handle to do.

There is no default, unlike identity/grpc's, and [ReportAuthorizer] says why: the
question has two halves and this package can only answer one. Whether a report is
the caller's own is a column it owns; whether this caller is a triager is a grant
it cannot see. [ReporterAuthorizer] is the narrow half, exported so a deployment
either passes it or composes it rather than re-deriving it.

The other eight RPCs are not row-gated, and that is a ruling rather than an
omission. CreateReport files in the caller's own name. The four queue listings
and the three lifecycle writes are the triager's, and their target is the queue
rather than a person — a grant to page every report in the tenant is the answer
to "whose", spelled where a consumer's policy can audit it.

# What comes off the principal, and what a request may say

The tenant, always, and it is never read off a request field: a scope a client
could name is a cross-tenant read hiding behind one. The reporter on a write,
likewise — one a client could name is a report filed in somebody else's words —
so the creation input reserves the field, and the only request in the schema that
carries a reporter is the one that names whose list to page.

Both absences are `reserved` in the .proto rather than merely undocumented, which
is what makes them a schema protoc enforces in a consumer's fork of the file as
well as here. schema_test.go asserts it.

# Errors

This package registers nothing. issuereports.HTTPMapper and
issuereports.GRPCMapper live beside the sentinels they map, and installing them
is the composition root's one call — errormappers.Register, which service.Register
makes for a service built from a service.Config and a service assembled by hand
makes itself. Without it, a report somebody else already resolved arrives as
codes.Unknown.

Every failure here is one grpcerrors.PrepareAndLogGRPCStatus with codes.Internal
as the *default*. The encoding interceptor re-runs the registered mappers over
the preserved chain, so the mapper wins over the guess made at the call site,
which is why no handler on this surface switches on a sentinel. The codes passed
as answers rather than defaults are the three nothing maps: a request that named
no input, a caller with no principal, and a refusal from the authorizer.
*/
package grpc

//platform:transport resource surface: the report queue and its guarded lifecycle — over `issuereports.Store`

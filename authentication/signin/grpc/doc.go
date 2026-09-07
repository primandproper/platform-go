/*
Package grpc serves the sign-in service over gRPC.

It is imported as signingrpc, and it is seven RPCs over
[github.com/primandproper/platform-go/v14/authentication/signin.Service]: two
doors, two reads and three credential writes. Each method converts, calls one
thing, and converts back. There is no orchestration here — anything that had to
happen in a transaction happened one layer down, where the transaction is.

# The two seams, and why there are two

Every other resource surface in this module reads who is calling off one seam,
because every request to it arrives with somebody on it. This one has three RPCs
that by definition do not: a caller signing in has not signed in yet.

So the scope — whose directory this is — comes off a [ScopeResolver] the
consumer supplies, which reads it from the connection: a host header, a piece of
metadata, a subdomain, or nothing at all. The default resolves
[github.com/primandproper/platform-go/v14/tenancy.Global], which is exactly what
a single-tenant deployment wants and is a directory with no users in it for a
multi-tenant one that forgot — a sign-in that refuses everybody rather than one
that signs them into somebody else's tenant.

The four authenticated RPCs read the caller off a [PrincipalExtractor], which is
identity/grpc's, aliased rather than redefined. A consumer writes one extractor
and both services use it; two would be two chances to disagree about who is
calling.

# Errors

A method here hands the service's error back with a default code and does not
switch on sentinels. What a refused sign-in means on the wire is decided once,
by signin.GRPCMapper, and a switch here would be a second copy of that decision
free to drift from it.

Every failure is one call, and there is deliberately no local helper wrapping it:

	grpcerrors.PrepareAndLogGRPCStatus(err, op.Logger(), op.Span(), codes.Internal, "updating a password")

It logs, traces, maps the code and hands back an error that is still the sentinel
the service returned, so the encoding interceptor has a chain to encode. The code
passed is the default for an error no mapper claims, and the message is the
description unless a registered client-safe sentinel has better words — which
this service needs more than most, since four of its refusals share
codes.PermissionDenied and three share codes.FailedPrecondition, and a client in
a language that cannot read the encoded details has only the message to tell
them apart.

That mapper reaches a client only once it is registered, which is
errormappers.Register — one call, made by service.Register for a service built
from a service.Config and by a hand-assembled service itself. This constructor
deliberately does not make it: a mapper that installs itself by a component
being constructed is a process-wide side effect a consumer cannot opt out of.
Without it every refusal below arrives as codes.Unknown, which for a sign-in
means a client cannot tell "wrong password" from "the database is down".

# What a consumer still owes

Transport security. Two of these RPCs carry a plaintext password and one answers
with a live second-factor secret; the schema's own documentation says so at
greater length. Nothing here checks that the connection is encrypted, because
nothing here can.

A rate limit. This package counts sign-in attempts and refuses none of them:
lockout, backoff and captchas are decisions in front of this service, and
signin.Hooks.AfterFailedSignIn is what informs them.
*/
package grpc

//platform:transport resource surface: sign-in and the credentials a person changes about themselves — over `signin.Service`

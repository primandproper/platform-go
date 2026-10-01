/*
Package grpc serves the sign-in service over gRPC.

It is imported as signingrpc, and it serves
[github.com/primandproper/primitives-go/v2/authentication/signin.Service]: a
registration and the two that finish one, the two password doors and the
passwordless pair, the exchange that keeps a login alive, the two ways out and
the listing of logins between them with its one way to end one, two reads and
three credential writes. Each method converts, calls one thing, and
converts back.

That is SignInService, and nothing on it is permissioned. [Server] serves a
second service beside it, SignInAdministrationService, which is what an
operator does to somebody else's logins — list them, end one, end all of them
— and every method on that one requires a permission [Permissions] declares.
No role holds any of them by default: who is an operator is the deployment's
policy, and this package names the grants that policy hands out rather than
deciding who holds them. An operator's end is reported to
signin.Hooks.AfterRevokeSignIns as signin.RevocationOperator, with the caller
as the actor, so an audit trail tells it apart from the person's own
sign-out. Its directory is the operator's own, off the principal rather than
the [ScopeResolver] below: the user it acts on is named by the request, so a
connection resolving to some other directory must not make that directory's
users the operator's to act on.
There is no orchestration here — anything that had to happen in a transaction
happened one layer down, where the transaction is.

# The two seams, and why there are two

Every other resource surface in this module reads who is calling off one seam,
because every request to it arrives with somebody on it. This one has RPCs that
by definition do not: a caller signing in has not signed in yet, neither has
somebody signing up or the registrant answering the link that was mailed to
them, and a caller signing
out is holding the credential that names the login rather than a live token.

So the scope — whose directory this is — comes off a [ScopeResolver] the
consumer supplies, which reads it from the connection: a host header, a piece of
metadata, a subdomain, or nothing at all. The default resolves
[github.com/primandproper/primitives-go/v2/tenancy.Global], which is exactly what
a single-tenant deployment wants and is a directory with no users in it for a
multi-tenant one that forgot — a sign-in that refuses everybody rather than one
that signs them into somebody else's tenant.

The authenticated RPCs read the caller off a
[github.com/primandproper/platform-go/v14/callers.PrincipalExtractor], which
resolves a [github.com/primandproper/platform-go/v14/callers.Principal]. Those
are one package for the whole module rather than an interface per surface: a
consumer writes one extractor and every service here uses it, where two would be
two chances to disagree about who is calling.

For a token the sign-in service minted, that extractor is this package's:
[NewPrincipalExtractor] verifies the token, resolves the caller through
identity's directory, and confers service roles only on a token minted through
the administrative door. [PrincipalExtractor.UnaryServerInterceptor] resolves
each request's caller once, against an [AuthenticationRequirements] table that
[RequireAuthentication] declares this service's methods onto. Tokens of any
other kind reach it through [WithFallback]. [PrincipalExtractor.Extract] answers
only for a request that interceptor or [PrincipalExtractor.HTTPMiddleware]
resolved, so installing them is not optional: a server with neither sees
nobody.

A caller an operator has forced to change their password is still signed in
and still resolved, and [PasswordChangeGate] is what holds them at the form:
installed behind the authentication interceptor, it refuses every call but
[PasswordChangeMethods] and the deployment's own [WithAllowedMethods] with
signin.ErrPasswordChangeRequired until the change is made. [PrincipalExtractor]
runs one inside its interceptors and middleware by default;
[WithoutPasswordChangeGate] is the deliberate no.

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
this service needs more than most, since several of its refusals share
codes.PermissionDenied and several more share codes.FailedPrecondition, and a
client in a language that cannot read the encoded details has only the message to tell
them apart.

That mapper reaches a client only once it is registered, which is
errormappers.Register — one call, made by service.Register for a service built
from a service.Config and by a hand-assembled service itself. This constructor
deliberately does not make it: a mapper that installs itself by a component
being constructed is a process-wide side effect a consumer cannot opt out of.
Without it every refusal below arrives as codes.Unknown, which for a sign-in
means a client cannot tell "wrong password" from "the database is down".

# Where you're signed in

ListSignIns and ListSignInsForUser answer each login with what signin knows
about it — when it began and last refreshed, the account, the operator behind an
impersonation, and the credential kind that began it — and with the attributes
a [SignInAnnotator] answers for it. The division is the one signin's package
documentation draws: the platform lists the logins and records how each one
happened, and the consumer annotates the device. [WithSignInAnnotator] is the
seam, asked once per listing for every family in it; a server built without one
lists every login with no attributes, and one whose annotator fails answers the
RPC with that error rather than with half the screen.

# What a consumer still owes

Transport security. Several of these RPCs carry a plaintext password and one
answers with a live second-factor secret; the schema's own documentation says so
at greater length. Nothing here checks that the connection is encrypted, because
nothing here can.

A rate limit. This package counts sign-in attempts and refuses none of them:
lockout, backoff and captchas are decisions in front of this service, and
signin.Hooks.AfterFailedSignIn is what informs them. Sign-up is the same
posture as sign-in rather than a more dangerous one: Register is anonymous and
open by default, the deployment's signin.RegistrationPolicy decides who it
admits, and how often it may be called is a decision in front of it like the
rest. A deployment that wants no sign-up at all closes the door with
[WithoutOpenRegistration], which a deployment built from signincfg sets by
naming Registration.Closed (SIGN_IN_REGISTRATION_CLOSED, under service) rather
than by writing it.
*/
package grpc

//platform:transport resource surface: sign-in and the credentials a person changes about themselves — over `signin.Service`

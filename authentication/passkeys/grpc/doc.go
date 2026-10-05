/*
Package grpc serves passkeys over gRPC: enrolling one, signing in with one,
and the list and archive a settings page offers.

It is imported as passkeysgrpc, and it serves
[github.com/primandproper/platform-go/v15/authentication/passkeys.Service].
Each method converts, calls the service, and converts back. The one thing it
adds is the reason it exists: a finished login goes on to a token.

# A passkey sign-in is a sign-in

Before this package, a consumer that signed people in with
authentication/signin and then added passkeys had two token stacks the moment
it did: signin's for a password, and one it minted itself for a passkey, with
its own lifetimes, its own claims and no refresh token family. FinishLogin
ends that. It hands the login the service proved to a [PrincipalIssuer] —
signin.Service, whose IssueForPrincipal is the door for a subject another
credential has proven — and answers with signin.v1's IssuedToken. The token is
the one a password sign-in gets, it is renewed and ended through
SignInService like any other, and signin's hooks see it stamped
[CredentialKind].

IssueForPrincipal has no transport of its own, and still does not: its caller
vouches for who is signing in, so it belongs behind a verification of the
credential and never behind a request that names a user. FinishLogin is that
verification, and it is the only path here that reaches the issuer.

A passkey is two factors only when the authenticator verified the person, so
signin.MultiFactor is passed only then. A key tap alone is one factor, and a
person holding a proven second factor is asked for it — FinishLogin carries a
totp_code for that, read as a password sign-in reads it.

# The two seams, and the third

The login half is anonymous, so the scope comes off a [ScopeResolver] as it
does for authentication/signin/grpc, and a deployment running both gives them
the same one. The self-service half reads the caller off a
[github.com/primandproper/platform-go/v15/callers.PrincipalExtractor], and
takes its subject from nowhere else.

The third is the [UserHandle]: the WebAuthn handle a signed-in user enrolls
under, derived from their user ID. It defaults to [UserIDHandle], the ID
itself, and a deployment whose UserResolver reads handles some other way
names its own. The service checks the two agree, so a mismatch refuses an
enrollment rather than filing a passkey under somebody else.

# Errors

A method here hands the service's error back with a default code and does not
switch on sentinels. The codes come from passkeys.GRPCMapper and, for a
proven subject sign-in will not admit, signin.GRPCMapper — both registered by
errormappers.Register, which this constructor deliberately does not call.

# What a consumer still owes

Transport security, since FinishLogin answers with a live token. A rate limit
on the login half, which passkeys.Hooks.AfterFailedPasskeyLogin informs. And
the enrollment gate: whether a live session is enough to add a way into an
account is passkeys.EnrollmentGate's question, and an impersonating operator's
session is one a gate may well refuse.
*/
package grpc

//platform:transport resource surface: enrolling a passkey and signing in with one, into sign-in's token — over `passkeys.Service`

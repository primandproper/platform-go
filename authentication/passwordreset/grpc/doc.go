/*
Package grpc serves the password reset flow over gRPC.

It is imported as passwordresetgrpc, and it is three RPCs over
[github.com/primandproper/platform-go/v14/authentication/passwordreset.Service]:
ask for a link, check that a link is still good, spend it. Each method converts,
calls one thing, and converts back. There is no orchestration here — the
redemption, the password write and the withdrawal of every other link that person
held are one transaction, and that transaction is one layer down.

# The one seam, and why there is only one

Every other resource surface in this module reads who is calling. This one never
does, on any method, because the premise of the flow is somebody who cannot sign
in: a caller able to prove who they are would be changing their password through
[github.com/primandproper/platform-go/v14/authentication/signin] instead.

So the scope — whose directory this is — comes off a [ScopeResolver] the consumer
supplies, which reads it from the connection: a host header, a piece of metadata,
a subdomain, or nothing at all in a single-tenant deployment. The default
resolves [github.com/primandproper/primitives-go/v2/tenancy.Global], which is
exactly what a single-tenant application wants and is a directory with no users
in it for a multi-tenant one that forgot — a reset flow that mails nobody rather
than one that mails somebody else's user.

A consumer running this beside signin/grpc gives both the same resolver, and the
failure when they differ is quiet rather than loud: a link mailed from one
directory and a sign-in attempted in another both succeed at what they do and
find nobody at the end.

# The silence this surface owes

RequestPasswordReset answers identically for an address somebody holds and one
nobody does, and the service holds its own answer to a floor so the two are not
distinguishable by timing either. This package adds nothing to that and takes
nothing away: the response message is empty, and there is no code path here that
varies with what the service found.

A consumer's own edge owes the same silence. Answering a known address with a 200
and an unknown one with a 404 — or logging the difference somewhere a support
tool can read it — puts back the account enumerator the floor exists to prevent,
in a place nothing in this module can reach.

The other two RPCs owe the opposite, and the contrast is deliberate. A link that
cannot be spent is expired, already used, or was never a link, and all three are
told apart. The secret is high-entropy, so learning which one happened requires
already holding the token — and somebody with a day-old link is owed the
difference between "that link expired" and "that link is not a link". See
passwordreset.ClientSafeSentinels, which is what puts each sentinel's own words
on the wire, and passwordreset.ClientSafeReasons, which gives each an identifier
a client branches on. A fourth outcome shares the list: a password the
service's policy refused, which leaves the link live and asks for another
password rather than another link.

# Errors

A method here hands the service's error back with a default code and does not
switch on sentinels. What a refused reset means on the wire is decided once, by
passwordreset.GRPCMapper, and a switch here would be a second copy of that
decision free to drift from it.

That mapper reaches a client only once it is registered, which is
errormappers.Register — one call, made by service.Register for a service built
from a service.Config and by a hand-assembled service itself. This constructor
deliberately does not make it: a mapper that installs itself by a component being
constructed is a process-wide side effect a consumer cannot opt out of. Without
it, all three refusals above arrive as codes.Unknown and a client cannot tell an
expired link from a database that is down.

# What a consumer still owes

Transport security. One of these RPCs carries a plaintext password and another
carries the secret that authorizes setting one.

A rate limit, in front of RequestPasswordReset, and it is not optional: it is the
one RPC in this module that sends mail on behalf of somebody who has proven
nothing at all.

The mail itself, which is passwordreset.Mailer, and the link's shape — this
service mints the secret and never renders a URL, because where a reset form
lives is the consumer's.
*/
package grpc

//platform:transport resource surface: ask for a reset link, check one, spend one — over `passwordreset.Service`

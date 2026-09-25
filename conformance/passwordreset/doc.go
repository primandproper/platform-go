/*
Package passwordreset is the way back in for somebody who cannot sign in, as
promises assertable against any subject that mounts it.

Three RPCs, all of them answered to nobody, and two promises that pull against
each other. The first is the point of the surface: a link that was mailed
restores a way in, once, and withdraws every other link its holder was sent.
The second is the silence it owes everyone else — a request for an address
nobody registered is answered exactly as one for an address somebody did, so
the form cannot be used to learn who has an account. Only the person holding a
link is told which of the three ways it failed, because learning that requires
already holding a secret nobody else has.

# The secret, and the directory

The secret never crosses a response; it reaches the person through the
deployment's mailer. So every assertion that redeems one reads it through the
PasswordResetToken action — the mail a person would have opened — and a subject
that cannot say what it mailed skips them, with the reason printed.

Every request here arrives with nobody on it, so whose directory a reset is
against comes off the connection rather than a principal, and service.New leaves
that at the global directory. The callers these assertions reset are therefore
asked for in tenancy.Global, which is where a single-tenant deployment keeps
everybody. A subject that has no callers there declines and the assertions skip;
one whose connection resolves a tenant of its own mints the caller wherever its
connection would place a request.

# What is here and what stayed behind

authentication/passwordreset/grpc keeps its construction and contract tests —
NewServer refusing a nil service, every method declared public — and its scope
resolver tests, which build a server with a resolver of their own and so vary
how it was built. What a deployed service cannot be made to do is resolve a
request it cannot place.
*/
package passwordreset

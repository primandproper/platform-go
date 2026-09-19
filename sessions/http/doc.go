/*
Package http binds a sessions.Store to a signed cookie and to net/http.

The identifier goes out through cookies.Manager, so it inherits that package's
signing, encryption, HttpOnly, Secure, and SameSite handling. Nothing else about
the session leaves the server.

	manager, _ := sessionshttp.NewManager(store, cookieManager)

	router.Use(manager.Middleware())

	// in a handler
	session, ok := sessionshttp.SessionFromContext[Principal](req.Context())

# The five calls that matter

Issue, after authenticating — never before, since a session issued to an
unauthenticated visitor is the thing a fixation attack plants.

RenewFor, at a sign-in that has state to carry across it. It rotates the
identifier and hands the session to whoever signed in, in one call: rotating
alone would leave the signed-in session held by nobody, which is a session no
security page lists and no "sign out everywhere" reaches.

Renew, immediately after any other privilege change — a step-up authentication
on a session that already has a holder. It rotates the identifier and carries
the payload, the holder and the metadata across; an error from either renewal
means the old identifier may still work, so the privilege change should be
refused rather than completed.

Save, to write a payload back.

End, to sign out. It clears the cookie whatever else happens, so a client never
leaves holding a usable one, and it treats a request with no session as a
success — sign-out is idempotent.

# The middleware decides nothing

It attaches a session when there is one and passes the request through when
there is not. Whether an anonymous request deserves a 401, a redirect, or a
perfectly good anonymous page is a per-route question this package cannot
answer, so handlers ask SessionFromContext and answer it themselves.

A store that cannot be read is treated the same as no session: logged, traced,
and served as anonymous. Every handler that requires a session then refuses,
which is the conservative direction, and a store outage does not turn every page
on the site into a 500.

# One answer for every bad cookie

An absent cookie, a cookie that does not verify, a cookie carrying an identifier
that names nothing, and a session that has expired all report
sessions.ErrNotFound. A client cannot tell from the response which of those it
managed, which is the point — the alternative is an oracle that says whether a
guessed identifier existed.

sessions.HTTPMapper maps that onto 401 via errors/http's
ErrFetchingSessionContextData, so a handler that returns it unmodified produces
the right status — once something has registered the mapper. Nothing here does:
this package writes no error response at all, since Middleware logs a load
failure and serves the request anonymously, and the 401 is written by a
consumer's handler through the consumer's own ToAPIResponse call. So the
registration is the composition root's, and it is one call for the whole domain
tier — errormappers.Register, which service.Register makes for a service built
from a service.Config and a service assembling itself by hand makes once, at
startup.

# A session is not a licence

The middleware answers "is there a session and whose is it". It does not answer
"may that person still be here", and a handler should not add a
principal.User.AccountStatus check of its own to make up the difference:
identity.Store.GetPrincipal refuses a user whose status does not admit sign-in
with identity.ErrSignInNotAdmitted, so a consumer that resolves the principal
behind a session through that read is already refusing a banned user on the next
request, on every surface at once. A check written here would be a second copy of
that rule, free to drift from it and reviewed by nobody.

What a session row does outlive is the ban, because nothing in identity holds a
handle on this store. A suspended user's row sits here until its absolute
deadline, listed on their own security page and counted by a "sign out
everywhere". Clearing it is the consumer's, from
identity.Hooks.AfterUpdateUserAccountStatus, with sessions.Store[T].RevokeAll over
a Holder naming the user. That revocation cannot join the transaction the status
write ran in — this store may be Redis — so the hook's documentation is where the
two ways of being wrong about the ordering are set out.

# Cookie lifetime

A session cookie's MaxAge is derived from the store's absolute timeout, not from
Session.ExpiresAt. ExpiresAt moves with the idle deadline, so a cookie cut to it
would expire in the browser after one idle window even for a user who never
stopped clicking, and the client would have discarded the only way to name a
session the server still holds.

Give cookies.Config a Lifetime at least as long as the absolute timeout.
securecookie refuses to decode a value older than that lifetime, so a shorter
one caps every session regardless of what the store thinks, and does it in a way
that looks like sessions randomly failing to load.

# What this package is not

It is not a session API. There are no routes here for listing a user's sessions
or revoking one from another device; those are an application's, over an
application's types. What this package ships is the binding between a store and
a cookie, and a cookie's signing, encryption, HttpOnly, Secure and SameSite are
security decisions this module has already made — leaving them to each consumer
is how they get made differently in each one.

The module README's "Transports" section is where that line is drawn for the
module as a whole.
*/
package http

//platform:transport binding: a signed cookie, whose security properties are ours

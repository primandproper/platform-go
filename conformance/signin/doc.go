/*
Package signin is the credential surface's promises, assertable against any
subject that mounts it.

Fifteen RPCs, and most of them answer somebody who is not signed in: the doors
a person signs in through, the links that finish a registration, the exchange
that keeps a login alive and the button that ends one. What a consumer is owed
about them is two things that pull against each other. The first is that the
right credential gets in — a registrant can sign in once their address is
proven, a rotated refresh token names the same login, a mailed link is a way in
for somebody with no password. The second is that every wrong credential gets
one answer: a wrong password, an unknown handle, a replayed refresh token, a
dead verification link and a dead sign-in link are all Unauthenticated with the
same message and the same reason, because an answer that differed would tell
whoever is guessing which half of the guess was right.

So every refusal here is asserted beside the success it is the other half of.
A surface that refused everybody would pass "a wrong password is refused" and
fail "the right one signs in", and only the pair says anything.

# Reasons, and why they are asserted

docs/client-contract.md makes the reason a client branches on part of this
surface's contract: a google.rpc.ErrorInfo detail in signin's own domain,
chosen once and never reworded, where the message may be. So a refusal is
asserted by its code and, where the contract lists one, its reason — and never
by a decoded Go sentinel, which is a detail of this module's client rather than
something a client in another language can read. The message is checked only
where the promise is about the message: that two refusals are word for word the
same.

The reason half is asserted unless the subject's Seams.ErrorReasonsStripped
says its edge drops reasons. The contract promises that a reason survives an
edge that strips the encoded chain, so asserting it is the default; a
deployment that rebuilds a status without its details breaks that promise, and
says so to have the codes checked and each skipped reason printed rather than a
suite that fails on a detail it does not send.

# Whose directory, and the three actions

Every request to the anonymous doors arrives with nobody on it, so whose
directory it is against comes off the connection rather than a caller, and
service.New leaves that at the global directory. The people these assertions
register and sign in are therefore placed in tenancy.Global through a
registrar asked for there, which is where a single-tenant deployment keeps
everybody — the reading the passwordreset suite takes of the same question.

Three secrets reach a person through mail rather than a response, and each
assertion that needs one reads it through an action: VerificationToken for the
link a registration mails, MagicLinkToken for a sign-in link, and
PasswordResetToken, which is how an existing caller is given a password the
suite knows without the suite writing one. A subject that cannot say what it
mailed skips those assertions with the reason printed.

Refresh tokens are optional — a deployment built without a store answers a
sign-in with none, which the client contract calls a valid shape — so the
assertions about rotation and signing out skip, with the reason printed, when
the sign-in they start from carried no refresh token.

# Who a call is made as

Register is the one call here a deployment may reserve. It requires a caller
and registers somebody else, which is the directory's administered door by
another name: a deployment whose public sign-up is a door of its own may keep
this one to a service role, as it may identity's Register. So every
registration here is made by an operator in the global directory where the
subject reserves it, and by an ordinary caller there where it does not, as
conformance.ReservableMethods describes.

The rest is made as an ordinary caller or as nobody, and each is a promise: a
signed-in caller reads their own status through GetAuthStatus, and every door,
link and exchange answers somebody with nobody on the connection.

# What is here and what stayed behind

authentication/signin/grpc keeps what is about a server rather than a call:
NewServer refusing what it cannot be built from, the permission rosters and
AnonymousMethods, the reserved scope name in the schema, and the converters. It
also keeps everything that varies how the service was built — a token issuer
made to fail, a scope resolver made to refuse, administrative roles named or
not, a service built without a refresh token store — and the one refusal that
needs a banned user, which no action here brings about. What refuses a request
with nobody on it is conformance/anonymous's, for every RPC at once.
*/
package signin

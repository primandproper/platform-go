/*
Package signin is the credential surface's promises, assertable against any
subject that mounts it.

Most of this surface answers somebody who is not signed in: the doors
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

Two secrets reach a person through mail rather than a response, and each
assertion that needs one reads it through an action: VerificationToken for the
link a registration mails, and MagicLinkToken for a sign-in link. A handle is
mailed too, and the HandleReminder action reads it. A subject that cannot say
what it mailed skips those assertions with the reason printed. A registration
answering an invitation is asserted through the link its sender copied, which
exists only where the deployment returns an invitation's token to its sender
and says so in Seams.InvitationTokenReturned; elsewhere it skips.

Refresh tokens are optional — a deployment built without a store answers a
sign-in with none, which the client contract calls a valid shape — so the
assertions about rotation and signing out skip, with the reason printed, when
the sign-in they start from carried no refresh token.

# Calling as somebody the suite signed in

An assertion about a signed-in person's own credentials — changing a
password, enrolling or replacing a second factor, listing and ending their
logins, a forced change — needs a caller whose every credential the suite
chose. A caller Seams.NewSubject minted is the deployment's, and may hold a
proven second factor the suite cannot read. So those assertions register
somebody, verify them, sign them in with the password they typed, and call as
the token that answered, through Seams.SignedIn: the conformance face of the
Authorizer seam in docs/client-contract.md, and a skip where the subject
supplies none. The caller is held to the calls it declares, as every minted
caller is.

A deployment that checks each access token's login on every request says so
in Seams.ImmediateRevocation, and is then held to it: an access token stops
working on the request after its login is ended by name, while another login
the same person holds goes on working. A deployment that does not is one whose
sign-out takes effect within one access-token lifetime, and the assertion
skips.

The administrative door is asserted the same way, once the registrant is
granted Seams.Roles.Administrator, and skips where the subject names none.

A forced change is asserted twice: that it is reported and still signs in, and
that every other call is refused with PASSWORD_CHANGE_REQUIRED until it is
made. The second is the gate signin/grpc's extractor runs by default, and
skips where the subject says Seams.PasswordChangeGateDisabled.

# Who a call is made as

The consumer declares its operator-only calls in Seams.OperatorMethods, and
the suite makes those as an operator and every other call as a member, as the
conformance package documentation describes. Each caller declares the calls it
makes and is held to them: a call it did not declare fails the test.

Register requires a caller and registers somebody else, which is the
directory's administered door by another name, so a deployment whose public
sign-up is a door of its own may keep it to its staff; every registration here
is made by a caller in the global directory declaring it. The doors reached
with nobody on the call — signing in, the links that finish a registration,
the exchange and the sign-out — are this module's declaration of what is
reachable without a caller, and a deployment may keep any of them to its staff
as well: an assertion that knocks on one skips, with the reservation named,
where the subject reserves it.

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

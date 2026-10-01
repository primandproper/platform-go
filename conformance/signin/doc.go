/*
Package signin is the credential surface's promises, assertable against any
subject that mounts it.

Most of this surface answers somebody who is not signed in: the sign-up door,
the doors a person signs in through, the links that finish a registration, the exchange
that keeps a login alive and the button that ends one. What a consumer is owed
about them is two things that pull against each other. The first is that the
right credential gets in — a registrant can sign in once their address is
proven, a rotated refresh token names the same login, a mailed link is a way in
for somebody with no password. The second is that every wrong credential gets
one answer: a wrong password, an unknown handle, a replayed refresh token, a
dead verification link and a dead sign-in link are all Unauthenticated with the
same message and the same reason, because an answer that differed would tell
whoever is guessing which half of the guess was right. The doors a person is
already signed in to — a password, a second factor or a handle changed — ask for
the password again and refuse it with the same message and reason under
PermissionDenied instead, because the token they came with is good and
Unauthenticated would tell a client to refresh it.

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
register and sign in are therefore placed in tenancy.Global by the sign-up
door with nobody on it, which is where a single-tenant deployment keeps
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
assertions about rotation and signing out skip, with the reason printed, where
the subject declares Seams.RefreshTokensUnissued. Elsewhere a sign-in that
carries none fails: read off the answer, the absence is also what a service that
stopped wiring its store would say.

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

# An operator and somebody else's logins

SignInAdministrationService is asserted from the operator's side: an operator
lists a member's logins, ends one, and the member's refresh through that login
is refused while their other login goes on working; a login named against the
wrong person ends nothing; and ending all of a member's logins ends every one
and nobody else's. The operator is a caller making those calls, which is an
administrator where the subject reserves them, as every call here is.

That a member is refused them is not asserted here. A deployment that keeps
them to its staff names them in Seams.OperatorMethods, and the reservations
suite holds every name there to a refusal. The assertions skip where the
subject mounts no administrative surface in Surfaces.SignInAdministration.

A forced change is asserted twice: that it is reported and still signs in, and
that every other call is refused with PASSWORD_CHANGE_REQUIRED until it is
made. The second is the gate signin/grpc's extractor runs by default, and
skips where the subject says Seams.PasswordChangeGateDisabled.

A registrant is asserted as a registration with no policy writes one: refused
at the password door with USER_UNVERIFIED until a link proves their address,
and holding no second-factor secret until they enroll. A
signin.RegistrationPolicy may write somebody else, and a subject whose policy
does says which. Seams.RegistrantsAdmittedUnverified holds the door to
admitting them at once, and the link to proving the address all the same, as
the registrant's own auth status reads it. Seams.RegistrationIssuesSecondFactor
holds the registration to answering with the secret it issued, unproven until a
code from it is, and skips the assertion about proving a factor nobody issued.
Seams.PasswordlessRegistrationRefused holds a registration naming no password
to the refusal a policy's is answered with — InvalidArgument, carrying
REGISTRATION_REFUSED — with nobody left behind, and skips the assertions about
somebody with no password. Every other assertion registers somebody with a
password, so it holds whichever arrivals a deployment admits.

A handle change is asserted as a password change is: an address or a username
moved with the wrong current password is refused and moves nothing, and the
right one moves it. That identity's UpdateProfile refuses the same two fields
is the identity suite's to assert, and it skips where the subject says
Seams.ReauthenticatedHandlesDisabled.

# Who a call is made as

The consumer declares its operator-only calls in Seams.OperatorMethods, and
the suite makes those as an operator and every other call as a member, as the
conformance package documentation describes. Each caller declares the calls it
makes and is held to them: a call it did not declare fails the test.

The doors reached with nobody on the call — signing up, signing in, the links
that finish a registration, the exchange and the sign-out — are this module's
declaration of what is reachable without a caller, and a deployment may keep
any of them to its staff: an assertion that knocks on one skips, with the
reservation named, where the subject reserves it. Register is the one the
suite still needs where it is kept, since nearly every assertion begins by
registering somebody, so a subject that reserves it has those registrations
made by an operator in the global directory instead; the assertion that
somebody signs up with nobody on the request is the one that skips.

A registration names no roles, because the request has no field for them: the
registrant owns their account with the deployment's default owner role, or the
one its registration policy gave them, and Seams.Roles.Owner is the
deployment saying which. A deployment that closed its sign-up door says so in
Seams.RegistrationClosed, and is then held to the refusal that tells a closed
door from a broken one — Unimplemented, carrying REGISTRATION_CLOSED — while
every assertion that registers somebody over the wire skips.

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

/*
Package signin is the service every application rewrites: proving somebody is
who they say they are, and handing back a token that says so.

The engines this module already ships each do one thing and store nothing —
argon2 hashes and compares, totp generates and verifies, tokens issues and
parses — and identity stores what they produce without ever calling them. What
was missing is the orchestration between the four, which is where every
application's own sign-in code lives and where its bugs live with it. This
package is that orchestration and nothing else: it hashes nothing, stores
nothing, and holds no schema of its own.

# What it decides, and what it refuses to

The refusals are one sentinel. An unknown handle, a wrong password and a wrong
second-factor code all come back as [ErrInvalidCredentials], because telling
them apart is telling an attacker which half of the guess was right —
[github.com/primandproper/primitives-go/v2/authentication.Authenticator] says as
much where it declines to ship a mismatch sentinel of its own, and this is the
caller it meant. An unknown handle also costs the same password hash a known one
does, so the two are not told apart by a stopwatch either.

There is one deliberate exception, and it is [ErrSecondFactorRequired]: a user
who holds a proven second factor and sent no code is told to send one. That does
reveal that the password was right, and there is no way to prompt for a code
without revealing it. The alternative is an application that cannot ask.

Because that disclosure is already made, it is made in a form a client can act
on. On gRPC the two refusals share [google.golang.org/grpc/codes.Unauthenticated]
and differ in their message, so a client deciding whether to show a code field
was comparing English sentences — the disclosure had happened and the client was
still guessing at it. [ClientSafeReasons] gives each of this package's
client-safe refusals a stable identifier carried in a
google.rpc.ErrorInfo detail, so the branch is on SECOND_FACTOR_REQUIRED rather
than on prose that may be reworded or localized. It discloses nothing the
message did not; it only stops a sentence from being an API.

What it does not decide is policy. Whether a user without a second factor may
sign in at all is [SecondFactorPolicy]; whether administrative sign-in exists is
whether a consumer named any roles for it; how long a token lives, and what it
carries beyond its subject, are [WithTokenTTL] and [ClaimsBuilder]. Each is an
option with a default, and the default is stated in the option's documentation
rather than buried here.

# The refresh flow, and the one schema this package has

A short access token and a long sign-in are the two things every application
wants and cannot have from one credential. Reconciling them is a second
credential that mints the first again without a password, and the whole risk of
that second credential is that it is worth stealing for as long as the sign-in
lasts. Rotation is the answer: [Service.ExchangeRefreshToken] spends the token it
was given and mints its successor in one transaction, so a copy somebody took
stops working the moment either party uses theirs — and when the loser presents
the spent one, [ErrRefreshTokenReused] ends the whole login. That is what turns a
stolen token from a shared session nobody can see into a detected event that
signs both parties out.

Where those tokens live is
[github.com/primandproper/platform-go/v14/authentication/signin/refreshtokens],
and it is a subpackage rather than a table this package holds because it is
optional: a service built without [WithRefreshTokenStore] mints one token per
sign-in and owns no schema at all, which is what this package was before rotation
existed and is still the right shape for a consumer using [Service.Authenticate]
as a credential check. The seam is [RefreshTokenStore] and the ruling it carries
is that a store, not a caller, decides who spent a token.

A family is one login: minted when the password was proven, inherited by every
successor, revoked as a unit by a detected reuse or by
[Service.RevokeRefreshTokenFamily]. It reaches a token as the "sid" claim —
[ClaimFamilyID] — so a consumer's interceptor can check a token against a
revocation, and it reaches [Hooks.AfterIssueToken] on the [SignIn], which is why
the refresh mint happens inside the login transaction rather than beside it.

[SignIn.FamilyID] is set whether or not a refresh token was stored, because it
names a sign-in rather than a row.

The door a login came through is kept on the row too, so an exchange of an
administrative session mints another administrative token on the administrative
lifetime. It reaches a token as [ClaimAdministrative], which is what lets a
consumer's interceptor refuse administrative work under an ordinary login by the
same person.

# The transaction, and what is outside it

Verifying a password is expensive by design — that is what argon2 is for — and
none of it happens inside a transaction. A sign-in reads the user on the
client's reader, compares, resolves the principal, and mints the token; only
then does it open a transaction, and all that transaction holds is
[Hooks.AfterAuthenticate] and, where a token was minted, [Hooks.AfterIssueToken]
after it. A consumer's record of a sign-in and the sign-in are the same fact, so
a hook that cannot commit refuses the sign-in and no token is returned.

# Two events, and the doors that stop between them

Proving a password and issuing a credential are two things, and this package
used to have one way through both: whoever wanted to know who somebody was had
to take a token as well and drop it. A token nobody holds is not a disclosure,
but it is a row in whatever the consumer indexes tokens by that can only age
out, and work done on every request for a credential with no recipient.

So there are four doors and they stop in two places. [Service.Authenticate] and
[Service.AdminAuthenticate] stop at the principal. [Service.LoginForToken] and
[Service.AdminLoginForToken] go on and mint. All four run the same proof in the
same order and all four run [Hooks.AfterAuthenticate], so an access log cannot
tell them apart by whether an entry appeared — only the second pair adds
[Hooks.AfterIssueToken].

The three credential writes are the same shape in reverse: read, verify and
hash outside, and a transaction that holds the write and the hook together.
Nothing here holds a write transaction open across a password hash.

# What is not here

No session. This package hands back tokens; what a consumer does with them — a
cookie through [github.com/primandproper/platform-go/v14/sessions], an
Authorization header, a gRPC credential — is theirs, and the interceptor that
turns one back into a caller is the consumer's too. Nothing in this package reads
a request.

A family is not a counter-example to that, and the distinction is worth stating
because the two are easy to confuse. A family is a group of credentials this
package minted; a session is a record of a caller that something turns a request
back into. [github.com/primandproper/platform-go/v14/sessions] has a store, an
identifier and a revocation of its own, and it is a different mechanism for a
different job — which is why the vocabulary here is "family" throughout, and why
the one place the two spellings meet is [ClaimFamilyID], where a wire convention
is translated once.

No passkeys and no password reset. Each is a flow of its own over an engine
this module already ships —
[github.com/primandproper/primitives-go/v2/authentication/webauthn],
[github.com/primandproper/platform-go/v14/authentication/passwordreset],
[github.com/primandproper/platform-go/v14/links] — and each is its own addition
rather than a branch inside the password flow.

# Registration, and the credential it carries

[Service.Register] is here, and the password is the reason. identity never
hashes — it stores what an engine produced — so its registration takes a user
whose hash is already set and its wire schema carries no password at all. That
is right, and on its own it left a gap: a registration over a transport produced
somebody with no credential, and the two methods for attaching one afterwards
both required the sign-in that person could not do. This package holds the
authenticator, so a registration that carries a credential belongs here. The
directory work is still identity's, through [Registrar] on one transaction.

A registration names how the registrant will prove who they are — [Password] or
[NoPassword] — and naming neither is refused. Passwordless is a supported
arrival rather than an unfinished one, but it is a decision somebody makes
rather than something inferred from a blank field: an empty password and a
deliberate absence of one are indistinguishable to a service reading a string,
and the second mints an account nobody can sign into. What a registrant who
named no password can still do is enroll a passkey or take
[Service.AttachPassword] later; what this package does not yet ship is a door
that mints a token from a mailed link, which is the way in most such people
expect.

No second-factor secret at registration, which some applications do mint there.
Enrolment stays behind authentication — [Service.RefreshTOTPSecret] then
[Service.VerifyTOTPSecret], both of which require a signed-in caller — because
the flow that mints a secret at registration has to let somebody verify it
unauthenticated, by user ID, and an endpoint that confirms whether a code
matches for a user ID is an enumeration oracle with a brute-force surface
attached. The order this package ships is register, verify, sign in, enroll.

# Getting in for the first time, and the two doors that have no caller

A registrant lands in identity.StatusUnverified, which admits no sign-in, and
the only thing that moved a user out of it was an operator's write behind an
operator's permission — which no registration flow holds. So two doors here
take a mailed verification token as their whole authority, because the person
they are about cannot be signed in and has no current password to re-type:
[Service.VerifyEmailAddress], which proves the address and promotes them, and
[Service.AttachPassword], which gives a password to somebody who holds none and
is refused for anybody who does.

That refusal is what keeps the second one narrow. An outstanding link furnishes
an account that has no password, once; against an account that has one it can do
nothing, and somebody who has forgotten theirs goes through
[github.com/primandproper/platform-go/v14/authentication/passwordreset] instead.
Attaching does not spend the link, so the same click can go on to verify — which
is the order a consumer doing both from one page wants.

[Service.CompleteVerification] is the third, and it has no transport door. Not
every registration asks for an email address to be proven, and a consumer who
proved a phone number, a payment or an operator's approval says so with it. What
may be proven that way is theirs to decide, which is exactly why there is no RPC:
the check is one only they can make.
*/
package signin

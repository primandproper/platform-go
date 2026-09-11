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

What it does not decide is policy. Whether a user without a second factor may
sign in at all is [SecondFactorPolicy]; whether administrative sign-in exists is
whether a consumer named any roles for it; how long a token lives, and what it
carries beyond its subject, are [WithTokenTTL] and [ClaimsBuilder]. Each is an
option with a default, and the default is stated in the option's documentation
rather than buried here.

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

No session. This package hands back a token; what a consumer does with it —
a cookie through
[github.com/primandproper/platform-go/v14/sessions], an Authorization header, a
gRPC credential — is theirs, and the interceptor that turns one back into a
caller is the consumer's too. Nothing in this package reads a request.

No passkeys, no password reset, no email verification and no session management.
Each is a flow of its own over an engine this module already ships —
[github.com/primandproper/primitives-go/v2/authentication/webauthn],
[github.com/primandproper/platform-go/v14/authentication/passwordreset],
[github.com/primandproper/platform-go/v14/links] — and each is its own addition
rather than a branch inside the password flow.

No registration. Making a user exist is
[github.com/primandproper/platform-go/v14/identity.Service.Register], which
mints the passwordless user identity already treats as first-class; attaching a
credential to one is [Service.UpdatePassword] and [Service.RefreshTOTPSecret]
afterwards.
*/
package signin

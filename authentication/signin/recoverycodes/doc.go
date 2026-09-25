/*
Package recoverycodes is where a user's recovery codes live: a set of
single-use, digest-at-rest second factors a person keeps on paper for the day
they no longer have the authenticator they enrolled.

It is the SQL implementation of
[github.com/primandproper/platform-go/v14/authentication/signin.RecoveryCodeStore],
and it ships the DDL it needs, so giving a lost phone a door that is not a
support ticket is a table and an option rather than a package somebody writes.
The seam is in the parent because the flow and its storage are genuinely
separable; the implementation is here beside refreshtokens and magiclinks, the
parent's other two optional stores, because signin is the only thing that ever
checks one of these codes.

# What this is for, in one paragraph

A user who holds a proven TOTP secret and loses the phone it is on could, until
this existed, get back in one way: an operator clearing the secret. That is the
recovery a social engineer targets, because it only takes convincing a person. A
recovery code moves the proof back to something only the user ever held.
[signin.Service.ReplaceRecoveryCodes] mints a set behind the password and a
second factor; any sign-in door, and [signin.Service.RefreshTOTPSecret], then
takes one of them in place of a TOTP code, once. The support path still exists
for somebody who has lost the codes too — but as the slow one behind this, not
the only one.

# Why a fast digest

The hash column is a SHA-256 of the code, unsalted, where a password would get
argon2. A password is something a person chose, from a space small enough to
enumerate, and the expensive hash is the only thing between a leaked table and
the passwords in it. A recovery code is sixty bits from a CSPRNG — see
[CodeLength] — so there is no dictionary to run, and an expensive hash would buy
nothing but a slower sign-in on the one path a person takes when everything else
has already gone wrong. refreshtokens and magiclinks make the same choice for
the same reason, against more bits.

Sixty is fewer than they carry, and the difference is deliberate: a recovery code
is typed off paper by a person rather than carried in a URL, and every character
is one more to mistype. What keeps sixty enough is where a guess has to be made.
Online, it is a sign-in door that counts every miss as a failed sign-in and hands
it to [signin.Hooks.AfterFailedSignIn]. Offline, it is a leaked database, which
also holds the TOTP secrets these codes stand in for.

# Why the key is the owner and the digest together

A refresh token and a sign-in link are keyed on their digest alone, and a
collision there is a generator that has stopped being random. Sixty bits across
a whole directory is not that: with enough people holding enough codes, two of
them will hold the same one some day, and a key on the digest alone would make
one of their replacements fail for a reason neither of them could see. Keyed on
the owner too, the two rows never meet, and every statement here names the owner
anyway — a code is only ever presented by somebody signing in as someone.

# Why no expiry, and no sweeper

A recovery code does not lapse. It is replaced by the person who holds it, or
deleted with them, and nothing else touches it. A deadline would be a date
nobody chose on the one credential somebody keeps in a drawer for the day they
need it — and the day they need it is, by construction, not a day they were
expecting. So there is no expires_at, no purge_after, and no Sweep.

What bounds the table is the replacement: [SQLStore.Replace] deletes the set it
replaces, spent codes included, so what one person holds is one set.

# Privacy

Unlike magiclinks, which records an address, this table holds a user id and
digests, and nothing else about anybody. That would argue for no adapter at all,
and it does not, for the reason magiclinks' own ruling turns on: there, a copy
of an address is deleted by a sweeper at a link's lifetime plus a retention
window, so an erasure that reaches identity leaves nothing here for long. There
is no sweeper here. A row outlives its owner until something deletes it, and an
erasure that deleted the user and left their codes would leave a user id with
digests under it indefinitely.

So this package ships the passwordreset/privacy shape, both halves:
[github.com/primandproper/platform-go/v14/authentication/signin/recoverycodes/privacy].
The eraser deletes every code a subject holds on the erasure's transaction; the
collector exports when their set was issued and which codes they have spent,
and never a digest — no statement in this package projects one to a list.
privacyadapters registers it beside the rest, under RecoveryCodes.

# What this store does not do

It reads no user table, so it does not check that a user exists, holds a second
factor, or holds a password. All three are the service's.

It decides no count. How many codes a set holds is signin's
WithRecoveryCodeCount, arriving on every replacement.

It rate limits nothing. A wrong recovery code is a wrong second-factor code, and
the door it is presented at counts it as a failed sign-in; what a consumer does
with that count is theirs, in front of the service.

It opens no transaction. Every write takes the caller's database.Tx and every
read the caller's executor, and it has no machinery of its own to run anywhere
else.
*/
package recoverycodes

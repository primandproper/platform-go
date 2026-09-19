/*
Package magiclinks is where a sign-in link's tokens live: a digest-keyed,
single-use credential mailed to an address and spent for a session.

It is the SQL implementation of
[github.com/primandproper/platform-go/v14/authentication/signin.MagicLinkStore],
and it ships the DDL it needs, so adopting a passwordless door is a table and
two options rather than a package somebody writes. The seam is in the parent
because the flow and its storage are genuinely separable; the implementation is
here because a schema, three dialects' worth of statements and a sweeper are not
things the service that orchestrates a password check should be carrying.

# What this is for, in one paragraph

A registration may name no password — [signin.NoPassword] — and identity has
always treated a user who holds none as supported rather than unfinished. What
was missing was the way back in. The four doors [signin.Service] shipped all
prove a password; the refresh exchange renews a sign-in that already happened;
the verification link proves an address once and is burned by the write that
records it; and password reset presumes the person wants a password. This is the
table behind "type your email, click the link, you are in", and it is the whole
of that flow's storage.

# Why this is not links

[github.com/primandproper/platform-go/v14/links] is a general-purpose link
minter over the same shape — a token digest, an action, a subject, an expiry, a
single-use resolution — and its own documentation names "magic_login" as an
example action. It was the obvious home and it is not this one, for a reason
that is about transactions rather than about tables.

links.Store deliberately takes no executor. Its records are minted by one
process and redeemed by another, so there is no caller transaction to join, and
its Resolve has to commit by itself: a transition sitting uncommitted inside
somebody's request is a link the next caller still finds active, and one
transition and one refusal is the whole of single use there.

A sign-in redemption is not the end of its flow. It is the beginning of a
sign-in: the spend, the address it proves, the standing it promotes, the refresh
token it mints and the two hooks a consumer commits beside it are one fact, and
signin already commits that fact in one transaction for the password door. A
spend that committed by itself would put the hooks outside it — so a consumer's
failure to record a sign-in would leave the link burned and the person asking
for another mail. This store takes the caller's Tx and buys single use inside
it, with the affected row count of a guarded UPDATE whose predicate repeats
every row-state test the answer depends on.

An application that wants a general link minter should still reach for links.
This is the one flow where the redemption has company.

# Why this is not passwordreset's table

The shapes are close enough that sharing one was worth considering: a digest, an
expiry, a redemption stamp, a sweeper. They differ in what a redemption
authorizes, and that difference is the whole of why two tables is the honest
answer. A reset token authorizes a password write; a sign-in link authorizes a
session. A single table would need a column saying which — and a column saying
which is a column that can be wrong, in the direction where a link mailed to
prove an address sets somebody's password.

# Two identifier columns, where the refresh token store has four

The address beside them is not a third. It names no row and resolves nothing: it
is where the mail went, recorded because a link proves control of one inbox and
of no other, so a redemption can be refused when its subject has moved to
another address. [signin.Service.RedeemMagicLink] is where that comparison is
made and where what it prevents is argued; this store writes the value down and
hands it back without reading anything out of it.

There is no family_id: a link is not a login, it is the thing that begins one,
and the family is minted by the service at redemption exactly as it is for a
password sign-in. There is no active_account_id: a password sign-in names the
account it wants, where a link arrives out of an inbox carrying whatever was in
the URL, so the account is resolved at redemption rather than remembered from a
request that named none. There is no administrative column, because there is no
administrative sign-in link — see [signin.Service.RedeemMagicLink].

# What this store does not do

It reads no user table, so it does not check that a subject exists, does not
check their standing, does not know whether they hold a second factor, and does
not compare the address it recorded against the one that subject holds now. All
four are the service's, on the transaction this store's spend runs in — the
last of them necessarily so, since the other side of that comparison is in a
table this package cannot see.

It sends nothing. What a link's URL looks like, where it points, what the mail
says and who it comes from are the consumer's, through
[signin.MagicLinkMailer]; this package holds the secret for as long as it takes
to return it.

It decides no lifetime. How long a link lives is signin's WithMagicLinkTTL,
resolved before the call and arriving on [signin.MagicLinkRequest] — a store
holding a default for it would be a second place that policy lived. What this
store does hold is the retention window past that deadline, because that is
storage's own business: see [WithRetention].

It rate limits nothing, and this is the flow where that omission costs the most.
A door that mails on every request is a way to send mail through somebody else's
domain if nothing throttles it. primitives-go's ratelimiting is the piece, it
goes in front of [signin.Service.RequestMagicLink], and neither this package nor
that service will do it for a consumer — see that method, which says so on
itself.

It opens no transaction. All three methods are writes and all three take the
caller's database.Tx. [SQLStore.Sweep] is the exception and the usual one: a
worker on a timer is the component servicing itself, so it runs on the handle
the store was built with.
*/
package magiclinks

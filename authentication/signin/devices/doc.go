/*
Package devices records where each login was last renewed from, and answers it
back on a "where you're signed in" screen.

signin lists a person's logins and records how each one happened, and stores
nothing about the device behind one: whether an address, a user agent or a
device name is recorded at all is the consumer's decision, and signin's package
documentation says so. This package does not change that default. It is what a
consumer who decided yes wires instead of building the same table, hook,
annotator, export and sweep themselves — and a deployment that does not wire it
records nothing, exactly as before.

# The pieces, and the one that is the consumer's

  - [SQLStore] keeps a row per login, keyed on signin's family identifier, in
    the table the migrations package renders for every supported engine.
  - [NewHooks] wraps a signin.Hooks so that every mint records the device on
    the token's own transaction, and every login ended deletes it on the
    revocation's.
  - [NewAnnotator] reads what was recorded back into the attributes
    signin/grpc's listing RPCs answer each login with.
  - The privacy package exports a person's rows and erases them.
  - [SQLStore.Sweep], or [WithSweeper], deletes a row once its login can no
    longer be alive.

What none of them can decide is which parts of a request to believe, and that is
the [Extractor] a consumer hands NewHooks. Which headers a deployment's edge
writes, which proxies it trusts and which clients send a device name are facts
about one deployment, and a wrong guess here is a client choosing the address
the screen shows for it. [PeerExtractor] is the conservative answer, named so a
deployment that takes it says so: the connection's own peer and the client's
user-agent, and no forwarded header at all.

# What is recorded, and how far to trust it

What a client says about itself is display, not evidence. Somebody signing in
to their own account can make their own screen say what they like, which tells
nobody anything; so nothing in this module reads these values for a decision,
and a rate limiter or a new-device alert that did would be one a client could
steer.

Every value is bounded before it is stored — trimmed, made valid UTF-8 and cut
to [MaxFieldLength] bytes on a character boundary — by the store, not by the
hook, so a consumer calling [SQLStore.Record] directly is bounded too. A user
agent a client chose to make a megabyte long is not one to store a megabyte of,
and MySQL would otherwise cut it silently and report success.

# One row per login, renewed

The key is the family, not the token. signin mints a new token in the same
family on every refresh, and runs AfterIssueToken for each, so the upsert
converges on one row and renews it: the row says where the login was last
renewed from, which is the device holding it now, and when it was first seen.

An empty [Origin] is recorded as empty rather than skipped. A login renewed
from somewhere the extractor could not read is one whose last-known device is
unknown, and leaving the previous renewal's address in place would show the
screen a device that may no longer hold it.

# Impersonation records nothing

A sign-in signin.Service.IssueImpersonationToken minted carries the operator as
its ActorID, and the request behind it is the operator's. The login it begins is
listed to the subject — so recording its device would show the person being
impersonated their operator's address and browser. The hook skips any sign-in
with an actor, which is also every refresh in an impersonated family.

# Why the write fails the sign-in

The hook writes on the token's transaction, after the hooks it wraps have run,
and an error from the write is the hook's error — so the mint rolls back. A
login whose token was issued and whose device was not recorded would be a row
on the screen that silently stopped saying where, which is the half-answer
signin/grpc's annotator contract already refuses to give at read time.

# How long a row lives

As long as its login does. A login ended early — signed out, ended from the
screen, ended by an operator or by a detected refresh-token reuse — has its row
deleted by the hook, in the transaction that ended it. A login that lapses on
its own ends with nobody to run a hook, so the row keeps its deadline: until
the refresh token would lapse, or, for a login minted without one, until the
access token does. The sweep deletes it after that.

So what the table holds for a person is their live logins and the lapsed ones
the sweep has not reached yet, never one they ended — which is what the export
the privacy package makes answers with.

# Privacy

The rows hold an address, a user agent and a device name against a user id,
which is personal data by any reading. The privacy package ships both halves,
the recoverycodes/privacy shape: a collector exporting every row a subject has,
and an eraser deleting them on the erasure's transaction. The user column
carries no REFERENCES, for the reason no signin table's does — this package is
usable by an application whose directory is not identity's — so nothing cascades
here, and the eraser is how an erasure reaches this table.

# What this does not wire

Nothing here is reached from signin's config or from service's registration.
The [Extractor] is a function, which no environment variable can express, and
the decision to record anything at all is one the deployment makes by writing
the three calls: NewSQLStore, NewHooks over the hooks signin is built with, and
signin/grpc's WithSignInAnnotator over NewAnnotator.
*/
package devices

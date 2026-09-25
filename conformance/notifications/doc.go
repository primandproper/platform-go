/*
Package notifications is the inbox and device surface's promises, assertable
against any subject that mounts it.

Nine RPCs, and every one of them is addressed by the caller rather than by the
request: the recipient of a notification and the owner of a handset come off
the connection, and no message has a field for either. So what a consumer needs
verified is one sentence, asserted from every angle the surface has — a caller
reaches their own inbox and their own devices, and somebody else's row reads
exactly as one that is not there. NotFound rather than PermissionDenied,
because the two answers being the same one is what stops a caller walking
identifiers from learning which of them are real.

The device half carries one promise of its own: a token names one handset, so
registering it again converges on the row that holds it — under its new owner,
if a phone changed hands, because the alternative is the previous owner's
notifications arriving on somebody else's lock screen.

# How an inbox fills

No RPC files a notification, and that is the design rather than a gap: a
notification is the application telling somebody something happened, and a
client that could file one could put words in the application's mouth. Every
assertion about the inbox therefore starts with the Notified action, which asks
the deployment to do what it does when it tells somebody something. A subject
that supplies none skips the inbox half, with the reason printed; the device
half needs nothing but a client.

# What is here and what stayed behind

notifications/grpc keeps its construction and contract tests: what NewServer
refuses to be built from, the permission roster, the reservations in the proto,
the store-method roster, the converters and the options. It keeps the principal
with no user identifier, which a subject cannot mint, and everything about
include_archived, which is the deployment's grants' answer rather than a
promise a suite can hold every deployment to.

It keeps the counts, too. MarkAllNotificationsRead reports how many rows it
moved and the unread listing reports a badge count, and both are exact only in
an inbox nothing else writes to — which a deployment that sends a welcome
notification on registration is not. What moved here is what those tests were
for: which named rows were marked, and which were left alone.

# Both directions, deliberately

Each confinement assertion proves the caller reaches its own row through the
same client before proving it cannot reach somebody else's. Without that, a
deployment whose addressing is comprehensively broken passes every "their row
is absent" assertion on the strength of reaching nothing at all.
*/
package notifications

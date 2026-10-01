/*
Package operations is the operations surface's promises, asserted over HTTP
against any subject that serves it: an operation is read, listed, cancelled and
followed by its owners, and by nobody else.

An operation is how a person follows work the service is doing for them — most
often their own privacy request — so the owner rule is the whole of their
security. What is asserted is that rule on every route operations/http mounts,
each refusal paired with its owner's success, so that a route refusing
everybody cannot pass as one refusing the right people.

# Two owners

service mounts the surface with two owners per caller: the tenant the request
is against, and the person making it. An application's own work belongs to the
tenant it was run for, and dataprivacy starts its operations owned by the
person the request is about, so a caller legitimately holds operations under
both — and a colleague in the same tenant holds the first and not the second.
Reads fan out across the set: a read by identifier is answered by whichever
owner holds it, and a listing is the union.

So the suite has two halves, one per owner, and they differ in which neighbor
is refused. The person-owned half mints two people in one directory, and the
colleague is refused on the listing, the cancellation and the event stream. The
tenant-owned half mints callers in two tenants, and the colleague is admitted,
which is the positive control for the fan-out; the caller in the other tenant is
refused on every route.

# Two sources

operations/http deliberately serves no generic start: each kind of work is
started from the consumer's own typed endpoint, so no client of this module's
surfaces can start one of the application's. The person-owned half's operations
therefore come from the one start a client can reach, a privacy export, and the
half skips on a subject that serves no privacy requests. The tenant-owned half's
come from Actions.Operated, and the half skips on a subject that supplies none
— which a deployment whose surface resolves only the person, and so whose
tenant-owned operations nobody could read, rightly does.

The read of a person-owned operation by a colleague is not asserted here. The
dataprivacy suite asserts it, as the promise that crosses the two packages, and
a second copy would be a second place for it to drift.

# What is not asserted

No count. A listing's counts are per owner and summed, and in a shared database
an owner holds rows no assertion made, so a listing is walked by cursor and
filtered by kind until the operation is found or the pages run out.

No final state after a cancellation by an owner. Cancelling a finished
operation returns it unchanged, and nothing waits on a worker here, so the owner
is asserted to get the operation back and nothing more. What is asserted is that
a refused cancellation did not cancel: nothing but a cancellation writes
cancelled, which holds at any moment whatever the worker has done.

The event stream is asserted only where the subject says it is mounted, with
HTTPSurfaces.OperationEvents. A 404 is also what a broken route answers, so it
is never read as an absent one.
*/
package operations

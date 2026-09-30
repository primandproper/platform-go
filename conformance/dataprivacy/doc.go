/*
Package dataprivacy is the privacy-request surface's promises, and the promise
operations/http makes about the work that fulfills one, asserted over HTTP
against any subject that serves them.

A subject access request is the one flow in this module whose whole purpose is
the person it is about, so what a consumer is owed is confinement, stated from
both ends: the request a caller submitted is theirs to list, read and cancel,
and nobody else's; and the operation the service opens to fulfill it is read by
that same person and by nobody else. The second half is the one that crosses a
package boundary — dataprivacy opens the operation, operations/http answers for
it — and so the one no test in either package can make alone.

# Why over HTTP

Both surfaces are served on the router rather than the gRPC server, for the
reason dataprivacy/http's documentation gives: the confirmation arrives by mail
as a link, the progress is an event stream, and the artifact is a download, so
the flow already lived on HTTP before it had a handler. The suite reaches them
through Subject.HTTP, a client and a base URL, and asserts statuses and the
bodies' identifiers — never a count, since a deployment's other requests may
share the table.

# Fulfillment

Confinement is half of what a request is owed; the other half is that it is
done. An export completes with an artifact, an expiry and no section missing; an
erasure completes and takes its subject and nobody beside them; and an export
whose window has lapsed reads expired, its artifact reference cleared, while
the request itself stays readable — a subject is entitled to know what was
asked in their name after the thing they asked for is gone.

Each is waited for with Session.Await, and what is polled is the request row
rather than the operation fulfilling it: the row is what a subject can see, and
an operation that finished without moving it is the failure worth catching.

An erasure is observed from the directory rather than from the row, because the
row belongs to somebody who may no longer be able to read it: a deployment that
signs an erased person out answers their next read as unauthenticated, and one
that does not answers it as before. So an administrator in the subject's
directory reads the subject until they are gone and the bystander beside them
after, which is also the promise being made — a completed erasure removes its
subject from the directory. A deployment that mounts no directory, or mints no
administrator, skips it. Where the erasure waits for confirmation, the subject
follows their own link first; nothing assumes a deployment does or does not.

An expiry is an action, Actions.ArtifactExpired, rather than a clock the suite
moves: the window is days long and stamped onto the row by the worker that
completed the export, and the sweep is a job the deployment schedules. No client
can bring either about, and the suite does not need to know how the subject
did. The artifact's own download route is the deployment's rather than this
module's, so nothing here fetches it.
*/
package dataprivacy

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
*/
package dataprivacy

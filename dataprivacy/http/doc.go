/*
Package http mounts the data-privacy request surface on a routing.Router.

It is imported as dataprivacyhttp.

	handlers, err := dataprivacyhttp.New(svc,
		dataprivacyhttp.WithSubjectResolver(subjectFromSession),
		dataprivacyhttp.WithLogger(logger))
	if err != nil {
		return err
	}

	handlers.Mount(router)

	errormappers.Register() // or service.Register, which makes this call

Mount registers all five routes — submit, list, read one, confirm, cancel —
which is the ordinary case. A consumer that wants some of them calls MountSubmit,
MountList, MountGet, MountConfirm and MountCancel itself and leaves the rest out.
Confirm is the one most likely to be left off, and the section on it below says
why somebody would.

There is no route list to hand back for somebody else to register: routing.Route
is what a registration returns rather than a value that can be registered, and
routing.Get is generic over its handler's input and output types, so the typed
registration has to happen where those types are still known. That is here, and
per-route methods are the whole of the choice that leaves.

# Why this domain is on HTTP when three others are on gRPC

identity/grpc, authentication/signin/grpc and authentication/oauth2clients/grpc
are the module's other domain surfaces, and gRPC is the house default. What the
three have in common is written down once in identity/grpc's documentation, and
this package holds to all of it — the subject binds off the caller rather than
off a request field, the writes that belong inside somebody else's transaction
stay in-process, and the mapper pair is registered at the composition root. The
protocol is the single place it diverges, and the reason is that the protocol
follows the flow rather than a default.

This flow was already on HTTP before there was a handler in it, because of three
decisions the package below had made without reference to any of this:

Progress is not answered here and deliberately is not — dataprivacy.Service says
so in as many words. "How far along is my export" is answered by operations/http
against Request.OperationID, over the same event stream every other long-running
thing in the application already uses.

Confirmation arrives by mail. An erasure submitted to a Service with a
confirmation window comes back StatusAwaitingConfirmation with an empty
OperationID, and nothing runs until Confirm. The Notifier is email, so Confirm is
reached by somebody clicking a link: a browser making a request, with whatever a
browser sends.

The artifact arrives the same way. Notification.DownloadURL is a freshly minted,
expiring URL for the object in storage, minted at notification time so that its
short expiry starts when the subject is told rather than when the runner
finished.

A gRPC surface would put submit, confirm and cancel on one protocol while the
confirm click, the progress stream and the download all lived on another — one
flow split across two protocols, to match three surfaces that had no such
constraint.

# The subject is resolved, never sent

WithSubjectResolver has no default and there is no name for going without one.
Every route here is scoped to the subject it returns: the submit endpoint records
a request about them, the listing returns theirs, and the three routes that name
a request refuse one that is somebody else's. A surface that took the subject
from the request body would let anyone export anyone, which is the whole of the
thing this package exists to do, performed on the wrong person.

A staff tool acting on a customer's behalf is not that endpoint and should not be
made from it. It calls dataprivacy.Service directly from its own handler, under
its own permission, and the audit entry names the agent because the Service's
ActorResolver reads them off the context — which is a distinction a resolver that
returned the target's subject would erase.

The read of one request applies the listing's own rule to one row: the IDs must
match, and the scope must match only where the resolved subject names one. That
is what dataprivacy.Store.List already does, and answering the two questions
differently would mean a request visible in a listing and absent from its own
URL.

A request belonging to somebody else is reported as dataprivacy.ErrRequestNotFound
rather than as a permission failure, which is the same answer as one that does not
exist. That is deliberate: a 403 for a request that exists and a 404 for one that
does not is an oracle telling whoever is guessing identifiers which of their
guesses are real.

# The confirm route is a GET, and what that costs

Confirm is registered as a GET because the link in the mail is how it is reached,
and a link click is a GET. Nothing else about it is unusual, and everything else
about it is: it changes state on a verb that is not supposed to.

The cost is that a client which prefetches links confirms erasures. Mail scanners
and reading panes fetch what they are sent, so on a deployment behind one the
window closes when the mail is delivered rather than when the subject clicks.
Worth knowing exactly what that loses, because it is less than it looks: the
window is a check that the request reached the person it is about, and a scanner
fetching the link has demonstrated the same control of the same mailbox. What it
loses is the second thoughts — the subject who submitted an erasure and changed
their mind before clicking, and who now has a Cancel to reach for instead.

A deployment that wants a human click keeps this route unmounted, mounts the
other four, and puts its own page in front: render an interstitial at the URL in
the mail and call dataprivacy.Service.Confirm from the form's POST. That is the
same shape as the start endpoint operations/http deliberately does not ship — the
consumer's page, over this module's service — and it is why the mounts here are
five methods rather than one.

Confirming twice is a conflict rather than a second confirmation. The second
request finds the row no longer awaiting one and reports
dataprivacy.ErrNotAwaitingConfirmation, which the mapper answers 409, and so does
a click that arrives after the window has lapsed.

# What is absent

No status route. Request.OperationID and operations/http are the answer, and
adding a second one here would mean a progress endpoint that reports less than
the one every other long-running thing in the application already has, drifting
from it on its own schedule. What this package does instead is point at it: every
response about a single request carries the paths to poll and to subscribe to,
built from operations/http's own exported constants so that the two cannot
disagree. See Receipt.

No download route. The artifact reaches the subject as the expiring URL in the
notification, and dataprivacy.Service.Download and Open remain in-process calls
for a consumer's own route to make. Serving the bytes is a different kind of
endpoint from the five here — a content type, a range request, a stream — and
the module's Transports section files that kind under uploads/registry rather
than under a domain's resource surface.

No cancellation that pretends to have stopped anything. Cancel on a request that
is already in progress asks its operation to stop and returns the request still
StatusInProgress, because that is what it is: the runner stops when it next
looks, at a point it can describe, and marks the row when it does. The response
says StatusInProgress and carries the progress paths, which is where the answer
arrives.

# Errors are the composition root's to register

This package registers no mapper. dataprivacy.HTTPMapper and
dataprivacy.GRPCMapper live beside the sentinels they map, and
errormappers.Register installs them — service.Register makes that call for a
service built from a service.Config, and a service assembled by hand makes it
itself. Without it every sentinel this surface returns is a 500: a request that
is not the caller's answers 500 where the read went to the trouble of returning a
404, and an unknown request type answers 500 where the mapper says 400.

operations/http.New registers its own HTTP mapper and is the module's single
exception. This package sits beside it and does not follow it there. One door
stays one door — a second surface registering for itself makes "which mappers
does this process answer with" a question about which handlers happen to have
been constructed, and the answer stops being one call at the composition root.
The exception was ever only defensible while operations/http was the only surface
that could make the statement at all.
*/
package http

//platform:transport resource surface: submit, confirm, cancel and read a privacy request — over `dataprivacy.Service`

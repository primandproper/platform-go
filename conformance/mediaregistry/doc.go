/*
Package mediaregistry is the media registry's promises, asserted against any
subject that serves them: the guarded object read over HTTP, and the resource
surface over gRPC. Each half skips, with the reason printed, where the subject
does not serve it.

# The serve route

mediaregistry/http serves one route, GET /objects/{objectID}, and what it exists
to provide is confinement: an object reaches the caller entitled to it and
nobody else. Every refusal it makes is deliberately one answer — another
tenant's object, an object the Entitlement declines, and an object that does not
exist all come back as the same 404 — so that whoever is guessing identifiers
cannot tell which of their guesses are real. That collapse is a promise a
deployment can break with its own wiring, through the TenantOf it reads a scope
with or an Entitlement it supplies, and never notice, which is what makes it
worth asserting against a server this module did not build.

# How an object comes to exist, for the serve route

The serve route's assertions start with the Registered action, which asks the
deployment to store and register an object the way its own upload path does and
report the object's identifier and bytes. A deployment may upload through
mediaregistry/grpc or through a path of its own — a form, a signed URL, a
migration from an existing bucket — and the action is how the suite reaches
either. A subject that supplies none skips those assertions, with the reason
printed.

# Indistinguishable, not merely refused

The refusals are compared with the answer for an identifier nothing was ever
registered under, status and body alike, rather than each checked for a 404 on
its own. Two different 404s are an oracle as surely as a 403 beside a 404 is,
and the comparison is what catches a deployment that has started wording them
apart.

Each one is asserted behind a positive control: the owner fetches the object
through the same route first, so a deployment that serves nobody cannot pass on
the strength of refusing everybody.

# The resource surface

mediaregistry/grpc's assertions make their objects through the surface itself,
so they need no action: an upload is read back by its sender, listed among
their objects, archived, and then absent from every read. Somebody else's
upload — a colleague's, and another tenant's — is absent from every read and
cannot be archived, and each refusal is compared with the answer for an
identifier nothing was uploaded under, code and message alike. A registration
of another user's key is refused as an absence, and compared with a
registration of a key under the caller's own prefix that holds nothing.

Every upload declares itself image/png, which a deployment accepting images
accepts. The colleague assertions skip where Seams.MediaObjectsShared says the
deployment's Entitlement lets a colleague read an object; the tenant and the
archive's ownership are not the Entitlement's to widen, and are asserted
regardless.

# What stayed behind

mediaregistry/http keeps everything about the bytes rather than the caller:
ranges, conditional requests, dispositions, a bucket that fails after the
headers are written, and what New refuses to be built from. mediaregistry/grpc
keeps its policies' defaults — the size cap, the refused content types, the key
layout — which a deployment is entitled to replace. Those vary how the
handler was built or what the bucket does, and a deployed service offers the
suite neither.
*/
package mediaregistry

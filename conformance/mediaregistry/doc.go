/*
Package mediaregistry is the guarded object read's promises, asserted over HTTP
against any subject that serves it.

mediaregistry/http serves one route, GET /objects/{objectID}, and what it exists
to provide is confinement: an object reaches the caller entitled to it and
nobody else. Every refusal it makes is deliberately one answer — another
tenant's object, an object the Entitlement declines, and an object that does not
exist all come back as the same 404 — so that whoever is guessing identifiers
cannot tell which of their guesses are real. That collapse is a promise a
deployment can break with its own wiring, through the TenantOf it reads a scope
with or an Entitlement it supplies, and never notice, which is what makes it
worth asserting against a server this module did not build.

# How an object comes to exist

No route or RPC creates one. The bytes arrive through whatever upload the
application offers and the row is written once they are stored, so every
assertion here starts with the Registered action, which asks the deployment to
do exactly that and report the object's identifier and bytes. A subject that
supplies none skips the suite, with the reason printed.

# Indistinguishable, not merely refused

The refusals are compared with the answer for an identifier nothing was ever
registered under, status and body alike, rather than each checked for a 404 on
its own. Two different 404s are an oracle as surely as a 403 beside a 404 is,
and the comparison is what catches a deployment that has started wording them
apart.

Each one is asserted behind a positive control: the owner fetches the object
through the same route first, so a deployment that serves nobody cannot pass on
the strength of refusing everybody.

# What stayed behind

mediaregistry/http keeps everything about the bytes rather than the caller:
ranges, conditional requests, dispositions, a bucket that fails after the
headers are written, and what New refuses to be built from. Those vary how the
handler was built or what the bucket does, and a deployed service offers the
suite neither.
*/
package mediaregistry

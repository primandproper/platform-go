/*
Package http serves the bytes behind a registry row, to the callers the row
entitles.

	handler, err := registryhttp.New(store, client, manager,
		registryhttp.WithCallerResolver(callerFromSession),
		registryhttp.WithLogger(logger))
	if err != nil {
		return err
	}

	handler.Mount(router)

One route, one object: GET /objects/{objectID}. The row is read in the caller's
scope, the caller is checked against the row, and only then does anything open
the bucket.

# Why this exists

registry's own documentation heads a section "Why the row is the access
control" — whether this caller may read this object is answered from the owner
and the scope on the row, not from the bucket — and then, two sections later,
says that nothing in that package opens, reads or removes an object. So the
package makes an access-control claim and declines to act on it, and the half
that acts on it is the half every consumer then writes: a handler that reads the
row, compares an owner, and copies bytes with a content type and a range.

The failure it prevents is the one registry names. A consumer without this
serves objects public-by-key, which makes an unguessable key the only protection
a private document has — and a key is not a secret. It appears in the row, in
the audit entry that recorded the upload, in the bucket listing, in the log line
of whatever process wrote it, and in whatever the consumer put it in to render
the page. A URL built from one is a bearer token that nobody rotates and that
nothing can revoke.

# It moves bytes, and that is legal

uploads is a primitive: it left in the v14 split, which is why this tree holds
only registry. A domain package importing a primitive is the direction the split
bought, so this package reaching for uploads.UploadManager inverts nothing. What
it does not do is any byte handling of its own — it opens what the manager
gives it and hands the reader to net/http.

# Whose shape this is standing in for

This is a binding rather than a resource surface, and it is the second one in
the module. sessions/http is the first: it binds a store to a signed cookie
whose signing, encryption, HttpOnly, Secure and SameSite are decisions this
module already made, and there is no resource of the consumer's in it. The claim
here is the same one. What is on the wire is an object's bytes under the content
type the row records, and the decisions being made for you — the guard runs
before the bucket is opened, a refusal is indistinguishable from an absence, an
active content type is never served inline, nothing is cached by a shared proxy
— are security properties rather than API design.

The README's Transports section objects to shipping handlers on three grounds,
and each is worth answering here rather than leaving to the reader.

They version your routes on somebody else's cadence. The path is yours:
WithBasePath moves the mount point, and Mount registers the one route wherever
you point it. What this package versions is the guard, which is the part you
want held to a release.

They ship types your proto does not have. There is no domain type on this wire
at all. The response body is the object; the request is a path parameter. A
generated client is not the thing that reads it — an <img> tag is.

They guess a scoping rule. It does not guess: the scope comes off the caller
through the resolver, is bound as a tenancy.Scope on the read, and an object in
another tenant reads as absent because that is what registry's store already
does with it.

# The row id addresses the object, not the key

The path parameter is the registry row's ID rather than the object's key, and
that is deliberate twice over. A key is a path — "avatars/<user>/original.png"
— so it is not a path parameter, and a route that accepted one would be a
wildcard whose segments this package would then have to reassemble. And putting
the key in the URL teaches every caller that the key is the address, which is
the habit the guard exists to break.

registry.Store.GetObjectByKey therefore goes unused here. A consumer that has
keys in URLs already — an existing bucket adopted under a registry, which is a
case that package explicitly supports — resolves the key to a row itself and
redirects, rather than being handed a second route here that re-legitimizes the
key as an address.

# Ranges, and what happens without them

A range is why this is HTTP rather than an RPC: a video that cannot be seeked
and a PDF whose reader must fetch the whole file to render page nine are the two
things a metadata surface leaves you to build.

Ranges are served when the UploadManager implements uploads.RangeReader, which
objectstorage.Uploader does. The object is presented to net/http as a seeker
that opens storage once, at the offset the request asked for, so a range costs
one read and the seeking net/http does to measure the object costs none — and
every conditional it already knows, If-Range and If-Modified-Since and
multipart ranges, comes along with it. See rangedObject for what that one read
covers and what it costs.

A manager that cannot open a range gets the whole object under Accept-Ranges:
none, and a Range header on such a request is ignored rather than answered.
Emulating a range by opening at zero and discarding to the offset was the other
option and is refused: it is byte handling this package has no business doing,
it costs the transfer it claims to save, and it would report a capability the
storage behind it does not have.

# The two headers this package will not let you set

Content-Disposition is decided here, from the row's content type, and it is not
configurable. A content type a browser executes — HTML, XHTML, SVG, and an
object whose row records no type at all — is served as an attachment; everything
else is served inline. Serving a user-uploaded document inline from the
application's own origin is stored cross-site scripting, and it is reached by
uploading a file rather than by finding a bug. X-Content-Type-Options: nosniff
rides along for the other half of the same problem, so a browser cannot decide
the row is wrong about what it is looking at.

Cache-Control is private, always. Every object this route serves is one a guard
was consulted about, and a shared cache that answered the second request would
be answering it without the guard.

# Refusals say nothing

An object that does not exist, an object in another tenant, and an object the
caller is not entitled to are one answer: 404, with the platform's own error
envelope. Separating them would be an oracle — a 403 for an object that exists
and a 404 for one that does not tells whoever is enumerating IDs which of their
guesses are real, which is the same reasoning registry's own ErrObjectNotFound
is written under.

That mapping is made here rather than through a registered error mapper.
registry ships no HTTPMapper, and this package does not add one: the only
sentinel that would reach a client through this route is ErrObjectNotFound, the
route answers it before any encoding happens, and a mapper installed for one
status would put the module's one metadata sentinel on a wire that carries no
metadata. Everything else is a 500, through errors/http, which is the honest
answer to a bucket that would not open.

# What is not here

No write, no delete, no archive. registry.Store.ArchiveObject is metadata-only
by an existing ruling — the row is hidden and the object stays in the bucket,
because whether a receipt is still needed for tax purposes is the consumer's
retention policy — and a DELETE on this route that removed bytes would overturn
that ruling from the transport. Uploading is the consumer's endpoint, over the
consumer's own form, because the key, the owner and the subject an object hangs
off are all theirs; registry.StoreAndRecord is the line at the end of it.

The seven store methods stay off the wire. There is no metadata surface here —
no list-my-objects, no read-the-record — because listing is a resource surface
over a consumer's noun and this is the guarded serve.
*/
package http

//platform:transport binding: an object's bytes, guarded by the row rather than by knowledge of the key

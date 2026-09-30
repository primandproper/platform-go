/*
Package grpc is the media registry's resource surface: putting an object in the
bucket as the caller's, registering one that arrived some other way, and reading
back what is theirs.

	srv, err := mediaregistrygrpc.NewServer(store, client, manager,
		mediaregistrygrpc.WithCallerResolver(callerFromSession),
		mediaregistrygrpc.WithContentTypePolicy(mediaregistrygrpc.AllowContentTypes("image/png", "image/jpeg")),
		mediaregistrygrpc.WithAfterUpload(meterUpload),
		mediaregistrygrpc.WithPillars(pillars))
	if err != nil {
		return err
	}

	grpcserver.New(..., []grpcserver.RegistrationFunc{srv.RegisterOn})

# Why this exists

mediaregistry/http shipped first and alone, as a binding: one route serving an
object's bytes, and the store's methods kept off the wire on the premise that a
listing is a resource surface over a consumer's noun. The premise does not hold.
An object is bytes in a bucket and the row saying whose they are, and every rule
a product has about them is a value rather than a shape — which types, how
large, where in the bucket, what gets metered. A deployment without this surface
writes its own gRPC service over a platform table, and the one this surface was
written against had the bugs a shared one would not: a refusal that told a
caller which objects exist, the whole upload buffered in memory, a registration
that accepted any unclaimed key, and no way to attach an upload to its sender.

# The defaults

Every policy is an option and every option but one has a default:

  - WithCallerResolver — who is asking and in which tenant. Required, and
    mediaregistry/http's CallerResolver, so a deployment hands both surfaces the
    same function. service derives it from Transports the way it derives the
    serve route's.
  - WithEntitlement — which objects a caller may read. OwnerOnly, and
    mediaregistry/http's Entitlement, for the same reason.
  - WithKeyFunc — where an upload's bytes go. DefaultKeyFunc,
    <principal>/<object id>/<name>.
  - WithContentTypePolicy — which declared types are accepted. The default
    refuses what a browser executes and a type nobody stated, using the list
    mediaregistry/http decides dispositions from. AllowContentTypes is the
    allowlist most products want instead.
  - WithMaxBytes — how large an upload may be. DefaultMaxBytes, 100 MiB, on by
    default and switched off by name with WithoutMaxBytes.
  - WithRecordKeyPolicy — which keys a caller may register as theirs.
    KeysUnderPrefix over the server's KeyFunc: the part of the bucket the layout
    gives this caller, and nothing else.
  - WithAfterUpload — what runs once an upload lands. Nothing: platform meters
    no upload, because which meter it counts against is the deployment's. It is
    where that metering goes, and it cannot fail an upload that succeeded.

# What an upload may be attached to

The caller's own user subject — UserSubjectType and their principal identifier
— or nothing. This surface cannot know whether recipe 123 is the caller's, so it
does not let a caller say so. An attachment to one of the consumer's nouns goes
through the consumer's own RPC, which authorizes the subject it names and calls
mediaregistry.StoreAndRecord with it; that is what the domain's upload RPCs are
for, and this surface does not replace them. There is no seam for authorizing
other subjects here until a second consumer asks for one.

The self-attachment is what an avatar is: a user uploads attached to
themselves, and ListObjectsBySubject answers with it. What "the avatar" means —
the newest, a pointer row, an event — is the consumer's reading of that list,
and platform never learns the word.

# Streaming, and what is never held

UploadObject is a client stream: a header, then chunks. The header is checked
against every rule that can refuse the upload before a chunk is read, the chunks
go to the UploadManager through a reader as they arrive, and the cap is enforced
as they pass, so an upload that crosses it is refused mid-stream with the rest
unread. Nothing buffers the object. The bytes are saved outside any transaction
and the row is written afterwards in a short one of its own — the arrangement
mediaregistry.StoreAndRecord's documentation recommends for a caller who cannot
hold a transaction open for as long as a large object takes.

# Refusals say nothing

An object that does not exist, one in another tenant, one the Entitlement
declines, one the caller tries to archive and does not own, an attachment to
somebody else, and a registration of a key outside the caller's part of the
bucket or of one holding nothing are all one answer: NOT_FOUND, with the same
message and the same encoded chain. Separating them would be an oracle, and it
is the answer mediaregistry/http's serve route gives. The reason is on the
span, for whoever operates the deployment.

What is not collapsed is a request that is malformed — a missing header, a name
with a path separator in it, a content type the deployment does not accept, an
upload over the cap — which is INVALID_ARGUMENT, because saying so discloses
nothing about anybody's objects. And a failure to decide, which is INTERNAL.

# Who may use each method

Each RPC declares a permission — see Permissions — and platform grants none of
them. UploadObject is a stream: a consumer's authentication and authorization
interceptors reach it only through their stream halves, and a server that
installs only unary ones leaves every upload unauthenticated.

# What is not here

No byte read. mediaregistry/http's guarded serve is where an object's bytes come
back, because a range is what an image tag and a video player send.

No read by key, and no listing of the whole tenant. A key is not an address —
mediaregistry/http's documentation makes that argument — and a tenant-wide
listing is an operator's read, which a deployment serves from its own console.
No owner-wide archive: mediaregistry.Store.ArchiveObjectsForOwner is erasure
machinery, reached through mediaregistry/privacy from a deployment's erasure
run, committing with the rest of a subject's footprint. roster_test.go is the
list, checked against the store.
*/
package grpc

//platform:transport resource surface: uploading, registering and reading back the caller's objects — over `mediaregistry.Store` and `uploads.UploadManager`

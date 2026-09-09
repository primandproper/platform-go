/*
Package grpc is comments on the wire: the gRPC service over
[github.com/primandproper/platform-go/v14/comments]'s Store, the converters
between the generated messages and its types, a typed client, and the default
permission fragment a consumer composes into their policy.

It is imported as commentsgrpc.

# The shape

Eight RPCs over comments.Store's ten methods. Five reads, each one call on
Client.Reader(); three writes, each one transaction this handler owns, because
the store's writes take a database.Tx and an RPC handler is the caller with
nothing of its own to join. No method here orchestrates anything: it converts,
calls one thing, and converts back.

The two absences are the bulk erasures, DeleteCommentsForTarget and
DeleteCommentsByAuthor, and they are one case rather than two. Both are hard
deletes of everything matching, and both exist to commit inside somebody else's
transaction — the one that removes the thing being discussed, or the one that
destroys the rest of a subject's footprint. An RPC moves the write into a
transaction of its own, at a moment the caller does not choose, so what you get
is a target gone with its comments still live, or a subject erased everywhere
but here. See identity/grpc for the pattern this is an instance of, and
comments.Store, which says it on each of the two methods.

# Three seams, and one of them has a default

Who is calling is [Principal], which a consumer's own authentication
interceptor puts on the context and [PrincipalExtractor] reads back. It is
identity/grpc's alias rather than a second interface: a deployment has one
notion of a caller.

What each method requires is [Permissions], declared in one call by [Require]
and evaluated by authorization/grpc's interceptor before the request body has
been looked at. Five grants over eight RPCs.

Whose comments a caller may touch is [AuthorAuthorizer], and it is here because
it cannot be there. A grant on the method says whether this caller may edit
comments at all; it cannot say whose, because the author of a comment is a
column and an interceptor holding the request and the grants has no handle to
read one. Unlike the other two seams it has a default, [OwnCommentsOnly], and
that default is closed: a consumer who says nothing gets a discussion in which
authors edit and archive their own words and nobody else's, rather than one in
which a single grant is the right to rewrite anybody's sentence under their own
name.

# Authorship comes off the connection

comments.Comment.Author is a string because this package does not own the
directory people live in, and on this surface it is filled from the principal
the consumer's interceptor resolved. It is not a request field, and
comments.proto reserves the name so that it cannot become one: a comment is a
sentence attributed to somebody, and attribution a client could choose is
attribution that says whatever the client wanted.

The same rule and the same reserved name cover the scope, which binds off the
connection on every read and every write. A comment in another tenant's scope
reads as one that does not exist.

# The catalog stays the consumer's

A target type is an opaque string in the schema and will not become a generated
enum. Which kinds of thing accept comments is comments.Targets, supplied through
comments.WithTargets, and an enum would put a consumer's vocabulary on this
module's release cadence — the first of the three objections the README's
Transports section says the split answered.

The optional existence check does not cross the wire and could not. It is a Go
func the consumer registers on a target definition, reading their own table on
their own connection; the RPC calls the store, the store runs the check, and
comments.ErrTargetNotFound is what comes back.

Reads are deliberately not gated on the catalog, here as in the store. The
catalog exists to stop a comment being written where nothing will list it, and
the type that has been withdrawn from a catalog is exactly the one whose rows an
operator still needs to reach.

# How a method fails

Every failure here is one call:

	grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "reading comment %q", id)

which logs, traces, maps the code and returns an error that is still the sentinel
comments gave it — the chain intact for the encoding interceptor, the status
alongside it.

The code passed is a default, not an answer. The interceptor re-runs MapToGRPC
over the preserved chain, so comments.GRPCMapper wins over whatever a method
guessed, and that is why nothing here switches on a sentinel. Six of this
package's refusals are also registered as client-safe, so the sentence each
carries reaches the client instead of the code's name — which matters most where
the codes collide, since four of the six are InvalidArgument and a person told
that six ways cannot tell which field to go back to.

# Mounting it

	srv, err := commentsgrpc.NewServer(store, client, principalFromContext,
	    commentsgrpc.WithPillars(pillars),
	    commentsgrpc.WithAuthorAuthorizer(yourModerationRule))

	reqs, err := commentsgrpc.Require(authzgrpc.NewRequirements()).Build()

	errormappers.Register()   // or service.Register, which makes this call

	grpcServer, err := grpcserver.NewGRPCServer(ctx, cfg,
	    []grpc.UnaryServerInterceptor{
	        authn,                                  // yours: puts the Principal on the context
	        enforcer.UnaryServerInterceptor(),      // authorization/grpc, over the requirements above
	        grpcerrors.UnaryErrorEncodingInterceptor(),
	    },
	    nil,
	    []grpcserver.RegistrationFunc{srv.RegisterOn},
	)

The errormappers.Register call is not optional and is not made here. Without it
every sentinel this service returns arrives as codes.Unknown — a reply to a
reply included — because the mapping lives beside the sentinels in comments and
nothing installs itself into a process-wide registry by being linked in. See
[github.com/primandproper/platform-go/v14/errormappers].
*/
package grpc

//platform:transport resource surface: one noun and its whole lifecycle — over `comments.Store`

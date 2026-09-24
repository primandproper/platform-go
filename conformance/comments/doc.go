/*
Package comments is the discussion surface's promises, assertable against any
subject that mounts it.

Eight RPCs, and three questions a consumer needs answered about them. Does a
comment go in the way it was written and come back the way it was stored, with
its author taken from the connection rather than from anything the request
said? Does a discussion hang together — a reply belongs to its parent's target,
a reply to a reply is refused, and a reply outlives the comment it answered?
And does nobody reach words that are not theirs to reach: a neighboring
tenant's by the scope, and a colleague's, for rewriting or archiving, by the
author authorizer?

# Where the targets come from

Which kinds of thing accept comments is the application's vocabulary, declared
in its comments.Targets, and no suite can guess one. So every assertion here
comments on the one target type the subject names in Seams.CommentTargetType,
and on a target identifier minted per test — which is what makes a listing by
target read only what that test wrote, in a deployment whose discussions the
suite does not own. A subject that names no target type skips all of it, with
the reason printed.

The type has to accept any identifier. A target type whose registration carries
an existence check refuses a comment on a thing the application does not have,
and the suite has no way of making one exist; a subject whose only target types
are checked has nothing to name here yet.

# What is here and what stayed behind

comments/grpc keeps everything about a server rather than about a call: what
NewServer refuses to be built from, the permission roster, the schema checks,
the converters, and the store-method roster that pins which store methods are
not on the wire. It also keeps everything that varies how the server was built
or who is calling it — an author authorizer that permits or cannot decide, an
existence check that finds nothing, a catalog narrowed after the fact, and a
principal that names nobody, which no deployment can produce.

It keeps include_archived too. Whether a caller receives archived comments
depends on the grants they hold, and a subject mints callers without saying
what they may do; an assertion about it would be asserting against a principal
no subject can describe.

# Both directions, deliberately

Each confinement assertion proves the caller reaches its own comment through
the same client before proving it cannot reach a neighbor's, and each refusal
of a colleague's words is preceded by the same call made on the caller's own. A
surface that refused everybody, or a scope that resolved to nothing, would
otherwise pass every one of them.

# The subject's rule

The refusals of a colleague's words assume what comments/grpc's own default
says: an author rewrites and archives only what they wrote. A deployment with
moderators supplies a wider rule, and is right to, but the callers a subject
mints are not its moderators — so the assertions hold there too, and a subject
that minted every caller as a moderator would be describing a deployment these
refusals were not written for.
*/
package comments

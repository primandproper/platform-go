package grpc

import (
	"github.com/primandproper/platform-go/v14/comments/commentspb"

	"github.com/primandproper/primitives-go/v2/authorization"
	authzgrpc "github.com/primandproper/primitives-go/v2/authorization/grpc"
)

// The permissions this service's methods require, in authorization's
// vocabulary.
//
// They are declared here rather than in the authorization package because
// authorization is a primitive: it owns what a Permission *is* — a string, a
// set, a role that inherits — and owns no domain's names.
//
// The strings are dotted and namespaced so that a consumer composing several
// domains' fragments cannot have two of them collide on "read". They are values
// rather than an enum because a consumer's policy is data — a YAML file, a
// table of roles — and it has to be able to name one without importing Go.
//
// There are five of them over eight RPCs, and the collapses are deliberate. The
// three reads of one discussion share one grant, because a comment's audience
// is the discussion it is in and a policy that let somebody read a thread's
// roots but not its replies would describe half a page. What does not collapse
// into them is the moderation read, which is every comment about a kind of
// thing rather than about one of them.
const (
	// PermissionCreateComments covers writing a comment and replying to one.
	//
	// It is one grant rather than two, because a reply is a comment: the
	// difference between them is a parent identifier, and a policy that
	// separated them would be a policy in which somebody may start a discussion
	// and not take part in it.
	PermissionCreateComments authorization.Permission = "comments.create"

	// PermissionReadComments covers reading one comment and paging one target's
	// discussion — its roots, and one root's replies.
	//
	// It is the grant a product gives to whoever can see the thing being
	// discussed. What it does not cover is either of the two reads that cross a
	// discussion: everything about a kind of thing, and everything one person
	// wrote.
	PermissionReadComments authorization.Permission = "comments.read"

	// PermissionModerateComments covers paging every comment about one kind of
	// thing — "everything anybody has said about recipes", roots and replies
	// alike, across every target of that type.
	//
	// It is its own grant because it is the only read here that is not about a
	// discussion somebody is in. It is what a moderation queue pages and what an
	// operator runs before withdrawing a target type, and it answers with rows
	// whose targets the caller may never have been shown.
	PermissionModerateComments authorization.Permission = "comments.moderate"

	// PermissionUpdateComments covers revising a body.
	//
	// The grant is the first of two questions, and [AuthorAuthorizer] is the
	// second: this says whether the caller may edit comments at all, and the
	// authorizer says whose. A holder acting on their own words is never asked
	// the second.
	PermissionUpdateComments authorization.Permission = "comments.update"

	// PermissionArchiveComments covers taking a comment out of the discussion.
	//
	// It is separate from PermissionUpdateComments, and the split is the one a
	// moderation policy actually makes: removing something somebody said and
	// rewriting it are different amounts of trust, and the second is the one
	// that leaves their name on a sentence they did not write.
	PermissionArchiveComments authorization.Permission = "comments.archive"
)

// Permissions is the default map from method name to what it requires: every
// RPC this service declares, and nothing else.
//
// Every method is in it. There is no second set of methods that require nothing
// — this service serves no RPC a caller reaches without a grant, because a read
// with no principal has no scope to filter on — so a method missing from this
// map is a bug rather than a decision, and the suite reads the service
// descriptor rather than a list in order to say so.
//
// The keys are the generated full method name constants rather than strings, so
// an RPC renamed in the .proto is a compile error here instead of a method that
// silently requires nothing.
//
// It returns a fresh map each call, so a consumer composing it into their own
// policy and then overriding an entry is editing their copy.
func Permissions() map[string][]authorization.Permission {
	return map[string][]authorization.Permission{
		commentspb.CommentsService_CreateComment_FullMethodName:            {PermissionCreateComments},
		commentspb.CommentsService_GetComment_FullMethodName:               {PermissionReadComments},
		commentspb.CommentsService_ListRootComments_FullMethodName:         {PermissionReadComments},
		commentspb.CommentsService_ListReplies_FullMethodName:              {PermissionReadComments},
		commentspb.CommentsService_ListCommentsByTargetType_FullMethodName: {PermissionModerateComments},
		commentspb.CommentsService_ListCommentsByAuthor_FullMethodName:     {PermissionReadComments},
		commentspb.CommentsService_UpdateComment_FullMethodName:            {PermissionUpdateComments},
		commentspb.CommentsService_ArchiveComment_FullMethodName:           {PermissionArchiveComments},
	}
}

// Require declares every method of this service on a requirements builder.
//
// It is one line over Permissions today and is the exported name anyway,
// because authorization/grpc is fail-closed: a method declared nowhere is
// denied, and what a consumer needs is a call that stays correct when this
// service's method set changes.
//
// A nil builder is tolerated and returns nil, so composing several domains'
// fragments in a chain does not need a nil check per link.
func Require(b *authzgrpc.RequirementsBuilder) *authzgrpc.RequirementsBuilder {
	if b == nil {
		return nil
	}

	return b.RequireAll(Permissions())
}

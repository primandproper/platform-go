package grpc_test

import (
	"testing"

	"github.com/primandproper/platform-go/v14/comments/commentspb"
	commentsgrpc "github.com/primandproper/platform-go/v14/comments/grpc"

	"github.com/primandproper/primitives-go/authorization"
	authzgrpc "github.com/primandproper/primitives-go/authorization/grpc"

	"github.com/shoenig/test"
	"github.com/shoenig/test/must"
)

// TestEveryRPCDeclaresAPermission reads the service descriptor rather than a
// list, so an RPC added to the .proto and forgotten here fails as a method that
// requires nothing rather than shipping as one.
//
// authorization/grpc is fail-closed, so the practical effect of the omission is
// a method nobody can call — but the failure this catches is the other one: a
// consumer who composed Permissions() into their policy and granted everything
// in it still has a method whose grant they never saw.
func TestEveryRPCDeclaresAPermission(T *testing.T) {
	T.Parallel()

	declared := commentsgrpc.Permissions()
	must.MapNotEmpty(T, declared)

	for _, method := range commentspb.CommentsService_ServiceDesc.Methods {
		full := "/" + commentspb.CommentsService_ServiceDesc.ServiceName + "/" + method.MethodName

		T.Run(method.MethodName, func(t *testing.T) {
			t.Parallel()

			required, ok := declared[full]
			must.True(t, ok, must.Sprintf("%s requires no permission", full))
			test.SliceNotEmpty(t, required)
		})
	}
}

// TestNoPermissionOutlivesItsRPC is the other direction: a key naming a method
// the service does not serve is a policy entry a consumer would grant for
// nothing.
func TestNoPermissionOutlivesItsRPC(T *testing.T) {
	T.Parallel()

	served := make([]string, 0, len(commentspb.CommentsService_ServiceDesc.Methods))
	for _, method := range commentspb.CommentsService_ServiceDesc.Methods {
		served = append(served, "/"+commentspb.CommentsService_ServiceDesc.ServiceName+"/"+method.MethodName)
	}

	for full := range commentsgrpc.Permissions() {
		test.SliceContains(T, served, full,
			test.Sprintf("%s is declared and is not an RPC on this service", full))
	}
}

// TestPermissionsReturnsAFreshMap: a consumer composing this into their own
// policy and then overriding an entry is editing their copy.
func TestPermissionsReturnsAFreshMap(T *testing.T) {
	T.Parallel()

	first := commentsgrpc.Permissions()
	first[commentspb.CommentsService_GetComment_FullMethodName] = nil

	second := commentsgrpc.Permissions()

	test.SliceNotEmpty(T, second[commentspb.CommentsService_GetComment_FullMethodName])
}

// TestTheModerationReadHasAGrantOfItsOwn pins the one collapse this file
// deliberately does not make: reading a discussion and reading every comment
// about a kind of thing are different questions asked by different people.
func TestTheModerationReadHasAGrantOfItsOwn(T *testing.T) {
	T.Parallel()

	declared := commentsgrpc.Permissions()

	test.SliceContains(T, declared[commentspb.CommentsService_ListCommentsByTargetType_FullMethodName],
		commentsgrpc.PermissionModerateComments)

	test.SliceNotContains(T, declared[commentspb.CommentsService_ListCommentsByTargetType_FullMethodName],
		commentsgrpc.PermissionReadComments)
}

// TestTheThreeDiscussionReadsShareOneGrant is the collapse this file does make,
// and the reason: a comment's audience is the discussion it is in, so a policy
// that let somebody read a thread's roots and not its replies would describe
// half a page.
func TestTheThreeDiscussionReadsShareOneGrant(T *testing.T) {
	T.Parallel()

	declared := commentsgrpc.Permissions()

	reads := []string{
		commentspb.CommentsService_GetComment_FullMethodName,
		commentspb.CommentsService_ListRootComments_FullMethodName,
		commentspb.CommentsService_ListReplies_FullMethodName,
	}

	for _, method := range reads {
		test.Eq(T, []authorization.Permission{commentsgrpc.PermissionReadComments}, declared[method])
	}
}

// TestRequireDeclaresEveryMethod is the call a consumer actually makes, and it
// stays correct when this service's method set changes.
func TestRequireDeclaresEveryMethod(T *testing.T) {
	T.Parallel()

	reqs, err := commentsgrpc.Require(authzgrpc.NewRequirements()).Build()
	must.NoError(T, err)
	must.NotNil(T, reqs)

	declared := reqs.Methods()

	for _, method := range commentspb.CommentsService_ServiceDesc.Methods {
		full := "/" + commentspb.CommentsService_ServiceDesc.ServiceName + "/" + method.MethodName

		test.SliceContains(T, declared, full, test.Sprintf("%s carries no requirement", full))
	}
}

// TestRequireToleratesANilBuilder: composing several domains' fragments in a
// chain should not need a nil check per link.
func TestRequireToleratesANilBuilder(T *testing.T) {
	T.Parallel()

	test.Nil(T, commentsgrpc.Require(nil))
}

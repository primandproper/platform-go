package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/comments"
	"github.com/primandproper/platform-go/v14/comments/commentspb"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/filtering"
	"github.com/primandproper/primitives-go/v2/filtering/filteringpb"
	filteringgrpc "github.com/primandproper/primitives-go/v2/filtering/grpc"

	"google.golang.org/grpc/codes"
)

// The five reads: one comment, a target's roots, a root's replies, everything
// about a kind of thing, and everything one person wrote.
//
// Each is one call on Client.Reader(), outside any transaction, because a read
// on this surface has nothing to join. Each binds the scope off the caller's
// principal, so a comment in another tenant's scope reads as one that does not
// exist — which is what it is from here, and is the answer that does not turn a
// read into an oracle for what other tenants have been saying.
//
// Two of them cross a discussion rather than serving one, and each is gated
// differently. [Server.ListCommentsByTargetType] is the moderation read and has
// a grant of its own. [Server.ListCommentsByAuthor] names a person, so an
// author who is not the caller is asked of [AuthorAuthorizer].
//
// None of them gates on the consumer's target catalog. That is comments' own
// asymmetry arriving intact: the catalog exists to stop a comment being written
// where nothing will list it, and the type that has been withdrawn from a
// catalog is exactly the one whose rows an operator still needs to reach.

// GetComment reads one of the caller's tenant's live comments.
//
// A comment that is absent, archived, or in another tenant's scope is
// comments.ErrCommentNotFound, which are the same answer from here.
func (s *Server) GetComment(
	ctx context.Context,
	request *commentspb.GetCommentRequest,
) (*commentspb.GetCommentResponse, error) {
	ctx, req, done, err := s.caller(ctx, commentspb.CommentsService_GetComment_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetCommentId()
	req.op.Set(commentIDKey, id)

	comment, err := s.store.GetComment(ctx, s.client.Reader(), req.scope, id)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "reading comment %q", id)

		return nil, err
	}

	return &commentspb.GetCommentResponse{Result: CommentToProto(comment)}, nil
}

// ListRootComments pages the top level of one target's discussion: the comments
// that reply to nothing.
//
// It is the read a discussion opens with, and it is a separate RPC from
// [Server.ListReplies] rather than one with an empty parent, because "the
// roots" and "this comment's replies" are two questions somebody asks
// deliberately.
//
// The count a client wants beside the discussion is on the response's
// pagination: the filtered count is of the target's roots, not of the page.
func (s *Server) ListRootComments(
	ctx context.Context,
	request *commentspb.ListRootCommentsRequest,
) (*commentspb.ListRootCommentsResponse, error) {
	ctx, req, done, err := s.caller(ctx, commentspb.CommentsService_ListRootComments_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	target := TargetFromProto(request.GetTarget())

	req.op.Set(targetTypeKey, target.Type.String()).Set(targetIDKey, target.ID)

	filter, err := readFilter(req, request.GetFilter(), "a target's root comments")
	if err != nil {
		return nil, err
	}

	page, err := s.store.ListRootComments(ctx, s.client.Reader(), req.scope, target, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal,
			"listing the root comments of %s %q", target.Type, target.ID)

		return nil, err
	}

	return &commentspb.ListRootCommentsResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    CommentsToProto(page.Data),
	}, nil
}

// ListReplies pages one root comment's replies.
//
// The target is named as well as the parent because a reply carries both and
// the statement keys on both, so naming it costs a caller nothing and buys the
// read the index it was written for.
//
// A parent that is no longer there is not an error: a reply outlives the
// comment it replies to — archived, or erased with its author — and it is still
// a reply.
func (s *Server) ListReplies(
	ctx context.Context,
	request *commentspb.ListRepliesRequest,
) (*commentspb.ListRepliesResponse, error) {
	ctx, req, done, err := s.caller(ctx, commentspb.CommentsService_ListReplies_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	target := TargetFromProto(request.GetTarget())
	parentID := request.GetParentId()

	req.op.Set(targetTypeKey, target.Type.String()).
		Set(targetIDKey, target.ID).
		Set(parentIDKey, parentID)

	filter, err := readFilter(req, request.GetFilter(), "a comment's replies")
	if err != nil {
		return nil, err
	}

	page, err := s.store.ListReplies(ctx, s.client.Reader(), req.scope, target, parentID, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing the replies to comment %q", parentID)

		return nil, err
	}

	return &commentspb.ListRepliesResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    CommentsToProto(page.Data),
	}, nil
}

// ListCommentsByTargetType pages every comment about one kind of thing — roots
// and replies alike, across every target of that type.
//
// It is the moderation read, and it is behind a grant of its own because it is
// the only read here that is not about a discussion the caller is in: the rows
// it returns are about targets they may never have been shown.
func (s *Server) ListCommentsByTargetType(
	ctx context.Context,
	request *commentspb.ListCommentsByTargetTypeRequest,
) (*commentspb.ListCommentsByTargetTypeResponse, error) {
	ctx, req, done, err := s.caller(ctx, commentspb.CommentsService_ListCommentsByTargetType_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	targetType := comments.TargetType(request.GetTargetType())
	req.op.Set(targetTypeKey, targetType.String())

	filter, err := readFilter(req, request.GetFilter(), "a target type's comments")
	if err != nil {
		return nil, err
	}

	page, err := s.store.ListCommentsByTargetType(ctx, s.client.Reader(), req.scope, targetType, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing the comments about %q", targetType)

		return nil, err
	}

	return &commentspb.ListCommentsByTargetTypeResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    CommentsToProto(page.Data),
	}, nil
}

// ListCommentsByAuthor pages what one person wrote within the caller's tenant.
//
// An empty author is the caller's own, which is what a "your comments" view
// sends and is the one value that reaches the store without a second question
// being asked. Naming somebody else is the moderation read of a person, and
// [AuthorAuthorizer] answers it — a refusal is PermissionDenied, before any row
// is read, so the code says nothing about whether that identifier belongs to
// anybody.
func (s *Server) ListCommentsByAuthor(
	ctx context.Context,
	request *commentspb.ListCommentsByAuthorRequest,
) (*commentspb.ListCommentsByAuthorResponse, error) {
	ctx, req, done, err := s.caller(ctx, commentspb.CommentsService_ListCommentsByAuthor_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	author := request.GetAuthor()
	if author == "" {
		author = req.userID
	}

	req.op.Set(authorKey, author)

	if err = s.authorizeNamedAuthor(ctx, req, author, "listing the comments written by %q", author); err != nil {
		return nil, err
	}

	filter, err := readFilter(req, request.GetFilter(), "an author's comments")
	if err != nil {
		return nil, err
	}

	page, err := s.store.ListCommentsByAuthor(ctx, s.client.Reader(), req.scope, author, filter)
	if err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "listing the comments written by %q", author)

		return nil, err
	}

	return &commentspb.ListCommentsByAuthorResponse{
		Pagination: filteringgrpc.PaginationToProto(page.Pagination),
		Results:    CommentsToProto(page.Data),
	}, nil
}

// readFilter reads the page a request asked for.
//
// It is a helper because a malformed filter is the one failure all four paged
// reads share, and because the code it answers with is a decision rather than a
// default: a sort direction nothing recognizes is the client's to fix, so it is
// InvalidArgument and not the Internal every other call site passes.
func readFilter(
	req *request,
	in *filteringpb.QueryFilter,
	what string,
) (*filtering.QueryFilter, error) {
	filter, err := filteringgrpc.FromProto(in)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "reading the filter of a page of %s", what)
	}

	return filter, nil
}

package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/comments"
	"github.com/primandproper/platform-go/v14/comments/commentspb"

	"github.com/primandproper/primitives-go/v2/database"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"

	"google.golang.org/grpc/codes"
)

// The three writes: say something, revise it, take it out of the discussion.
//
// Each is one transaction that this handler owns, because comments.Store's
// writes take a database.Tx and only Client.WithTransaction produces one. That
// is the store's rule arriving where it was aimed: a comment is rarely the only
// row a consumer writes, and the handler is the caller with nothing of its own
// to join.
//
// None of them switches on a sentinel. The error goes through
// grpcerrors.PrepareAndLogGRPCStatus with codes.Internal as the *default*, and
// the encoding interceptor re-runs the registered mappers over the preserved
// chain, so comments.GRPCMapper wins over the guess made here. The one place a
// code is passed as an answer rather than a default is a request that named no
// comment at all, which nothing maps because it is this package's own reading
// of a malformed message.

// CreateComment writes one comment in the caller's tenant.
//
// The author is the caller. It is not a field on the request and the schema
// reserves the name, because a comment is a sentence attributed to somebody and
// attribution a client could choose is attribution that says whatever the
// client wanted.
//
// Everything else about the write is the store's: the target is checked against
// the consumer's catalog and, where that definition registers one, against its
// existence hook; a reply's parent must be a live root in the same scope, on the
// same target; the identifier is minted and the creation time is read back
// inside the transaction. Six of those refusals are client-safe sentinels, so a
// client is told which of them refused it rather than being told InvalidArgument
// six ways.
func (s *Server) CreateComment(
	ctx context.Context,
	request *commentspb.CreateCommentRequest,
) (*commentspb.CreateCommentResponse, error) {
	ctx, req, done, err := s.caller(ctx, commentspb.CommentsService_CreateComment_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	comment := commentFromProto(request.GetComment(), req.userID)
	if comment == nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(ErrNilCommentInput,
			req.op.Logger(), req.op.Span(), codes.InvalidArgument, "writing a comment")

		return nil, err
	}

	req.op.Set(authorKey, comment.Author).
		Set(parentIDKey, comment.ParentID).
		Set(targetTypeKey, comment.Target.Type.String()).
		Set(targetIDKey, comment.Target.ID)

	// The store answers with the row it wrote — the identifier it minted, the
	// creation time the database assigned, the target a reply adopted from its
	// parent — so the response is what was stored rather than the request that
	// asked for it, and this handler needs no read to follow the write.
	var stored *comments.Comment

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		created, createErr := s.store.CreateComment(ctx, tx, req.scope, comment)
		if createErr != nil {
			return createErr
		}

		stored = created

		return nil
	}); err != nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, "writing a comment")

		return nil, err
	}

	// Set after the write rather than before it, because a create names no
	// identifier: the one recorded here is the one the store minted, and it is
	// what somebody reading this span is looking for.
	req.op.Set(commentIDKey, stored.ID)

	return &commentspb.CreateCommentResponse{Result: CommentToProto(stored)}, nil
}

// UpdateComment revises what was said, and only that.
//
// It does not move the comment: the target is what the comment is about, the
// parent is which conversation it is in, and the author is who said it, so the
// request carries a body and the schema reserves the other three names.
//
// A caller editing somebody else's words is asked of [AuthorAuthorizer] first,
// and a refusal is answered NotFound rather than PermissionDenied — see
// authorizeRowAuthor for why the read having already happened changes the
// answer.
func (s *Server) UpdateComment(
	ctx context.Context,
	request *commentspb.UpdateCommentRequest,
) (*commentspb.UpdateCommentResponse, error) {
	ctx, req, done, err := s.caller(ctx, commentspb.CommentsService_UpdateComment_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetCommentId()
	req.op.Set(commentIDKey, id)

	var revised *comments.Comment

	if err = s.mutate(ctx, req, id, func(tx database.Tx, existing *comments.Comment) error {
		// The whole entity is not sent back to the store, because the statement
		// assigns the body alone. What is sent is the identifier and the new
		// text; the target, the parent and the author stay whatever the row
		// already holds.
		edit := &comments.Comment{ID: existing.ID, Body: request.GetBody()}

		// The store answers with the stored row, which is what carries the
		// last_updated_at the database stamped: responding with the edit instead
		// would answer with an "edited" marker that has no time on it. It is read
		// back on this transaction, so what comes back is the row the write just
		// left rather than a read of one nothing else can see yet.
		stored, updateErr := s.store.UpdateComment(ctx, tx, req.scope, edit)
		if updateErr != nil {
			return updateErr
		}

		revised = stored

		return nil
	}, "editing comment %q", id); err != nil {
		return nil, err
	}

	return &commentspb.UpdateCommentResponse{Result: CommentToProto(revised)}, nil
}

// ArchiveComment takes one comment out of the discussion, leaving the row for
// whoever asks later what was said.
//
// It archives exactly the comment named. A root's replies stay where they are,
// which is the store's decision and the right one on a wire too: a moderator
// removing an off-topic root has not removed the answers to it, and "in reply
// to a removed comment" is what every discussion UI already renders. A client
// that wants the subtree gone pages ListReplies and archives each.
//
// A comment already archived is comments.ErrCommentNotFound rather than a quiet
// success, because an archived comment is not in the discussion and this method
// addresses the discussion.
func (s *Server) ArchiveComment(
	ctx context.Context,
	request *commentspb.ArchiveCommentRequest,
) (*commentspb.ArchiveCommentResponse, error) {
	ctx, req, done, err := s.caller(ctx, commentspb.CommentsService_ArchiveComment_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	id := request.GetCommentId()
	req.op.Set(commentIDKey, id)

	// The store answers with the row it hid, and nothing here carries it:
	// ArchiveCommentResponse has no field for a comment, because an archive on
	// the wire says only that the comment left the discussion. The row is for a
	// consumer writing an entry beside the write, which this handler is not —
	// its transaction holds the read, the authorization and this one call, and
	// nothing to describe it to.
	if err = s.mutate(ctx, req, id, func(tx database.Tx, existing *comments.Comment) error {
		_, archiveErr := s.store.ArchiveComment(ctx, tx, req.scope, existing.ID)

		return archiveErr
	}, "archiving comment %q", id); err != nil {
		return nil, err
	}

	return &commentspb.ArchiveCommentResponse{}, nil
}

// mutate runs one authorized write against one comment: it opens the
// transaction, reads the row to learn who wrote it, asks [AuthorAuthorizer]
// where that is somebody other than the caller, and then runs write inside the
// same transaction.
//
// The read is inside the transaction rather than in front of it, which is the
// whole reason this helper exists. identity/grpc records the trap: an ownership
// check standing ahead of a write is not a check, because it reads through one
// connection what the write acts on through another. Here the read and the
// write are the same transaction, so the row the authorizer was shown is the
// row the write lands on — and a comment archived by somebody else in between
// makes the write itself report comments.ErrCommentNotFound rather than
// succeeding against a row that has moved.
//
// A refusal is carried out around the transaction rather than through it. The
// authorizer's error is already a prepared status — NotFound, with the chain
// intact — and handing it back to PrepareAndLogGRPCStatus a second time with
// codes.Internal as the default would map the chain again, find nothing that
// claims [ErrTargetNotPermitted], and answer Internal to a caller who was
// refused. So it is captured and returned as it stands, and only an error the
// transaction produced on its own is prepared here.
func (s *Server) mutate(
	ctx context.Context,
	req *request,
	commentID string,
	write func(tx database.Tx, existing *comments.Comment) error,
	descriptionFmt string,
	descriptionArgs ...any,
) error {
	var refusal error

	if err := s.client.WithTransaction(ctx, func(tx database.Tx) error {
		existing, readErr := s.store.GetComment(ctx, tx, req.scope, commentID)
		if readErr != nil {
			return readErr
		}

		req.op.Set(authorKey, existing.Author).
			Set(parentIDKey, existing.ParentID).
			Set(targetTypeKey, existing.Target.Type.String()).
			Set(targetIDKey, existing.Target.ID)

		refusal = s.authorizeRowAuthor(ctx, req, existing.Author, descriptionFmt, descriptionArgs...)
		if refusal != nil {
			return refusal
		}

		return write(tx, existing)
	}); err != nil {
		if refusal != nil {
			return refusal
		}

		return grpcerrors.PrepareAndLogGRPCStatus(err,
			req.op.Logger(), req.op.Span(), codes.Internal, descriptionFmt, descriptionArgs...)
	}

	return nil
}

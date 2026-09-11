package grpc

import (
	"context"
	"errors"

	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"

	"google.golang.org/grpc/codes"
)

// ErrTargetNotPermitted indicates a caller who may make this call and may not
// make it about the person this request named or this row was written by.
//
// It is identity/grpc's sentinel rather than a second one of this package's
// own, and deliberately the same value: a consumer has one rule about whose
// rows a caller has standing in, and an authorizer written for the directory
// refuses a comment with the answer it already returns. An errors.Is against
// either name matches.
//
// It is never registered as a client-safe sentinel. Its text says the caller
// was refused, and two of the three places this surface asks the question
// answer as though the row were absent — see the two refusal shapes at the
// bottom of this file.
var ErrTargetNotPermitted = identitygrpc.ErrTargetNotPermitted

// AuthorAuthorizer decides whether the caller may act on a comment somebody
// else wrote.
//
// # Why it exists
//
// Three of this service's RPCs reach words that are not the caller's:
// UpdateComment revises what somebody said, ArchiveComment takes it out of the
// discussion, and ListCommentsByAuthor pages everything one person has written.
// The permission fragment in front of them is a grant on the method — a holder
// of comments.update may call UpdateComment, and nothing in a per-method check
// says whose comment. Without a second question, one grant is the right to
// rewrite anybody's sentence under their own name, which is the failure
// identity/grpc paid for once already: a grant on the method made a member's
// management right directory-wide until something asked whose row it was.
//
// The consumer's authorization interceptor cannot answer it. It holds the
// request and the caller's grants, before the body is parsed, and the author of
// a comment is a column — so the question is asked from inside the handler,
// where the store already is.
//
// It is not tenancy. The scope comes off the [Principal] and every store read
// and write binds it, so nothing here crosses a deployment; this is the check
// inside one.
//
// # It is only ever asked about somebody else
//
// A caller acting on their own comment is not asked at all, on any of the
// three. That is not an optimization: it is what makes [OwnCommentsOnly] a
// usable default rather than a surface with two of its writes disabled, and it
// means an implementation only ever sees the interesting case.
//
// # Why the default is closed rather than absent
//
// billing/grpc ships no default and takes its authorizer positionally, because
// every default available to it is wrong in a way nothing reports — one that
// refuses everything makes six of its RPCs answer as though nothing existed.
// This surface is in identity/grpc's position instead, where a closed default
// is a coherent product: authors edit and archive their own comments, and
// "your comments" is the page a by-author read serves. A consumer who says
// nothing gets a discussion where nobody can rewrite anybody else's words,
// rather than one where a grant is tenant-wide.
//
// A consumer with moderators supplies the rule they already have — a role
// check, a membership read, identity/grpc's own MembershipAuthorizer wrapped in
// [AuthorAuthorizerFunc] — through [WithAuthorAuthorizer].
//
// # What implementations owe
//
// A nil error means permitted. [ErrTargetNotPermitted] means refused. Any other
// error is a failure to decide — a database that would not answer — and reaches
// the client as codes.Internal, which is what keeps an unavailable store from
// reading as a refusal. The three are distinguished by errors.Is, so an
// implementation may wrap the sentinel with context of its own and still be
// refusing.
type AuthorAuthorizer interface {
	// AuthorizeAuthor is asked before an RPC reads or writes comments written by
	// author, and only where author is somebody other than the caller.
	AuthorizeAuthor(ctx context.Context, caller Principal, author string) error
}

// AuthorAuthorizerFunc adapts a function to [AuthorAuthorizer], for a consumer
// whose rule is one closure over something they already hold.
type AuthorAuthorizerFunc func(ctx context.Context, caller Principal, author string) error

var _ AuthorAuthorizer = AuthorAuthorizerFunc(nil)

// AuthorizeAuthor calls f.
func (f AuthorAuthorizerFunc) AuthorizeAuthor(ctx context.Context, caller Principal, author string) error {
	return f(ctx, caller, author)
}

// OwnCommentsOnly is the default [AuthorAuthorizer]: a caller may act on their
// own comments and on nobody else's.
//
// It is a type rather than a function so that a consumer reading a constructor
// call sees the name of the rule they are getting, and so that "this deployment
// has no moderators" is something they can write down explicitly.
//
// It still compares the two identifiers rather than refusing outright, even
// though the server does not ask about a caller's own comments. The seam's
// contract is that an implementation is handed an author and answers about it,
// and a default that answered "no" to every question including the one it is
// the whole point of would be a trap for anyone composing it into a rule of
// their own.
type OwnCommentsOnly struct{}

var _ AuthorAuthorizer = OwnCommentsOnly{}

// AuthorizeAuthor permits the caller's own comments and refuses everybody
// else's with [ErrTargetNotPermitted].
func (OwnCommentsOnly) AuthorizeAuthor(_ context.Context, caller Principal, author string) error {
	if caller != nil && author != "" && caller.UserID() == author {
		return nil
	}

	return ErrTargetNotPermitted
}

// The two ways this surface asks the question, and the one thing that differs
// between them: what a refusal looks like to the client.
//
// They are helpers rather than five lines per method because the code is the
// part that can be got wrong quietly, and there are two of them.

// authorizeNamedAuthor gates an RPC whose request named the person, and answers
// a refusal with codes.PermissionDenied.
//
// The caller learns nothing from it: a person they have no standing over and a
// person nobody has heard of are both refused by the same rule and answered the
// same way, so the code cannot be used to tell which user identifiers exist.
// That is identity/grpc's reading of the same question, and it holds here
// because the check happens before any read.
func (s *Server) authorizeNamedAuthor(
	ctx context.Context,
	req *request,
	author string,
	descriptionFmt string,
	descriptionArgs ...any,
) error {
	return s.authorize(ctx, req, author, codes.PermissionDenied, descriptionFmt, descriptionArgs...)
}

// authorizeRowAuthor gates an RPC that read a comment and then asked who wrote
// it, and answers a refusal with codes.NotFound.
//
// This is where this surface diverges from identity/grpc, and the divergence is
// the point rather than an inconsistency. There the question is asked ahead of
// the read, so a refusal is all the server can say. Here the read has already
// happened, and answering PermissionDenied would tell a caller walking comment
// identifiers exactly which of them are real — the absent row says NotFound and
// the forbidden one would say something else. So both say NotFound, which is
// also the answer a comment in another tenant's scope already gets.
//
// The chain returned is still the refusal, which is what the log and the span
// record. Only the status the client reads is the absence, and
// [ErrTargetNotPermitted] is not client-safe, so its wording does not travel
// with it.
func (s *Server) authorizeRowAuthor(
	ctx context.Context,
	req *request,
	author string,
	descriptionFmt string,
	descriptionArgs ...any,
) error {
	return s.authorize(ctx, req, author, codes.NotFound, descriptionFmt, descriptionArgs...)
}

// authorize asks the seam and turns what it said into a status: the refusal
// code its caller chose, and codes.Internal for an authorizer that could not
// decide.
//
// The caller's own comments never reach the seam. That short-circuit is here
// rather than in the three RPCs that ask, so that it cannot be present in two
// of them and missing from the third.
//
// The Internal case is not a detail. A refusal is a sentence about the caller
// and an outage is not; reporting the second as the first tells a consumer to
// widen their policy while their database is down, and leaves a dashboard
// counting server faults reading zero through it.
func (s *Server) authorize(
	ctx context.Context,
	req *request,
	author string,
	refused codes.Code,
	descriptionFmt string,
	descriptionArgs ...any,
) error {
	if author == req.userID {
		return nil
	}

	err := s.authors.AuthorizeAuthor(ctx, req.principal, author)
	if err == nil {
		return nil
	}

	code := codes.Internal
	if errors.Is(err, ErrTargetNotPermitted) {
		code = refused
	}

	return grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), code,
		descriptionFmt, descriptionArgs...)
}

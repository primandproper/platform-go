package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/callers"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"

	"google.golang.org/grpc/codes"
)

var _ signinpb.SignInAdministrationServiceServer = (*Server)(nil)

// ListSignInsForUser answers the live logins of the user the request names,
// most recently refreshed first.
//
// It is ListSignIns with the subject taken from the request rather than the
// caller, which is the whole of why it is on a service of its own behind
// [PermissionReadAnySignIns]. No entry is marked current: the request was made
// through the operator's login, and none of these is that. The attributes are
// the [SignInAnnotator]'s answer for the named user, as ListSignIns' are for
// the caller.
func (s *Server) ListSignInsForUser(
	ctx context.Context,
	request *signinpb.ListSignInsForUserRequest,
) (_ *signinpb.ListSignInsForUserResponse, err error) {
	ctx, req, done, err := s.administrator(ctx, signinpb.SignInAdministrationService_ListSignInsForUser_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	operator(req, request.GetUserId())

	signIns, err := s.svc.ListSignIns(ctx, req.scope, request.GetUserId(), listLimit(request.GetLimit()))
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "listing a user's sign-ins")
	}

	converted, err := s.annotated(ctx, req.scope, request.GetUserId(), signIns)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "annotating a user's sign-ins")
	}

	return &signinpb.ListSignInsForUserResponse{SignIns: converted}, nil
}

// EndSignInForUser ends one of the named user's logins, named by its family.
//
// The user is part of the key, through signin.HeldBy, so a family that is not
// theirs ends nothing — an operator who pasted the wrong identifier ends
// nobody's login rather than a stranger's. It is reported to
// signin.Hooks.AfterRevokeSignIns as signin.RevocationOperator, with the
// caller as the actor.
func (s *Server) EndSignInForUser(
	ctx context.Context,
	request *signinpb.EndSignInForUserRequest,
) (_ *signinpb.EndSignInForUserResponse, err error) {
	ctx, req, done, err := s.administrator(ctx, signinpb.SignInAdministrationService_EndSignInForUser_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	actor := operator(req, request.GetUserId())

	if _, err = s.svc.RevokeRefreshTokenFamily(ctx, req.scope, request.GetFamilyId(),
		signin.HeldBy(request.GetUserId()), signin.RevokedBy(actor)); err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "ending a user's sign-in")
	}

	return &signinpb.EndSignInForUserResponse{}, nil
}

// EndAllSignInsForUser ends every login the named user holds.
//
// It is SignOutEverywhere done to somebody rather than by them, and it is
// reported that way: signin.RevocationOperator, with the caller as the actor,
// as EndSignInForUser is.
func (s *Server) EndAllSignInsForUser(
	ctx context.Context,
	request *signinpb.EndAllSignInsForUserRequest,
) (_ *signinpb.EndAllSignInsForUserResponse, err error) {
	ctx, req, done, err := s.administrator(ctx, signinpb.SignInAdministrationService_EndAllSignInsForUser_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	actor := operator(req, request.GetUserId())

	if _, err = s.svc.RevokeRefreshTokensForSubject(ctx, req.scope, request.GetUserId(),
		signin.RevokedBy(actor)); err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "ending a user's sign-ins")
	}

	return &signinpb.EndAllSignInsForUserResponse{}, nil
}

// administrator is caller for the administrative RPCs, with the scope taken
// off the operator rather than off the resolver.
//
// caller takes the scope off the resolver, which is safe where the subject is
// the caller: a caller looked up in a directory they are not in is found
// nowhere. Here the subject is named by the request, so the resolver's answer
// would be the directory the operator acts on — and an operator holding the
// permission in one directory, on a connection that resolves to another, would
// be listing and ending the sign-ins of that other directory's users. The
// operator's own directory is the one they administer, which is how
// identity/grpc's operator writes read it too.
func (s *Server) administrator(ctx context.Context, method string) (
	context.Context, *request, func(err error), error,
) {
	ctx, req, done, err := s.caller(ctx, method)
	if err != nil {
		return ctx, nil, done, err
	}

	req.scope = req.principal.Scope()
	req.op.Set(scopeKey, req.scope.String())

	return ctx, req, done, nil
}

// operator records who an administrative RPC is about and who is asking, and
// answers with the second.
//
// The span's user is the subject, as it is on identity's operator writes, and
// the caller moves to the actor key: callers.ActorOf, so an operator acting
// through an impersonation is named rather than the person they are acting as.
func operator(req *request, userID string) string {
	actor := callers.ActorOf(req.principal)

	req.op.Set(userIDKey, userID).Set(actorIDKey, actor)

	return actor
}

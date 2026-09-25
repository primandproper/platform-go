package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/authentication/passwordreset/passwordresetpb"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// RequestPasswordReset mints a link for whoever holds an address and mails it.
//
// It answers identically for an address somebody holds and one nobody does, and
// the response is empty so that it can. The service holds its own answer to a
// floor so the two cannot be told apart by a stopwatch either — see
// passwordreset.Service.Request and WithRequestFloor.
//
// A consumer owes a rate limit in front of this call. It is the one RPC in this
// module that sends mail on behalf of somebody who has proven nothing at all.
func (s *Server) RequestPasswordReset(
	ctx context.Context,
	request *passwordresetpb.RequestPasswordResetRequest,
) (*passwordresetpb.RequestPasswordResetResponse, error) {
	ctx, req, done, err := s.anonymous(ctx, passwordresetpb.PasswordResetService_RequestPasswordReset_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	if err = s.svc.Request(ctx, req.scope, request.GetEmailAddress()); err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "requesting a password reset")
	}

	return &passwordresetpb.RequestPasswordResetResponse{}, nil
}

// VerifyPasswordResetToken reports whether a link can still be spent.
//
// It is the page load before the form, so that somebody who followed a dead link
// is told before they type a password rather than after. It holds nothing open:
// what decides the reset is CompletePasswordReset's own answer, and a token live
// here can be spent by somebody else a moment later.
//
// The three refusals are told apart — expired, already used, never a link — for
// the reason passwordreset.ClientSafeSentinels gives: the secret is
// high-entropy, so learning which one happened requires already holding it.
func (s *Server) VerifyPasswordResetToken(
	ctx context.Context,
	request *passwordresetpb.VerifyPasswordResetTokenRequest,
) (*passwordresetpb.VerifyPasswordResetTokenResponse, error) {
	ctx, req, done, err := s.anonymous(ctx, passwordresetpb.PasswordResetService_VerifyPasswordResetToken_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	token, err := s.svc.Verify(ctx, req.scope, request.GetToken())
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "verifying a password reset token")
	}

	// The deadline and nothing else. What the token knows besides it — which
	// user, which row — is either useless to an anonymous caller or a disclosure
	// to whoever is holding a forwarded link. See the response message.
	return &passwordresetpb.VerifyPasswordResetTokenResponse{
		ExpiresAt: timestamppb.New(token.ExpiresAt),
	}, nil
}

// CompletePasswordReset spends a link and writes the password it was issued for.
//
// The redemption, the password write and the withdrawal of every other link that
// person held are one transaction, one layer down. Nothing is orchestrated here.
//
// It mints no token. Completing a reset does not sign anybody in, and the next
// call a client makes is a sign-in with the password that was just chosen.
//
// A password the service's policy refuses is InvalidArgument carrying
// REPLACEMENT_PASSWORD_REFUSED, and leaves the link live.
func (s *Server) CompletePasswordReset(
	ctx context.Context,
	request *passwordresetpb.CompletePasswordResetRequest,
) (*passwordresetpb.CompletePasswordResetResponse, error) {
	ctx, req, done, err := s.anonymous(ctx, passwordresetpb.PasswordResetService_CompletePasswordReset_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	if _, err = s.svc.Complete(ctx, req.scope, request.GetToken(), request.GetNewPassword()); err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "completing a password reset")
	}

	return &passwordresetpb.CompletePasswordResetResponse{}, nil
}

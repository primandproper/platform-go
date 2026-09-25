package grpc

import (
	"context"
	"math"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// FamilyIdentifier is what a principal implements to say which login the
// request was made through: the access token's "sid" claim, signin.ClaimFamilyID.
//
// It is an optional interface, declared here where it is needed and asserted
// for at the call site, which is callers.Principal's rule for anything past its
// three methods. A consumer whose principal already carries the claim satisfies
// it by adding the method; one whose does not keeps compiling, and ListSignIns
// marks no entry as the current one rather than guessing which it is.
type FamilyIdentifier interface {
	// FamilyID is the login the request's access token belongs to, or empty
	// for a token that names none.
	FamilyID() string
}

// ListSignIns answers the calling user's live logins, most recently refreshed
// first, with the one the request was made through marked current.
//
// It takes its subject from the caller and has no field that could name
// anybody else — the SignOutEverywhere arrangement, for the same reason. An
// operator's view of somebody else's logins is signin.Service.ListSignIns,
// reached through the consumer's own administrative surface with its own
// authorization in front of it.
//
// Which entry is current comes off the principal, through [FamilyIdentifier].
// A principal that does not implement it marks nothing, which is the honest
// answer for a server that was not told.
func (s *Server) ListSignIns(
	ctx context.Context,
	request *signinpb.ListSignInsRequest,
) (_ *signinpb.ListSignInsResponse, err error) {
	ctx, req, done, err := s.caller(ctx, signinpb.SignInService_ListSignIns_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	signIns, err := s.svc.ListSignIns(ctx, req.scope, req.principal.UserID(), listLimit(request.GetLimit()))
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "listing sign-ins")
	}

	var current string
	if identified, ok := req.principal.(FamilyIdentifier); ok {
		current = identified.FamilyID()
	}

	response := &signinpb.ListSignInsResponse{SignIns: make([]*signinpb.ActiveSignIn, 0, len(signIns))}

	for _, signIn := range signIns {
		converted := ActiveSignInToProto(signIn)
		converted.Current = current != "" && signIn.FamilyID == current

		response.SignIns = append(response.SignIns, converted)
	}

	return response, nil
}

// EndSignIn ends one of the calling user's logins, named by its family.
//
// The caller is part of the key, so a family identifier that is not theirs
// ends nothing; and the response is empty whichever of the three reasons a
// request ended nothing was the case, so the RPC is not a way to learn which
// identifiers are live. See signin.Service.EndSignIn.
func (s *Server) EndSignIn(
	ctx context.Context,
	request *signinpb.EndSignInRequest,
) (_ *signinpb.EndSignInResponse, err error) {
	ctx, req, done, err := s.caller(ctx, signinpb.SignInService_EndSignIn_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	if _, err = s.svc.EndSignIn(ctx, req.scope, req.principal.UserID(), request.GetFamilyId()); err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "ending a sign-in")
	}

	return &signinpb.EndSignInResponse{}, nil
}

// ActiveSignInToProto renders one live login. It leaves current false, since
// whether a login is the caller's own is a fact about the request rather than
// about the login.
func ActiveSignInToProto(s *signin.ActiveSignIn) *signinpb.ActiveSignIn {
	if s == nil {
		return nil
	}

	return &signinpb.ActiveSignIn{
		FamilyId:        s.FamilyID,
		SignedInAt:      timestamppb.New(s.SignedInAt),
		LastRefreshedAt: timestamppb.New(s.LastRefreshedAt),
		ExpiresAt:       timestamppb.New(s.ExpiresAt),
		ActiveAccountId: s.ActiveAccountID,
		Administrative:  s.Administrative,
	}
}

// listLimit narrows the wire's limit to the service's. A value too large for
// the service's type is the service's ceiling, which is what the service does
// with any value past it.
func listLimit(limit uint32) uint16 {
	if limit > math.MaxUint16 {
		return signin.MaxSignInListLimit
	}

	return uint16(limit)
}

package grpc

import (
	"context"
	"maps"
	"math"

	"github.com/primandproper/platform-go/v15/authentication/signin"
	"github.com/primandproper/platform-go/v15/authentication/signin/signinpb"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// FamilyIdentifier is what a principal implements to say which login the
// request was made through: the access token's "sid" claim, signin.ClaimFamilyID.
//
// It is an optional interface, declared here where it is needed and asserted
// for at the call site, which is callers.Principal's rule for anything past its
// own methods. A consumer whose principal already carries the claim satisfies
// it by adding the method; one whose does not keeps compiling, and ListSignIns
// marks no entry as the current one rather than guessing which it is.
// EndOtherSignIns cannot make that choice — a sign-out that does not know which
// login is asking has no login to keep — so there it is a refusal.
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
// operator's view of somebody else's logins is [Server.ListSignInsForUser], on
// SignInAdministrationService behind a permission.
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

	userID := req.principal.UserID()

	signIns, err := s.svc.ListSignIns(ctx, req.scope, userID, listLimit(request.GetLimit()))
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "listing sign-ins")
	}

	converted, err := s.annotated(ctx, req.scope, userID, signIns)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "annotating sign-ins")
	}

	var current string
	if identified, ok := req.principal.(FamilyIdentifier); ok {
		current = identified.FamilyID()
	}

	for _, signIn := range converted {
		signIn.Current = current != "" && signIn.GetFamilyId() == current
	}

	return &signinpb.ListSignInsResponse{SignIns: converted}, nil
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

// EndOtherSignIns ends every one of the calling user's logins except the one
// the request was made through.
//
// Which login that is comes off the principal, through [FamilyIdentifier], and
// nowhere else — the request has no field that could name a different one to
// keep. A principal that does not implement it, or names no family, is
// signin.ErrSignInNotIdentified, refused rather than read as "keep nothing":
// ListSignIns can honestly mark nothing current when it is not told, but a
// sign-out that is not told which device is asking would end them all, and that
// is SignOutEverywhere, which a client asks for by name. See
// signin.Service.EndOtherSignIns.
func (s *Server) EndOtherSignIns(
	ctx context.Context,
	_ *signinpb.EndOtherSignInsRequest,
) (_ *signinpb.EndOtherSignInsResponse, err error) {
	ctx, req, done, err := s.caller(ctx, signinpb.SignInService_EndOtherSignIns_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	var keep string
	if identified, ok := req.principal.(FamilyIdentifier); ok {
		keep = identified.FamilyID()
	}

	if _, err = s.svc.EndOtherSignIns(ctx, req.scope, req.principal.UserID(), keep); err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "ending other sign-ins")
	}

	return &signinpb.EndOtherSignInsResponse{}, nil
}

// annotated renders a listing with what the consumer's [SignInAnnotator]
// recorded about each login, or with no attributes on a server built without
// one. The annotator is asked once, for every family at once, and not at all
// for an empty listing: there is nothing it could say about no logins.
//
// Its error is the listing's — see [SignInAnnotator] for why there is no
// answer without it.
func (s *Server) annotated(
	ctx context.Context,
	scope tenancy.Scope,
	userID string,
	signIns []*signin.ActiveSignIn,
) ([]*signinpb.ActiveSignIn, error) {
	converted := make([]*signinpb.ActiveSignIn, 0, len(signIns))
	for _, signIn := range signIns {
		converted = append(converted, ActiveSignInToProto(signIn))
	}

	if s.annotate == nil || len(signIns) == 0 {
		return converted, nil
	}

	familyIDs := make([]string, 0, len(signIns))
	for _, signIn := range signIns {
		familyIDs = append(familyIDs, signIn.FamilyID)
	}

	attributes, err := s.annotate(ctx, scope, userID, familyIDs)
	if err != nil {
		return nil, err
	}

	for _, signIn := range converted {
		if recorded := attributes[signIn.GetFamilyId()]; len(recorded) > 0 {
			signIn.Attributes = maps.Clone(recorded)
		}
	}

	return converted, nil
}

// ActiveSignInToProto renders one live login. It leaves current false, since
// whether a login is the caller's own is a fact about the request rather than
// about the login, and attributes empty, since what the consumer recorded about
// a login's device is the [SignInAnnotator]'s to say.
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
		ActorId:         s.ActorID,
		CredentialKind:  string(s.CredentialKind),
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

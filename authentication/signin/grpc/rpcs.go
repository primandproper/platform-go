package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"

	"google.golang.org/grpc/codes"
)

// LoginForToken proves a password — and a second-factor code from a user who
// holds one — and answers with a token.
//
// It is anonymous: the caller is proving who they are, so there is nobody on the
// context yet, and the directory their handle is looked up in comes off the
// [ScopeResolver] rather than off the request. A request naming both a username
// and an email address is refused rather than resolved by precedence.
//
// Every way of failing to prove anything answers Unauthenticated with the same
// message, which is the point — see signin.ErrInvalidCredentials. The one
// exception is a user who holds a proven second factor and sent no code: they
// are told to send one, because a client that cannot be told that cannot ask.
func (s *Server) LoginForToken(
	ctx context.Context,
	request *signinpb.LoginForTokenRequest,
) (*signinpb.LoginForTokenResponse, error) {
	ctx, req, done, err := s.anonymous(ctx, signinpb.SignInService_LoginForToken_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	credentials := credentialsFromProto(request.GetCredentials())
	if credentials == nil {
		err = fail(req.op, ErrNilCredentials, codes.InvalidArgument, "signing in")

		return nil, err
	}

	signedIn, err := s.svc.LoginForToken(ctx, req.scope, credentials)
	if err != nil {
		return nil, fail(req.op, err, codes.Unauthenticated, "signing in")
	}

	return &signinpb.LoginForTokenResponse{Token: IssuedTokenToProto(signedIn)}, nil
}

// AdminLoginForToken is LoginForToken through the administrative door: the
// caller must hold one of the service roles the consumer named as
// administrative, and must hold a proven second factor whatever the service's
// policy says.
//
// A service that named no administrative roles answers every call here with
// PermissionDenied, and a caller cannot tell that apart from not being an
// administrator. Both are deliberate — see signin.ErrAdminLoginDisabled.
func (s *Server) AdminLoginForToken(
	ctx context.Context,
	request *signinpb.AdminLoginForTokenRequest,
) (*signinpb.AdminLoginForTokenResponse, error) {
	ctx, req, done, err := s.anonymous(ctx, signinpb.SignInService_AdminLoginForToken_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	credentials := credentialsFromProto(request.GetCredentials())
	if credentials == nil {
		err = fail(req.op, ErrNilCredentials, codes.InvalidArgument, "signing in as an administrator")

		return nil, err
	}

	signedIn, err := s.svc.AdminLoginForToken(ctx, req.scope, credentials)
	if err != nil {
		return nil, fail(req.op, err, codes.Unauthenticated, "signing in as an administrator")
	}

	return &signinpb.AdminLoginForTokenResponse{Token: IssuedTokenToProto(signedIn)}, nil
}

// GetAuthStatus reports where the calling user stands, and is the one RPC here
// that answers a request with no caller on it rather than refusing it.
//
// "Am I signed in" is a question whose answer can be no, and a client asking it
// on load is asking precisely because it does not know. Answering
// Unauthenticated would make every client treat its own first question as an
// error, so an anonymous request gets authenticated=false and an absent status.
//
// A principal that resolves and then cannot be read — a token naming a user the
// directory has since archived — is an error rather than a false, because
// something is wrong there and saying "not signed in" would hide it.
func (s *Server) GetAuthStatus(
	ctx context.Context,
	_ *signinpb.GetAuthStatusRequest,
) (*signinpb.GetAuthStatusResponse, error) {
	ctx, req, done, err := s.anonymous(ctx, signinpb.SignInService_GetAuthStatus_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	principal, ok := s.principals(ctx)
	if !ok || principal == nil {
		return &signinpb.GetAuthStatusResponse{Authenticated: false}, nil
	}

	req.principal = principal
	req.op.Set(userIDKey, principal.UserID())

	status, err := s.svc.GetAuthStatus(ctx, req.scope, req.principal.UserID(), req.principal.ActiveAccountID())
	if err != nil {
		return nil, fail(req.op, err, codes.Internal, "reading an authentication status")
	}

	return &signinpb.GetAuthStatusResponse{
		Authenticated: true,
		Status:        AuthStatusToProto(status),
	}, nil
}

// GetSelf reads the calling user, redacted.
//
// The subject is the caller and there is no target on the request. Reading
// somebody else is the directory's GetUser, behind the permission that guards
// it.
func (s *Server) GetSelf(
	ctx context.Context,
	_ *signinpb.GetSelfRequest,
) (*signinpb.GetSelfResponse, error) {
	ctx, req, done, err := s.caller(ctx, signinpb.SignInService_GetSelf_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	user, err := s.svc.GetSelf(ctx, req.scope, req.principal.UserID())
	if err != nil {
		return nil, fail(req.op, err, codes.Internal, "reading the calling user")
	}

	return &signinpb.GetSelfResponse{User: identitygrpc.UserToProto(user)}, nil
}

// UpdatePassword replaces the calling user's password.
//
// The current password is checked and a second-factor code is required from a
// user who holds a proven one: being signed in is not by itself proof enough to
// change the credential the sign-in was obtained with.
//
// Whether the new password is acceptable is the consumer's rule, in front of
// this call. Nothing here holds a password policy.
func (s *Server) UpdatePassword(
	ctx context.Context,
	request *signinpb.UpdatePasswordRequest,
) (*signinpb.UpdatePasswordResponse, error) {
	ctx, req, done, err := s.caller(ctx, signinpb.SignInService_UpdatePassword_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	update := &signin.PasswordUpdate{
		CurrentPassword: request.GetCurrentPassword(),
		NewPassword:     request.GetNewPassword(),
		TOTPCode:        request.GetTotpCode(),
	}

	if err = s.svc.UpdatePassword(ctx, req.scope, req.principal.UserID(), update); err != nil {
		return nil, fail(req.op, err, codes.Internal, "updating a password")
	}

	return &signinpb.UpdatePasswordResponse{}, nil
}

// RefreshTOTPSecret issues the calling user a new second-factor secret and
// returns it to them, once.
//
// The response carries a live secret, which is why the schema says this service
// requires transport security and why nothing should log, cache or retry this
// response. The secret is unproven until VerifyTOTPSecret succeeds, so between
// the two calls the user holds no second factor at all.
func (s *Server) RefreshTOTPSecret(
	ctx context.Context,
	request *signinpb.RefreshTOTPSecretRequest,
) (*signinpb.RefreshTOTPSecretResponse, error) {
	ctx, req, done, err := s.caller(ctx, signinpb.SignInService_RefreshTOTPSecret_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	refresh := &signin.SecretRefresh{
		CurrentPassword: request.GetCurrentPassword(),
		TOTPCode:        request.GetTotpCode(),
	}

	enrollment, err := s.svc.RefreshTOTPSecret(ctx, req.scope, req.principal.UserID(), refresh)
	if err != nil {
		return nil, fail(req.op, err, codes.Internal, "refreshing a second-factor secret")
	}

	return &signinpb.RefreshTOTPSecretResponse{
		Secret:          enrollment.Secret,
		ProvisioningUri: enrollment.URI,
	}, nil
}

// VerifyTOTPSecret records that the calling user proved possession of the secret
// they were issued, which is what turns it into a second factor.
func (s *Server) VerifyTOTPSecret(
	ctx context.Context,
	request *signinpb.VerifyTOTPSecretRequest,
) (*signinpb.VerifyTOTPSecretResponse, error) {
	ctx, req, done, err := s.caller(ctx, signinpb.SignInService_VerifyTOTPSecret_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	if err = s.svc.VerifyTOTPSecret(ctx, req.scope, req.principal.UserID(), request.GetTotpCode()); err != nil {
		return nil, fail(req.op, err, codes.Internal, "verifying a second-factor secret")
	}

	return &signinpb.VerifyTOTPSecretResponse{}, nil
}

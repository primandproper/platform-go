package grpc

import (
	"context"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"

	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"
	"github.com/primandproper/primitives-go/v2/idempotency"
	idempotencygrpc "github.com/primandproper/primitives-go/v2/idempotency/grpc"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
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
//
// signin.GRPCMapper is what says so, and the code passed below is codes.Internal
// like every other RPC in the module. A door that defaulted to Unauthenticated
// would answer a failed token issuer, a refused ClaimsBuilder, a hook that
// rolled the sign-in back or a ScopeResolver that could not name a directory
// with "wrong password" — an outage dressed as a credential the caller could fix
// by retyping it.
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
		err = grpcerrors.PrepareAndLogGRPCStatus(ErrNilCredentials, req.op.Logger(), req.op.Span(), codes.InvalidArgument, "signing in")

		return nil, err
	}

	signedIn, err := s.svc.LoginForToken(ctx, req.scope, credentials)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "signing in")
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
//
// Its default code is LoginForToken's, and for the same reason.
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
		err = grpcerrors.PrepareAndLogGRPCStatus(ErrNilCredentials, req.op.Logger(), req.op.Span(), codes.InvalidArgument, "signing in as an administrator")

		return nil, err
	}

	signedIn, err := s.svc.AdminLoginForToken(ctx, req.scope, credentials)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "signing in as an administrator")
	}

	return &signinpb.AdminLoginForTokenResponse{Token: IssuedTokenToProto(signedIn)}, nil
}

// ExchangeRefreshToken spends a refresh token and answers with a fresh pair: a
// new access token, and the successor to the token that was presented.
//
// It is anonymous, like the two doors, and for the same reason: the credential
// presented is the whole of the request's authority. Requiring a principal would
// require a live access token to renew an expired one, which is the one moment a
// client has none.
//
// Every refusal answers Unauthenticated with the same message a wrong password
// gets, replayed tokens included. That is deliberate — see
// signin.ErrRefreshTokenReused, which is mapped for this transport and left out
// of the client-safe list precisely so that a client cannot tell a detected
// theft from an ordinary refusal. What a caller does about any of them is the
// same thing: sign in again.
//
// A service built without signin.WithRefreshTokenStore answers every call here
// with Internal, because a consumer's client calling an RPC their own server
// cannot serve is a wiring failure rather than a request to correct.
//
// This is the one RPC in this package that reads the conventional
// `idempotency-key` metadata entry, and it reads it rather than being wrapped in
// the idempotency interceptor. The interceptor's Manager records its result
// outside the work's transaction and says what that cannot promise — work that
// has its effect and then fails — which for a credential rotation is the exact
// failure the key is here to fix. The key is therefore carried to the service and
// stored by signin's own transaction; see signin.IdempotentRefreshTokenStore.
//
// A request that sends none takes the path it takes today, and so does a service
// whose store does not implement that interface. A key the store rejects as
// malformed answers InvalidArgument through the platform mapper, rather than
// being dropped — a client told its retry was protected when it was not is worse
// off than one told to fix its header.
func (s *Server) ExchangeRefreshToken(
	ctx context.Context,
	request *signinpb.ExchangeRefreshTokenRequest,
) (*signinpb.ExchangeRefreshTokenResponse, error) {
	ctx, req, done, err := s.anonymous(ctx, signinpb.SignInService_ExchangeRefreshToken_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	ctx = withIdempotencyKey(ctx)

	signedIn, err := s.svc.ExchangeRefreshToken(ctx, req.scope, request.GetRefreshToken())
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "exchanging a refresh token")
	}

	return &signinpb.ExchangeRefreshTokenResponse{Token: IssuedTokenToProto(signedIn)}, nil
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
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "reading an authentication status")
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
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "reading the calling user")
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
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "updating a password")
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
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "refreshing a second-factor secret")
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
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "verifying a second-factor secret")
	}

	return &signinpb.VerifyTOTPSecretResponse{}, nil
}

// Register creates somebody who can then sign in: the user, what they own, and
// the credential they chose.
//
// It is the one RPC here whose caller is not the subject, and the principal it
// requires is the registrar's — the same reading identity/grpc.Register takes of
// the same question. An unauthenticated public sign-up is a flow with policy in
// it, a captcha, a rate limit, an invitation, an email domain rule, and this
// service holds none of that. A consumer building open registration puts that
// policy in front of this call and gives the request a principal of its own.
//
// The scope is still the resolver's rather than that principal's, which is the
// one place this surface differs from identity's: one wiring decision governs
// every RPC here, including the ones with nobody on them.
//
// A request naming neither credential arm is refused with InvalidArgument. It is
// not read as no_password — see signin.Credential for why that inference is the
// one this schema exists to prevent.
//
// What comes back carries no verification token. The secret that promotes this
// registrant out of the unverified standing travels to them in mail the consumer
// sends from inside the transaction that wrote their row, and never back to
// whoever called this.
func (s *Server) Register(
	ctx context.Context,
	request *signinpb.RegisterRequest,
) (*signinpb.RegisterResponse, error) {
	ctx, req, done, err := s.caller(ctx, signinpb.SignInService_Register_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	registration := registrationFromProto(request)
	if registration == nil {
		err = grpcerrors.PrepareAndLogGRPCStatus(signin.ErrNilRegistration, req.op.Logger(), req.op.Span(), codes.InvalidArgument, "registering a user")

		return nil, err
	}

	registered, err := s.svc.Register(ctx, req.scope, registration)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "registering a user")
	}

	return &signinpb.RegisterResponse{Registration: RegisteredToProto(registered)}, nil
}

// AttachPassword gives a password to somebody who holds none, answered with the
// verification link that was mailed to them.
//
// It is anonymous, and the token is the whole of its authority. The subject
// cannot be signed in — they hold no password, and an unverified standing admits
// no sign-in — so the two proofs UpdatePassword rests on are both unavailable,
// and the mail sent to the address the account was registered with is the only
// thing that reaches the person it is about. There is no field naming a user:
// who this is about is read off the row the token named.
//
// An account that already holds a password is refused, which is what keeps the
// capability narrow: an outstanding link furnishes an account that has none,
// once, and can do nothing to one that has. Somebody who has forgotten a
// password they hold goes through the password reset flow.
func (s *Server) AttachPassword(
	ctx context.Context,
	request *signinpb.AttachPasswordRequest,
) (*signinpb.AttachPasswordResponse, error) {
	ctx, req, done, err := s.anonymous(ctx, signinpb.SignInService_AttachPassword_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	attachment := &signin.PasswordAttachment{
		Token:       request.GetToken(),
		NewPassword: request.GetNewPassword(),
	}

	if err = s.svc.AttachPassword(ctx, req.scope, attachment); err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "attaching a password")
	}

	return &signinpb.AttachPasswordResponse{}, nil
}

// VerifyEmailAddress answers a verification link: it proves the address, spends
// the link, and promotes the registrant out of the unverified standing that
// admits no sign-in.
//
// It is anonymous for the reason AttachPassword is, and it is the RPC that makes
// a registration usable at all — without it a registrant proved their address
// and stayed exactly as unable to sign in as before, because the only other
// thing that moves that standing is an operator's write behind an operator's
// permission.
//
// Every way of failing to resolve the link is one answer, Unauthenticated with
// the message a wrong password gets. Expired, already answered, never issued and
// simply wrong share a remedy, and telling them apart tells whoever is guessing
// which guesses are getting warm.
func (s *Server) VerifyEmailAddress(
	ctx context.Context,
	request *signinpb.VerifyEmailAddressRequest,
) (*signinpb.VerifyEmailAddressResponse, error) {
	ctx, req, done, err := s.anonymous(ctx, signinpb.SignInService_VerifyEmailAddress_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	if err = s.svc.VerifyEmailAddress(ctx, req.scope, request.GetToken()); err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "verifying an email address")
	}

	return &signinpb.VerifyEmailAddressResponse{}, nil
}

// RequestMagicLink mails somebody a link that signs them in, and answers the
// same way whatever it found.
//
// An address nobody holds, an address whose owner is banned, an address whose
// owner is terminated and an address that got a mail are one answer: an empty
// response and no error. The service pads its own timing so the four cannot be
// told apart by a stopwatch either, and this handler adds nothing that could
// tell them apart by shape — which is the whole of the enumeration defense, and
// the reason there is nothing to put in the response.
//
// A consumer building their own transport over this service owes the same
// silence. Answering a known address with a 200 and an unknown one with a 404
// rebuilds the oracle one layer up, where neither the padding nor this handler
// reaches.
//
// What is reported is this service failing rather than a fact about the address:
// a store that will not write and a mailer that will not send are Internal, and
// they are the only non-empty answers this RPC has.
//
// It is anonymous for the reason the two registration doors are: the person
// asking cannot sign in yet, which is the dead end it exists to open. Rate
// limiting is the consumer's, in front of this call — it is the one RPC in this
// service that sends mail on request, so a deployment without a limit in front
// of it is a way to send mail through their own domain at somebody else's
// direction.
func (s *Server) RequestMagicLink(
	ctx context.Context,
	request *signinpb.RequestMagicLinkRequest,
) (*signinpb.RequestMagicLinkResponse, error) {
	ctx, req, done, err := s.anonymous(ctx, signinpb.SignInService_RequestMagicLink_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	if err = s.svc.RequestMagicLink(ctx, req.scope, request.GetEmailAddress()); err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "requesting a sign-in link")
	}

	return &signinpb.RequestMagicLinkResponse{}, nil
}

// RedeemMagicLink answers a sign-in link: it spends the link, proves the address
// it was mailed to, promotes a registrant who was waiting on exactly that, and
// issues a token.
//
// It is anonymous for the reason AttachPassword and VerifyEmailAddress are: the
// token mailed to the person it is about is the whole of the request's
// authority, and requiring a principal would require a sign-in from somebody
// who cannot sign in yet. It has no field naming a user; who it is about is read
// off the row the token named.
//
// Every way of failing to spend the link is one answer, Unauthenticated with the
// message a wrong password gets. A wrong second-factor code is the same answer,
// and a user who holds a proven secret and sent no code is told to send one —
// which is the same disclosure the password door makes, in the same place, for
// the same reason.
//
// What it produces is what LoginForToken produces. The two doors differ in what
// was proven, not in what they mint.
func (s *Server) RedeemMagicLink(
	ctx context.Context,
	request *signinpb.RedeemMagicLinkRequest,
) (*signinpb.RedeemMagicLinkResponse, error) {
	ctx, req, done, err := s.anonymous(ctx, signinpb.SignInService_RedeemMagicLink_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	signedIn, err := s.svc.RedeemMagicLink(ctx, req.scope, &signin.MagicLinkCredentials{
		Token:           request.GetToken(),
		TOTPCode:        request.GetTotpCode(),
		ActiveAccountID: request.GetActiveAccountId(),
	})
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "redeeming a sign-in link")
	}

	return &signinpb.RedeemMagicLinkResponse{Token: IssuedTokenToProto(signedIn)}, nil
}

// withIdempotencyKey moves the incoming `idempotency-key` metadata entry onto
// ctx, where the service reads it.
//
// The metadata name is idempotencygrpc.MetadataKey, which is what this package's
// own client stamps on this RPC through that package's client interceptor — so a
// consumer using it sends the header by putting a key on the context rather than
// by learning a second convention here. That client stamps it on this RPC alone,
// and its documentation says why the rest of them are excluded.
//
// A request with no key, or with an empty one, is returned unchanged and takes
// the ordinary exchange path. What is deliberately not done here is validating
// the key: the store is what refuses a malformed one, because the store is what
// has to live with the column's width, and a check in two places is a check that
// can come to disagree about what is acceptable.
func withIdempotencyKey(ctx context.Context) context.Context {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ctx
	}

	values := md.Get(idempotencygrpc.MetadataKey)
	if len(values) == 0 || values[0] == "" {
		return ctx
	}

	// The first entry, and never a join of them. Metadata is repeatable and a
	// client sending two keys has sent two claims about which operation this is;
	// concatenating them would mint a third key belonging to neither, which would
	// match nothing on the retry and read as a replay.
	return idempotency.WithKey(ctx, idempotency.Key(values[0]))
}

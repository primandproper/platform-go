package grpc

import (
	"context"
	"encoding/json"

	"github.com/primandproper/platform-go/v15/authentication/passkeys"
	"github.com/primandproper/platform-go/v15/authentication/passkeys/passkeyspb"
	"github.com/primandproper/platform-go/v15/authentication/signin"
	signingrpc "github.com/primandproper/platform-go/v15/authentication/signin/grpc"

	"github.com/primandproper/primitives-go/v2/database"
	grpcerrors "github.com/primandproper/primitives-go/v2/errors/grpc"

	"github.com/go-webauthn/webauthn/protocol"
	"google.golang.org/grpc/codes"
)

// BeginRegistration issues the options the caller's browser needs to create a
// passkey on their own account.
//
// The subject is the principal and nothing else: the handle is derived from
// their user ID by the server's [UserHandle], and the service refuses one that
// resolves to anybody but them. The service's EnrollmentGate runs here, so a
// refused enrollment is refused before anybody touches a key, with whatever
// error the consumer's gate returned.
func (s *Server) BeginRegistration(
	ctx context.Context,
	_ *passkeyspb.BeginRegistrationRequest,
) (*passkeyspb.BeginRegistrationResponse, error) {
	ctx, req, done, err := s.caller(ctx, passkeyspb.PasskeysService_BeginRegistration_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	handle, err := s.handle(ctx, req)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "beginning a passkey registration")
	}

	creation, err := s.svc.BeginRegistration(ctx, s.client.Reader(), req.scope, req.principal.UserID(), handle)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "beginning a passkey registration")
	}

	options, err := json.Marshal(creation)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "rendering passkey registration options")
	}

	return &passkeyspb.BeginRegistrationResponse{Options: options}, nil
}

// FinishRegistration verifies the attestation the caller's browser returned
// and enrolls the passkey it produced.
//
// It opens no transaction, unlike ArchivePasskey: the service spends the
// ceremony's challenge before it writes and then writes in a transaction of
// its own, since a transaction held open around the challenge's consumption
// is a second writer the ceremony store waits on — see
// passkeys.Service.FinishRegistration. The registration hook runs in the
// service's transaction, so a hook that refuses leaves no passkey behind.
func (s *Server) FinishRegistration(
	ctx context.Context,
	request *passkeyspb.FinishRegistrationRequest,
) (*passkeyspb.FinishRegistrationResponse, error) {
	ctx, req, done, err := s.caller(ctx, passkeyspb.PasskeysService_FinishRegistration_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	handle, err := s.handle(ctx, req)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "finishing a passkey registration")
	}

	registered, err := s.svc.FinishRegistration(ctx, req.scope, req.principal.UserID(), handle,
		request.GetFriendlyName(), request.GetResponse())
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "finishing a passkey registration")
	}

	return &passkeyspb.FinishRegistrationResponse{Passkey: PasskeyToProto(registered)}, nil
}

// BeginLogin issues the options a browser needs to sign in: a named login for
// the username the request carries, or the discoverable one when it carries
// none.
//
// It is anonymous, and a username that names nobody is answered exactly as
// one that names somebody — see passkeys.Service.BeginLogin, which is why no
// named login's options list the user's credentials.
func (s *Server) BeginLogin(
	ctx context.Context,
	request *passkeyspb.BeginLoginRequest,
) (*passkeyspb.BeginLoginResponse, error) {
	ctx, req, done, err := s.anonymous(ctx, passkeyspb.PasskeysService_BeginLogin_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	var assertion *protocol.CredentialAssertion

	if username := request.GetUsername(); username == "" {
		assertion, err = s.svc.BeginDiscoverableLogin(ctx)
	} else {
		assertion, err = s.svc.BeginLogin(ctx, s.client.Reader(), req.scope, username)
	}

	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "beginning a passkey login")
	}

	options, err := json.Marshal(assertion)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "rendering passkey login options")
	}

	return &passkeyspb.BeginLoginResponse{Options: options}, nil
}

// FinishLogin verifies the assertion a browser returned and answers with a
// token for whoever it proved, minted through the server's [PrincipalIssuer].
//
// # The token is sign-in's
//
// This is the one path from a passkey to a token, and it goes through
// signin.Service.IssueForPrincipal, which is why that method has no transport
// of its own: its caller has to have proven the credential, and this RPC is
// the caller that has. The token is stamped [CredentialKind], so sign-in's
// hooks tell a passkey from a password, and it carries the lifetimes, claims
// and refresh token family a password sign-in's does.
//
// # One factor or two
//
// A passkey is two factors only when the authenticator verified the person —
// a PIN or a biometric — and passkeys.Login.UserVerified says which this was.
// Only then is signin.MultiFactor passed. A key tap alone is one factor, so a
// person holding a proven second factor is asked for it, and the request's
// totp_code is read exactly as a password sign-in reads it.
//
// # Refusals
//
// Every refused ceremony is passkeys.ErrLoginFailed, Unauthenticated, whatever
// refused it — an unknown username included. A key whose sign count did not
// advance is ErrSignCountRegressed, PermissionDenied, and no token is issued.
// A proven subject sign-in will not admit is refused with sign-in's own
// sentinels, mapped by signin.GRPCMapper.
func (s *Server) FinishLogin(
	ctx context.Context,
	request *passkeyspb.FinishLoginRequest,
) (*passkeyspb.FinishLoginResponse, error) {
	ctx, req, done, err := s.anonymous(ctx, passkeyspb.PasskeysService_FinishLogin_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	var login *passkeys.Login

	if username := request.GetUsername(); username == "" {
		login, err = s.svc.FinishDiscoverableLogin(ctx, req.scope, request.GetResponse())
	} else {
		login, err = s.svc.FinishLogin(ctx, req.scope, username, request.GetResponse())
	}

	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "finishing a passkey login")
	}

	req.op.Set(userIDKey, login.Credential.BelongsToUser)

	opts := []signin.IssueOption{signin.WithCredentialKind(CredentialKind)}
	if login.UserVerified {
		opts = append(opts, signin.MultiFactor())
	} else if code := request.GetTotpCode(); code != "" {
		opts = append(opts, signin.WithTOTPCode(code))
	}

	signedIn, err := s.issuer.IssueForPrincipal(ctx, req.scope, login.Credential.BelongsToUser, request.GetActiveAccountId(), opts...)
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "issuing a token for a passkey login")
	}

	return &passkeyspb.FinishLoginResponse{Token: signingrpc.IssuedTokenToProto(signedIn)}, nil
}

// ListPasskeys answers with the caller's live passkeys, oldest first.
func (s *Server) ListPasskeys(
	ctx context.Context,
	_ *passkeyspb.ListPasskeysRequest,
) (*passkeyspb.ListPasskeysResponse, error) {
	ctx, req, done, err := s.caller(ctx, passkeyspb.PasskeysService_ListPasskeys_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	credentials, err := s.svc.ListCredentials(ctx, s.client.Reader(), req.scope, req.principal.UserID())
	if err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "listing passkeys")
	}

	out := make([]*passkeyspb.Passkey, 0, len(credentials))
	for _, credential := range credentials {
		out = append(out, PasskeyToProto(credential))
	}

	return &passkeyspb.ListPasskeysResponse{Passkeys: out}, nil
}

// ArchivePasskey takes one of the caller's passkeys off their account, in a
// transaction this server opens, and answers with it as the archive left it.
//
// The caller is part of the statement, so a passkey that is somebody else's is
// NotFound exactly as one that does not exist is. The service's
// last-credential guard refuses the archive that would leave the caller with
// no way in, FailedPrecondition.
func (s *Server) ArchivePasskey(
	ctx context.Context,
	request *passkeyspb.ArchivePasskeyRequest,
) (*passkeyspb.ArchivePasskeyResponse, error) {
	ctx, req, done, err := s.caller(ctx, passkeyspb.PasskeysService_ArchivePasskey_FullMethodName)
	if err != nil {
		return nil, err
	}

	defer func() { done(err) }()

	var archived *passkeys.Credential

	if err = s.client.WithTransaction(ctx, func(tx database.Tx) error {
		var txErr error
		archived, txErr = s.svc.ArchiveCredential(ctx, tx, req.scope, request.GetId(), req.principal.UserID())

		return txErr
	}); err != nil {
		return nil, grpcerrors.PrepareAndLogGRPCStatus(err, req.op.Logger(), req.op.Span(), codes.Internal, "archiving a passkey")
	}

	return &passkeyspb.ArchivePasskeyResponse{Passkey: PasskeyToProto(archived)}, nil
}

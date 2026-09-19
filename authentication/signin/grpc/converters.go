package grpc

import (
	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// The conversions between this package's wire types and the service's.
//
// There are three, and there are only three because this schema borrows
// identity's User rather than declaring one of its own — so everything about a
// user converts through identitygrpc.UserToProto, which is already the one
// place that decision is made.
//
// Each is exported for the same reason identity/grpc's are: a consumer
// assembling one of these messages by hand, or reading one in a test, should
// not have to reimplement a conversion this package already got right.

// credentialsFromProto reads a sign-in request's credentials.
//
// A nil message is a nil result rather than an empty Credentials, so that a
// request with no credentials on it is refused as a malformed request instead
// of being run as a sign-in by nobody with no password — which the service
// would refuse anyway, with a sentinel that says something less useful.
func credentialsFromProto(c *signinpb.Credentials) *signin.Credentials {
	if c == nil {
		return nil
	}

	return &signin.Credentials{
		Username:        c.GetUsername(),
		EmailAddress:    c.GetEmailAddress(),
		Password:        c.GetPassword(),
		TOTPCode:        c.GetTotpCode(),
		ActiveAccountID: c.GetActiveAccountId(),
	}
}

// CredentialsToProto renders credentials onto the wire. It is the direction a
// client converts in, and it is here rather than in the client package so that
// both ends of the conversion sit beside each other.
func CredentialsToProto(c *signin.Credentials) *signinpb.Credentials {
	if c == nil {
		return nil
	}

	return &signinpb.Credentials{
		Username:        c.Username,
		EmailAddress:    c.EmailAddress,
		Password:        c.Password,
		TotpCode:        c.TOTPCode,
		ActiveAccountId: c.ActiveAccountID,
	}
}

// IssuedTokenToProto renders a completed sign-in as the tokens it produced.
//
// The principal is deliberately dropped. A client that needs it calls
// GetAuthStatus, which reads it fresh — see the schema's own note on why a token
// response carries no permissions.
//
// The refresh token is not dropped, which is the one place this conversion hands
// a client something it must protect more carefully than the access token beside
// it. It is empty for a service built without signin.WithRefreshTokenStore, and
// the family is set either way.
func IssuedTokenToProto(s *signin.SignIn) *signinpb.IssuedToken {
	if s == nil {
		return nil
	}

	token := &signinpb.IssuedToken{
		Token:          s.Token,
		TokenId:        s.TokenID,
		RefreshToken:   s.RefreshToken,
		FamilyId:       s.FamilyID,
		Administrative: s.Administrative,
	}

	if !s.ExpiresAt.IsZero() {
		token.ExpiresAt = timestamppb.New(s.ExpiresAt)
	}

	// Absent rather than the zero instant, for a service that mints no refresh
	// token: a timestamp of 1970 in a field named "expires at" is a deadline a
	// client can compare against and be wrong about, where an absent one is a
	// question it cannot ask.
	if !s.RefreshTokenExpiresAt.IsZero() {
		token.RefreshTokenExpiresAt = timestamppb.New(s.RefreshTokenExpiresAt)
	}

	if s.Principal != nil {
		token.ActiveAccountId = s.Principal.ActiveAccountID
	}

	return token
}

// AuthStatusToProto renders an authentication status.
func AuthStatusToProto(s *signin.AuthStatus) *signinpb.AuthStatus {
	if s == nil {
		return nil
	}

	return &signinpb.AuthStatus{
		User:                   identitygrpc.UserToProto(s.User),
		ActiveAccountId:        s.ActiveAccountID,
		AccountIds:             s.AccountIDs,
		HasPassword:            s.HasPassword,
		TwoFactorEnrolled:      s.TwoFactorEnrolled,
		RequiresPasswordChange: s.RequiresPasswordChange,
		EmailAddressVerified:   s.EmailAddressVerified,
	}
}

// registrationFromProto reads a registration request into what the service
// takes.
//
// The credential is the one field that can be absent in a way this converter
// must not paper over: an unset oneof becomes a nil signin.Credential, and the
// service refuses it. Defaulting it to signin.NoPassword here would be this
// package deciding that a client which forgot to populate the field meant to
// create an account nobody can sign into.
//
// A nil message is a nil result, for the reason credentialsFromProto's is.
func registrationFromProto(r *signinpb.RegisterRequest) *signin.Registration {
	if r == nil {
		return nil
	}

	registration := &signin.Registration{
		User:       identitygrpc.UserFromRegistrationInput(r.GetUser()),
		Account:    identitygrpc.AccountFromCreationInput(r.GetAccount()),
		Credential: credentialFromProto(r),
		OwnerRoles: r.GetOwnerRoles(),
	}

	if invitation := r.GetInvitation(); invitation != nil {
		registration.InvitationID = invitation.GetInvitationId()
		registration.InvitationToken = invitation.GetToken()
		registration.InvitationStatusNote = invitation.GetStatusNote()
	}

	return registration
}

// credentialFromProto reads the arm a registration named, and answers nil for a
// request that named neither.
//
// nil is the honest reading of an unset oneof and is what the service refuses.
// The alternative — treating "the client sent nothing" as "the client chose no
// password" — is the inference the Credential type exists to prevent.
func credentialFromProto(r *signinpb.RegisterRequest) signin.Credential {
	switch credential := r.GetCredential().(type) {
	case *signinpb.RegisterRequest_Password:
		return signin.Password(credential.Password)
	case *signinpb.RegisterRequest_NoPassword:
		return signin.NoPassword()
	default:
		return nil
	}
}

// RegisteredToProto renders what a registration produced.
//
// The verification token is deliberately not carried across. It is on the Go
// value because the consumer's hook has nowhere else to take it from; it is
// absent from the message because whoever called this RPC is a client rather
// than the person the secret is about, and the schema has no field for it to
// land in — see the proto's own documentation.
func RegisteredToProto(r *signin.Registered) *signinpb.Registered {
	if r == nil {
		return nil
	}

	return &signinpb.Registered{
		User:       identitygrpc.UserToProto(r.User),
		Account:    identitygrpc.AccountToProto(r.Account),
		Membership: identitygrpc.MembershipToProto(r.Membership),
		Invitation: identitygrpc.InvitationToProto(r.Invitation),
	}
}

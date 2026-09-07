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

// IssuedTokenToProto renders a completed sign-in as the token it produced.
//
// The principal is deliberately dropped. A client that needs it calls
// GetAuthStatus, which reads it fresh — see the schema's own note on why a token
// response carries no permissions.
func IssuedTokenToProto(s *signin.SignIn) *signinpb.IssuedToken {
	if s == nil {
		return nil
	}

	token := &signinpb.IssuedToken{
		Token:          s.Token,
		TokenId:        s.TokenID,
		Administrative: s.Administrative,
	}

	if !s.ExpiresAt.IsZero() {
		token.ExpiresAt = timestamppb.New(s.ExpiresAt)
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

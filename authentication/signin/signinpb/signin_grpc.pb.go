// Package primandproper.platform.signin.v1 is the wire schema for proving
// somebody is who they say they are: registering them, the password door, the
// administrative door, the credential writes they make about themselves, and
// the two reads a client makes on load.
//
// This file is shipped inside the published Go module, and it is the file
// itself that is shipped -- not a copy for you to keep in sync. A consumer puts
// the module's proto directories on protoc's path and imports this file by its
// canonical name, exactly as identity.proto and filtering.proto already work:
//
//	PLATFORM_PROTO := $(shell go list -m -f '{{.Dir}}' github.com/primandproper/platform-go/v14)
//
//	protoc --proto_path proto/ \
//	    --proto_path $(PLATFORM_PROTO)/authentication/signin/proto \
//	    --proto_path $(PLATFORM_PROTO)/identity/proto \
//	    --proto_path $(PLATFORM_PROTO)/filtering/proto \
//	    --go_opt=Mprimandproper/platform/signin/v1/signin.proto=github.com/primandproper/platform-go/v14/authentication/signin/signinpb \
//	    --go_opt=Mprimandproper/platform/identity/v1/identity.proto=github.com/primandproper/platform-go/v14/identity/identitypb \
//	    $(CONSUMER_PROTO_FILES)   # the platform files deliberately absent from that list
//
// Go links against the bindings this module already generated, in
// github.com/primandproper/platform-go/v14/authentication/signin/signinpb.
// Swift, TypeScript and Kotlin have no such bindings to link against and
// generate this file directly, which is the whole point of shipping the schema.
//
// Field numbers are the compatibility promise, across every language a consumer
// generates into. Numbers are never reused and never repurposed: a field that
// goes away is reserved.
//
// # The user, and why it is identity's
//
// This file imports identity.proto and answers with its User rather than
// defining one of its own. There is one directory and one user, and a sign-in
// service that shipped a second message for the same row would be a second
// message a client has to convert between -- for the sole benefit of not having
// to put two files on protoc's path. What this file adds beside that user is
// [AuthStatus], which carries the three facts a redacted user cannot: whether
// they hold a password at all, whether their second factor has actually been
// proven, whether their address has been. All three are computed from columns no
// response ever carries.
//
// # What is not here, and why
//
// No scope field, anywhere, and the name is reserved so there cannot be one.
// The reason identity.proto gives at greater length: a scope a client could
// name is a cross-tenant read hiding behind a request field. Sign-in is the one
// place in the module where the scope cannot come off a principal, because the
// caller has not proved they are one yet, so it comes off the connection
// instead -- a resolver the consumer supplies, from a host, a header, or
// nothing at all in a single-tenant deployment. See authentication/signin/grpc.
//
// That is what makes the reservation matter more here than anywhere else on
// this lane. Every other surface resolves the scope from a caller who has
// already been authenticated; these two doors resolve it for a request nobody
// has vouched for, so a scope field would be one an anonymous caller fills in.
// Reserving the name rather than only saying so is audit.proto's pattern:
// `reserved "scope";` is a schema protoc refuses to accept a scope field into,
// in this repository and in a consumer's fork of the file alike, whereas a
// comment is a request to the next author. It is reserved on every request
// message, on the inputs they are built from -- [Credentials],
// [RegistrationInvitation] -- and on [IssuedToken], [AuthStatus] and
// [Registered], which the responses are built from.
//
// No hashed password and no stored second-factor secret, in either direction.
// The two secrets that do cross are the ones that have to: a plaintext password
// on the way in, which is the only thing a password can be proved with, and a
// freshly minted TOTP secret on the way out, which is the whole content of an
// enrollment. Both make this service's transport security a requirement rather
// than a recommendation -- see [RefreshTOTPSecretResponse] for what the second
// one costs.
//
// A refresh token, and still no session. [IssuedToken] carries a second
// credential that mints the first again without a password, and a family
// identifier naming the login both belong to. What it does not carry is a
// session: keeping either token in a cookie is
// github.com/primandproper/platform-go/v14/sessions, and turning one back into a
// caller is the consumer's interceptor. Neither is a decision a schema should be
// making for everybody.
//
// A service that stores no refresh tokens leaves both new fields empty, which is
// the shape this file described before they existed and is still a valid one --
// see signin.WithRefreshTokenStore. family_id is populated either way, because it
// names a sign-in rather than a stored row, and it is the same value the access
// token carries as its conventional "sid" claim.
//
// Registration is here, and the password is why. identity's schema carries no
// password in either direction and should not: that package never hashes, it
// stores what an engine produced, so a plaintext password on its wire would put
// the choice of hashing engine into the transport. This service holds the
// authenticator, so a registration that carries a credential is its RPC --
// Register below hashes what arrives and hands identity the hash. The directory
// work is still identity's, done through its own service on one transaction.
//
// A registration names the credential the registrant chose, as a oneof, and a
// request that names neither arm is refused rather than read as passwordless.
// Choosing email-only authentication and forgetting to populate a password
// field look identical to a server reading an empty string, and the first is a
// product decision while the second mints an account nobody can reach. A oneof
// is what makes that distinction the schema's rather than one server's.
//
// No verification token in any response. The secret a registration mints
// travels to the person it is about, in mail the consumer sends from inside the
// transaction that wrote the row -- it is never handed back to whoever called
// Register, who is a client rather than the subject. It arrives back here only
// as a request field on the two RPCs that answer a link, which is identity's
// rule for an invitation's token and is the same rule for the same reason.
//
// No passkeys, no password reset and no session management. Each is a flow of
// its own over an engine this module already ships, and each is its own file
// rather than a branch in this one. Email-link sign-in -- a door that mints a
// token from a clicked link rather than from a password -- is not here either:
// it is a sibling of LoginForToken rather than a branch inside it, and it is
// the one thing a registrant who named no password still needs.

// Code generated by protoc-gen-go-grpc. DO NOT EDIT.
// versions:
// - protoc-gen-go-grpc v1.5.1
// - protoc             v6.33.1
// source: primandproper/platform/signin/v1/signin.proto

package signinpb

import (
	context "context"

	grpc "google.golang.org/grpc"
	codes "google.golang.org/grpc/codes"
	status "google.golang.org/grpc/status"
)

// This is a compile-time assertion to ensure that this generated file
// is compatible with the grpc package it is being compiled against.
// Requires gRPC-Go v1.64.0 or later.
const _ = grpc.SupportPackageIsVersion9

const (
	SignInService_Register_FullMethodName             = "/primandproper.platform.signin.v1.SignInService/Register"
	SignInService_AttachPassword_FullMethodName       = "/primandproper.platform.signin.v1.SignInService/AttachPassword"
	SignInService_VerifyEmailAddress_FullMethodName   = "/primandproper.platform.signin.v1.SignInService/VerifyEmailAddress"
	SignInService_RequestMagicLink_FullMethodName     = "/primandproper.platform.signin.v1.SignInService/RequestMagicLink"
	SignInService_RedeemMagicLink_FullMethodName      = "/primandproper.platform.signin.v1.SignInService/RedeemMagicLink"
	SignInService_LoginForToken_FullMethodName        = "/primandproper.platform.signin.v1.SignInService/LoginForToken"
	SignInService_AdminLoginForToken_FullMethodName   = "/primandproper.platform.signin.v1.SignInService/AdminLoginForToken"
	SignInService_ExchangeRefreshToken_FullMethodName = "/primandproper.platform.signin.v1.SignInService/ExchangeRefreshToken"
	SignInService_GetAuthStatus_FullMethodName        = "/primandproper.platform.signin.v1.SignInService/GetAuthStatus"
	SignInService_GetSelf_FullMethodName              = "/primandproper.platform.signin.v1.SignInService/GetSelf"
	SignInService_UpdatePassword_FullMethodName       = "/primandproper.platform.signin.v1.SignInService/UpdatePassword"
	SignInService_RefreshTOTPSecret_FullMethodName    = "/primandproper.platform.signin.v1.SignInService/RefreshTOTPSecret"
	SignInService_VerifyTOTPSecret_FullMethodName     = "/primandproper.platform.signin.v1.SignInService/VerifyTOTPSecret"
)

// SignInServiceClient is the client API for SignInService service.
//
// For semantics around ctx use and closing/ending streaming RPCs, please refer to https://pkg.go.dev/google.golang.org/grpc/?tab=doc#ClientConn.NewStream.
//
// SignInService is sign-in.
//
// Eight of its RPCs are anonymous by definition and five require a caller. What
// none of them requires is a permission: there is no grant that would make
// "sign in" safer, and the four authenticated ones take their subject from the
// caller and have no field that could name anybody else. See
// authentication/signin/grpc's Require for how that is declared to an
// authorization policy, which is not the same thing as being left out of one.
type SignInServiceClient interface {
	// Arriving, and the two ways a registration is finished. Register requires a
	// caller -- the consumer's own registrar, for the reason identity's Register
	// requires one: an open sign-up is a flow with policy in it, a captcha, a rate
	// limit, an email domain rule, and this service holds none of that. The other
	// two are anonymous and carry their own authority, which is the token that was
	// mailed to the person they are about.
	Register(ctx context.Context, in *RegisterRequest, opts ...grpc.CallOption) (*RegisterResponse, error)
	AttachPassword(ctx context.Context, in *AttachPasswordRequest, opts ...grpc.CallOption) (*AttachPasswordResponse, error)
	VerifyEmailAddress(ctx context.Context, in *VerifyEmailAddressRequest, opts ...grpc.CallOption) (*VerifyEmailAddressResponse, error)
	// The passwordless door, both halves anonymous. Requesting a link names an
	// address and is answered the same way whoever holds it; redeeming one carries
	// the token that was mailed, which is the whole of its authority. Neither can
	// name a user, so neither is a way to ask about one.
	//
	// Rate limiting is the consumer's, in front of RequestMagicLink, and it is not
	// optional: this is the one RPC in this service that sends mail on request.
	RequestMagicLink(ctx context.Context, in *RequestMagicLinkRequest, opts ...grpc.CallOption) (*RequestMagicLinkResponse, error)
	RedeemMagicLink(ctx context.Context, in *RedeemMagicLinkRequest, opts ...grpc.CallOption) (*RedeemMagicLinkResponse, error)
	// The two doors, and the one that keeps a sign-in alive without reopening
	// either of them.
	LoginForToken(ctx context.Context, in *LoginForTokenRequest, opts ...grpc.CallOption) (*LoginForTokenResponse, error)
	AdminLoginForToken(ctx context.Context, in *AdminLoginForTokenRequest, opts ...grpc.CallOption) (*AdminLoginForTokenResponse, error)
	ExchangeRefreshToken(ctx context.Context, in *ExchangeRefreshTokenRequest, opts ...grpc.CallOption) (*ExchangeRefreshTokenResponse, error)
	// The two reads a client makes on load.
	GetAuthStatus(ctx context.Context, in *GetAuthStatusRequest, opts ...grpc.CallOption) (*GetAuthStatusResponse, error)
	GetSelf(ctx context.Context, in *GetSelfRequest, opts ...grpc.CallOption) (*GetSelfResponse, error)
	// The three writes a signed-in person makes about their own credentials.
	UpdatePassword(ctx context.Context, in *UpdatePasswordRequest, opts ...grpc.CallOption) (*UpdatePasswordResponse, error)
	RefreshTOTPSecret(ctx context.Context, in *RefreshTOTPSecretRequest, opts ...grpc.CallOption) (*RefreshTOTPSecretResponse, error)
	VerifyTOTPSecret(ctx context.Context, in *VerifyTOTPSecretRequest, opts ...grpc.CallOption) (*VerifyTOTPSecretResponse, error)
}

type signInServiceClient struct {
	cc grpc.ClientConnInterface
}

func NewSignInServiceClient(cc grpc.ClientConnInterface) SignInServiceClient {
	return &signInServiceClient{cc}
}

func (c *signInServiceClient) Register(ctx context.Context, in *RegisterRequest, opts ...grpc.CallOption) (*RegisterResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(RegisterResponse)
	err := c.cc.Invoke(ctx, SignInService_Register_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *signInServiceClient) AttachPassword(ctx context.Context, in *AttachPasswordRequest, opts ...grpc.CallOption) (*AttachPasswordResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(AttachPasswordResponse)
	err := c.cc.Invoke(ctx, SignInService_AttachPassword_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *signInServiceClient) VerifyEmailAddress(ctx context.Context, in *VerifyEmailAddressRequest, opts ...grpc.CallOption) (*VerifyEmailAddressResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(VerifyEmailAddressResponse)
	err := c.cc.Invoke(ctx, SignInService_VerifyEmailAddress_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *signInServiceClient) RequestMagicLink(ctx context.Context, in *RequestMagicLinkRequest, opts ...grpc.CallOption) (*RequestMagicLinkResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(RequestMagicLinkResponse)
	err := c.cc.Invoke(ctx, SignInService_RequestMagicLink_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *signInServiceClient) RedeemMagicLink(ctx context.Context, in *RedeemMagicLinkRequest, opts ...grpc.CallOption) (*RedeemMagicLinkResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(RedeemMagicLinkResponse)
	err := c.cc.Invoke(ctx, SignInService_RedeemMagicLink_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *signInServiceClient) LoginForToken(ctx context.Context, in *LoginForTokenRequest, opts ...grpc.CallOption) (*LoginForTokenResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(LoginForTokenResponse)
	err := c.cc.Invoke(ctx, SignInService_LoginForToken_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *signInServiceClient) AdminLoginForToken(ctx context.Context, in *AdminLoginForTokenRequest, opts ...grpc.CallOption) (*AdminLoginForTokenResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(AdminLoginForTokenResponse)
	err := c.cc.Invoke(ctx, SignInService_AdminLoginForToken_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *signInServiceClient) ExchangeRefreshToken(ctx context.Context, in *ExchangeRefreshTokenRequest, opts ...grpc.CallOption) (*ExchangeRefreshTokenResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(ExchangeRefreshTokenResponse)
	err := c.cc.Invoke(ctx, SignInService_ExchangeRefreshToken_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *signInServiceClient) GetAuthStatus(ctx context.Context, in *GetAuthStatusRequest, opts ...grpc.CallOption) (*GetAuthStatusResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(GetAuthStatusResponse)
	err := c.cc.Invoke(ctx, SignInService_GetAuthStatus_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *signInServiceClient) GetSelf(ctx context.Context, in *GetSelfRequest, opts ...grpc.CallOption) (*GetSelfResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(GetSelfResponse)
	err := c.cc.Invoke(ctx, SignInService_GetSelf_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *signInServiceClient) UpdatePassword(ctx context.Context, in *UpdatePasswordRequest, opts ...grpc.CallOption) (*UpdatePasswordResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(UpdatePasswordResponse)
	err := c.cc.Invoke(ctx, SignInService_UpdatePassword_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *signInServiceClient) RefreshTOTPSecret(ctx context.Context, in *RefreshTOTPSecretRequest, opts ...grpc.CallOption) (*RefreshTOTPSecretResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(RefreshTOTPSecretResponse)
	err := c.cc.Invoke(ctx, SignInService_RefreshTOTPSecret_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (c *signInServiceClient) VerifyTOTPSecret(ctx context.Context, in *VerifyTOTPSecretRequest, opts ...grpc.CallOption) (*VerifyTOTPSecretResponse, error) {
	cOpts := append([]grpc.CallOption{grpc.StaticMethod()}, opts...)
	out := new(VerifyTOTPSecretResponse)
	err := c.cc.Invoke(ctx, SignInService_VerifyTOTPSecret_FullMethodName, in, out, cOpts...)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SignInServiceServer is the server API for SignInService service.
// All implementations must embed UnimplementedSignInServiceServer
// for forward compatibility.
//
// SignInService is sign-in.
//
// Eight of its RPCs are anonymous by definition and five require a caller. What
// none of them requires is a permission: there is no grant that would make
// "sign in" safer, and the four authenticated ones take their subject from the
// caller and have no field that could name anybody else. See
// authentication/signin/grpc's Require for how that is declared to an
// authorization policy, which is not the same thing as being left out of one.
type SignInServiceServer interface {
	// Arriving, and the two ways a registration is finished. Register requires a
	// caller -- the consumer's own registrar, for the reason identity's Register
	// requires one: an open sign-up is a flow with policy in it, a captcha, a rate
	// limit, an email domain rule, and this service holds none of that. The other
	// two are anonymous and carry their own authority, which is the token that was
	// mailed to the person they are about.
	Register(context.Context, *RegisterRequest) (*RegisterResponse, error)
	AttachPassword(context.Context, *AttachPasswordRequest) (*AttachPasswordResponse, error)
	VerifyEmailAddress(context.Context, *VerifyEmailAddressRequest) (*VerifyEmailAddressResponse, error)
	// The passwordless door, both halves anonymous. Requesting a link names an
	// address and is answered the same way whoever holds it; redeeming one carries
	// the token that was mailed, which is the whole of its authority. Neither can
	// name a user, so neither is a way to ask about one.
	//
	// Rate limiting is the consumer's, in front of RequestMagicLink, and it is not
	// optional: this is the one RPC in this service that sends mail on request.
	RequestMagicLink(context.Context, *RequestMagicLinkRequest) (*RequestMagicLinkResponse, error)
	RedeemMagicLink(context.Context, *RedeemMagicLinkRequest) (*RedeemMagicLinkResponse, error)
	// The two doors, and the one that keeps a sign-in alive without reopening
	// either of them.
	LoginForToken(context.Context, *LoginForTokenRequest) (*LoginForTokenResponse, error)
	AdminLoginForToken(context.Context, *AdminLoginForTokenRequest) (*AdminLoginForTokenResponse, error)
	ExchangeRefreshToken(context.Context, *ExchangeRefreshTokenRequest) (*ExchangeRefreshTokenResponse, error)
	// The two reads a client makes on load.
	GetAuthStatus(context.Context, *GetAuthStatusRequest) (*GetAuthStatusResponse, error)
	GetSelf(context.Context, *GetSelfRequest) (*GetSelfResponse, error)
	// The three writes a signed-in person makes about their own credentials.
	UpdatePassword(context.Context, *UpdatePasswordRequest) (*UpdatePasswordResponse, error)
	RefreshTOTPSecret(context.Context, *RefreshTOTPSecretRequest) (*RefreshTOTPSecretResponse, error)
	VerifyTOTPSecret(context.Context, *VerifyTOTPSecretRequest) (*VerifyTOTPSecretResponse, error)
	mustEmbedUnimplementedSignInServiceServer()
}

// UnimplementedSignInServiceServer must be embedded to have
// forward compatible implementations.
//
// NOTE: this should be embedded by value instead of pointer to avoid a nil
// pointer dereference when methods are called.
type UnimplementedSignInServiceServer struct{}

func (UnimplementedSignInServiceServer) Register(context.Context, *RegisterRequest) (*RegisterResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method Register not implemented")
}
func (UnimplementedSignInServiceServer) AttachPassword(context.Context, *AttachPasswordRequest) (*AttachPasswordResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method AttachPassword not implemented")
}
func (UnimplementedSignInServiceServer) VerifyEmailAddress(context.Context, *VerifyEmailAddressRequest) (*VerifyEmailAddressResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method VerifyEmailAddress not implemented")
}
func (UnimplementedSignInServiceServer) RequestMagicLink(context.Context, *RequestMagicLinkRequest) (*RequestMagicLinkResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method RequestMagicLink not implemented")
}
func (UnimplementedSignInServiceServer) RedeemMagicLink(context.Context, *RedeemMagicLinkRequest) (*RedeemMagicLinkResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method RedeemMagicLink not implemented")
}
func (UnimplementedSignInServiceServer) LoginForToken(context.Context, *LoginForTokenRequest) (*LoginForTokenResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method LoginForToken not implemented")
}
func (UnimplementedSignInServiceServer) AdminLoginForToken(context.Context, *AdminLoginForTokenRequest) (*AdminLoginForTokenResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method AdminLoginForToken not implemented")
}
func (UnimplementedSignInServiceServer) ExchangeRefreshToken(context.Context, *ExchangeRefreshTokenRequest) (*ExchangeRefreshTokenResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method ExchangeRefreshToken not implemented")
}
func (UnimplementedSignInServiceServer) GetAuthStatus(context.Context, *GetAuthStatusRequest) (*GetAuthStatusResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetAuthStatus not implemented")
}
func (UnimplementedSignInServiceServer) GetSelf(context.Context, *GetSelfRequest) (*GetSelfResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method GetSelf not implemented")
}
func (UnimplementedSignInServiceServer) UpdatePassword(context.Context, *UpdatePasswordRequest) (*UpdatePasswordResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method UpdatePassword not implemented")
}
func (UnimplementedSignInServiceServer) RefreshTOTPSecret(context.Context, *RefreshTOTPSecretRequest) (*RefreshTOTPSecretResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method RefreshTOTPSecret not implemented")
}
func (UnimplementedSignInServiceServer) VerifyTOTPSecret(context.Context, *VerifyTOTPSecretRequest) (*VerifyTOTPSecretResponse, error) {
	return nil, status.Errorf(codes.Unimplemented, "method VerifyTOTPSecret not implemented")
}
func (UnimplementedSignInServiceServer) mustEmbedUnimplementedSignInServiceServer() {}
func (UnimplementedSignInServiceServer) testEmbeddedByValue()                       {}

// UnsafeSignInServiceServer may be embedded to opt out of forward compatibility for this service.
// Use of this interface is not recommended, as added methods to SignInServiceServer will
// result in compilation errors.
type UnsafeSignInServiceServer interface {
	mustEmbedUnimplementedSignInServiceServer()
}

func RegisterSignInServiceServer(s grpc.ServiceRegistrar, srv SignInServiceServer) {
	// If the following call pancis, it indicates UnimplementedSignInServiceServer was
	// embedded by pointer and is nil.  This will cause panics if an
	// unimplemented method is ever invoked, so we test this at initialization
	// time to prevent it from happening at runtime later due to I/O.
	if t, ok := srv.(interface{ testEmbeddedByValue() }); ok {
		t.testEmbeddedByValue()
	}
	s.RegisterService(&SignInService_ServiceDesc, srv)
}

func _SignInService_Register_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(RegisterRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SignInServiceServer).Register(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SignInService_Register_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SignInServiceServer).Register(ctx, req.(*RegisterRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SignInService_AttachPassword_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(AttachPasswordRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SignInServiceServer).AttachPassword(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SignInService_AttachPassword_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SignInServiceServer).AttachPassword(ctx, req.(*AttachPasswordRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SignInService_VerifyEmailAddress_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(VerifyEmailAddressRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SignInServiceServer).VerifyEmailAddress(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SignInService_VerifyEmailAddress_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SignInServiceServer).VerifyEmailAddress(ctx, req.(*VerifyEmailAddressRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SignInService_RequestMagicLink_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(RequestMagicLinkRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SignInServiceServer).RequestMagicLink(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SignInService_RequestMagicLink_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SignInServiceServer).RequestMagicLink(ctx, req.(*RequestMagicLinkRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SignInService_RedeemMagicLink_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(RedeemMagicLinkRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SignInServiceServer).RedeemMagicLink(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SignInService_RedeemMagicLink_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SignInServiceServer).RedeemMagicLink(ctx, req.(*RedeemMagicLinkRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SignInService_LoginForToken_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(LoginForTokenRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SignInServiceServer).LoginForToken(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SignInService_LoginForToken_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SignInServiceServer).LoginForToken(ctx, req.(*LoginForTokenRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SignInService_AdminLoginForToken_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(AdminLoginForTokenRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SignInServiceServer).AdminLoginForToken(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SignInService_AdminLoginForToken_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SignInServiceServer).AdminLoginForToken(ctx, req.(*AdminLoginForTokenRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SignInService_ExchangeRefreshToken_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(ExchangeRefreshTokenRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SignInServiceServer).ExchangeRefreshToken(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SignInService_ExchangeRefreshToken_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SignInServiceServer).ExchangeRefreshToken(ctx, req.(*ExchangeRefreshTokenRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SignInService_GetAuthStatus_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetAuthStatusRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SignInServiceServer).GetAuthStatus(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SignInService_GetAuthStatus_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SignInServiceServer).GetAuthStatus(ctx, req.(*GetAuthStatusRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SignInService_GetSelf_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(GetSelfRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SignInServiceServer).GetSelf(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SignInService_GetSelf_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SignInServiceServer).GetSelf(ctx, req.(*GetSelfRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SignInService_UpdatePassword_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(UpdatePasswordRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SignInServiceServer).UpdatePassword(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SignInService_UpdatePassword_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SignInServiceServer).UpdatePassword(ctx, req.(*UpdatePasswordRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SignInService_RefreshTOTPSecret_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(RefreshTOTPSecretRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SignInServiceServer).RefreshTOTPSecret(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SignInService_RefreshTOTPSecret_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SignInServiceServer).RefreshTOTPSecret(ctx, req.(*RefreshTOTPSecretRequest))
	}
	return interceptor(ctx, in, info, handler)
}

func _SignInService_VerifyTOTPSecret_Handler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	in := new(VerifyTOTPSecretRequest)
	if err := dec(in); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(SignInServiceServer).VerifyTOTPSecret(ctx, in)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: SignInService_VerifyTOTPSecret_FullMethodName,
	}
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		return srv.(SignInServiceServer).VerifyTOTPSecret(ctx, req.(*VerifyTOTPSecretRequest))
	}
	return interceptor(ctx, in, info, handler)
}

// SignInService_ServiceDesc is the grpc.ServiceDesc for SignInService service.
// It's only intended for direct use with grpc.RegisterService,
// and not to be introspected or modified (even as a copy)
var SignInService_ServiceDesc = grpc.ServiceDesc{
	ServiceName: "primandproper.platform.signin.v1.SignInService",
	HandlerType: (*SignInServiceServer)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "Register",
			Handler:    _SignInService_Register_Handler,
		},
		{
			MethodName: "AttachPassword",
			Handler:    _SignInService_AttachPassword_Handler,
		},
		{
			MethodName: "VerifyEmailAddress",
			Handler:    _SignInService_VerifyEmailAddress_Handler,
		},
		{
			MethodName: "RequestMagicLink",
			Handler:    _SignInService_RequestMagicLink_Handler,
		},
		{
			MethodName: "RedeemMagicLink",
			Handler:    _SignInService_RedeemMagicLink_Handler,
		},
		{
			MethodName: "LoginForToken",
			Handler:    _SignInService_LoginForToken_Handler,
		},
		{
			MethodName: "AdminLoginForToken",
			Handler:    _SignInService_AdminLoginForToken_Handler,
		},
		{
			MethodName: "ExchangeRefreshToken",
			Handler:    _SignInService_ExchangeRefreshToken_Handler,
		},
		{
			MethodName: "GetAuthStatus",
			Handler:    _SignInService_GetAuthStatus_Handler,
		},
		{
			MethodName: "GetSelf",
			Handler:    _SignInService_GetSelf_Handler,
		},
		{
			MethodName: "UpdatePassword",
			Handler:    _SignInService_UpdatePassword_Handler,
		},
		{
			MethodName: "RefreshTOTPSecret",
			Handler:    _SignInService_RefreshTOTPSecret_Handler,
		},
		{
			MethodName: "VerifyTOTPSecret",
			Handler:    _SignInService_VerifyTOTPSecret_Handler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "primandproper/platform/signin/v1/signin.proto",
}

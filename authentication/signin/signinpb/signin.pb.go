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
// [RegistrationInvitation] -- and on [IssuedToken], [AuthStatus],
// [Registered] and [TOTPEnrollment], which the responses are built from.
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
// as a request field on the RPCs that answer a link, which is identity's
// rule for an invitation's token and is the same rule for the same reason.
//
// No passkeys and no password reset. Each is a flow of its own over an engine
// this module already ships, and each is its own file rather than a branch in
// this one. The logins a person holds are here -- ListSignIns and EndSignIn read
// and end the refresh token families above -- and a session in the sense of
// github.com/primandproper/platform-go/v14/sessions still is not. Email-link sign-in -- a door that mints a
// token from a clicked link rather than from a password -- is not here either:
// it is a sibling of LoginForToken rather than a branch inside it, and it is
// the one thing a registrant who named no password still needs.
//
// # Two services, and why the second exists
//
// SignInService permissions nothing: every RPC on it is a door, a finish of a
// registration, or about the caller and nobody else. An operator's view of
// somebody else's logins is a different act, and it is SignInAdministrationService
// rather than a field on those RPCs naming a user -- a field that would turn every
// "about the caller" guarantee on SignInService into a question about the
// caller's grants. The second service is the one that is permissioned, one
// declared permission per RPC and no default grant for any of them, so which
// callers are operators stays the deployment's policy. See
// authentication/signin/grpc's Permissions.

// Code generated by protoc-gen-go. DO NOT EDIT.
// versions:
// 	protoc-gen-go v1.36.11
// 	protoc        v6.33.1
// source: primandproper/platform/signin/v1/signin.proto

package signinpb

import (
	reflect "reflect"
	sync "sync"
	unsafe "unsafe"

	identitypb "github.com/primandproper/platform-go/v14/identity/identitypb"

	protoreflect "google.golang.org/protobuf/reflect/protoreflect"
	protoimpl "google.golang.org/protobuf/runtime/protoimpl"
	timestamppb "google.golang.org/protobuf/types/known/timestamppb"
)

const (
	// Verify that this generated code is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(20 - protoimpl.MinVersion)
	// Verify that runtime/protoimpl is sufficiently up-to-date.
	_ = protoimpl.EnforceVersion(protoimpl.MaxVersion - 20)
)

// Credentials is what a sign-in form submits.
//
// Exactly one of username and email_address names the user. Both is refused and
// neither is refused: a client sending both has a bug, and picking one for them
// makes it a bug that signs somebody in.
type Credentials struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// username is the handle the user signs in with.
	Username string `protobuf:"bytes,1,opt,name=username,proto3" json:"username,omitempty"`
	// email_address is the address they signed up with, as an alternative handle.
	EmailAddress string `protobuf:"bytes,2,opt,name=email_address,json=emailAddress,proto3" json:"email_address,omitempty"`
	// password is the plaintext password. It is compared against a stored hash
	// and is never stored, logged or traced. It is the reason this RPC requires
	// transport security.
	Password string `protobuf:"bytes,3,opt,name=password,proto3" json:"password,omitempty"`
	// totp_code is the second-factor code. It is required from a user who holds a
	// proven second factor and ignored for everybody else, so a client that always
	// sends it when it has it is always correct.
	TotpCode string `protobuf:"bytes,4,opt,name=totp_code,json=totpCode,proto3" json:"totp_code,omitempty"`
	// active_account_id is the account the token should be issued for. Empty means
	// the user's default account. An account the user is not a live member of is
	// refused rather than honoured, which is the check that stops a client
	// choosing whose data its token reaches.
	ActiveAccountId string `protobuf:"bytes,5,opt,name=active_account_id,json=activeAccountID,proto3" json:"active_account_id,omitempty"`
	unknownFields   protoimpl.UnknownFields
	sizeCache       protoimpl.SizeCache
}

func (x *Credentials) Reset() {
	*x = Credentials{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[0]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Credentials) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Credentials) ProtoMessage() {}

func (x *Credentials) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[0]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Credentials.ProtoReflect.Descriptor instead.
func (*Credentials) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{0}
}

func (x *Credentials) GetUsername() string {
	if x != nil {
		return x.Username
	}
	return ""
}

func (x *Credentials) GetEmailAddress() string {
	if x != nil {
		return x.EmailAddress
	}
	return ""
}

func (x *Credentials) GetPassword() string {
	if x != nil {
		return x.Password
	}
	return ""
}

func (x *Credentials) GetTotpCode() string {
	if x != nil {
		return x.TotpCode
	}
	return ""
}

func (x *Credentials) GetActiveAccountId() string {
	if x != nil {
		return x.ActiveAccountId
	}
	return ""
}

// IssuedToken is a token and what it is good for.
//
// It carries no user and no permissions, deliberately. A client that needs
// either calls GetAuthStatus, which reads them fresh -- the alternative is a
// permission set frozen at sign-in, where revoking a role has no effect until
// the token expires.
type IssuedToken struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// token is the credential itself.
	Token string `protobuf:"bytes,1,opt,name=token,proto3" json:"token,omitempty"`
	// token_id is the issuer's "jti": the handle a revocation list names, and the
	// one part of this message safe to record.
	TokenId string `protobuf:"bytes,2,opt,name=token_id,json=tokenID,proto3" json:"token_id,omitempty"`
	// expires_at is when the token stops being accepted, as the service asked for
	// it.
	ExpiresAt *timestamppb.Timestamp `protobuf:"bytes,3,opt,name=expires_at,json=expiresAt,proto3" json:"expires_at,omitempty"`
	// active_account_id is the account the token was issued for -- the one the
	// request named, or the user's default. It is here as well as in the token's
	// own claims so that a client which cannot parse the token still knows which
	// account it is holding one for.
	ActiveAccountId string `protobuf:"bytes,4,opt,name=active_account_id,json=activeAccountID,proto3" json:"active_account_id,omitempty"`
	// administrative reports whether this token came through the administrative
	// door, which typically means a shorter lifetime.
	Administrative bool `protobuf:"varint,5,opt,name=administrative,proto3" json:"administrative,omitempty"`
	// refresh_token is the credential that mints the next token without a
	// password, by way of ExchangeRefreshToken. It is empty for a service that
	// stores no refresh tokens.
	//
	// It is single-use. Exchanging it spends it and returns its successor, and
	// presenting one that was already spent ends the whole login -- every token
	// that sign-in issued stops working, because a token presented twice means two
	// parties hold one credential and which of them is asking cannot be told from
	// the server's side. A client that retries an exchange must retry it with the
	// successor it was given, never with the token it has already sent.
	//
	// It is longer lived than token and is therefore the one worth stealing. It
	// belongs in whatever the client's most protected store is, and it belongs
	// nowhere a log, a crash report or a URL can reach.
	RefreshToken string `protobuf:"bytes,6,opt,name=refresh_token,json=refreshToken,proto3" json:"refresh_token,omitempty"`
	// refresh_token_expires_at is when the refresh token stops being
	// exchangeable, and is absent when there is none.
	//
	// It is the deadline that bounds the sign-in. Every exchange mints a successor
	// with a fresh window, so a client that keeps refreshing keeps the login and
	// one that stops loses it here.
	RefreshTokenExpiresAt *timestamppb.Timestamp `protobuf:"bytes,7,opt,name=refresh_token_expires_at,json=refreshTokenExpiresAt,proto3" json:"refresh_token_expires_at,omitempty"`
	// family_id names the continuous login this token belongs to: the same value
	// on the token a sign-in mints and on every successor an exchange mints after
	// it.
	//
	// It is what the access token carries as its "sid" claim, so an interceptor
	// that parses the token and a client that read this field are naming the same
	// login. It is populated whether or not a refresh token was issued.
	FamilyId      string `protobuf:"bytes,8,opt,name=family_id,json=familyID,proto3" json:"family_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *IssuedToken) Reset() {
	*x = IssuedToken{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[1]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *IssuedToken) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*IssuedToken) ProtoMessage() {}

func (x *IssuedToken) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[1]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use IssuedToken.ProtoReflect.Descriptor instead.
func (*IssuedToken) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{1}
}

func (x *IssuedToken) GetToken() string {
	if x != nil {
		return x.Token
	}
	return ""
}

func (x *IssuedToken) GetTokenId() string {
	if x != nil {
		return x.TokenId
	}
	return ""
}

func (x *IssuedToken) GetExpiresAt() *timestamppb.Timestamp {
	if x != nil {
		return x.ExpiresAt
	}
	return nil
}

func (x *IssuedToken) GetActiveAccountId() string {
	if x != nil {
		return x.ActiveAccountId
	}
	return ""
}

func (x *IssuedToken) GetAdministrative() bool {
	if x != nil {
		return x.Administrative
	}
	return false
}

func (x *IssuedToken) GetRefreshToken() string {
	if x != nil {
		return x.RefreshToken
	}
	return ""
}

func (x *IssuedToken) GetRefreshTokenExpiresAt() *timestamppb.Timestamp {
	if x != nil {
		return x.RefreshTokenExpiresAt
	}
	return nil
}

func (x *IssuedToken) GetFamilyId() string {
	if x != nil {
		return x.FamilyId
	}
	return ""
}

// AuthStatus is where a signed-in caller stands: who they are, which account
// they are in, and what a client has to make them do before anything else.
type AuthStatus struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// user is the caller, redacted, exactly as identity returns them.
	User *identitypb.User `protobuf:"bytes,1,opt,name=user,proto3" json:"user,omitempty"`
	// active_account_id is the account this caller's requests are against.
	ActiveAccountId string `protobuf:"bytes,2,opt,name=active_account_id,json=activeAccountID,proto3" json:"active_account_id,omitempty"`
	// account_ids is every account they are a live member of, default first.
	AccountIds []string `protobuf:"bytes,3,rep,name=account_ids,json=accountIDs,proto3" json:"account_ids,omitempty"`
	// has_password reports whether they hold a password credential at all. A
	// passwordless user -- registered with a passkey, or federated -- reports
	// false, and a client offering them a change-password form is offering them a
	// form that cannot work.
	HasPassword bool `protobuf:"varint,4,opt,name=has_password,json=hasPassword,proto3" json:"has_password,omitempty"`
	// two_factor_enrolled reports whether they hold a second factor they have
	// actually proven. A secret issued and never verified is not one.
	TwoFactorEnrolled bool `protobuf:"varint,5,opt,name=two_factor_enrolled,json=twoFactorEnrolled,proto3" json:"two_factor_enrolled,omitempty"`
	// requires_password_change reports whether an operator has forced a password
	// change. The service still signs such a user in -- the alternative is a user
	// who cannot reach the form -- so sending them to it is the client's job, and
	// this is how the client is told.
	RequiresPasswordChange bool `protobuf:"varint,6,opt,name=requires_password_change,json=requiresPasswordChange,proto3" json:"requires_password_change,omitempty"`
	// email_address_verified reports whether their address has been proven
	// reachable.
	EmailAddressVerified bool `protobuf:"varint,7,opt,name=email_address_verified,json=emailAddressVerified,proto3" json:"email_address_verified,omitempty"`
	unknownFields        protoimpl.UnknownFields
	sizeCache            protoimpl.SizeCache
}

func (x *AuthStatus) Reset() {
	*x = AuthStatus{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[2]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *AuthStatus) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*AuthStatus) ProtoMessage() {}

func (x *AuthStatus) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[2]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use AuthStatus.ProtoReflect.Descriptor instead.
func (*AuthStatus) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{2}
}

func (x *AuthStatus) GetUser() *identitypb.User {
	if x != nil {
		return x.User
	}
	return nil
}

func (x *AuthStatus) GetActiveAccountId() string {
	if x != nil {
		return x.ActiveAccountId
	}
	return ""
}

func (x *AuthStatus) GetAccountIds() []string {
	if x != nil {
		return x.AccountIds
	}
	return nil
}

func (x *AuthStatus) GetHasPassword() bool {
	if x != nil {
		return x.HasPassword
	}
	return false
}

func (x *AuthStatus) GetTwoFactorEnrolled() bool {
	if x != nil {
		return x.TwoFactorEnrolled
	}
	return false
}

func (x *AuthStatus) GetRequiresPasswordChange() bool {
	if x != nil {
		return x.RequiresPasswordChange
	}
	return false
}

func (x *AuthStatus) GetEmailAddressVerified() bool {
	if x != nil {
		return x.EmailAddressVerified
	}
	return false
}

type LoginForTokenRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Credentials   *Credentials           `protobuf:"bytes,1,opt,name=credentials,proto3" json:"credentials,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *LoginForTokenRequest) Reset() {
	*x = LoginForTokenRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[3]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *LoginForTokenRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*LoginForTokenRequest) ProtoMessage() {}

func (x *LoginForTokenRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[3]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use LoginForTokenRequest.ProtoReflect.Descriptor instead.
func (*LoginForTokenRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{3}
}

func (x *LoginForTokenRequest) GetCredentials() *Credentials {
	if x != nil {
		return x.Credentials
	}
	return nil
}

type LoginForTokenResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Token         *IssuedToken           `protobuf:"bytes,1,opt,name=token,proto3" json:"token,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *LoginForTokenResponse) Reset() {
	*x = LoginForTokenResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[4]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *LoginForTokenResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*LoginForTokenResponse) ProtoMessage() {}

func (x *LoginForTokenResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[4]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use LoginForTokenResponse.ProtoReflect.Descriptor instead.
func (*LoginForTokenResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{4}
}

func (x *LoginForTokenResponse) GetToken() *IssuedToken {
	if x != nil {
		return x.Token
	}
	return nil
}

// AdminLoginForTokenRequest is the administrative door. It carries the same
// credentials and is a message of its own so that the two doors can diverge
// without either becoming a field on the other.
type AdminLoginForTokenRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Credentials   *Credentials           `protobuf:"bytes,1,opt,name=credentials,proto3" json:"credentials,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *AdminLoginForTokenRequest) Reset() {
	*x = AdminLoginForTokenRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[5]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *AdminLoginForTokenRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*AdminLoginForTokenRequest) ProtoMessage() {}

func (x *AdminLoginForTokenRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[5]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use AdminLoginForTokenRequest.ProtoReflect.Descriptor instead.
func (*AdminLoginForTokenRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{5}
}

func (x *AdminLoginForTokenRequest) GetCredentials() *Credentials {
	if x != nil {
		return x.Credentials
	}
	return nil
}

type AdminLoginForTokenResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Token         *IssuedToken           `protobuf:"bytes,1,opt,name=token,proto3" json:"token,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *AdminLoginForTokenResponse) Reset() {
	*x = AdminLoginForTokenResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[6]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *AdminLoginForTokenResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*AdminLoginForTokenResponse) ProtoMessage() {}

func (x *AdminLoginForTokenResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[6]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use AdminLoginForTokenResponse.ProtoReflect.Descriptor instead.
func (*AdminLoginForTokenResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{6}
}

func (x *AdminLoginForTokenResponse) GetToken() *IssuedToken {
	if x != nil {
		return x.Token
	}
	return nil
}

// ExchangeRefreshTokenRequest spends a refresh token for a fresh pair.
//
// It is anonymous, like the two doors, and for the same reason: the credential
// presented is the whole of the request's authority, and a caller holding one has
// not been authenticated yet. There is no field naming a user -- who the new
// token is for is read off the row the presented token named, not off anything a
// client sends.
type ExchangeRefreshTokenRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// refresh_token is the credential a previous IssuedToken carried.
	RefreshToken  string `protobuf:"bytes,1,opt,name=refresh_token,json=refreshToken,proto3" json:"refresh_token,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ExchangeRefreshTokenRequest) Reset() {
	*x = ExchangeRefreshTokenRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[7]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ExchangeRefreshTokenRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ExchangeRefreshTokenRequest) ProtoMessage() {}

func (x *ExchangeRefreshTokenRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[7]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ExchangeRefreshTokenRequest.ProtoReflect.Descriptor instead.
func (*ExchangeRefreshTokenRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{7}
}

func (x *ExchangeRefreshTokenRequest) GetRefreshToken() string {
	if x != nil {
		return x.RefreshToken
	}
	return ""
}

type ExchangeRefreshTokenResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Token         *IssuedToken           `protobuf:"bytes,1,opt,name=token,proto3" json:"token,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ExchangeRefreshTokenResponse) Reset() {
	*x = ExchangeRefreshTokenResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[8]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ExchangeRefreshTokenResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ExchangeRefreshTokenResponse) ProtoMessage() {}

func (x *ExchangeRefreshTokenResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[8]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ExchangeRefreshTokenResponse.ProtoReflect.Descriptor instead.
func (*ExchangeRefreshTokenResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{8}
}

func (x *ExchangeRefreshTokenResponse) GetToken() *IssuedToken {
	if x != nil {
		return x.Token
	}
	return nil
}

// SwitchAccountRequest spends a refresh token for a fresh pair against another
// account the same person belongs to: the same login, moved.
//
// It is anonymous for ExchangeRefreshTokenRequest's reason -- the refresh token
// is the whole of the request's authority -- and it is a request of its own
// rather than a field on that one because switching is a deliberate act, not a
// refresh. Who the new token is for is read off the row the presented token
// named; which account it is for is the one thing a client says.
//
// The account must be one that person is currently a member of. Naming any
// other is refused exactly as a dead token is, so the answer says nothing about
// the account named, and the presented token is left unspent: the login stays
// where it was. The successor stays in the same login -- ListSignIns shows it
// once, EndSignIn ends it, and presenting the spent token again ends it as a
// replay of any exchange does.
type SwitchAccountRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// refresh_token is the credential a previous IssuedToken carried.
	RefreshToken string `protobuf:"bytes,1,opt,name=refresh_token,json=refreshToken,proto3" json:"refresh_token,omitempty"`
	// account_id is the account the new pair is for. Empty is INVALID_ARGUMENT:
	// a switch says where it is going.
	AccountId     string `protobuf:"bytes,2,opt,name=account_id,json=accountID,proto3" json:"account_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *SwitchAccountRequest) Reset() {
	*x = SwitchAccountRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[9]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SwitchAccountRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SwitchAccountRequest) ProtoMessage() {}

func (x *SwitchAccountRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[9]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SwitchAccountRequest.ProtoReflect.Descriptor instead.
func (*SwitchAccountRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{9}
}

func (x *SwitchAccountRequest) GetRefreshToken() string {
	if x != nil {
		return x.RefreshToken
	}
	return ""
}

func (x *SwitchAccountRequest) GetAccountId() string {
	if x != nil {
		return x.AccountId
	}
	return ""
}

type SwitchAccountResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Token         *IssuedToken           `protobuf:"bytes,1,opt,name=token,proto3" json:"token,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *SwitchAccountResponse) Reset() {
	*x = SwitchAccountResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[10]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SwitchAccountResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SwitchAccountResponse) ProtoMessage() {}

func (x *SwitchAccountResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[10]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SwitchAccountResponse.ProtoReflect.Descriptor instead.
func (*SwitchAccountResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{10}
}

func (x *SwitchAccountResponse) GetToken() *IssuedToken {
	if x != nil {
		return x.Token
	}
	return nil
}

// SignOutRequest ends one login: every refresh token this sign-in issued stops
// being exchangeable.
//
// It is anonymous and carries the refresh token, which is
// ExchangeRefreshTokenRequest's arrangement and is here for the same two
// reasons. The credential presented is the whole of the request's authority, so
// no field names a user and none could. And it is the one shape that still works
// when the access token has already expired, which is when somebody signing out
// usually is: they came back to an application that had been closed for a week
// and pressed the button.
//
// It grants nothing a caller did not already have. Whoever holds a live refresh
// token can already end the family by presenting it twice -- that is what reuse
// detection does -- so this is the same act performed on purpose, with the
// server told that it was deliberate rather than having to assume theft.
//
// What it does on its own is stop the access token already in somebody's hands
// being replaced, so a sign-out takes effect within one access-token lifetime:
// an access token is checked by the consumer's interceptor against the issuer's
// signature rather than against any table this service holds. A deployment that
// needs it to take effect at once has its interceptor ask
// signin.Service.CheckSignIn on every request -- the sign-in extractor's
// WithSignInCheck -- which refuses the ended login's access token from the next
// request on, rather than asking for another RPC.
type SignOutRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// refresh_token is the credential a previous IssuedToken carried. A token
	// nobody holds, one already spent and one already revoked are one answer --
	// there is nothing here for a caller to learn, and the family is ended either
	// way or was already.
	RefreshToken  string `protobuf:"bytes,1,opt,name=refresh_token,json=refreshToken,proto3" json:"refresh_token,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *SignOutRequest) Reset() {
	*x = SignOutRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[11]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SignOutRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SignOutRequest) ProtoMessage() {}

func (x *SignOutRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[11]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SignOutRequest.ProtoReflect.Descriptor instead.
func (*SignOutRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{11}
}

func (x *SignOutRequest) GetRefreshToken() string {
	if x != nil {
		return x.RefreshToken
	}
	return ""
}

// SignOutResponse is empty, and is a message rather than google.protobuf.Empty
// so that it can gain a field without becoming a breaking change -- see
// UpdatePasswordResponse.
//
// It deliberately does not report how many tokens were withdrawn. The service
// has that number and it is a row count rather than a device count: a login that
// has refreshed forty times is forty rows, so a client rendering "signed out of
// N devices" would be rendering something else entirely.
type SignOutResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *SignOutResponse) Reset() {
	*x = SignOutResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[12]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SignOutResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SignOutResponse) ProtoMessage() {}

func (x *SignOutResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[12]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SignOutResponse.ProtoReflect.Descriptor instead.
func (*SignOutResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{12}
}

// SignOutEverywhereRequest ends every login the calling user holds, on every
// device, including the one asking.
//
// It requires a caller and names nobody, which is GetAuthStatusRequest's rule
// for GetAuthStatusRequest's reason: the subject is whoever is calling, and a
// field naming somebody else would be an administrator's revocation wearing a
// sign-out's clothes. An operator ending somebody else's sessions is a different
// act with a different permission, and it is
// SignInAdministrationService.EndAllSignInsForUser rather than this RPC.
//
// It is the door for a password a person thinks somebody else has seen, so it
// takes no credential and re-proves nothing: a user who is told "your password
// may be compromised" and then asked for that password before they may act on it
// has been given advice they cannot take. What it costs is bounded by what it
// does -- ending your own sessions, which anyone holding your access token could
// achieve by waiting -- and the remedy for a stolen access token is here rather
// than nowhere.
type SignOutEverywhereRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *SignOutEverywhereRequest) Reset() {
	*x = SignOutEverywhereRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[13]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SignOutEverywhereRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SignOutEverywhereRequest) ProtoMessage() {}

func (x *SignOutEverywhereRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[13]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SignOutEverywhereRequest.ProtoReflect.Descriptor instead.
func (*SignOutEverywhereRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{13}
}

// SignOutEverywhereResponse is empty -- see SignOutResponse, whose reasoning
// applies here with more force, since this count spans every login.
type SignOutEverywhereResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *SignOutEverywhereResponse) Reset() {
	*x = SignOutEverywhereResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[14]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *SignOutEverywhereResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*SignOutEverywhereResponse) ProtoMessage() {}

func (x *SignOutEverywhereResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[14]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use SignOutEverywhereResponse.ProtoReflect.Descriptor instead.
func (*SignOutEverywhereResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{14}
}

// ActiveSignIn is one live login, as a "where you're signed in" screen shows
// it.
//
// The platform lists the logins and records how each one happened; the device
// is the consumer's. The server stores no device, no browser and no address,
// and that is a decision rather than a gap. Whether any of those is recorded at
// all is the consumer's, and a consumer that does record them keys them on
// family_id -- signin's AfterIssueToken hook runs inside every mint with the
// family on it -- and hands them back through the server's sign-in annotator,
// which fills attributes.
type ActiveSignIn struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// family_id names the login: the value every IssuedToken it produced carried,
	// the access token's "sid" claim, and what EndSignInRequest names.
	FamilyId string `protobuf:"bytes,1,opt,name=family_id,json=familyID,proto3" json:"family_id,omitempty"`
	// signed_in_at is when the login began. Refreshing does not move it.
	SignedInAt *timestamppb.Timestamp `protobuf:"bytes,2,opt,name=signed_in_at,json=signedInAt,proto3" json:"signed_in_at,omitempty"`
	// last_refreshed_at is when the login last exchanged a refresh token, or
	// signed_in_at for a login that never has.
	LastRefreshedAt *timestamppb.Timestamp `protobuf:"bytes,3,opt,name=last_refreshed_at,json=lastRefreshedAt,proto3" json:"last_refreshed_at,omitempty"`
	// expires_at is when the login ends if nothing refreshes it before then.
	ExpiresAt *timestamppb.Timestamp `protobuf:"bytes,4,opt,name=expires_at,json=expiresAt,proto3" json:"expires_at,omitempty"`
	// active_account_id is the account the login's tokens are for.
	ActiveAccountId string `protobuf:"bytes,5,opt,name=active_account_id,json=activeAccountID,proto3" json:"active_account_id,omitempty"`
	// administrative reports whether the login came through the administrative
	// door.
	Administrative bool `protobuf:"varint,6,opt,name=administrative,proto3" json:"administrative,omitempty"`
	// current reports whether this is the login the request itself was made
	// through. It is false on every entry when the server cannot tell -- a
	// consumer whose principal does not carry the access token's "sid" -- which
	// is the answer that marks nothing rather than guessing.
	Current bool `protobuf:"varint,7,opt,name=current,proto3" json:"current,omitempty"`
	// actor_id is the operator signed in as this person through this login --
	// an impersonation an operator surface began -- and empty for a login of the
	// person's own. A client shows it, because "somebody else is signed in as
	// you" is a thing a person is owed the chance to see and end.
	ActorId string `protobuf:"bytes,8,opt,name=actor_id,json=actorID,proto3" json:"actor_id,omitempty"`
	// credential_kind is how the login happened: what proved the sign-in that
	// began it -- "password", "recovery_code", "magic_link", "principal",
	// "impersonation", or a kind the consumer named when it proved the principal
	// itself. A refresh does not change it. It is empty only for a login whose
	// store recorded none.
	CredentialKind string `protobuf:"bytes,9,opt,name=credential_kind,json=credentialKind,proto3" json:"credential_kind,omitempty"`
	// attributes is what the consumer recorded about the login's device -- a
	// device name, a user agent, an address -- as the server's sign-in annotator
	// answered for this family. The platform stores none of it and names none of
	// its keys. It is empty on a server built without an annotator, and for a
	// login the annotator had nothing for.
	Attributes    map[string]string `protobuf:"bytes,10,rep,name=attributes,proto3" json:"attributes,omitempty" protobuf_key:"bytes,1,opt,name=key" protobuf_val:"bytes,2,opt,name=value"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ActiveSignIn) Reset() {
	*x = ActiveSignIn{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[15]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ActiveSignIn) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ActiveSignIn) ProtoMessage() {}

func (x *ActiveSignIn) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[15]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ActiveSignIn.ProtoReflect.Descriptor instead.
func (*ActiveSignIn) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{15}
}

func (x *ActiveSignIn) GetFamilyId() string {
	if x != nil {
		return x.FamilyId
	}
	return ""
}

func (x *ActiveSignIn) GetSignedInAt() *timestamppb.Timestamp {
	if x != nil {
		return x.SignedInAt
	}
	return nil
}

func (x *ActiveSignIn) GetLastRefreshedAt() *timestamppb.Timestamp {
	if x != nil {
		return x.LastRefreshedAt
	}
	return nil
}

func (x *ActiveSignIn) GetExpiresAt() *timestamppb.Timestamp {
	if x != nil {
		return x.ExpiresAt
	}
	return nil
}

func (x *ActiveSignIn) GetActiveAccountId() string {
	if x != nil {
		return x.ActiveAccountId
	}
	return ""
}

func (x *ActiveSignIn) GetAdministrative() bool {
	if x != nil {
		return x.Administrative
	}
	return false
}

func (x *ActiveSignIn) GetCurrent() bool {
	if x != nil {
		return x.Current
	}
	return false
}

func (x *ActiveSignIn) GetActorId() string {
	if x != nil {
		return x.ActorId
	}
	return ""
}

func (x *ActiveSignIn) GetCredentialKind() string {
	if x != nil {
		return x.CredentialKind
	}
	return ""
}

func (x *ActiveSignIn) GetAttributes() map[string]string {
	if x != nil {
		return x.Attributes
	}
	return nil
}

// ListSignInsRequest asks for the calling user's live logins, most recently
// refreshed first.
//
// It requires a caller and names nobody, which is SignOutEverywhereRequest's
// rule for SignOutEverywhereRequest's reason: an operator listing somebody
// else's logins is a different act with a different permission, and it is
// SignInAdministrationService.ListSignInsForUser rather than this RPC.
type ListSignInsRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// limit is how many to answer with. Zero is the service's default and a
	// value past its ceiling is the ceiling; see signin.DefaultSignInListLimit and
	// signin.MaxSignInListLimit. What a limit leaves out is what has been idle
	// longest.
	Limit         uint32 `protobuf:"varint,1,opt,name=limit,proto3" json:"limit,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListSignInsRequest) Reset() {
	*x = ListSignInsRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[16]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListSignInsRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListSignInsRequest) ProtoMessage() {}

func (x *ListSignInsRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[16]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListSignInsRequest.ProtoReflect.Descriptor instead.
func (*ListSignInsRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{16}
}

func (x *ListSignInsRequest) GetLimit() uint32 {
	if x != nil {
		return x.Limit
	}
	return 0
}

type ListSignInsResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	SignIns       []*ActiveSignIn        `protobuf:"bytes,1,rep,name=sign_ins,json=signIns,proto3" json:"sign_ins,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListSignInsResponse) Reset() {
	*x = ListSignInsResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[17]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListSignInsResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListSignInsResponse) ProtoMessage() {}

func (x *ListSignInsResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[17]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListSignInsResponse.ProtoReflect.Descriptor instead.
func (*ListSignInsResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{17}
}

func (x *ListSignInsResponse) GetSignIns() []*ActiveSignIn {
	if x != nil {
		return x.SignIns
	}
	return nil
}

// EndSignInRequest ends one of the calling user's logins, named by its family.
//
// It is the door SignOutRequest could not be. That one carries the login's
// refresh token, which only the device holding it has; this one carries the
// family identifier a ListSignInsResponse handed out, so a person can end the
// login on the phone they lost from the laptop in front of them.
//
// A family identifier is not a secret, and that is why this requires a caller
// and why the caller is part of the key: a family that is not theirs matches
// nothing. The answer does not say whether anything was ended -- a family that
// is somebody else's, one that never existed and one already ended all answer
// the same, so the RPC cannot be used to learn which identifiers are live.
//
// Ending the login the request was made through is allowed and is a sign-out.
// Like SignOut, it stops that login's access tokens being replaced rather than
// stopping the one already issued.
type EndSignInRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	FamilyId      string                 `protobuf:"bytes,1,opt,name=family_id,json=familyID,proto3" json:"family_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *EndSignInRequest) Reset() {
	*x = EndSignInRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[18]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *EndSignInRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*EndSignInRequest) ProtoMessage() {}

func (x *EndSignInRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[18]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use EndSignInRequest.ProtoReflect.Descriptor instead.
func (*EndSignInRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{18}
}

func (x *EndSignInRequest) GetFamilyId() string {
	if x != nil {
		return x.FamilyId
	}
	return ""
}

// EndSignInResponse is empty -- see SignOutResponse.
type EndSignInResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *EndSignInResponse) Reset() {
	*x = EndSignInResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[19]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *EndSignInResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*EndSignInResponse) ProtoMessage() {}

func (x *EndSignInResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[19]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use EndSignInResponse.ProtoReflect.Descriptor instead.
func (*EndSignInResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{19}
}

// EndOtherSignInsRequest ends every login the calling user holds except the one
// the request was made through: "sign out my other devices".
//
// It names nobody and no login. The subject is the caller, and the login it
// keeps is read off the caller's access token -- its "sid" claim, the family_id
// ListSignIns marks current -- rather than taken from a field, so a request
// cannot keep a login other than its own. A caller whose token names no login is
// refused with FAILED_PRECONDITION and the reason SIGN_IN_NOT_IDENTIFIED rather
// than having every login ended: that is SignOutEverywhere, which is a request a
// client makes on purpose.
//
// It is one revocation on the server rather than ListSignIns followed by an
// EndSignIn for each entry, and that is what it is for. The loop decides which
// logins are "other" a round trip before it ends them, so one made in between
// survives; here the decision and the revocation are the same statement.
type EndOtherSignInsRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *EndOtherSignInsRequest) Reset() {
	*x = EndOtherSignInsRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[20]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *EndOtherSignInsRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*EndOtherSignInsRequest) ProtoMessage() {}

func (x *EndOtherSignInsRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[20]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use EndOtherSignInsRequest.ProtoReflect.Descriptor instead.
func (*EndOtherSignInsRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{20}
}

// EndOtherSignInsResponse is empty -- see SignOutResponse. A client that wants
// to show what is left calls ListSignIns.
type EndOtherSignInsResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *EndOtherSignInsResponse) Reset() {
	*x = EndOtherSignInsResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[21]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *EndOtherSignInsResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*EndOtherSignInsResponse) ProtoMessage() {}

func (x *EndOtherSignInsResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[21]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use EndOtherSignInsResponse.ProtoReflect.Descriptor instead.
func (*EndOtherSignInsResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{21}
}

// GetAuthStatusRequest names nobody. The subject is whoever is calling, and a
// field naming somebody else would be a directory read wearing a whoami's
// clothes.
type GetAuthStatusRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetAuthStatusRequest) Reset() {
	*x = GetAuthStatusRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[22]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetAuthStatusRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetAuthStatusRequest) ProtoMessage() {}

func (x *GetAuthStatusRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[22]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetAuthStatusRequest.ProtoReflect.Descriptor instead.
func (*GetAuthStatusRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{22}
}

type GetAuthStatusResponse struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// authenticated reports whether the request arrived with a caller on it. It is
	// the one RPC in this service that answers an anonymous request rather than
	// refusing it, because "am I signed in" is a question whose answer can be no.
	Authenticated bool `protobuf:"varint,1,opt,name=authenticated,proto3" json:"authenticated,omitempty"`
	// status is absent when authenticated is false, and present otherwise.
	Status        *AuthStatus `protobuf:"bytes,2,opt,name=status,proto3" json:"status,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetAuthStatusResponse) Reset() {
	*x = GetAuthStatusResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[23]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetAuthStatusResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetAuthStatusResponse) ProtoMessage() {}

func (x *GetAuthStatusResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[23]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetAuthStatusResponse.ProtoReflect.Descriptor instead.
func (*GetAuthStatusResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{23}
}

func (x *GetAuthStatusResponse) GetAuthenticated() bool {
	if x != nil {
		return x.Authenticated
	}
	return false
}

func (x *GetAuthStatusResponse) GetStatus() *AuthStatus {
	if x != nil {
		return x.Status
	}
	return nil
}

type GetSelfRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetSelfRequest) Reset() {
	*x = GetSelfRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[24]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetSelfRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetSelfRequest) ProtoMessage() {}

func (x *GetSelfRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[24]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetSelfRequest.ProtoReflect.Descriptor instead.
func (*GetSelfRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{24}
}

type GetSelfResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	User          *identitypb.User       `protobuf:"bytes,1,opt,name=user,proto3" json:"user,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *GetSelfResponse) Reset() {
	*x = GetSelfResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[25]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *GetSelfResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*GetSelfResponse) ProtoMessage() {}

func (x *GetSelfResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[25]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use GetSelfResponse.ProtoReflect.Descriptor instead.
func (*GetSelfResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{25}
}

func (x *GetSelfResponse) GetUser() *identitypb.User {
	if x != nil {
		return x.User
	}
	return nil
}

// UpdatePasswordRequest changes the calling user's own password. There is no
// field naming a user: an operator resetting somebody else's credential is a
// different act with a different permission, and it is not this one.
type UpdatePasswordRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// current_password is required and is checked. A token proves somebody had the
	// password once; asking again is the whole point, and the laptop left unlocked
	// in between is the reason.
	CurrentPassword string `protobuf:"bytes,1,opt,name=current_password,json=currentPassword,proto3" json:"current_password,omitempty"`
	// new_password is what replaces it. Whether it is long enough, unusual enough
	// or unlike the last four is the consumer's rule, applied in front of this
	// call.
	NewPassword string `protobuf:"bytes,2,opt,name=new_password,json=newPassword,proto3" json:"new_password,omitempty"`
	// totp_code is required from a user who holds a proven second factor.
	TotpCode      string `protobuf:"bytes,3,opt,name=totp_code,json=totpCode,proto3" json:"totp_code,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *UpdatePasswordRequest) Reset() {
	*x = UpdatePasswordRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[26]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *UpdatePasswordRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*UpdatePasswordRequest) ProtoMessage() {}

func (x *UpdatePasswordRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[26]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use UpdatePasswordRequest.ProtoReflect.Descriptor instead.
func (*UpdatePasswordRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{26}
}

func (x *UpdatePasswordRequest) GetCurrentPassword() string {
	if x != nil {
		return x.CurrentPassword
	}
	return ""
}

func (x *UpdatePasswordRequest) GetNewPassword() string {
	if x != nil {
		return x.NewPassword
	}
	return ""
}

func (x *UpdatePasswordRequest) GetTotpCode() string {
	if x != nil {
		return x.TotpCode
	}
	return ""
}

// UpdatePasswordResponse is empty and is a message rather than
// google.protobuf.Empty so that it can gain a field without becoming a
// breaking change.
type UpdatePasswordResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *UpdatePasswordResponse) Reset() {
	*x = UpdatePasswordResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[27]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *UpdatePasswordResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*UpdatePasswordResponse) ProtoMessage() {}

func (x *UpdatePasswordResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[27]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use UpdatePasswordResponse.ProtoReflect.Descriptor instead.
func (*UpdatePasswordResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{27}
}

// UpdateEmailAddressRequest moves the calling user's own address. There is no
// field naming a user, for UpdatePasswordRequest's reason.
//
// An address is where a password reset is mailed, so moving it is guarded as a
// password change is, by one of two proofs. current_password, with totp_code
// from a user who holds a proven second factor. Or, with no password sent, the
// sign-in this request came through, if it began recently enough -- read off
// the caller's token rather than off this message, so there is no field for
// it. A user who holds no password has only the second, and a stale sign-in is
// refused with REAUTHENTICATION_REQUIRED: sign in again, then ask.
//
// The address's proof goes with the old address, and a deployment that mails
// verification links mails the new address one.
type UpdateEmailAddressRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// current_password is checked when it is sent. Empty offers the recent
	// sign-in instead.
	CurrentPassword string `protobuf:"bytes,1,opt,name=current_password,json=currentPassword,proto3" json:"current_password,omitempty"`
	// totp_code is required alongside current_password from a user who holds a
	// proven second factor.
	TotpCode string `protobuf:"bytes,2,opt,name=totp_code,json=totpCode,proto3" json:"totp_code,omitempty"`
	// new_email_address is the address that replaces the current one.
	NewEmailAddress string `protobuf:"bytes,3,opt,name=new_email_address,json=newEmailAddress,proto3" json:"new_email_address,omitempty"`
	unknownFields   protoimpl.UnknownFields
	sizeCache       protoimpl.SizeCache
}

func (x *UpdateEmailAddressRequest) Reset() {
	*x = UpdateEmailAddressRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[28]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *UpdateEmailAddressRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*UpdateEmailAddressRequest) ProtoMessage() {}

func (x *UpdateEmailAddressRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[28]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use UpdateEmailAddressRequest.ProtoReflect.Descriptor instead.
func (*UpdateEmailAddressRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{28}
}

func (x *UpdateEmailAddressRequest) GetCurrentPassword() string {
	if x != nil {
		return x.CurrentPassword
	}
	return ""
}

func (x *UpdateEmailAddressRequest) GetTotpCode() string {
	if x != nil {
		return x.TotpCode
	}
	return ""
}

func (x *UpdateEmailAddressRequest) GetNewEmailAddress() string {
	if x != nil {
		return x.NewEmailAddress
	}
	return ""
}

// UpdateEmailAddressResponse carries the user as they stand after the change.
type UpdateEmailAddressResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	User          *identitypb.User       `protobuf:"bytes,1,opt,name=user,proto3" json:"user,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *UpdateEmailAddressResponse) Reset() {
	*x = UpdateEmailAddressResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[29]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *UpdateEmailAddressResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*UpdateEmailAddressResponse) ProtoMessage() {}

func (x *UpdateEmailAddressResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[29]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use UpdateEmailAddressResponse.ProtoReflect.Descriptor instead.
func (*UpdateEmailAddressResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{29}
}

func (x *UpdateEmailAddressResponse) GetUser() *identitypb.User {
	if x != nil {
		return x.User
	}
	return nil
}

// UpdateUsernameRequest renames the calling user, guarded exactly as
// UpdateEmailAddressRequest is: a username is what somebody signs in with.
type UpdateUsernameRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// current_password is UpdateEmailAddressRequest.current_password.
	CurrentPassword string `protobuf:"bytes,1,opt,name=current_password,json=currentPassword,proto3" json:"current_password,omitempty"`
	// totp_code is UpdateEmailAddressRequest.totp_code.
	TotpCode string `protobuf:"bytes,2,opt,name=totp_code,json=totpCode,proto3" json:"totp_code,omitempty"`
	// new_username is the handle that replaces the current one, as the person
	// spelled it.
	NewUsername   string `protobuf:"bytes,3,opt,name=new_username,json=newUsername,proto3" json:"new_username,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *UpdateUsernameRequest) Reset() {
	*x = UpdateUsernameRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[30]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *UpdateUsernameRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*UpdateUsernameRequest) ProtoMessage() {}

func (x *UpdateUsernameRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[30]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use UpdateUsernameRequest.ProtoReflect.Descriptor instead.
func (*UpdateUsernameRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{30}
}

func (x *UpdateUsernameRequest) GetCurrentPassword() string {
	if x != nil {
		return x.CurrentPassword
	}
	return ""
}

func (x *UpdateUsernameRequest) GetTotpCode() string {
	if x != nil {
		return x.TotpCode
	}
	return ""
}

func (x *UpdateUsernameRequest) GetNewUsername() string {
	if x != nil {
		return x.NewUsername
	}
	return ""
}

// UpdateUsernameResponse carries the user as they stand after the change.
type UpdateUsernameResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	User          *identitypb.User       `protobuf:"bytes,1,opt,name=user,proto3" json:"user,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *UpdateUsernameResponse) Reset() {
	*x = UpdateUsernameResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[31]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *UpdateUsernameResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*UpdateUsernameResponse) ProtoMessage() {}

func (x *UpdateUsernameResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[31]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use UpdateUsernameResponse.ProtoReflect.Descriptor instead.
func (*UpdateUsernameResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{31}
}

func (x *UpdateUsernameResponse) GetUser() *identitypb.User {
	if x != nil {
		return x.User
	}
	return nil
}

// RefreshTOTPSecretRequest asks for a new second-factor secret for the calling
// user, replacing whatever they hold.
type RefreshTOTPSecretRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// current_password is required. Issuing a new second-factor secret to whoever
	// is holding an unlocked laptop is how a second factor stops being one.
	CurrentPassword string `protobuf:"bytes,1,opt,name=current_password,json=currentPassword,proto3" json:"current_password,omitempty"`
	// totp_code is a code from the secret being replaced, required from a user who
	// holds a proven one. Somebody enrolling for the first time has none and sends
	// none -- which means losing a phone is not a way to replace the factor that
	// phone held, and recovering from that is the consumer's, through an operator.
	TotpCode      string `protobuf:"bytes,2,opt,name=totp_code,json=totpCode,proto3" json:"totp_code,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *RefreshTOTPSecretRequest) Reset() {
	*x = RefreshTOTPSecretRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[32]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *RefreshTOTPSecretRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RefreshTOTPSecretRequest) ProtoMessage() {}

func (x *RefreshTOTPSecretRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[32]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RefreshTOTPSecretRequest.ProtoReflect.Descriptor instead.
func (*RefreshTOTPSecretRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{32}
}

func (x *RefreshTOTPSecretRequest) GetCurrentPassword() string {
	if x != nil {
		return x.CurrentPassword
	}
	return ""
}

func (x *RefreshTOTPSecretRequest) GetTotpCode() string {
	if x != nil {
		return x.TotpCode
	}
	return ""
}

// RefreshTOTPSecretResponse carries a live second-factor secret, which makes it
// the one response in this module that must not be logged, cached, retried into
// a shared store, or rendered anywhere it will be read twice.
//
// It is unavoidable: a secret nobody can see is a secret nobody can enroll. The
// secret is unproven until VerifyTOTPSecret succeeds, so between this response
// and that call the user holds no second factor at all.
type RefreshTOTPSecretResponse struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// secret is the base32 shared secret, for somebody typing it in.
	Secret string `protobuf:"bytes,1,opt,name=secret,proto3" json:"secret,omitempty"`
	// provisioning_uri is the otpauth:// URI that same secret encodes to, for a
	// client rendering a QR code.
	ProvisioningUri string `protobuf:"bytes,2,opt,name=provisioning_uri,json=provisioningUri,proto3" json:"provisioning_uri,omitempty"`
	unknownFields   protoimpl.UnknownFields
	sizeCache       protoimpl.SizeCache
}

func (x *RefreshTOTPSecretResponse) Reset() {
	*x = RefreshTOTPSecretResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[33]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *RefreshTOTPSecretResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RefreshTOTPSecretResponse) ProtoMessage() {}

func (x *RefreshTOTPSecretResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[33]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RefreshTOTPSecretResponse.ProtoReflect.Descriptor instead.
func (*RefreshTOTPSecretResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{33}
}

func (x *RefreshTOTPSecretResponse) GetSecret() string {
	if x != nil {
		return x.Secret
	}
	return ""
}

func (x *RefreshTOTPSecretResponse) GetProvisioningUri() string {
	if x != nil {
		return x.ProvisioningUri
	}
	return ""
}

type VerifyTOTPSecretRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// totp_code is a code from the secret the user was just issued. Producing one
	// is what proves possession, which is what turns the secret into a second
	// factor.
	TotpCode      string `protobuf:"bytes,1,opt,name=totp_code,json=totpCode,proto3" json:"totp_code,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *VerifyTOTPSecretRequest) Reset() {
	*x = VerifyTOTPSecretRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[34]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *VerifyTOTPSecretRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*VerifyTOTPSecretRequest) ProtoMessage() {}

func (x *VerifyTOTPSecretRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[34]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use VerifyTOTPSecretRequest.ProtoReflect.Descriptor instead.
func (*VerifyTOTPSecretRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{34}
}

func (x *VerifyTOTPSecretRequest) GetTotpCode() string {
	if x != nil {
		return x.TotpCode
	}
	return ""
}

// VerifyTOTPSecretResponse is empty -- see UpdatePasswordResponse.
type VerifyTOTPSecretResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *VerifyTOTPSecretResponse) Reset() {
	*x = VerifyTOTPSecretResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[35]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *VerifyTOTPSecretResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*VerifyTOTPSecretResponse) ProtoMessage() {}

func (x *VerifyTOTPSecretResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[35]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use VerifyTOTPSecretResponse.ProtoReflect.Descriptor instead.
func (*VerifyTOTPSecretResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{35}
}

// NoPassword is the registrant who will hold no password: an arrival who will
// sign in some other way, or not yet.
//
// It is an empty message rather than a bool because presence is the whole
// meaning. A bool arm has a false value that means nothing, and a schema with a
// value that means nothing is a schema a client can populate meaninglessly.
type NoPassword struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *NoPassword) Reset() {
	*x = NoPassword{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[36]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *NoPassword) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*NoPassword) ProtoMessage() {}

func (x *NoPassword) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[36]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use NoPassword.ProtoReflect.Descriptor instead.
func (*NoPassword) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{36}
}

// RegistrationInvitation names the invitation a registration answers.
//
// Naming one changes what the registration writes: the registrant is created
// and the invitation answered in their name on one transaction, and no account
// is minted, because somebody arriving on an invitation is joining one that
// already exists. An invitation that has expired, been withdrawn, already been
// answered or was presented with the wrong token takes the registration down
// with it.
type RegistrationInvitation struct {
	state        protoimpl.MessageState `protogen:"open.v1"`
	InvitationId string                 `protobuf:"bytes,1,opt,name=invitation_id,json=invitationID,proto3" json:"invitation_id,omitempty"`
	// token is the secret the invitation link carried. It arrives here because
	// this is where it arrives from -- on a link -- which is the one direction a
	// token travels on any schema in this module.
	Token string `protobuf:"bytes,2,opt,name=token,proto3" json:"token,omitempty"`
	// status_note is what the answer records about itself.
	StatusNote    string `protobuf:"bytes,3,opt,name=status_note,json=statusNote,proto3" json:"status_note,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *RegistrationInvitation) Reset() {
	*x = RegistrationInvitation{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[37]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *RegistrationInvitation) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RegistrationInvitation) ProtoMessage() {}

func (x *RegistrationInvitation) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[37]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RegistrationInvitation.ProtoReflect.Descriptor instead.
func (*RegistrationInvitation) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{37}
}

func (x *RegistrationInvitation) GetInvitationId() string {
	if x != nil {
		return x.InvitationId
	}
	return ""
}

func (x *RegistrationInvitation) GetToken() string {
	if x != nil {
		return x.Token
	}
	return ""
}

func (x *RegistrationInvitation) GetStatusNote() string {
	if x != nil {
		return x.StatusNote
	}
	return ""
}

// RegisterRequest is somebody arriving: who they are, what they will own, and
// how they will prove who they are.
type RegisterRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// user is the directory half, and is identity's own input message: there is
	// one directory and one shape for a registrant, and a second message for the
	// same four fields would be a second thing to keep in step.
	User *identitypb.UserRegistrationInput `protobuf:"bytes,1,opt,name=user,proto3" json:"user,omitempty"`
	// account is the first account the registrant owns. It is ignored by a
	// registration that answers an invitation.
	Account *identitypb.AccountCreationInput `protobuf:"bytes,2,opt,name=account,proto3" json:"account,omitempty"`
	// owner_roles are the roles the registrant holds in the account they own,
	// and are the consumer's own role names. A membership with none is a member
	// who may do nothing, so a registration that mints an account names at least
	// one. They are ignored by a registration that answers an invitation, which
	// takes its roles off the invitation.
	OwnerRoles []string `protobuf:"bytes,3,rep,name=owner_roles,json=ownerRoles,proto3" json:"owner_roles,omitempty"`
	// credential is how this person will prove who they are afterwards, and it is
	// required: a request naming neither arm is refused rather than read as
	// no_password. See the file documentation.
	//
	// Types that are valid to be assigned to Credential:
	//
	//	*RegisterRequest_Password
	//	*RegisterRequest_NoPassword
	Credential isRegisterRequest_Credential `protobuf_oneof:"credential"`
	// invitation names an invitation this registration answers, and is absent for
	// an ordinary one.
	Invitation *RegistrationInvitation `protobuf:"bytes,6,opt,name=invitation,proto3" json:"invitation,omitempty"`
	// agreements are the documents the registrant accepted in registering. They
	// are stamped on the user the registration writes, on the same transaction,
	// and AGREEMENT_UNSPECIFIED refuses the request. Whether any are required is
	// the consumer's, through signin.RegistrationPolicy.
	Agreements    []identitypb.Agreement `protobuf:"varint,7,rep,packed,name=agreements,proto3,enum=primandproper.platform.identity.v1.Agreement" json:"agreements,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *RegisterRequest) Reset() {
	*x = RegisterRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[38]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *RegisterRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RegisterRequest) ProtoMessage() {}

func (x *RegisterRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[38]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RegisterRequest.ProtoReflect.Descriptor instead.
func (*RegisterRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{38}
}

func (x *RegisterRequest) GetUser() *identitypb.UserRegistrationInput {
	if x != nil {
		return x.User
	}
	return nil
}

func (x *RegisterRequest) GetAccount() *identitypb.AccountCreationInput {
	if x != nil {
		return x.Account
	}
	return nil
}

func (x *RegisterRequest) GetOwnerRoles() []string {
	if x != nil {
		return x.OwnerRoles
	}
	return nil
}

func (x *RegisterRequest) GetCredential() isRegisterRequest_Credential {
	if x != nil {
		return x.Credential
	}
	return nil
}

func (x *RegisterRequest) GetPassword() string {
	if x != nil {
		if x, ok := x.Credential.(*RegisterRequest_Password); ok {
			return x.Password
		}
	}
	return ""
}

func (x *RegisterRequest) GetNoPassword() *NoPassword {
	if x != nil {
		if x, ok := x.Credential.(*RegisterRequest_NoPassword); ok {
			return x.NoPassword
		}
	}
	return nil
}

func (x *RegisterRequest) GetInvitation() *RegistrationInvitation {
	if x != nil {
		return x.Invitation
	}
	return nil
}

func (x *RegisterRequest) GetAgreements() []identitypb.Agreement {
	if x != nil {
		return x.Agreements
	}
	return nil
}

type isRegisterRequest_Credential interface {
	isRegisterRequest_Credential()
}

type RegisterRequest_Password struct {
	// password is the plaintext the registrant chose. It is hashed by the
	// service with the consumer's own authenticator and never stored, logged or
	// traced as it arrived, and it is the second reason this service requires
	// transport security.
	Password string `protobuf:"bytes,4,opt,name=password,proto3,oneof"`
}

type RegisterRequest_NoPassword struct {
	// no_password says they will hold none. The ways in for such a person are a
	// passkey ceremony or attaching a password later through AttachPassword,
	// which is what the verification mail's link is good for.
	NoPassword *NoPassword `protobuf:"bytes,5,opt,name=no_password,json=noPassword,proto3,oneof"`
}

func (*RegisterRequest_Password) isRegisterRequest_Credential() {}

func (*RegisterRequest_NoPassword) isRegisterRequest_Credential() {}

// TOTPEnrollment is a second-factor secret minted with a registration: the
// same pair RefreshTOTPSecretResponse carries, costing the same to hand around.
// It is unproven until VerifyTOTPSecret succeeds.
type TOTPEnrollment struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// secret is the base32 shared secret, for somebody typing it in.
	Secret string `protobuf:"bytes,1,opt,name=secret,proto3" json:"secret,omitempty"`
	// provisioning_uri is the otpauth:// URI that same secret encodes to, for a
	// client rendering a QR code.
	ProvisioningUri string `protobuf:"bytes,2,opt,name=provisioning_uri,json=provisioningUri,proto3" json:"provisioning_uri,omitempty"`
	unknownFields   protoimpl.UnknownFields
	sizeCache       protoimpl.SizeCache
}

func (x *TOTPEnrollment) Reset() {
	*x = TOTPEnrollment{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[39]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *TOTPEnrollment) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*TOTPEnrollment) ProtoMessage() {}

func (x *TOTPEnrollment) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[39]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use TOTPEnrollment.ProtoReflect.Descriptor instead.
func (*TOTPEnrollment) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{39}
}

func (x *TOTPEnrollment) GetSecret() string {
	if x != nil {
		return x.Secret
	}
	return ""
}

func (x *TOTPEnrollment) GetProvisioningUri() string {
	if x != nil {
		return x.ProvisioningUri
	}
	return ""
}

// Registered is what a registration produced.
//
// account is set by a registration that minted one and invitation by one that
// answered one, never both. There is no verification token here and there will
// not be -- see the file documentation.
type Registered struct {
	state      protoimpl.MessageState `protogen:"open.v1"`
	User       *identitypb.User       `protobuf:"bytes,1,opt,name=user,proto3" json:"user,omitempty"`
	Account    *identitypb.Account    `protobuf:"bytes,2,opt,name=account,proto3" json:"account,omitempty"`
	Membership *identitypb.Membership `protobuf:"bytes,3,opt,name=membership,proto3" json:"membership,omitempty"`
	Invitation *identitypb.Invitation `protobuf:"bytes,4,opt,name=invitation,proto3" json:"invitation,omitempty"`
	// totp_enrollment is the second factor minted with the registration, and is
	// absent unless the consumer's registration policy asked for one. It makes
	// this response, where it is set, one that must not be logged, cached or
	// rendered anywhere it will be read twice -- see RefreshTOTPSecretResponse.
	TotpEnrollment *TOTPEnrollment `protobuf:"bytes,5,opt,name=totp_enrollment,json=totpEnrollment,proto3" json:"totp_enrollment,omitempty"`
	unknownFields  protoimpl.UnknownFields
	sizeCache      protoimpl.SizeCache
}

func (x *Registered) Reset() {
	*x = Registered{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[40]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *Registered) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*Registered) ProtoMessage() {}

func (x *Registered) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[40]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use Registered.ProtoReflect.Descriptor instead.
func (*Registered) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{40}
}

func (x *Registered) GetUser() *identitypb.User {
	if x != nil {
		return x.User
	}
	return nil
}

func (x *Registered) GetAccount() *identitypb.Account {
	if x != nil {
		return x.Account
	}
	return nil
}

func (x *Registered) GetMembership() *identitypb.Membership {
	if x != nil {
		return x.Membership
	}
	return nil
}

func (x *Registered) GetInvitation() *identitypb.Invitation {
	if x != nil {
		return x.Invitation
	}
	return nil
}

func (x *Registered) GetTotpEnrollment() *TOTPEnrollment {
	if x != nil {
		return x.TotpEnrollment
	}
	return nil
}

type RegisterResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Registration  *Registered            `protobuf:"bytes,1,opt,name=registration,proto3" json:"registration,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *RegisterResponse) Reset() {
	*x = RegisterResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[41]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *RegisterResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RegisterResponse) ProtoMessage() {}

func (x *RegisterResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[41]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RegisterResponse.ProtoReflect.Descriptor instead.
func (*RegisterResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{41}
}

func (x *RegisterResponse) GetRegistration() *Registered {
	if x != nil {
		return x.Registration
	}
	return nil
}

// AttachPasswordRequest gives a password to somebody who holds none, answered
// with the verification link that was mailed to them.
//
// There is no field naming a user. Who this is about is read off the row the
// token named, which is the same rule ExchangeRefreshTokenRequest follows and is
// what keeps an anonymous RPC from being a way to name somebody else.
type AttachPasswordRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// token is the secret the verification link carried, and is the whole of this
	// request's authority: the subject cannot be signed in -- they hold no
	// password, and their standing admits no sign-in until they are verified --
	// so the mail sent to the address the account was registered with is the only
	// thing that reaches them.
	//
	// It is refused for an account that already holds a password, which is what
	// keeps the capability narrow. Somebody who has forgotten a password they
	// have goes through the password reset flow instead.
	Token string `protobuf:"bytes,1,opt,name=token,proto3" json:"token,omitempty"`
	// new_password is the password they chose. Whether it is acceptable is the
	// consumer's rule, applied in front of this call.
	NewPassword   string `protobuf:"bytes,2,opt,name=new_password,json=newPassword,proto3" json:"new_password,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *AttachPasswordRequest) Reset() {
	*x = AttachPasswordRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[42]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *AttachPasswordRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*AttachPasswordRequest) ProtoMessage() {}

func (x *AttachPasswordRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[42]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use AttachPasswordRequest.ProtoReflect.Descriptor instead.
func (*AttachPasswordRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{42}
}

func (x *AttachPasswordRequest) GetToken() string {
	if x != nil {
		return x.Token
	}
	return ""
}

func (x *AttachPasswordRequest) GetNewPassword() string {
	if x != nil {
		return x.NewPassword
	}
	return ""
}

// AttachPasswordResponse is empty -- see UpdatePasswordResponse.
type AttachPasswordResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *AttachPasswordResponse) Reset() {
	*x = AttachPasswordResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[43]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *AttachPasswordResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*AttachPasswordResponse) ProtoMessage() {}

func (x *AttachPasswordResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[43]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use AttachPasswordResponse.ProtoReflect.Descriptor instead.
func (*AttachPasswordResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{43}
}

// VerifyEmailAddressRequest answers a verification link.
//
// It proves the address, spends the link, and promotes the registrant out of
// the unverified standing that admits no sign-in -- which is the half that
// makes a registration usable. A suspended or terminated user who answers an
// outstanding link has their address stamped and their standing left alone: an
// operator's decision is not something an email overturns.
type VerifyEmailAddressRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// token is the secret the link carried. Expired, already answered, never
	// issued and simply wrong are one answer, for the reason a wrong password and
	// an unknown handle are one answer.
	Token         string `protobuf:"bytes,1,opt,name=token,proto3" json:"token,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *VerifyEmailAddressRequest) Reset() {
	*x = VerifyEmailAddressRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[44]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *VerifyEmailAddressRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*VerifyEmailAddressRequest) ProtoMessage() {}

func (x *VerifyEmailAddressRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[44]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use VerifyEmailAddressRequest.ProtoReflect.Descriptor instead.
func (*VerifyEmailAddressRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{44}
}

func (x *VerifyEmailAddressRequest) GetToken() string {
	if x != nil {
		return x.Token
	}
	return ""
}

// VerifyEmailAddressResponse is empty -- see UpdatePasswordResponse.
type VerifyEmailAddressResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *VerifyEmailAddressResponse) Reset() {
	*x = VerifyEmailAddressResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[45]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *VerifyEmailAddressResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*VerifyEmailAddressResponse) ProtoMessage() {}

func (x *VerifyEmailAddressResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[45]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use VerifyEmailAddressResponse.ProtoReflect.Descriptor instead.
func (*VerifyEmailAddressResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{45}
}

// RequestVerificationEmailRequest asks for a fresh link proving the calling
// user's address, mailed to it, and retires the link they were sent before.
//
// It requires a caller and names nobody, for SignOutEverywhereRequest's reason:
// the subject is whoever is calling, and a field naming somebody else would be a
// way to mail strangers from this deployment's domain. An address that is
// already proven is refused as EMAIL_ADDRESS_ALREADY_VERIFIED and keeps its
// proof, so asking again can never un-verify anybody.
//
// Rate limiting is the deployment's, in front of it: every request sends mail.
type RequestVerificationEmailRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *RequestVerificationEmailRequest) Reset() {
	*x = RequestVerificationEmailRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[46]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *RequestVerificationEmailRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RequestVerificationEmailRequest) ProtoMessage() {}

func (x *RequestVerificationEmailRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[46]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RequestVerificationEmailRequest.ProtoReflect.Descriptor instead.
func (*RequestVerificationEmailRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{46}
}

// RequestVerificationEmailResponse is empty. The link goes to the address and
// never back to the caller -- a response carrying it would let whoever holds a
// session prove an inbox they have never seen.
type RequestVerificationEmailResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *RequestVerificationEmailResponse) Reset() {
	*x = RequestVerificationEmailResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[47]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *RequestVerificationEmailResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RequestVerificationEmailResponse) ProtoMessage() {}

func (x *RequestVerificationEmailResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[47]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RequestVerificationEmailResponse.ProtoReflect.Descriptor instead.
func (*RequestVerificationEmailResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{47}
}

// RequestVerificationEmailByAddressRequest asks for a fresh verification link
// mailed to an address, from somebody who cannot sign in to ask: a registrant
// whose first link never arrived, told USER_UNVERIFIED at the password door.
//
// It is anonymous, and so it is answered identically whoever holds the address
// -- nobody, a registrant it mailed, somebody already proven, somebody whose
// standing admits no mail -- and held to the magic-link door's timing floor, for
// RequestMagicLinkRequest's reason. A client renders "if that address is waiting
// on a link, we sent another" and nothing that depends on the answer.
//
// Rate limiting is the deployment's, in front of it: anybody can send it.
type RequestVerificationEmailByAddressRequest struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	EmailAddress  string                 `protobuf:"bytes,1,opt,name=email_address,json=emailAddress,proto3" json:"email_address,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *RequestVerificationEmailByAddressRequest) Reset() {
	*x = RequestVerificationEmailByAddressRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[48]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *RequestVerificationEmailByAddressRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RequestVerificationEmailByAddressRequest) ProtoMessage() {}

func (x *RequestVerificationEmailByAddressRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[48]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RequestVerificationEmailByAddressRequest.ProtoReflect.Descriptor instead.
func (*RequestVerificationEmailByAddressRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{48}
}

func (x *RequestVerificationEmailByAddressRequest) GetEmailAddress() string {
	if x != nil {
		return x.EmailAddress
	}
	return ""
}

// RequestVerificationEmailByAddressResponse is empty, and the same for every
// address -- see RequestMagicLinkResponse.
type RequestVerificationEmailByAddressResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *RequestVerificationEmailByAddressResponse) Reset() {
	*x = RequestVerificationEmailByAddressResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[49]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *RequestVerificationEmailByAddressResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RequestVerificationEmailByAddressResponse) ProtoMessage() {}

func (x *RequestVerificationEmailByAddressResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[49]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RequestVerificationEmailByAddressResponse.ProtoReflect.Descriptor instead.
func (*RequestVerificationEmailByAddressResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{49}
}

// RequestMagicLinkRequest asks for a link that signs the holder of an address
// in, which is the half of passwordless that makes it a product decision rather
// than an unfinished account.
//
// There is no field naming a user, for AttachPasswordRequest's reason: the
// address is the whole of what this names, and who it belongs to is read off
// the directory rather than supplied by the caller.
type RequestMagicLinkRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// email_address is who to mail. It is folded the way the directory folds a
	// handle, so it reads the row the password door would have read.
	EmailAddress  string `protobuf:"bytes,1,opt,name=email_address,json=emailAddress,proto3" json:"email_address,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *RequestMagicLinkRequest) Reset() {
	*x = RequestMagicLinkRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[50]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *RequestMagicLinkRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RequestMagicLinkRequest) ProtoMessage() {}

func (x *RequestMagicLinkRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[50]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RequestMagicLinkRequest.ProtoReflect.Descriptor instead.
func (*RequestMagicLinkRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{50}
}

func (x *RequestMagicLinkRequest) GetEmailAddress() string {
	if x != nil {
		return x.EmailAddress
	}
	return ""
}

// RequestMagicLinkResponse is empty, and its emptiness is load-bearing rather
// than incidental.
//
// It carries no token, no "we sent it" flag, and no field that could differ
// between an address somebody holds and one nobody does. The service answers
// identically for both and pads its own timing so the two cannot be told apart
// by a stopwatch either; a response that said more would rebuild the account
// enumerator that padding exists to prevent.
//
// A consumer's own handler owes the same silence. Answering a known address with
// a 200 and an unknown one with a 404 puts the oracle back in their transport,
// where nothing in this module can reach it.
type RequestMagicLinkResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *RequestMagicLinkResponse) Reset() {
	*x = RequestMagicLinkResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[51]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *RequestMagicLinkResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RequestMagicLinkResponse) ProtoMessage() {}

func (x *RequestMagicLinkResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[51]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RequestMagicLinkResponse.ProtoReflect.Descriptor instead.
func (*RequestMagicLinkResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{51}
}

// RedeemMagicLinkRequest answers a sign-in link.
//
// It spends the link, proves the address it was mailed to, promotes a registrant
// who was waiting on exactly that, and issues a token -- the same token, family
// and refresh token a password sign-in issues. A suspended, banned or terminated
// user is refused and left where an operator put them.
//
// There is no administrative counterpart and there will not be one. A service
// role able to ban a user is the one credential a password alone does not answer
// for, and a mailed link is strictly weaker than a password.
type RedeemMagicLinkRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// token is the secret the link carried, and is the whole of this request's
	// authority. Expired, already followed, withdrawn and simply wrong are one
	// answer, for the reason a wrong password and an unknown handle are one
	// answer.
	Token string `protobuf:"bytes,1,opt,name=token,proto3" json:"token,omitempty"`
	// totp_code is the second-factor code, required from a user who holds a proven
	// secret and ignored from one who does not.
	//
	// It is not optional for an enrolled user and cannot be made optional: a door
	// that let a mailed link stand in for an enrolled second factor would be a way
	// around it reachable by whoever controls the inbox.
	TotpCode string `protobuf:"bytes,2,opt,name=totp_code,json=totpCode,proto3" json:"totp_code,omitempty"`
	// active_account_id is which account the minted token is for. Empty takes the
	// subject's default, which is what a link followed out of an inbox almost
	// always wants -- the request that asked for the mail named an address and
	// nothing else. An account the subject is not a live member of is refused by
	// the directory rather than honored.
	ActiveAccountId string `protobuf:"bytes,3,opt,name=active_account_id,json=activeAccountID,proto3" json:"active_account_id,omitempty"`
	unknownFields   protoimpl.UnknownFields
	sizeCache       protoimpl.SizeCache
}

func (x *RedeemMagicLinkRequest) Reset() {
	*x = RedeemMagicLinkRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[52]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *RedeemMagicLinkRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RedeemMagicLinkRequest) ProtoMessage() {}

func (x *RedeemMagicLinkRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[52]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RedeemMagicLinkRequest.ProtoReflect.Descriptor instead.
func (*RedeemMagicLinkRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{52}
}

func (x *RedeemMagicLinkRequest) GetToken() string {
	if x != nil {
		return x.Token
	}
	return ""
}

func (x *RedeemMagicLinkRequest) GetTotpCode() string {
	if x != nil {
		return x.TotpCode
	}
	return ""
}

func (x *RedeemMagicLinkRequest) GetActiveAccountId() string {
	if x != nil {
		return x.ActiveAccountId
	}
	return ""
}

// RedeemMagicLinkResponse carries what a completed sign-in carries, which is the
// same IssuedToken LoginForTokenResponse carries. The two doors differ in what
// was proven, not in what they produce.
type RedeemMagicLinkResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	Token         *IssuedToken           `protobuf:"bytes,1,opt,name=token,proto3" json:"token,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *RedeemMagicLinkResponse) Reset() {
	*x = RedeemMagicLinkResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[53]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *RedeemMagicLinkResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RedeemMagicLinkResponse) ProtoMessage() {}

func (x *RedeemMagicLinkResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[53]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RedeemMagicLinkResponse.ProtoReflect.Descriptor instead.
func (*RedeemMagicLinkResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{53}
}

func (x *RedeemMagicLinkResponse) GetToken() *IssuedToken {
	if x != nil {
		return x.Token
	}
	return nil
}

// RequestHandleReminderRequest asks for the handle the holder of an address signs
// in with to be mailed to them.
//
// There is no scope field, for RequestMagicLinkRequest's reason.
type RequestHandleReminderRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// email_address is who to mail. It is folded the way the directory folds a
	// handle, so it reads the row the password door would have read.
	EmailAddress  string `protobuf:"bytes,1,opt,name=email_address,json=emailAddress,proto3" json:"email_address,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *RequestHandleReminderRequest) Reset() {
	*x = RequestHandleReminderRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[54]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *RequestHandleReminderRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RequestHandleReminderRequest) ProtoMessage() {}

func (x *RequestHandleReminderRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[54]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RequestHandleReminderRequest.ProtoReflect.Descriptor instead.
func (*RequestHandleReminderRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{54}
}

func (x *RequestHandleReminderRequest) GetEmailAddress() string {
	if x != nil {
		return x.EmailAddress
	}
	return ""
}

// RequestHandleReminderResponse is empty, for RequestMagicLinkResponse's reason:
// no field here could differ between an address somebody holds and one nobody
// does, and a consumer's own handler owes the same silence.
type RequestHandleReminderResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *RequestHandleReminderResponse) Reset() {
	*x = RequestHandleReminderResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[55]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *RequestHandleReminderResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*RequestHandleReminderResponse) ProtoMessage() {}

func (x *RequestHandleReminderResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[55]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use RequestHandleReminderResponse.ProtoReflect.Descriptor instead.
func (*RequestHandleReminderResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{55}
}

// ListSignInsForUserRequest asks for the live logins of the user it names, most
// recently refreshed first: an operator's view of somebody else's "where you're
// signed in" screen.
type ListSignInsForUserRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// user_id is whose logins to list. Empty is INVALID_ARGUMENT.
	UserId string `protobuf:"bytes,1,opt,name=user_id,json=userID,proto3" json:"user_id,omitempty"`
	// limit is ListSignInsRequest.limit, read the same way.
	Limit         uint32 `protobuf:"varint,2,opt,name=limit,proto3" json:"limit,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListSignInsForUserRequest) Reset() {
	*x = ListSignInsForUserRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[56]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListSignInsForUserRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListSignInsForUserRequest) ProtoMessage() {}

func (x *ListSignInsForUserRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[56]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListSignInsForUserRequest.ProtoReflect.Descriptor instead.
func (*ListSignInsForUserRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{56}
}

func (x *ListSignInsForUserRequest) GetUserId() string {
	if x != nil {
		return x.UserId
	}
	return ""
}

func (x *ListSignInsForUserRequest) GetLimit() uint32 {
	if x != nil {
		return x.Limit
	}
	return 0
}

// ListSignInsForUserResponse carries ActiveSignIn exactly as ListSignIns does,
// except that current is false on every entry: the request was made through the
// operator's login, which is never one of these.
type ListSignInsForUserResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	SignIns       []*ActiveSignIn        `protobuf:"bytes,1,rep,name=sign_ins,json=signIns,proto3" json:"sign_ins,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *ListSignInsForUserResponse) Reset() {
	*x = ListSignInsForUserResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[57]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *ListSignInsForUserResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*ListSignInsForUserResponse) ProtoMessage() {}

func (x *ListSignInsForUserResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[57]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use ListSignInsForUserResponse.ProtoReflect.Descriptor instead.
func (*ListSignInsForUserResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{57}
}

func (x *ListSignInsForUserResponse) GetSignIns() []*ActiveSignIn {
	if x != nil {
		return x.SignIns
	}
	return nil
}

// EndSignInForUserRequest ends one of the named user's logins, named by its
// family.
//
// Both fields are the key, as the caller and the family are for EndSignIn: a
// family that is not user_id's ends nothing, so an operator who pasted the
// wrong identifier ends nobody's login rather than a stranger's. The answer
// does not say whether anything ended; ListSignInsForUser does.
type EndSignInForUserRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// user_id is whose login to end. Empty is INVALID_ARGUMENT.
	UserId string `protobuf:"bytes,1,opt,name=user_id,json=userID,proto3" json:"user_id,omitempty"`
	// family_id is the login, as ListSignInsForUser answered it. Empty is
	// INVALID_ARGUMENT.
	FamilyId      string `protobuf:"bytes,2,opt,name=family_id,json=familyID,proto3" json:"family_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *EndSignInForUserRequest) Reset() {
	*x = EndSignInForUserRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[58]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *EndSignInForUserRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*EndSignInForUserRequest) ProtoMessage() {}

func (x *EndSignInForUserRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[58]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use EndSignInForUserRequest.ProtoReflect.Descriptor instead.
func (*EndSignInForUserRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{58}
}

func (x *EndSignInForUserRequest) GetUserId() string {
	if x != nil {
		return x.UserId
	}
	return ""
}

func (x *EndSignInForUserRequest) GetFamilyId() string {
	if x != nil {
		return x.FamilyId
	}
	return ""
}

// EndSignInForUserResponse is empty -- see SignOutResponse.
type EndSignInForUserResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *EndSignInForUserResponse) Reset() {
	*x = EndSignInForUserResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[59]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *EndSignInForUserResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*EndSignInForUserResponse) ProtoMessage() {}

func (x *EndSignInForUserResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[59]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use EndSignInForUserResponse.ProtoReflect.Descriptor instead.
func (*EndSignInForUserResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{59}
}

// EndAllSignInsForUserRequest ends every login the named user holds: what an
// operator runs for an account they think is compromised, and
// SignOutEverywhere done to somebody rather than by them.
type EndAllSignInsForUserRequest struct {
	state protoimpl.MessageState `protogen:"open.v1"`
	// user_id is whose logins to end. Empty is INVALID_ARGUMENT.
	UserId        string `protobuf:"bytes,1,opt,name=user_id,json=userID,proto3" json:"user_id,omitempty"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *EndAllSignInsForUserRequest) Reset() {
	*x = EndAllSignInsForUserRequest{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[60]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *EndAllSignInsForUserRequest) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*EndAllSignInsForUserRequest) ProtoMessage() {}

func (x *EndAllSignInsForUserRequest) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[60]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use EndAllSignInsForUserRequest.ProtoReflect.Descriptor instead.
func (*EndAllSignInsForUserRequest) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{60}
}

func (x *EndAllSignInsForUserRequest) GetUserId() string {
	if x != nil {
		return x.UserId
	}
	return ""
}

// EndAllSignInsForUserResponse is empty -- see SignOutEverywhereResponse.
type EndAllSignInsForUserResponse struct {
	state         protoimpl.MessageState `protogen:"open.v1"`
	unknownFields protoimpl.UnknownFields
	sizeCache     protoimpl.SizeCache
}

func (x *EndAllSignInsForUserResponse) Reset() {
	*x = EndAllSignInsForUserResponse{}
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[61]
	ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
	ms.StoreMessageInfo(mi)
}

func (x *EndAllSignInsForUserResponse) String() string {
	return protoimpl.X.MessageStringOf(x)
}

func (*EndAllSignInsForUserResponse) ProtoMessage() {}

func (x *EndAllSignInsForUserResponse) ProtoReflect() protoreflect.Message {
	mi := &file_primandproper_platform_signin_v1_signin_proto_msgTypes[61]
	if x != nil {
		ms := protoimpl.X.MessageStateOf(protoimpl.Pointer(x))
		if ms.LoadMessageInfo() == nil {
			ms.StoreMessageInfo(mi)
		}
		return ms
	}
	return mi.MessageOf(x)
}

// Deprecated: Use EndAllSignInsForUserResponse.ProtoReflect.Descriptor instead.
func (*EndAllSignInsForUserResponse) Descriptor() ([]byte, []int) {
	return file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP(), []int{61}
}

var File_primandproper_platform_signin_v1_signin_proto protoreflect.FileDescriptor

const file_primandproper_platform_signin_v1_signin_proto_rawDesc = "" +
	"\n" +
	"-primandproper/platform/signin/v1/signin.proto\x12 primandproper.platform.signin.v1\x1a\x1fgoogle/protobuf/timestamp.proto\x1a1primandproper/platform/identity/v1/identity.proto\"\xba\x01\n" +
	"\vCredentials\x12\x1a\n" +
	"\busername\x18\x01 \x01(\tR\busername\x12#\n" +
	"\remail_address\x18\x02 \x01(\tR\femailAddress\x12\x1a\n" +
	"\bpassword\x18\x03 \x01(\tR\bpassword\x12\x1b\n" +
	"\ttotp_code\x18\x04 \x01(\tR\btotpCode\x12*\n" +
	"\x11active_account_id\x18\x05 \x01(\tR\x0factiveAccountIDR\x05scope\"\xeb\x02\n" +
	"\vIssuedToken\x12\x14\n" +
	"\x05token\x18\x01 \x01(\tR\x05token\x12\x19\n" +
	"\btoken_id\x18\x02 \x01(\tR\atokenID\x129\n" +
	"\n" +
	"expires_at\x18\x03 \x01(\v2\x1a.google.protobuf.TimestampR\texpiresAt\x12*\n" +
	"\x11active_account_id\x18\x04 \x01(\tR\x0factiveAccountID\x12&\n" +
	"\x0eadministrative\x18\x05 \x01(\bR\x0eadministrative\x12#\n" +
	"\rrefresh_token\x18\x06 \x01(\tR\frefreshToken\x12S\n" +
	"\x18refresh_token_expires_at\x18\a \x01(\v2\x1a.google.protobuf.TimestampR\x15refreshTokenExpiresAt\x12\x1b\n" +
	"\tfamily_id\x18\b \x01(\tR\bfamilyIDR\x05scope\"\xe1\x02\n" +
	"\n" +
	"AuthStatus\x12<\n" +
	"\x04user\x18\x01 \x01(\v2(.primandproper.platform.identity.v1.UserR\x04user\x12*\n" +
	"\x11active_account_id\x18\x02 \x01(\tR\x0factiveAccountID\x12\x1f\n" +
	"\vaccount_ids\x18\x03 \x03(\tR\n" +
	"accountIDs\x12!\n" +
	"\fhas_password\x18\x04 \x01(\bR\vhasPassword\x12.\n" +
	"\x13two_factor_enrolled\x18\x05 \x01(\bR\x11twoFactorEnrolled\x128\n" +
	"\x18requires_password_change\x18\x06 \x01(\bR\x16requiresPasswordChange\x124\n" +
	"\x16email_address_verified\x18\a \x01(\bR\x14emailAddressVerifiedR\x05scope\"n\n" +
	"\x14LoginForTokenRequest\x12O\n" +
	"\vcredentials\x18\x01 \x01(\v2-.primandproper.platform.signin.v1.CredentialsR\vcredentialsR\x05scope\"\\\n" +
	"\x15LoginForTokenResponse\x12C\n" +
	"\x05token\x18\x01 \x01(\v2-.primandproper.platform.signin.v1.IssuedTokenR\x05token\"s\n" +
	"\x19AdminLoginForTokenRequest\x12O\n" +
	"\vcredentials\x18\x01 \x01(\v2-.primandproper.platform.signin.v1.CredentialsR\vcredentialsR\x05scope\"a\n" +
	"\x1aAdminLoginForTokenResponse\x12C\n" +
	"\x05token\x18\x01 \x01(\v2-.primandproper.platform.signin.v1.IssuedTokenR\x05token\"I\n" +
	"\x1bExchangeRefreshTokenRequest\x12#\n" +
	"\rrefresh_token\x18\x01 \x01(\tR\frefreshTokenR\x05scope\"c\n" +
	"\x1cExchangeRefreshTokenResponse\x12C\n" +
	"\x05token\x18\x01 \x01(\v2-.primandproper.platform.signin.v1.IssuedTokenR\x05token\"a\n" +
	"\x14SwitchAccountRequest\x12#\n" +
	"\rrefresh_token\x18\x01 \x01(\tR\frefreshToken\x12\x1d\n" +
	"\n" +
	"account_id\x18\x02 \x01(\tR\taccountIDR\x05scope\"\\\n" +
	"\x15SwitchAccountResponse\x12C\n" +
	"\x05token\x18\x01 \x01(\v2-.primandproper.platform.signin.v1.IssuedTokenR\x05token\"<\n" +
	"\x0eSignOutRequest\x12#\n" +
	"\rrefresh_token\x18\x01 \x01(\tR\frefreshTokenR\x05scope\"\x11\n" +
	"\x0fSignOutResponse\"!\n" +
	"\x18SignOutEverywhereRequestR\x05scope\"\x1b\n" +
	"\x19SignOutEverywhereResponse\"\xc4\x04\n" +
	"\fActiveSignIn\x12\x1b\n" +
	"\tfamily_id\x18\x01 \x01(\tR\bfamilyID\x12<\n" +
	"\fsigned_in_at\x18\x02 \x01(\v2\x1a.google.protobuf.TimestampR\n" +
	"signedInAt\x12F\n" +
	"\x11last_refreshed_at\x18\x03 \x01(\v2\x1a.google.protobuf.TimestampR\x0flastRefreshedAt\x129\n" +
	"\n" +
	"expires_at\x18\x04 \x01(\v2\x1a.google.protobuf.TimestampR\texpiresAt\x12*\n" +
	"\x11active_account_id\x18\x05 \x01(\tR\x0factiveAccountID\x12&\n" +
	"\x0eadministrative\x18\x06 \x01(\bR\x0eadministrative\x12\x18\n" +
	"\acurrent\x18\a \x01(\bR\acurrent\x12\x19\n" +
	"\bactor_id\x18\b \x01(\tR\aactorID\x12'\n" +
	"\x0fcredential_kind\x18\t \x01(\tR\x0ecredentialKind\x12^\n" +
	"\n" +
	"attributes\x18\n" +
	" \x03(\v2>.primandproper.platform.signin.v1.ActiveSignIn.AttributesEntryR\n" +
	"attributes\x1a=\n" +
	"\x0fAttributesEntry\x12\x10\n" +
	"\x03key\x18\x01 \x01(\tR\x03key\x12\x14\n" +
	"\x05value\x18\x02 \x01(\tR\x05value:\x028\x01R\x05scope\"1\n" +
	"\x12ListSignInsRequest\x12\x14\n" +
	"\x05limit\x18\x01 \x01(\rR\x05limitR\x05scope\"`\n" +
	"\x13ListSignInsResponse\x12I\n" +
	"\bsign_ins\x18\x01 \x03(\v2..primandproper.platform.signin.v1.ActiveSignInR\asignIns\"6\n" +
	"\x10EndSignInRequest\x12\x1b\n" +
	"\tfamily_id\x18\x01 \x01(\tR\bfamilyIDR\x05scope\"\x13\n" +
	"\x11EndSignInResponse\"\x1f\n" +
	"\x16EndOtherSignInsRequestR\x05scope\"\x19\n" +
	"\x17EndOtherSignInsResponse\"\x1d\n" +
	"\x14GetAuthStatusRequestR\x05scope\"\x83\x01\n" +
	"\x15GetAuthStatusResponse\x12$\n" +
	"\rauthenticated\x18\x01 \x01(\bR\rauthenticated\x12D\n" +
	"\x06status\x18\x02 \x01(\v2,.primandproper.platform.signin.v1.AuthStatusR\x06status\"\x17\n" +
	"\x0eGetSelfRequestR\x05scope\"O\n" +
	"\x0fGetSelfResponse\x12<\n" +
	"\x04user\x18\x01 \x01(\v2(.primandproper.platform.identity.v1.UserR\x04user\"\x89\x01\n" +
	"\x15UpdatePasswordRequest\x12)\n" +
	"\x10current_password\x18\x01 \x01(\tR\x0fcurrentPassword\x12!\n" +
	"\fnew_password\x18\x02 \x01(\tR\vnewPassword\x12\x1b\n" +
	"\ttotp_code\x18\x03 \x01(\tR\btotpCodeR\x05scope\"\x18\n" +
	"\x16UpdatePasswordResponse\"\x96\x01\n" +
	"\x19UpdateEmailAddressRequest\x12)\n" +
	"\x10current_password\x18\x01 \x01(\tR\x0fcurrentPassword\x12\x1b\n" +
	"\ttotp_code\x18\x02 \x01(\tR\btotpCode\x12*\n" +
	"\x11new_email_address\x18\x03 \x01(\tR\x0fnewEmailAddressR\x05scope\"Z\n" +
	"\x1aUpdateEmailAddressResponse\x12<\n" +
	"\x04user\x18\x01 \x01(\v2(.primandproper.platform.identity.v1.UserR\x04user\"\x89\x01\n" +
	"\x15UpdateUsernameRequest\x12)\n" +
	"\x10current_password\x18\x01 \x01(\tR\x0fcurrentPassword\x12\x1b\n" +
	"\ttotp_code\x18\x02 \x01(\tR\btotpCode\x12!\n" +
	"\fnew_username\x18\x03 \x01(\tR\vnewUsernameR\x05scope\"V\n" +
	"\x16UpdateUsernameResponse\x12<\n" +
	"\x04user\x18\x01 \x01(\v2(.primandproper.platform.identity.v1.UserR\x04user\"i\n" +
	"\x18RefreshTOTPSecretRequest\x12)\n" +
	"\x10current_password\x18\x01 \x01(\tR\x0fcurrentPassword\x12\x1b\n" +
	"\ttotp_code\x18\x02 \x01(\tR\btotpCodeR\x05scope\"^\n" +
	"\x19RefreshTOTPSecretResponse\x12\x16\n" +
	"\x06secret\x18\x01 \x01(\tR\x06secret\x12)\n" +
	"\x10provisioning_uri\x18\x02 \x01(\tR\x0fprovisioningUri\"=\n" +
	"\x17VerifyTOTPSecretRequest\x12\x1b\n" +
	"\ttotp_code\x18\x01 \x01(\tR\btotpCodeR\x05scope\"\x1a\n" +
	"\x18VerifyTOTPSecretResponse\"\x13\n" +
	"\n" +
	"NoPasswordR\x05scope\"{\n" +
	"\x16RegistrationInvitation\x12#\n" +
	"\rinvitation_id\x18\x01 \x01(\tR\finvitationID\x12\x14\n" +
	"\x05token\x18\x02 \x01(\tR\x05token\x12\x1f\n" +
	"\vstatus_note\x18\x03 \x01(\tR\n" +
	"statusNoteR\x05scope\"\x82\x04\n" +
	"\x0fRegisterRequest\x12M\n" +
	"\x04user\x18\x01 \x01(\v29.primandproper.platform.identity.v1.UserRegistrationInputR\x04user\x12R\n" +
	"\aaccount\x18\x02 \x01(\v28.primandproper.platform.identity.v1.AccountCreationInputR\aaccount\x12\x1f\n" +
	"\vowner_roles\x18\x03 \x03(\tR\n" +
	"ownerRoles\x12\x1c\n" +
	"\bpassword\x18\x04 \x01(\tH\x00R\bpassword\x12O\n" +
	"\vno_password\x18\x05 \x01(\v2,.primandproper.platform.signin.v1.NoPasswordH\x00R\n" +
	"noPassword\x12X\n" +
	"\n" +
	"invitation\x18\x06 \x01(\v28.primandproper.platform.signin.v1.RegistrationInvitationR\n" +
	"invitation\x12M\n" +
	"\n" +
	"agreements\x18\a \x03(\x0e2-.primandproper.platform.identity.v1.AgreementR\n" +
	"agreementsB\f\n" +
	"\n" +
	"credentialR\x05scope\"Z\n" +
	"\x0eTOTPEnrollment\x12\x16\n" +
	"\x06secret\x18\x01 \x01(\tR\x06secret\x12)\n" +
	"\x10provisioning_uri\x18\x02 \x01(\tR\x0fprovisioningUriR\x05scope\"\x93\x03\n" +
	"\n" +
	"Registered\x12<\n" +
	"\x04user\x18\x01 \x01(\v2(.primandproper.platform.identity.v1.UserR\x04user\x12E\n" +
	"\aaccount\x18\x02 \x01(\v2+.primandproper.platform.identity.v1.AccountR\aaccount\x12N\n" +
	"\n" +
	"membership\x18\x03 \x01(\v2..primandproper.platform.identity.v1.MembershipR\n" +
	"membership\x12N\n" +
	"\n" +
	"invitation\x18\x04 \x01(\v2..primandproper.platform.identity.v1.InvitationR\n" +
	"invitation\x12Y\n" +
	"\x0ftotp_enrollment\x18\x05 \x01(\v20.primandproper.platform.signin.v1.TOTPEnrollmentR\x0etotpEnrollmentR\x05scope\"d\n" +
	"\x10RegisterResponse\x12P\n" +
	"\fregistration\x18\x01 \x01(\v2,.primandproper.platform.signin.v1.RegisteredR\fregistration\"W\n" +
	"\x15AttachPasswordRequest\x12\x14\n" +
	"\x05token\x18\x01 \x01(\tR\x05token\x12!\n" +
	"\fnew_password\x18\x02 \x01(\tR\vnewPasswordR\x05scope\"\x18\n" +
	"\x16AttachPasswordResponse\"8\n" +
	"\x19VerifyEmailAddressRequest\x12\x14\n" +
	"\x05token\x18\x01 \x01(\tR\x05tokenR\x05scope\"\x1c\n" +
	"\x1aVerifyEmailAddressResponse\"(\n" +
	"\x1fRequestVerificationEmailRequestR\x05scope\"\"\n" +
	" RequestVerificationEmailResponse\"V\n" +
	"(RequestVerificationEmailByAddressRequest\x12#\n" +
	"\remail_address\x18\x01 \x01(\tR\femailAddressR\x05scope\"+\n" +
	")RequestVerificationEmailByAddressResponse\"E\n" +
	"\x17RequestMagicLinkRequest\x12#\n" +
	"\remail_address\x18\x01 \x01(\tR\femailAddressR\x05scope\"\x1a\n" +
	"\x18RequestMagicLinkResponse\"~\n" +
	"\x16RedeemMagicLinkRequest\x12\x14\n" +
	"\x05token\x18\x01 \x01(\tR\x05token\x12\x1b\n" +
	"\ttotp_code\x18\x02 \x01(\tR\btotpCode\x12*\n" +
	"\x11active_account_id\x18\x03 \x01(\tR\x0factiveAccountIDR\x05scope\"^\n" +
	"\x17RedeemMagicLinkResponse\x12C\n" +
	"\x05token\x18\x01 \x01(\v2-.primandproper.platform.signin.v1.IssuedTokenR\x05token\"J\n" +
	"\x1cRequestHandleReminderRequest\x12#\n" +
	"\remail_address\x18\x01 \x01(\tR\femailAddressR\x05scope\"\x1f\n" +
	"\x1dRequestHandleReminderResponse\"Q\n" +
	"\x19ListSignInsForUserRequest\x12\x17\n" +
	"\auser_id\x18\x01 \x01(\tR\x06userID\x12\x14\n" +
	"\x05limit\x18\x02 \x01(\rR\x05limitR\x05scope\"g\n" +
	"\x1aListSignInsForUserResponse\x12I\n" +
	"\bsign_ins\x18\x01 \x03(\v2..primandproper.platform.signin.v1.ActiveSignInR\asignIns\"V\n" +
	"\x17EndSignInForUserRequest\x12\x17\n" +
	"\auser_id\x18\x01 \x01(\tR\x06userID\x12\x1b\n" +
	"\tfamily_id\x18\x02 \x01(\tR\bfamilyIDR\x05scope\"\x1a\n" +
	"\x18EndSignInForUserResponse\"=\n" +
	"\x1bEndAllSignInsForUserRequest\x12\x17\n" +
	"\auser_id\x18\x01 \x01(\tR\x06userIDR\x05scope\"\x1e\n" +
	"\x1cEndAllSignInsForUserResponse2\x83\x1a\n" +
	"\rSignInService\x12q\n" +
	"\bRegister\x121.primandproper.platform.signin.v1.RegisterRequest\x1a2.primandproper.platform.signin.v1.RegisterResponse\x12\x83\x01\n" +
	"\x0eAttachPassword\x127.primandproper.platform.signin.v1.AttachPasswordRequest\x1a8.primandproper.platform.signin.v1.AttachPasswordResponse\x12\x8f\x01\n" +
	"\x12VerifyEmailAddress\x12;.primandproper.platform.signin.v1.VerifyEmailAddressRequest\x1a<.primandproper.platform.signin.v1.VerifyEmailAddressResponse\x12\xa1\x01\n" +
	"\x18RequestVerificationEmail\x12A.primandproper.platform.signin.v1.RequestVerificationEmailRequest\x1aB.primandproper.platform.signin.v1.RequestVerificationEmailResponse\x12\xbc\x01\n" +
	"!RequestVerificationEmailByAddress\x12J.primandproper.platform.signin.v1.RequestVerificationEmailByAddressRequest\x1aK.primandproper.platform.signin.v1.RequestVerificationEmailByAddressResponse\x12\x89\x01\n" +
	"\x10RequestMagicLink\x129.primandproper.platform.signin.v1.RequestMagicLinkRequest\x1a:.primandproper.platform.signin.v1.RequestMagicLinkResponse\x12\x86\x01\n" +
	"\x0fRedeemMagicLink\x128.primandproper.platform.signin.v1.RedeemMagicLinkRequest\x1a9.primandproper.platform.signin.v1.RedeemMagicLinkResponse\x12\x98\x01\n" +
	"\x15RequestHandleReminder\x12>.primandproper.platform.signin.v1.RequestHandleReminderRequest\x1a?.primandproper.platform.signin.v1.RequestHandleReminderResponse\x12\x80\x01\n" +
	"\rLoginForToken\x126.primandproper.platform.signin.v1.LoginForTokenRequest\x1a7.primandproper.platform.signin.v1.LoginForTokenResponse\x12\x8f\x01\n" +
	"\x12AdminLoginForToken\x12;.primandproper.platform.signin.v1.AdminLoginForTokenRequest\x1a<.primandproper.platform.signin.v1.AdminLoginForTokenResponse\x12\x95\x01\n" +
	"\x14ExchangeRefreshToken\x12=.primandproper.platform.signin.v1.ExchangeRefreshTokenRequest\x1a>.primandproper.platform.signin.v1.ExchangeRefreshTokenResponse\x12\x80\x01\n" +
	"\rSwitchAccount\x126.primandproper.platform.signin.v1.SwitchAccountRequest\x1a7.primandproper.platform.signin.v1.SwitchAccountResponse\x12n\n" +
	"\aSignOut\x120.primandproper.platform.signin.v1.SignOutRequest\x1a1.primandproper.platform.signin.v1.SignOutResponse\x12\x8c\x01\n" +
	"\x11SignOutEverywhere\x12:.primandproper.platform.signin.v1.SignOutEverywhereRequest\x1a;.primandproper.platform.signin.v1.SignOutEverywhereResponse\x12z\n" +
	"\vListSignIns\x124.primandproper.platform.signin.v1.ListSignInsRequest\x1a5.primandproper.platform.signin.v1.ListSignInsResponse\x12t\n" +
	"\tEndSignIn\x122.primandproper.platform.signin.v1.EndSignInRequest\x1a3.primandproper.platform.signin.v1.EndSignInResponse\x12\x86\x01\n" +
	"\x0fEndOtherSignIns\x128.primandproper.platform.signin.v1.EndOtherSignInsRequest\x1a9.primandproper.platform.signin.v1.EndOtherSignInsResponse\x12\x80\x01\n" +
	"\rGetAuthStatus\x126.primandproper.platform.signin.v1.GetAuthStatusRequest\x1a7.primandproper.platform.signin.v1.GetAuthStatusResponse\x12n\n" +
	"\aGetSelf\x120.primandproper.platform.signin.v1.GetSelfRequest\x1a1.primandproper.platform.signin.v1.GetSelfResponse\x12\x83\x01\n" +
	"\x0eUpdatePassword\x127.primandproper.platform.signin.v1.UpdatePasswordRequest\x1a8.primandproper.platform.signin.v1.UpdatePasswordResponse\x12\x8c\x01\n" +
	"\x11RefreshTOTPSecret\x12:.primandproper.platform.signin.v1.RefreshTOTPSecretRequest\x1a;.primandproper.platform.signin.v1.RefreshTOTPSecretResponse\x12\x89\x01\n" +
	"\x10VerifyTOTPSecret\x129.primandproper.platform.signin.v1.VerifyTOTPSecretRequest\x1a:.primandproper.platform.signin.v1.VerifyTOTPSecretResponse\x12\x8f\x01\n" +
	"\x12UpdateEmailAddress\x12;.primandproper.platform.signin.v1.UpdateEmailAddressRequest\x1a<.primandproper.platform.signin.v1.UpdateEmailAddressResponse\x12\x83\x01\n" +
	"\x0eUpdateUsername\x127.primandproper.platform.signin.v1.UpdateUsernameRequest\x1a8.primandproper.platform.signin.v1.UpdateUsernameResponse2\xd3\x03\n" +
	"\x1bSignInAdministrationService\x12\x8f\x01\n" +
	"\x12ListSignInsForUser\x12;.primandproper.platform.signin.v1.ListSignInsForUserRequest\x1a<.primandproper.platform.signin.v1.ListSignInsForUserResponse\x12\x89\x01\n" +
	"\x10EndSignInForUser\x129.primandproper.platform.signin.v1.EndSignInForUserRequest\x1a:.primandproper.platform.signin.v1.EndSignInForUserResponse\x12\x95\x01\n" +
	"\x14EndAllSignInsForUser\x12=.primandproper.platform.signin.v1.EndAllSignInsForUserRequest\x1a>.primandproper.platform.signin.v1.EndAllSignInsForUserResponseBRZPgithub.com/primandproper/platform-go/v14/authentication/signin/signinpb;signinpbb\x06proto3"

var (
	file_primandproper_platform_signin_v1_signin_proto_rawDescOnce sync.Once
	file_primandproper_platform_signin_v1_signin_proto_rawDescData []byte
)

func file_primandproper_platform_signin_v1_signin_proto_rawDescGZIP() []byte {
	file_primandproper_platform_signin_v1_signin_proto_rawDescOnce.Do(func() {
		file_primandproper_platform_signin_v1_signin_proto_rawDescData = protoimpl.X.CompressGZIP(unsafe.Slice(unsafe.StringData(file_primandproper_platform_signin_v1_signin_proto_rawDesc), len(file_primandproper_platform_signin_v1_signin_proto_rawDesc)))
	})
	return file_primandproper_platform_signin_v1_signin_proto_rawDescData
}

var file_primandproper_platform_signin_v1_signin_proto_msgTypes = make([]protoimpl.MessageInfo, 63)
var file_primandproper_platform_signin_v1_signin_proto_goTypes = []any{
	(*Credentials)(nil),                               // 0: primandproper.platform.signin.v1.Credentials
	(*IssuedToken)(nil),                               // 1: primandproper.platform.signin.v1.IssuedToken
	(*AuthStatus)(nil),                                // 2: primandproper.platform.signin.v1.AuthStatus
	(*LoginForTokenRequest)(nil),                      // 3: primandproper.platform.signin.v1.LoginForTokenRequest
	(*LoginForTokenResponse)(nil),                     // 4: primandproper.platform.signin.v1.LoginForTokenResponse
	(*AdminLoginForTokenRequest)(nil),                 // 5: primandproper.platform.signin.v1.AdminLoginForTokenRequest
	(*AdminLoginForTokenResponse)(nil),                // 6: primandproper.platform.signin.v1.AdminLoginForTokenResponse
	(*ExchangeRefreshTokenRequest)(nil),               // 7: primandproper.platform.signin.v1.ExchangeRefreshTokenRequest
	(*ExchangeRefreshTokenResponse)(nil),              // 8: primandproper.platform.signin.v1.ExchangeRefreshTokenResponse
	(*SwitchAccountRequest)(nil),                      // 9: primandproper.platform.signin.v1.SwitchAccountRequest
	(*SwitchAccountResponse)(nil),                     // 10: primandproper.platform.signin.v1.SwitchAccountResponse
	(*SignOutRequest)(nil),                            // 11: primandproper.platform.signin.v1.SignOutRequest
	(*SignOutResponse)(nil),                           // 12: primandproper.platform.signin.v1.SignOutResponse
	(*SignOutEverywhereRequest)(nil),                  // 13: primandproper.platform.signin.v1.SignOutEverywhereRequest
	(*SignOutEverywhereResponse)(nil),                 // 14: primandproper.platform.signin.v1.SignOutEverywhereResponse
	(*ActiveSignIn)(nil),                              // 15: primandproper.platform.signin.v1.ActiveSignIn
	(*ListSignInsRequest)(nil),                        // 16: primandproper.platform.signin.v1.ListSignInsRequest
	(*ListSignInsResponse)(nil),                       // 17: primandproper.platform.signin.v1.ListSignInsResponse
	(*EndSignInRequest)(nil),                          // 18: primandproper.platform.signin.v1.EndSignInRequest
	(*EndSignInResponse)(nil),                         // 19: primandproper.platform.signin.v1.EndSignInResponse
	(*EndOtherSignInsRequest)(nil),                    // 20: primandproper.platform.signin.v1.EndOtherSignInsRequest
	(*EndOtherSignInsResponse)(nil),                   // 21: primandproper.platform.signin.v1.EndOtherSignInsResponse
	(*GetAuthStatusRequest)(nil),                      // 22: primandproper.platform.signin.v1.GetAuthStatusRequest
	(*GetAuthStatusResponse)(nil),                     // 23: primandproper.platform.signin.v1.GetAuthStatusResponse
	(*GetSelfRequest)(nil),                            // 24: primandproper.platform.signin.v1.GetSelfRequest
	(*GetSelfResponse)(nil),                           // 25: primandproper.platform.signin.v1.GetSelfResponse
	(*UpdatePasswordRequest)(nil),                     // 26: primandproper.platform.signin.v1.UpdatePasswordRequest
	(*UpdatePasswordResponse)(nil),                    // 27: primandproper.platform.signin.v1.UpdatePasswordResponse
	(*UpdateEmailAddressRequest)(nil),                 // 28: primandproper.platform.signin.v1.UpdateEmailAddressRequest
	(*UpdateEmailAddressResponse)(nil),                // 29: primandproper.platform.signin.v1.UpdateEmailAddressResponse
	(*UpdateUsernameRequest)(nil),                     // 30: primandproper.platform.signin.v1.UpdateUsernameRequest
	(*UpdateUsernameResponse)(nil),                    // 31: primandproper.platform.signin.v1.UpdateUsernameResponse
	(*RefreshTOTPSecretRequest)(nil),                  // 32: primandproper.platform.signin.v1.RefreshTOTPSecretRequest
	(*RefreshTOTPSecretResponse)(nil),                 // 33: primandproper.platform.signin.v1.RefreshTOTPSecretResponse
	(*VerifyTOTPSecretRequest)(nil),                   // 34: primandproper.platform.signin.v1.VerifyTOTPSecretRequest
	(*VerifyTOTPSecretResponse)(nil),                  // 35: primandproper.platform.signin.v1.VerifyTOTPSecretResponse
	(*NoPassword)(nil),                                // 36: primandproper.platform.signin.v1.NoPassword
	(*RegistrationInvitation)(nil),                    // 37: primandproper.platform.signin.v1.RegistrationInvitation
	(*RegisterRequest)(nil),                           // 38: primandproper.platform.signin.v1.RegisterRequest
	(*TOTPEnrollment)(nil),                            // 39: primandproper.platform.signin.v1.TOTPEnrollment
	(*Registered)(nil),                                // 40: primandproper.platform.signin.v1.Registered
	(*RegisterResponse)(nil),                          // 41: primandproper.platform.signin.v1.RegisterResponse
	(*AttachPasswordRequest)(nil),                     // 42: primandproper.platform.signin.v1.AttachPasswordRequest
	(*AttachPasswordResponse)(nil),                    // 43: primandproper.platform.signin.v1.AttachPasswordResponse
	(*VerifyEmailAddressRequest)(nil),                 // 44: primandproper.platform.signin.v1.VerifyEmailAddressRequest
	(*VerifyEmailAddressResponse)(nil),                // 45: primandproper.platform.signin.v1.VerifyEmailAddressResponse
	(*RequestVerificationEmailRequest)(nil),           // 46: primandproper.platform.signin.v1.RequestVerificationEmailRequest
	(*RequestVerificationEmailResponse)(nil),          // 47: primandproper.platform.signin.v1.RequestVerificationEmailResponse
	(*RequestVerificationEmailByAddressRequest)(nil),  // 48: primandproper.platform.signin.v1.RequestVerificationEmailByAddressRequest
	(*RequestVerificationEmailByAddressResponse)(nil), // 49: primandproper.platform.signin.v1.RequestVerificationEmailByAddressResponse
	(*RequestMagicLinkRequest)(nil),                   // 50: primandproper.platform.signin.v1.RequestMagicLinkRequest
	(*RequestMagicLinkResponse)(nil),                  // 51: primandproper.platform.signin.v1.RequestMagicLinkResponse
	(*RedeemMagicLinkRequest)(nil),                    // 52: primandproper.platform.signin.v1.RedeemMagicLinkRequest
	(*RedeemMagicLinkResponse)(nil),                   // 53: primandproper.platform.signin.v1.RedeemMagicLinkResponse
	(*RequestHandleReminderRequest)(nil),              // 54: primandproper.platform.signin.v1.RequestHandleReminderRequest
	(*RequestHandleReminderResponse)(nil),             // 55: primandproper.platform.signin.v1.RequestHandleReminderResponse
	(*ListSignInsForUserRequest)(nil),                 // 56: primandproper.platform.signin.v1.ListSignInsForUserRequest
	(*ListSignInsForUserResponse)(nil),                // 57: primandproper.platform.signin.v1.ListSignInsForUserResponse
	(*EndSignInForUserRequest)(nil),                   // 58: primandproper.platform.signin.v1.EndSignInForUserRequest
	(*EndSignInForUserResponse)(nil),                  // 59: primandproper.platform.signin.v1.EndSignInForUserResponse
	(*EndAllSignInsForUserRequest)(nil),               // 60: primandproper.platform.signin.v1.EndAllSignInsForUserRequest
	(*EndAllSignInsForUserResponse)(nil),              // 61: primandproper.platform.signin.v1.EndAllSignInsForUserResponse
	nil,                                               // 62: primandproper.platform.signin.v1.ActiveSignIn.AttributesEntry
	(*timestamppb.Timestamp)(nil),                     // 63: google.protobuf.Timestamp
	(*identitypb.User)(nil),                           // 64: primandproper.platform.identity.v1.User
	(*identitypb.UserRegistrationInput)(nil),          // 65: primandproper.platform.identity.v1.UserRegistrationInput
	(*identitypb.AccountCreationInput)(nil),           // 66: primandproper.platform.identity.v1.AccountCreationInput
	(identitypb.Agreement)(0),                         // 67: primandproper.platform.identity.v1.Agreement
	(*identitypb.Account)(nil),                        // 68: primandproper.platform.identity.v1.Account
	(*identitypb.Membership)(nil),                     // 69: primandproper.platform.identity.v1.Membership
	(*identitypb.Invitation)(nil),                     // 70: primandproper.platform.identity.v1.Invitation
}
var file_primandproper_platform_signin_v1_signin_proto_depIdxs = []int32{
	63, // 0: primandproper.platform.signin.v1.IssuedToken.expires_at:type_name -> google.protobuf.Timestamp
	63, // 1: primandproper.platform.signin.v1.IssuedToken.refresh_token_expires_at:type_name -> google.protobuf.Timestamp
	64, // 2: primandproper.platform.signin.v1.AuthStatus.user:type_name -> primandproper.platform.identity.v1.User
	0,  // 3: primandproper.platform.signin.v1.LoginForTokenRequest.credentials:type_name -> primandproper.platform.signin.v1.Credentials
	1,  // 4: primandproper.platform.signin.v1.LoginForTokenResponse.token:type_name -> primandproper.platform.signin.v1.IssuedToken
	0,  // 5: primandproper.platform.signin.v1.AdminLoginForTokenRequest.credentials:type_name -> primandproper.platform.signin.v1.Credentials
	1,  // 6: primandproper.platform.signin.v1.AdminLoginForTokenResponse.token:type_name -> primandproper.platform.signin.v1.IssuedToken
	1,  // 7: primandproper.platform.signin.v1.ExchangeRefreshTokenResponse.token:type_name -> primandproper.platform.signin.v1.IssuedToken
	1,  // 8: primandproper.platform.signin.v1.SwitchAccountResponse.token:type_name -> primandproper.platform.signin.v1.IssuedToken
	63, // 9: primandproper.platform.signin.v1.ActiveSignIn.signed_in_at:type_name -> google.protobuf.Timestamp
	63, // 10: primandproper.platform.signin.v1.ActiveSignIn.last_refreshed_at:type_name -> google.protobuf.Timestamp
	63, // 11: primandproper.platform.signin.v1.ActiveSignIn.expires_at:type_name -> google.protobuf.Timestamp
	62, // 12: primandproper.platform.signin.v1.ActiveSignIn.attributes:type_name -> primandproper.platform.signin.v1.ActiveSignIn.AttributesEntry
	15, // 13: primandproper.platform.signin.v1.ListSignInsResponse.sign_ins:type_name -> primandproper.platform.signin.v1.ActiveSignIn
	2,  // 14: primandproper.platform.signin.v1.GetAuthStatusResponse.status:type_name -> primandproper.platform.signin.v1.AuthStatus
	64, // 15: primandproper.platform.signin.v1.GetSelfResponse.user:type_name -> primandproper.platform.identity.v1.User
	64, // 16: primandproper.platform.signin.v1.UpdateEmailAddressResponse.user:type_name -> primandproper.platform.identity.v1.User
	64, // 17: primandproper.platform.signin.v1.UpdateUsernameResponse.user:type_name -> primandproper.platform.identity.v1.User
	65, // 18: primandproper.platform.signin.v1.RegisterRequest.user:type_name -> primandproper.platform.identity.v1.UserRegistrationInput
	66, // 19: primandproper.platform.signin.v1.RegisterRequest.account:type_name -> primandproper.platform.identity.v1.AccountCreationInput
	36, // 20: primandproper.platform.signin.v1.RegisterRequest.no_password:type_name -> primandproper.platform.signin.v1.NoPassword
	37, // 21: primandproper.platform.signin.v1.RegisterRequest.invitation:type_name -> primandproper.platform.signin.v1.RegistrationInvitation
	67, // 22: primandproper.platform.signin.v1.RegisterRequest.agreements:type_name -> primandproper.platform.identity.v1.Agreement
	64, // 23: primandproper.platform.signin.v1.Registered.user:type_name -> primandproper.platform.identity.v1.User
	68, // 24: primandproper.platform.signin.v1.Registered.account:type_name -> primandproper.platform.identity.v1.Account
	69, // 25: primandproper.platform.signin.v1.Registered.membership:type_name -> primandproper.platform.identity.v1.Membership
	70, // 26: primandproper.platform.signin.v1.Registered.invitation:type_name -> primandproper.platform.identity.v1.Invitation
	39, // 27: primandproper.platform.signin.v1.Registered.totp_enrollment:type_name -> primandproper.platform.signin.v1.TOTPEnrollment
	40, // 28: primandproper.platform.signin.v1.RegisterResponse.registration:type_name -> primandproper.platform.signin.v1.Registered
	1,  // 29: primandproper.platform.signin.v1.RedeemMagicLinkResponse.token:type_name -> primandproper.platform.signin.v1.IssuedToken
	15, // 30: primandproper.platform.signin.v1.ListSignInsForUserResponse.sign_ins:type_name -> primandproper.platform.signin.v1.ActiveSignIn
	38, // 31: primandproper.platform.signin.v1.SignInService.Register:input_type -> primandproper.platform.signin.v1.RegisterRequest
	42, // 32: primandproper.platform.signin.v1.SignInService.AttachPassword:input_type -> primandproper.platform.signin.v1.AttachPasswordRequest
	44, // 33: primandproper.platform.signin.v1.SignInService.VerifyEmailAddress:input_type -> primandproper.platform.signin.v1.VerifyEmailAddressRequest
	46, // 34: primandproper.platform.signin.v1.SignInService.RequestVerificationEmail:input_type -> primandproper.platform.signin.v1.RequestVerificationEmailRequest
	48, // 35: primandproper.platform.signin.v1.SignInService.RequestVerificationEmailByAddress:input_type -> primandproper.platform.signin.v1.RequestVerificationEmailByAddressRequest
	50, // 36: primandproper.platform.signin.v1.SignInService.RequestMagicLink:input_type -> primandproper.platform.signin.v1.RequestMagicLinkRequest
	52, // 37: primandproper.platform.signin.v1.SignInService.RedeemMagicLink:input_type -> primandproper.platform.signin.v1.RedeemMagicLinkRequest
	54, // 38: primandproper.platform.signin.v1.SignInService.RequestHandleReminder:input_type -> primandproper.platform.signin.v1.RequestHandleReminderRequest
	3,  // 39: primandproper.platform.signin.v1.SignInService.LoginForToken:input_type -> primandproper.platform.signin.v1.LoginForTokenRequest
	5,  // 40: primandproper.platform.signin.v1.SignInService.AdminLoginForToken:input_type -> primandproper.platform.signin.v1.AdminLoginForTokenRequest
	7,  // 41: primandproper.platform.signin.v1.SignInService.ExchangeRefreshToken:input_type -> primandproper.platform.signin.v1.ExchangeRefreshTokenRequest
	9,  // 42: primandproper.platform.signin.v1.SignInService.SwitchAccount:input_type -> primandproper.platform.signin.v1.SwitchAccountRequest
	11, // 43: primandproper.platform.signin.v1.SignInService.SignOut:input_type -> primandproper.platform.signin.v1.SignOutRequest
	13, // 44: primandproper.platform.signin.v1.SignInService.SignOutEverywhere:input_type -> primandproper.platform.signin.v1.SignOutEverywhereRequest
	16, // 45: primandproper.platform.signin.v1.SignInService.ListSignIns:input_type -> primandproper.platform.signin.v1.ListSignInsRequest
	18, // 46: primandproper.platform.signin.v1.SignInService.EndSignIn:input_type -> primandproper.platform.signin.v1.EndSignInRequest
	20, // 47: primandproper.platform.signin.v1.SignInService.EndOtherSignIns:input_type -> primandproper.platform.signin.v1.EndOtherSignInsRequest
	22, // 48: primandproper.platform.signin.v1.SignInService.GetAuthStatus:input_type -> primandproper.platform.signin.v1.GetAuthStatusRequest
	24, // 49: primandproper.platform.signin.v1.SignInService.GetSelf:input_type -> primandproper.platform.signin.v1.GetSelfRequest
	26, // 50: primandproper.platform.signin.v1.SignInService.UpdatePassword:input_type -> primandproper.platform.signin.v1.UpdatePasswordRequest
	32, // 51: primandproper.platform.signin.v1.SignInService.RefreshTOTPSecret:input_type -> primandproper.platform.signin.v1.RefreshTOTPSecretRequest
	34, // 52: primandproper.platform.signin.v1.SignInService.VerifyTOTPSecret:input_type -> primandproper.platform.signin.v1.VerifyTOTPSecretRequest
	28, // 53: primandproper.platform.signin.v1.SignInService.UpdateEmailAddress:input_type -> primandproper.platform.signin.v1.UpdateEmailAddressRequest
	30, // 54: primandproper.platform.signin.v1.SignInService.UpdateUsername:input_type -> primandproper.platform.signin.v1.UpdateUsernameRequest
	56, // 55: primandproper.platform.signin.v1.SignInAdministrationService.ListSignInsForUser:input_type -> primandproper.platform.signin.v1.ListSignInsForUserRequest
	58, // 56: primandproper.platform.signin.v1.SignInAdministrationService.EndSignInForUser:input_type -> primandproper.platform.signin.v1.EndSignInForUserRequest
	60, // 57: primandproper.platform.signin.v1.SignInAdministrationService.EndAllSignInsForUser:input_type -> primandproper.platform.signin.v1.EndAllSignInsForUserRequest
	41, // 58: primandproper.platform.signin.v1.SignInService.Register:output_type -> primandproper.platform.signin.v1.RegisterResponse
	43, // 59: primandproper.platform.signin.v1.SignInService.AttachPassword:output_type -> primandproper.platform.signin.v1.AttachPasswordResponse
	45, // 60: primandproper.platform.signin.v1.SignInService.VerifyEmailAddress:output_type -> primandproper.platform.signin.v1.VerifyEmailAddressResponse
	47, // 61: primandproper.platform.signin.v1.SignInService.RequestVerificationEmail:output_type -> primandproper.platform.signin.v1.RequestVerificationEmailResponse
	49, // 62: primandproper.platform.signin.v1.SignInService.RequestVerificationEmailByAddress:output_type -> primandproper.platform.signin.v1.RequestVerificationEmailByAddressResponse
	51, // 63: primandproper.platform.signin.v1.SignInService.RequestMagicLink:output_type -> primandproper.platform.signin.v1.RequestMagicLinkResponse
	53, // 64: primandproper.platform.signin.v1.SignInService.RedeemMagicLink:output_type -> primandproper.platform.signin.v1.RedeemMagicLinkResponse
	55, // 65: primandproper.platform.signin.v1.SignInService.RequestHandleReminder:output_type -> primandproper.platform.signin.v1.RequestHandleReminderResponse
	4,  // 66: primandproper.platform.signin.v1.SignInService.LoginForToken:output_type -> primandproper.platform.signin.v1.LoginForTokenResponse
	6,  // 67: primandproper.platform.signin.v1.SignInService.AdminLoginForToken:output_type -> primandproper.platform.signin.v1.AdminLoginForTokenResponse
	8,  // 68: primandproper.platform.signin.v1.SignInService.ExchangeRefreshToken:output_type -> primandproper.platform.signin.v1.ExchangeRefreshTokenResponse
	10, // 69: primandproper.platform.signin.v1.SignInService.SwitchAccount:output_type -> primandproper.platform.signin.v1.SwitchAccountResponse
	12, // 70: primandproper.platform.signin.v1.SignInService.SignOut:output_type -> primandproper.platform.signin.v1.SignOutResponse
	14, // 71: primandproper.platform.signin.v1.SignInService.SignOutEverywhere:output_type -> primandproper.platform.signin.v1.SignOutEverywhereResponse
	17, // 72: primandproper.platform.signin.v1.SignInService.ListSignIns:output_type -> primandproper.platform.signin.v1.ListSignInsResponse
	19, // 73: primandproper.platform.signin.v1.SignInService.EndSignIn:output_type -> primandproper.platform.signin.v1.EndSignInResponse
	21, // 74: primandproper.platform.signin.v1.SignInService.EndOtherSignIns:output_type -> primandproper.platform.signin.v1.EndOtherSignInsResponse
	23, // 75: primandproper.platform.signin.v1.SignInService.GetAuthStatus:output_type -> primandproper.platform.signin.v1.GetAuthStatusResponse
	25, // 76: primandproper.platform.signin.v1.SignInService.GetSelf:output_type -> primandproper.platform.signin.v1.GetSelfResponse
	27, // 77: primandproper.platform.signin.v1.SignInService.UpdatePassword:output_type -> primandproper.platform.signin.v1.UpdatePasswordResponse
	33, // 78: primandproper.platform.signin.v1.SignInService.RefreshTOTPSecret:output_type -> primandproper.platform.signin.v1.RefreshTOTPSecretResponse
	35, // 79: primandproper.platform.signin.v1.SignInService.VerifyTOTPSecret:output_type -> primandproper.platform.signin.v1.VerifyTOTPSecretResponse
	29, // 80: primandproper.platform.signin.v1.SignInService.UpdateEmailAddress:output_type -> primandproper.platform.signin.v1.UpdateEmailAddressResponse
	31, // 81: primandproper.platform.signin.v1.SignInService.UpdateUsername:output_type -> primandproper.platform.signin.v1.UpdateUsernameResponse
	57, // 82: primandproper.platform.signin.v1.SignInAdministrationService.ListSignInsForUser:output_type -> primandproper.platform.signin.v1.ListSignInsForUserResponse
	59, // 83: primandproper.platform.signin.v1.SignInAdministrationService.EndSignInForUser:output_type -> primandproper.platform.signin.v1.EndSignInForUserResponse
	61, // 84: primandproper.platform.signin.v1.SignInAdministrationService.EndAllSignInsForUser:output_type -> primandproper.platform.signin.v1.EndAllSignInsForUserResponse
	58, // [58:85] is the sub-list for method output_type
	31, // [31:58] is the sub-list for method input_type
	31, // [31:31] is the sub-list for extension type_name
	31, // [31:31] is the sub-list for extension extendee
	0,  // [0:31] is the sub-list for field type_name
}

func init() { file_primandproper_platform_signin_v1_signin_proto_init() }
func file_primandproper_platform_signin_v1_signin_proto_init() {
	if File_primandproper_platform_signin_v1_signin_proto != nil {
		return
	}
	file_primandproper_platform_signin_v1_signin_proto_msgTypes[38].OneofWrappers = []any{
		(*RegisterRequest_Password)(nil),
		(*RegisterRequest_NoPassword)(nil),
	}
	type x struct{}
	out := protoimpl.TypeBuilder{
		File: protoimpl.DescBuilder{
			GoPackagePath: reflect.TypeOf(x{}).PkgPath(),
			RawDescriptor: unsafe.Slice(unsafe.StringData(file_primandproper_platform_signin_v1_signin_proto_rawDesc), len(file_primandproper_platform_signin_v1_signin_proto_rawDesc)),
			NumEnums:      0,
			NumMessages:   63,
			NumExtensions: 0,
			NumServices:   2,
		},
		GoTypes:           file_primandproper_platform_signin_v1_signin_proto_goTypes,
		DependencyIndexes: file_primandproper_platform_signin_v1_signin_proto_depIdxs,
		MessageInfos:      file_primandproper_platform_signin_v1_signin_proto_msgTypes,
	}.Build()
	File_primandproper_platform_signin_v1_signin_proto = out.File
	file_primandproper_platform_signin_v1_signin_proto_goTypes = nil
	file_primandproper_platform_signin_v1_signin_proto_depIdxs = nil
}

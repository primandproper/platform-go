package grpc

import (
	"context"
	"errors"
	"maps"
	"strings"

	"github.com/primandproper/platform-go/v15/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v15/identity"

	"github.com/primandproper/primitives-go/v2/authentication/oauth2server"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Authentication is what a method requires of the credential its request
// carries.
type Authentication uint8

const (
	// AuthenticationRequired refuses a request that does not resolve to a
	// caller with codes.Unauthenticated, before the handler runs.
	AuthenticationRequired Authentication = iota + 1

	// AuthenticationOptional resolves a caller where the request carries a
	// usable credential and lets it through with nobody on it where it does
	// not — a credential that names nobody included, though not one naming
	// somebody the directory refuses to admit, which is refused as that person
	// under either requirement. It is for a method whose
	// answer depends on who is asking and which answers somebody who is not,
	// such as signin's GetAuthStatus, which says "no" to an expired token
	// rather than refusing it.
	AuthenticationOptional

	// AuthenticationAnonymous does not look at the credential at all. It is
	// for a method whose request is its own authority — a sign-in, a refresh
	// token exchange, a mailed link — where a stale access token riding along
	// on the connection must not turn the request into a refusal.
	AuthenticationAnonymous
)

// String renders the requirement for a log line.
func (a Authentication) String() string {
	switch a {
	case AuthenticationRequired:
		return "required"
	case AuthenticationOptional:
		return "optional"
	case AuthenticationAnonymous:
		return "anonymous"
	default:
		return "unknown"
	}
}

// The errors building a requirements table returns.
var (
	// ErrDuplicateAuthenticationMethod indicates a method, or a service, whose
	// requirement was declared twice. It is refused rather than resolved by
	// order, because two declarations of one method are two decisions, and a
	// table that silently kept one of them is a table nobody can read.
	ErrDuplicateAuthenticationMethod = platformerrors.New("a method's authentication requirement was declared twice")

	// ErrInvalidAuthenticationMethod indicates a declaration that names no
	// method or service, or names a requirement that is none of the three.
	ErrInvalidAuthenticationMethod = platformerrors.New("an authentication requirement names no method or no valid requirement")
)

// AuthenticationRequirementsBuilder accumulates which methods need a caller.
//
// It is the authentication analog of primitives-go's
// authorization/grpc.RequirementsBuilder, and fails closed the same way: a
// method nothing declared is refused, so an RPC added to a service later and
// decided about nowhere is a refusal a developer sees rather than a method
// that serves anybody.
//
// A requirement is declared per method, or per service for every method of
// the service not declared by name. The second is how a surface whose every
// method needs a caller is declared without restating its method list, which
// would go stale the day the surface grew an RPC; the first is how the
// surfaces' own lists — signin's through [RequireAuthentication],
// waitlists/grpc's PublicMethods, identity/grpc's SelfServiceMethods — and a
// consumer's own methods go in.
type AuthenticationRequirementsBuilder struct {
	methods  map[string]Authentication
	services map[string]Authentication
	err      error
}

// NewAuthenticationRequirements starts an empty table.
func NewAuthenticationRequirements() *AuthenticationRequirementsBuilder {
	return &AuthenticationRequirementsBuilder{
		methods:  map[string]Authentication{},
		services: map[string]Authentication{},
	}
}

// Declare sets what each named method requires. Methods are gRPC full method
// names, "/package.Service/Method" — the generated _FullMethodName constants.
func (b *AuthenticationRequirementsBuilder) Declare(requirement Authentication, methods ...string) *AuthenticationRequirementsBuilder {
	if b == nil {
		return nil
	}

	for _, method := range methods {
		b.put(b.methods, method, requirement)
	}

	return b
}

// DeclareService sets what every method of each named service requires,
// except the methods declared by name. Services are full service names,
// "package.Service" — a generated ServiceDesc's ServiceName.
func (b *AuthenticationRequirementsBuilder) DeclareService(
	requirement Authentication,
	services ...string,
) *AuthenticationRequirementsBuilder {
	if b == nil {
		return nil
	}

	for _, service := range services {
		b.put(b.services, strings.Trim(service, "/"), requirement)
	}

	return b
}

// put records one declaration, remembering the first mistake.
func (b *AuthenticationRequirementsBuilder) put(into map[string]Authentication, name string, requirement Authentication) {
	if b.err != nil {
		return
	}

	switch {
	case name == "", requirement.String() == "unknown":
		b.err = platformerrors.Wrapf(ErrInvalidAuthenticationMethod, "%q as %s", name, requirement)
	default:
		if _, taken := into[name]; taken {
			b.err = platformerrors.Wrapf(ErrDuplicateAuthenticationMethod, "%q", name)

			return
		}

		into[name] = requirement
	}
}

// Build freezes the table, or reports the first declaration that was wrong.
func (b *AuthenticationRequirementsBuilder) Build() (*AuthenticationRequirements, error) {
	if b == nil {
		return nil, platformerrors.ErrNilInputParameter
	}

	if b.err != nil {
		return nil, b.err
	}

	reqs := &AuthenticationRequirements{
		methods:  make(map[string]Authentication, len(b.methods)),
		services: make(map[string]Authentication, len(b.services)),
	}

	maps.Copy(reqs.methods, b.methods)

	maps.Copy(reqs.services, b.services)

	return reqs, nil
}

// AuthenticationRequirements is a built table: what each method requires.
type AuthenticationRequirements struct {
	methods  map[string]Authentication
	services map[string]Authentication
}

// Lookup reports what a method requires, and whether anything declared it —
// by name first, then by its service.
func (r *AuthenticationRequirements) Lookup(fullMethod string) (Authentication, bool) {
	if r == nil {
		return 0, false
	}

	if requirement, ok := r.methods[fullMethod]; ok {
		return requirement, true
	}

	service, _, ok := strings.Cut(strings.TrimPrefix(fullMethod, "/"), "/")
	if !ok {
		return 0, false
	}

	requirement, ok := r.services[service]

	return requirement, ok
}

// RequireAuthentication declares every method this package serves onto a
// requirements table, from its own two lists and [Permissions].
//
// The anonymous methods are declared anonymous, save two that are optional.
// GetAuthStatus, because a whoami that never looks at the credential answers
// "no" to everybody. And Register, because a caller who is signed in — an
// operator provisioning users — is still somebody the deployment's
// signin.RegistrationPolicy may want to read off the context, and a door that
// never looked would hand it nobody. The self-service methods are required, and
// so is every SignInAdministrationService method. It takes
// and returns the builder, as [Require] does, so a consumer composes several
// surfaces and their own methods into one table:
//
//	reqs, err := signingrpc.RequireAuthentication(signingrpc.NewAuthenticationRequirements()).
//		Declare(signingrpc.AuthenticationOptional, waitlistsgrpc.PublicMethods()...).
//		DeclareService(signingrpc.AuthenticationRequired, identitypb.IdentityService_ServiceDesc.ServiceName).
//		Declare(signingrpc.AuthenticationAnonymous, healthpb.Health_Check_FullMethodName).
//		Build()
func RequireAuthentication(b *AuthenticationRequirementsBuilder) *AuthenticationRequirementsBuilder {
	if b == nil {
		return nil
	}

	for _, method := range AnonymousMethods() {
		requirement := AuthenticationAnonymous
		if method == signinpb.SignInService_GetAuthStatus_FullMethodName ||
			method == signinpb.SignInService_Register_FullMethodName {
			requirement = AuthenticationOptional
		}

		b.Declare(requirement, method)
	}

	b.Declare(AuthenticationRequired, SelfServiceMethods()...)

	for method := range Permissions() {
		b.Declare(AuthenticationRequired, method)
	}

	return b
}

// UnaryServerInterceptor resolves each request's caller once, by reqs, and
// puts them where Extract reads them.
//
// A method reqs does not declare is refused with codes.PermissionDenied, the
// code primitives-go's authorization enforcer refuses an undeclared method
// with. A method that requires a caller and has none is refused with
// codes.Unauthenticated. Two refusals hold whatever a method requires beyond
// anonymous: a token naming somebody whose account status admits no sign-in is
// codes.PermissionDenied — identity's own answer to a banned caller, and the
// honest one, since the credential is theirs and it is the person who is
// refused — as is an access token lacking a scope WithAccessTokens requires,
// and a directory that could not be read is codes.Unavailable, since an
// outage is not a credential to discard.
//
// A caller who owes a forced password change is then refused with
// signin.ErrPasswordChangeRequired, as codes.FailedPrecondition, on every method
// the extractor's PasswordChangeGate does not allow; see PrincipalExtractor.
//
// A nil table declares nothing, so every method is refused: fail-closed
// includes the table that was never built.
func (e *PrincipalExtractor) UnaryServerInterceptor(reqs *AuthenticationRequirements) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, err := e.admit(ctx, reqs, info.FullMethod)
		if err != nil {
			return nil, err
		}

		return handler(ctx, req)
	}
}

// StreamServerInterceptor is UnaryServerInterceptor for streaming methods.
func (e *PrincipalExtractor) StreamServerInterceptor(reqs *AuthenticationRequirements) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		ctx, err := e.admit(ss.Context(), reqs, info.FullMethod)
		if err != nil {
			return err
		}

		return handler(srv, &resolvedStream{ServerStream: ss, ctx: ctx})
	}
}

// resolvedStream is a server stream carrying the context admit returned.
type resolvedStream struct {
	grpc.ServerStream

	ctx context.Context //nolint:containedctx // a ServerStream's context is a field by gRPC's design.
}

func (s *resolvedStream) Context() context.Context { return s.ctx }

// admit is both interceptors: the requirement, the resolution, and the
// context the handler runs with.
func (e *PrincipalExtractor) admit(ctx context.Context, reqs *AuthenticationRequirements, method string) (context.Context, error) {
	requirement, declared := reqs.Lookup(method)
	if !declared {
		return ctx, status.Errorf(codes.PermissionDenied, "%s declares no authentication requirement", method)
	}

	if requirement == AuthenticationAnonymous {
		return withResolved(ctx, nil), nil
	}

	principal, err := e.resolve(ctx, bearerFromMetadata(ctx))

	switch {
	case errors.Is(err, oauth2server.ErrInsufficientScope):
		return ctx, status.Error(codes.PermissionDenied, "the access token does not carry a required scope")
	case err != nil && !errors.Is(err, ErrUnauthenticated):
		e.o11y.Logger().Error("resolving the caller of "+method, err)

		return ctx, status.Error(codes.Unavailable, "the caller could not be resolved")
	case errors.Is(err, identity.ErrSignInNotAdmitted):
		return ctx, status.Error(codes.PermissionDenied, "the caller's account does not admit sign-in")
	case principal == nil && requirement == AuthenticationRequired:
		return ctx, status.Error(codes.Unauthenticated, "authentication required")
	}

	ctx = withResolved(ctx, principal)

	if e.gate != nil && principal != nil {
		if err = e.gate.admitRPC(ctx, method); err != nil {
			return ctx, err
		}
	}

	return ctx, nil
}

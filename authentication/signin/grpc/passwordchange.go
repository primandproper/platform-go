package grpc

import (
	"context"
	"net/http"

	"github.com/primandproper/platform-go/v15/authentication/passwordreset/passwordresetpb"
	"github.com/primandproper/platform-go/v15/authentication/signin"
	"github.com/primandproper/platform-go/v15/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v15/callers"
	"github.com/primandproper/platform-go/v15/identity"
	"github.com/primandproper/platform-go/v15/identity/identitypb"

	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/encoding"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	httperrors "github.com/primandproper/primitives-go/v2/errors/http"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// gateName scopes the gate's spans and log lines, apart from the extractor's
// and the server's.
const gateName = "signin_grpc_password_change_gate"

// The errors building a PasswordChangeGate returns.
var (
	// ErrNilGateExtractor indicates a nil callers.PrincipalExtractor handed to
	// NewPasswordChangeGate.
	ErrNilGateExtractor = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil principal extractor for the password change gate")

	// ErrNilPasswordChangeRequired indicates a nil PasswordChangeRequired handed
	// to NewPasswordChangeGate. There is no default to fall back on: which
	// reading of the flag is right depends on what the deployment's principals
	// carry, and DirectoryPasswordChange is the one to name when unsure.
	ErrNilPasswordChangeRequired = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil password change reader for the password change gate")

	// ErrNilPasswordChangeClient indicates a nil database.Client handed to
	// DirectoryPasswordChange.
	ErrNilPasswordChangeClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client for the password change reader")

	// ErrNilPasswordChangeDirectory indicates a nil PasswordChangeDirectory
	// handed to DirectoryPasswordChange.
	ErrNilPasswordChangeDirectory = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil directory for the password change reader")
)

// PasswordChangeRequired reports whether the caller on a request owes a
// forced password change.
//
// An error is a reading that could not be made, and the gate refuses the
// request as unavailable rather than guessing either way: letting a flagged
// caller through on an outage is the hole the gate exists to close, and
// refusing them as flagged would tell a client to send somebody to a form they
// may have no reason to fill in.
type PasswordChangeRequired func(ctx context.Context, principal callers.Principal) (bool, error)

// PasswordChangeDirectory is the one read DirectoryPasswordChange makes, for a
// principal that does not already carry the flag. identity.Store satisfies it.
type PasswordChangeDirectory interface {
	GetUser(ctx context.Context, q database.SQLQueryExecutor, scope tenancy.Scope, userID string) (*identity.User, error)
}

// DirectoryPasswordChange is the PasswordChangeRequired that reads the flag
// through identity: off the principal where it already carries the
// directory's answer, and from the directory where it does not.
//
// A *Caller carries it — PrincipalExtractor read the user on this request to
// resolve them — so a deployment whose every caller came through that extractor
// pays nothing for the gate. A principal some other extractor produced, such as
// WithFallback's, is read by its UserID in its Scope on the client's reader,
// and one the directory does not hold — a service account, a legacy session's
// user — owes nothing: the flag is a fact about this directory's users, and
// a caller who is not one has no password here to change.
func DirectoryPasswordChange(client database.Client, directory PasswordChangeDirectory) (PasswordChangeRequired, error) {
	if client == nil {
		return nil, ErrNilPasswordChangeClient
	}

	if directory == nil {
		return nil, ErrNilPasswordChangeDirectory
	}

	return func(ctx context.Context, principal callers.Principal) (bool, error) {
		if carrier, ok := principal.(interface{ Identity() *identity.Principal }); ok {
			if carried := carrier.Identity(); carried != nil && carried.User != nil {
				return carried.User.RequiresPasswordChange, nil
			}
		}

		user, err := directory.GetUser(ctx, client.Reader(), principal.Scope(), principal.UserID())
		if platformerrors.Is(err, identity.ErrUserNotFound) {
			return false, nil
		}

		if err != nil {
			return false, platformerrors.Wrap(err, "reading whether the caller owes a password change")
		}

		return user.RequiresPasswordChange, nil
	}, nil
}

// PasswordChangeMethods are this module's own RPCs a caller who owes a forced
// password change may still make.
//
// It is a fact about this module's surfaces rather than a policy, which is why
// it is here rather than in every deployment's interceptor. A flagged caller
// needs four things, and each entry is one of them:
//
//   - To be told. GetAuthStatus is how the contract says a client learns a
//     change is owed, and GetSelf and identity's GetPrincipal are the reads a
//     client makes to render the person it is about to send to the form.
//   - To make the change. UpdatePassword, and passwordreset's three RPCs, since
//     a reset clears the flag too and a person who forgot the password they
//     were told to change has no other way to change it.
//   - To get rid of a credential they no longer trust. SignOut,
//     SignOutEverywhere, ListSignIns, EndSignIn and EndOtherSignIns, because
//     the reason an operator forces a change is so often a credential somebody
//     else holds, and ending that somebody's logins is exactly the remedy a
//     gate must not stand in front of.
//   - To sign in at all. Every anonymous door but one, because what they
//     present is their own authority and not the caller the gate refuses: a
//     flagged person has to be able to sign in, renew their token and follow a
//     mailed link, or they could never reach the form.
//
// What is not on it is everything else, TOTP enrollment and registration
// included: neither discharges the obligation, and both are the kind of change
// a person holding a stolen password would make first. Register is the one
// anonymous method left off. Nobody is required to call it, so the gate never
// sees a caller who arrived with no credential; what it refuses is a flagged
// caller who arrived signed in, which is an operator provisioning users with a
// password they were told to change.
func PasswordChangeMethods() []string {
	methods := []string{
		signinpb.SignInService_GetSelf_FullMethodName,
		signinpb.SignInService_UpdatePassword_FullMethodName,
		signinpb.SignInService_SignOutEverywhere_FullMethodName,
		signinpb.SignInService_ListSignIns_FullMethodName,
		signinpb.SignInService_EndSignIn_FullMethodName,
		signinpb.SignInService_EndOtherSignIns_FullMethodName,
		identitypb.IdentityService_GetPrincipal_FullMethodName,
		passwordresetpb.PasswordResetService_RequestPasswordReset_FullMethodName,
		passwordresetpb.PasswordResetService_VerifyPasswordResetToken_FullMethodName,
		passwordresetpb.PasswordResetService_CompletePasswordReset_FullMethodName,
	}

	// GetAuthStatus and SignOut are among the anonymous methods.
	for _, method := range AnonymousMethods() {
		if method != signinpb.SignInService_Register_FullMethodName {
			methods = append(methods, method)
		}
	}

	return methods
}

// PasswordChangeGate refuses every call from a caller who owes a forced
// password change, save the ones that discharge it.
//
// identity.User.RequiresPasswordChange is set by an operator and cleared by any
// password write, and the sign-in doors still admit somebody who holds it —
// the alternative is a user who cannot reach the form. Until this gate, nothing
// held them there: a flag reported by GetAuthStatus and enforced by nobody is
// an operator control that works only when every client cooperates. The gate
// answers every other call with signin.ErrPasswordChangeRequired —
// FailedPrecondition over gRPC and a 403 over HTTP, carrying the reason
// PASSWORD_CHANGE_REQUIRED — until the change is made, and the same call then
// succeeds with nothing else to clear.
//
// # What passes
//
// A method on PasswordChangeMethods or named to WithAllowedMethods; a request
// with nobody on it, since there is no caller to owe anything and whether it
// may proceed is the authentication interceptor's decision; and a caller who
// owes nothing.
//
// # Where it goes
//
// Behind the authentication interceptor, which is what puts a caller where the
// extractor reads one: a gate ahead of it sees nobody on any request and lets
// everything through. PrincipalExtractor builds one and runs it inside its own
// interceptors and HTTPMiddleware, right after resolving the caller, so a
// deployment authenticating through it has the gate on every method and route
// without installing anything. A deployment authenticating through its own
// interceptor builds the gate here and appends UnaryServerInterceptor and
// StreamServerInterceptor after that interceptor, and HTTPMiddleware after its
// authentication middleware.
//
// # Whose flag
//
// The principal's UserID's. A principal acting as somebody else — an operator
// impersonating a member — is gated on the member whose UserID it carries,
// because that is whose credential the flag is about and whose account every
// call acts on. An operator who must act on a flagged member's behalf does so
// through the directory's own operator methods, which act on the member as a
// target rather than as them.
type PasswordChangeGate struct {
	extract  callers.PrincipalExtractor
	required PasswordChangeRequired
	allowed  map[string]struct{}
	codec    encoding.Codec
	o11y     observability.Observer

	// What the options wrote, kept only until the observer is built from it.
	logger         logging.Logger
	tracerProvider tracing.Provider
}

// PasswordChangeGateOption configures a PasswordChangeGate.
type PasswordChangeGateOption func(*PasswordChangeGate)

// WithAllowedMethods adds methods a flagged caller may still make, as gRPC full
// method names, to PasswordChangeMethods — which are always allowed and cannot
// be removed. It is how a deployment names the calls of its own that a person
// on their way to the form needs, and that is the whole of the deployment's
// part in the gate.
func WithAllowedMethods(methods ...string) PasswordChangeGateOption {
	return func(g *PasswordChangeGate) {
		for _, method := range methods {
			if method != "" {
				g.allowed[method] = struct{}{}
			}
		}
	}
}

// WithGateLogger sets the logger. Absent means no logging.
func WithGateLogger(logger logging.Logger) PasswordChangeGateOption {
	return func(g *PasswordChangeGate) { g.logger = logger }
}

// WithGateTracerProvider sets the tracer provider. Absent means no tracing.
func WithGateTracerProvider(tracerProvider tracing.Provider) PasswordChangeGateOption {
	return func(g *PasswordChangeGate) { g.tracerProvider = tracerProvider }
}

// WithGatePillars supplies the logger and tracer provider at once. The gate
// records no metrics of its own, for the extractor's reason: every request it
// passes or refuses is counted by the surface or server that received it.
func WithGatePillars(p *observability.Pillars) PasswordChangeGateOption {
	return func(g *PasswordChangeGate) { g.logger, g.tracerProvider, _ = p.Deps() }
}

// NewPasswordChangeGate builds the gate over the extractor that says who is
// calling and the reading that says whether they owe a change.
//
// extract is read, not resolved: the gate reads the caller the authentication
// interceptor already put on the request, and never a credential of its own.
// required is DirectoryPasswordChange for a deployment whose principals are
// this module's, or the deployment's own reading for one whose principal
// already carries the flag some other way.
func NewPasswordChangeGate(
	extract callers.PrincipalExtractor,
	required PasswordChangeRequired,
	opts ...PasswordChangeGateOption,
) (*PasswordChangeGate, error) {
	if extract == nil {
		return nil, ErrNilGateExtractor
	}

	if required == nil {
		return nil, ErrNilPasswordChangeRequired
	}

	g := &PasswordChangeGate{
		extract:  extract,
		required: required,
		allowed:  map[string]struct{}{},
	}

	WithAllowedMethods(PasswordChangeMethods()...)(g)

	for _, opt := range opts {
		if opt != nil {
			opt(g)
		}
	}

	g.o11y = observability.NewObserver(gateName, g.logger, g.tracerProvider)
	g.codec = encoding.NewClientEncoder(encoding.ContentTypeJSON,
		encoding.WithLogger(g.logger),
		encoding.WithTracerProvider(g.tracerProvider))

	return g, nil
}

// Allows reports whether a method is one a flagged caller may still make.
func (g *PasswordChangeGate) Allows(fullMethod string) bool {
	_, ok := g.allowed[fullMethod]

	return ok
}

// UnaryServerInterceptor refuses a flagged caller's call to any method the gate
// does not allow. See PasswordChangeGate for where it belongs in the chain.
func (g *PasswordChangeGate) UnaryServerInterceptor() grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := g.admitRPC(ctx, info.FullMethod); err != nil {
			return nil, err
		}

		return handler(ctx, req)
	}
}

// StreamServerInterceptor is UnaryServerInterceptor for streaming methods.
func (g *PasswordChangeGate) StreamServerInterceptor() grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := g.admitRPC(ss.Context(), info.FullMethod); err != nil {
			return err
		}

		return handler(srv, ss)
	}
}

// HTTPMiddleware refuses a flagged caller's request unless allow says it is one
// they may still make. A nil allow allows nothing, which is right for a
// deployment none of whose HTTP routes discharge the obligation: this module's
// sign-in surface is gRPC, so no route of its own needs to be named.
//
// The refusal is the platform's error envelope under signin.HTTPMapper's 403,
// encoded as JSON. A directory that could not be read is a 503 in the same
// envelope.
func (g *PasswordChangeGate) HTTPMiddleware(allow func(*http.Request) bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if allow != nil && allow(r) {
				next.ServeHTTP(w, r)

				return
			}

			ctx := r.Context()

			owed, err := g.owes(ctx)

			switch {
			case err != nil:
				g.o11y.Logger().Error("reading whether the caller of "+r.URL.Path+" owes a password change", err)
				g.unavailableHTTP(ctx, w)
			case owed:
				g.refuseHTTP(ctx, w)
			default:
				next.ServeHTTP(w, r)
			}
		})
	}
}

// admitRPC is both interceptors' judgement.
func (g *PasswordChangeGate) admitRPC(ctx context.Context, method string) error {
	if g.Allows(method) {
		return nil
	}

	owed, err := g.owes(ctx)
	if err != nil {
		g.o11y.Logger().Error("reading whether the caller of "+method+" owes a password change", err)

		return status.Error(codes.Unavailable, "the caller's password standing could not be read")
	}

	if !owed {
		return nil
	}

	// Shaped as a status so a server with no error-encoding interceptor still
	// answers FailedPrecondition in the sentinel's own words, and wrapping the
	// sentinel so one that has an encoder attaches the reason as well.
	return observability.GRPCStatusError(
		platformerrors.Wrapf(signin.ErrPasswordChangeRequired, "calling %s", method),
		codes.FailedPrecondition,
		signin.ErrPasswordChangeRequired.Error(),
	)
}

// owes reports whether the caller on ctx owes a forced password change. A
// request with nobody on it owes nothing.
func (g *PasswordChangeGate) owes(ctx context.Context) (bool, error) {
	principal, ok := g.extract(ctx)
	if !ok || principal == nil {
		return false, nil
	}

	return g.required(ctx, principal)
}

// refuseHTTP writes the 403. It reads signin.HTTPMapper directly rather than
// the process-wide registry, so what a client is told does not depend on
// whether a composition root called errormappers.Register — and cannot
// disagree with what the registry answers once it did.
func (g *PasswordChangeGate) refuseHTTP(ctx context.Context, w http.ResponseWriter) {
	code, msg, _ := signin.HTTPMapper.Map(signin.ErrPasswordChangeRequired)
	g.writeHTTPError(ctx, w, httperrors.HTTPStatusForCode(code), code, msg)
}

// unavailableHTTP writes the 503 for a standing that could not be read, in the
// words the interceptors use for the same failure. Its code is
// ErrNothingSpecific rather than ErrCircuitBroken, the one code whose own status
// is a 503: that code round-trips through httperrors.ErrorForCode to
// circuitbreaking's sentinel, and a typed client told a breaker had tripped
// would be told something that did not happen.
func (g *PasswordChangeGate) unavailableHTTP(ctx context.Context, w http.ResponseWriter) {
	g.writeHTTPError(ctx, w, http.StatusServiceUnavailable, httperrors.ErrNothingSpecific, "the caller's password standing could not be read")
}

// writeHTTPError answers with the platform's error envelope under statusCode.
func (g *PasswordChangeGate) writeHTTPError(ctx context.Context, w http.ResponseWriter, statusCode int, code httperrors.ErrorCode, msg string) {
	encoded, err := g.codec.Marshal(ctx, httperrors.NewAPIErrorResponse(msg, code, httperrors.ResponseDetails{}))
	if err != nil {
		http.Error(w, http.StatusText(statusCode), statusCode)

		return
	}

	w.Header().Set("Content-Type", g.codec.ContentType())
	w.WriteHeader(statusCode)
	_, _ = w.Write(encoded) //nolint:errcheck // the status is already sent; a failed body write has nobody to report to.
}

package grpc

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/identity"

	"github.com/primandproper/primitives-go/v2/authentication/tokens"
	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/observability/logging"
	"github.com/primandproper/primitives-go/v2/observability/tracing"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"google.golang.org/grpc/metadata"
)

// extractorName scopes the extractor's spans and log lines, apart from the
// server's: the two are built separately and a trace should say which one
// refused.
const extractorName = "signin_grpc_extractor"

// The errors the extractor returns for its own failures.
var (
	// ErrNilTokenVerifier indicates a nil TokenVerifier.
	ErrNilTokenVerifier = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil token verifier for the sign-in principal extractor")

	// ErrNilPrincipalDirectory indicates a nil PrincipalDirectory.
	ErrNilPrincipalDirectory = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil directory for the sign-in principal extractor")

	// ErrNilExtractorClient indicates a nil database.Client, which the
	// extractor reads the directory on.
	ErrNilExtractorClient = platformerrors.Wrap(platformerrors.ErrNilInputParameter, "nil database client for the sign-in principal extractor")

	// ErrUnauthenticated indicates a request whose credential names nobody
	// this deployment will treat as a caller: a token that does not verify,
	// one this module did not mint, or one naming somebody the directory
	// refuses — unknown, archived, banned, or no longer a member of the
	// account the token was minted against.
	//
	// It is the one answer for all of them, and the interceptor turns it into
	// codes.Unauthenticated. What it is not is a failure of the directory
	// itself: a read that could not be made is returned as that error, so an
	// outage is not reported to a client as a credential they should discard.
	ErrUnauthenticated = platformerrors.New("the request's sign-in credential names no caller")

	// ErrNotASignInToken indicates a token that verified and does not carry
	// the claims signin.DefaultClaims writes — a token some other builder
	// produced. It is what sends a request to WithFallback rather than
	// refusing it, alongside a token that does not verify at all.
	ErrNotASignInToken = platformerrors.Wrap(ErrUnauthenticated, "a token the sign-in service did not mint")
)

// TokenVerifier parses a token this deployment issued back into its claims.
//
// It is tokens.Issuer's other half, and a tokens.Issuer satisfies it: the same
// configured issuer is handed to signin.NewService, which may only mint, and to
// NewPrincipalExtractor, which may only read. The narrowing is signin's own
// ruling kept — a sign-in service that could parse a token is one that could
// be asked to authenticate a request.
type TokenVerifier interface {
	ParseToken(ctx context.Context, token string) (tokens.Claims, error)
}

// PrincipalDirectory is the directory read every authenticated request makes.
//
// It is identity.SignInReader's GetPrincipal, and identity.Store satisfies it.
// That read is where standing is enforced — a user whose status admits no
// sign-in is refused there rather than answered — so the extractor owes no
// status check of its own, and a ban takes effect on the next request whatever
// minted the token it arrived with.
type PrincipalDirectory interface {
	GetPrincipal(
		ctx context.Context,
		q database.SQLQueryExecutor,
		scope tenancy.Scope,
		userID, activeAccountID string,
	) (*identity.Principal, error)
}

// GrantsResolver is the consumer's role policy: what a caller may do.
//
// It is handed the principal the extractor resolved — a *Caller for a token
// this module minted, or whatever the fallback returned for one it did not —
// and answers with that caller's grants. A *Caller's Identity carries the roles
// the directory holds, less the service roles an ordinary-door token does not
// carry; see WithOrdinaryServiceRoles.
//
// An impersonated request is the subject's, and its Identity is the subject's
// too. Whether it also carries the operator's grants is this resolver's call and
// nobody else's: callers.DelegatedActor(principal) names the operator, so a
// resolver may grant the subject's permissions alone, the operator's alone, or
// their intersection — and one that never asks resolves an impersonation exactly
// as it resolves the subject signing in themselves.
type GrantsResolver func(ctx context.Context, principal callers.Principal) (authorization.Grants, error)

// ServiceRolesPolicy says which of a caller's service roles a token minted
// through the ordinary door keeps. See WithOrdinaryServiceRoles.
type ServiceRolesPolicy func(ctx context.Context, serviceRoles []string) []string

// Caller is who a sign-in token turned back into: the principal
// NewPrincipalExtractor puts on a request.
//
// It satisfies callers.Principal, so every surface in this module reads it
// unchanged, and FamilyIdentifier, so ListSignIns marks the login the request
// was made through as current. Its Scope is the directory the token was minted
// in, which is the user's own.
type Caller struct {
	principal      *identity.Principal
	familyID       string
	tokenID        string
	actorID        string
	scope          tenancy.Scope
	administrative bool
}

var (
	_ callers.Principal = (*Caller)(nil)
	_ callers.Delegated = (*Caller)(nil)
	_ FamilyIdentifier  = (*Caller)(nil)
)

// UserID is the calling user's identifier.
func (c *Caller) UserID() string { return c.principal.User.ID }

// Scope is the directory the caller's token was minted in.
func (c *Caller) Scope() tenancy.Scope { return c.scope }

// ActiveAccountID is the account the directory resolved for this request: the
// one the token names, which the directory re-checked the caller still belongs
// to.
func (c *Caller) ActiveAccountID() string { return c.principal.ActiveAccountID }

// FamilyID is the login the request's token belongs to — signin.ClaimFamilyID.
func (c *Caller) FamilyID() string { return c.familyID }

// TokenID is the access token's own identifier.
func (c *Caller) TokenID() string { return c.tokenID }

// Administrative is whether the login came through the administrative door —
// signin.ClaimAdministrative.
func (c *Caller) Administrative() bool { return c.administrative }

// ActorID is the operator acting through this caller's token — signin.ClaimActor
// on a token signin.Service.IssueImpersonationToken minted — and empty on every
// other. It makes a Caller a callers.Delegated, so callers.ActorOf names the
// operator while UserID, Scope and Identity stay the subject's.
//
// What an impersonated request may do is the GrantsResolver's to decide; see
// that type.
func (c *Caller) ActorID() string { return c.actorID }

// Identity is the directory's answer for this request: the user, redacted,
// their memberships and the active account.
//
// On a token minted through the ordinary door, the user's service roles are
// the ones WithOrdinaryServiceRoles kept — none, by default — so a policy that
// resolves grants from Identity().Roles() withholds service-level permissions
// from an administrator who signed in the ordinary way without having to know
// the rule exists. The directory row is unchanged; only this copy is.
func (c *Caller) Identity() *identity.Principal { return c.principal }

// PrincipalExtractor turns a sign-in token back into a caller.
//
// It is the interceptor every consumer of signin was writing, for the one case
// this module defines end to end: a token signin minted, resolved through
// identity's directory, for methods classified by the surfaces' own lists. A
// deployment that also accepts another kind of token — an OAuth2 access token,
// a legacy session — keeps its own extractor for those and chains it in with
// WithFallback.
//
// # What it decides
//
// That the token verifies and carries signin.DefaultClaims' four claims; that
// the directory still admits the user and still counts them a member of the
// account the token was minted against; and what an ordinary-door token of
// somebody who holds service roles carries — nothing of those roles unless
// WithOrdinaryServiceRoles says otherwise. signin.ClaimAdministrative is the
// only thing that confers service roles, which is what makes the
// administrative door's shorter lifetime and mandatory second factor mean
// anything.
//
// # What it does not decide
//
// What anybody may do. Grants are the consumer's role policy, supplied through
// WithGrants and read back through Grants; with none, Grants reports nothing,
// which every grant-reading surface treats as a denial.
//
// # How it reaches a request
//
// Extract is a callers.PrincipalExtractor, and answers only for a request the
// interceptors or HTTPMiddleware resolved; a deployment installs them in front
// of it. The interceptors resolve the caller once per request, enforce an
// AuthenticationRequirements table, and answer an unusable credential with the
// honest code — Unavailable for a directory outage rather than
// codes.Unauthenticated. HTTPMiddleware is the router's counterpart.
type PrincipalExtractor struct {
	verifier  TokenVerifier
	client    database.Client
	directory PrincipalDirectory

	grants   GrantsResolver
	ordinary ServiceRolesPolicy
	fallback callers.PrincipalExtractor

	o11y observability.Observer

	// What the options wrote, kept only until the observer is built from it.
	logger         logging.Logger
	tracerProvider tracing.Provider
}

// ExtractorOption configures a PrincipalExtractor.
type ExtractorOption func(*PrincipalExtractor)

// WithGrants names the consumer's role policy. A nil resolver is ignored,
// leaving none, and Grants then reports no grants for anybody.
func WithGrants(resolve GrantsResolver) ExtractorOption {
	return func(e *PrincipalExtractor) {
		if resolve != nil {
			e.grants = resolve
		}
	}
}

// WithOrdinaryServiceRoles says which service roles a token minted through the
// ordinary door keeps. It is handed the roles the directory holds for the user
// and returns the ones the request carries. A nil policy is ignored, leaving
// the default, which keeps none.
//
// The default is the point of this extractor existing. An administrator who
// signs in the ordinary way has skipped the administrative door's second factor
// and has a longer-lived token, and a policy that read their service roles off
// that login would hand operator permissions to exactly the credential the
// administrative door exists to keep them off. A deployment whose service roles
// include some an ordinary login should keep — a "beta tester" role, a support
// flag that confers nothing dangerous — names them here, and the decision is
// then written down where somebody reviewing it can see it.
func WithOrdinaryServiceRoles(keep ServiceRolesPolicy) ExtractorOption {
	return func(e *PrincipalExtractor) {
		if keep != nil {
			e.ordinary = keep
		}
	}
}

// WithFallback names the extractor consulted for a request this one cannot
// place: one carrying no bearer token, or a token that does not verify or was
// not minted by signin. A nil extractor is ignored, leaving none, and such a
// request then carries nobody.
//
// It is not consulted for a token this module minted whose user the directory
// refuses. A banned user's token is still that user's, and a fallback that
// could answer for it would be a second opinion on a ban.
//
// The fallback reads the request context like any callers.PrincipalExtractor,
// so whatever it recognizes has to be on that context already — the
// consumer's own interceptor, installed ahead of this one, puts it there.
func WithFallback(fallback callers.PrincipalExtractor) ExtractorOption {
	return func(e *PrincipalExtractor) {
		if fallback != nil {
			e.fallback = fallback
		}
	}
}

// WithExtractorLogger sets the logger. Absent means no logging.
func WithExtractorLogger(logger logging.Logger) ExtractorOption {
	return func(e *PrincipalExtractor) { e.logger = logger }
}

// WithExtractorTracerProvider sets the tracer provider. Absent means no
// tracing.
func WithExtractorTracerProvider(tracerProvider tracing.Provider) ExtractorOption {
	return func(e *PrincipalExtractor) { e.tracerProvider = tracerProvider }
}

// WithExtractorPillars supplies the logger and tracer provider at once. The
// extractor records no metrics of its own — every request it resolves is
// counted by the surface that serves it — so the pillars' metrics provider is
// not read.
func WithExtractorPillars(p *observability.Pillars) ExtractorOption {
	return func(e *PrincipalExtractor) { e.logger, e.tracerProvider, _ = p.Deps() }
}

// NewPrincipalExtractor builds the extractor for tokens signin minted.
//
// The three positional dependencies are the ones there is no default for: the
// verifier, because a token's format and key are the deployment's; the client,
// whose reader the directory is read on; and the directory, which is whose
// users these are.
func NewPrincipalExtractor(
	verifier TokenVerifier,
	client database.Client,
	directory PrincipalDirectory,
	opts ...ExtractorOption,
) (*PrincipalExtractor, error) {
	if verifier == nil {
		return nil, ErrNilTokenVerifier
	}

	if client == nil {
		return nil, ErrNilExtractorClient
	}

	if directory == nil {
		return nil, ErrNilPrincipalDirectory
	}

	e := &PrincipalExtractor{
		verifier:  verifier,
		client:    client,
		directory: directory,
		ordinary:  keepNoServiceRoles,
	}

	for _, opt := range opts {
		if opt != nil {
			opt(e)
		}
	}

	e.o11y = observability.NewObserver(extractorName, e.logger, e.tracerProvider)

	return e, nil
}

// keepNoServiceRoles is the default ServiceRolesPolicy: an ordinary-door token
// carries none of its user's service roles.
func keepNoServiceRoles(context.Context, []string) []string { return nil }

// Authenticate resolves one bearer token into a caller.
//
// It is the whole of the extractor's judgement, exposed for a transport this
// package ships no middleware for. An error wrapping ErrUnauthenticated is a
// credential that names nobody; any other error is the directory failing to
// answer, and says nothing about the credential.
func (e *PrincipalExtractor) Authenticate(ctx context.Context, token string) (_ *Caller, err error) {
	ctx, op := e.o11y.Begin(ctx)
	defer op.End()

	if token == "" {
		return nil, op.Error(platformerrors.Wrap(ErrUnauthenticated, "an empty bearer token"), "authenticating a request")
	}

	claims, err := e.verifier.ParseToken(ctx, token)
	if err != nil {
		// Unverifiable is a credential problem, never an outage: the verifier
		// checks a signature against a key it already holds.
		return nil, op.Error(platformerrors.Join(ErrUnauthenticated, err), "verifying a bearer token")
	}

	userID := claims.Subject()
	if userID == "" {
		return nil, op.Error(platformerrors.Wrap(ErrNotASignInToken, "no subject"), "reading a bearer token")
	}

	administrative, ok := boolClaim(claims, signin.ClaimAdministrative)
	if !ok {
		return nil, op.Error(platformerrors.Wrapf(ErrNotASignInToken, "no %q claim", signin.ClaimAdministrative), "reading a bearer token")
	}

	scopeClaim, _ := claims.GetString(signin.ClaimScope)
	accountID, _ := claims.GetString(signin.ClaimAccountID)
	familyID, _ := claims.GetString(signin.ClaimFamilyID)

	scope := scopeFromClaim(scopeClaim)

	op.Set(scopeKey, scope.String())
	op.Set(userIDKey, userID)

	principal, err := e.directory.GetPrincipal(ctx, e.client.Reader(), scope, userID, accountID)
	if err != nil {
		if refusesTheCaller(err) {
			return nil, op.Error(platformerrors.Join(ErrUnauthenticated, err), "resolving a bearer token's caller")
		}

		return nil, op.Error(err, "resolving a bearer token's caller")
	}

	if principal == nil || principal.User == nil {
		return nil, op.Error(platformerrors.Wrap(ErrUnauthenticated, "the directory answered with nobody"), "resolving a bearer token's caller")
	}

	actorID, _ := claims.GetString(signin.ClaimActor)
	if actorID == userID {
		actorID = ""
	}

	if actorID != "" {
		op.Set(actorIDKey, actorID)

		// Present, not merely non-empty: the empty string is tenancy.Global's
		// owner, which is exactly where a deployment's staff may live. A token
		// naming an operator and no scope for them is not one signin minted.
		actorScopeClaim, present := claims.GetString(signin.ClaimActorScope)
		if !present {
			return nil, op.Error(platformerrors.Wrapf(ErrNotASignInToken, "a %q claim with no %q claim", signin.ClaimActor, signin.ClaimActorScope), "reading a bearer token")
		}

		actorScope := tenancy.FromOwner(actorScopeClaim)
		op.Set(actorScopeKey, actorScope.String())

		if err = e.operatorStands(ctx, actorScope, actorID); err != nil {
			return nil, op.Error(err, "resolving a bearer token's operator")
		}
	}

	if !administrative {
		principal = e.withOrdinaryServiceRoles(ctx, principal)
	}

	return &Caller{
		principal:      principal,
		scope:          scope,
		familyID:       familyID,
		tokenID:        claims.JTI(),
		actorID:        actorID,
		administrative: administrative,
	}, nil
}

// operatorStands checks that the operator named on an impersonation token is
// still somebody the directory admits.
//
// It is the ban rule applied to the second identity. A subject's ban takes
// effect on the next request whatever minted their token, and an operator's has
// to as well: otherwise suspending an operator mid-impersonation would leave
// them acting as a customer until the token lapsed. It resolves no account —
// the operator's memberships are not what the request is against.
//
// It reads the operator in actorScope, the token's signin.ClaimActorScope, and
// not in the subject's scope: an operator who is staff in a directory of their
// own is nobody in the customer's, and a re-check there would refuse every
// impersonation that crossed the line on its first request.
func (e *PrincipalExtractor) operatorStands(ctx context.Context, actorScope tenancy.Scope, actorID string) error {
	operator, err := e.directory.GetPrincipal(ctx, e.client.Reader(), actorScope, actorID, "")
	if err != nil {
		if refusesTheCaller(err) {
			return platformerrors.Join(ErrUnauthenticated, err)
		}

		return err
	}

	if operator == nil || operator.User == nil {
		return platformerrors.Wrap(ErrUnauthenticated, "the directory answered with no operator")
	}

	return nil
}

// withOrdinaryServiceRoles is the principal an ordinary-door token carries: the
// directory's answer with the user's service roles narrowed to what the policy
// keeps. The directory's value is copied rather than edited, so a directory
// that caches its answers is not handed back one with roles missing.
func (e *PrincipalExtractor) withOrdinaryServiceRoles(ctx context.Context, principal *identity.Principal) *identity.Principal {
	held := principal.ServiceRoles()
	if len(held) == 0 {
		return principal
	}

	kept := e.ordinary(ctx, append([]string(nil), held...))

	user := *principal.User
	user.ServiceRoles = kept

	narrowed := *principal
	narrowed.User = &user

	return &narrowed
}

// refusesTheCaller reports whether a directory error is an answer about the
// caller rather than a failure to give one.
func refusesTheCaller(err error) bool {
	return errors.Is(err, identity.ErrSignInNotAdmitted) ||
		errors.Is(err, identity.ErrUserNotFound) ||
		errors.Is(err, identity.ErrMembershipNotFound) ||
		errors.Is(err, platformerrors.ErrInvalidIDProvided) ||
		errors.Is(err, tenancy.ErrNoScope)
}

// scopeFromClaim reads signin.ClaimScope back.
//
// The claim is the scope's owner identifier, which is the empty string for
// tenancy.Global. "<global>" is read as Global too: it is how the claim was
// written before DefaultClaims stopped using the scope's prose rendering, and a
// token minted then is still live for its lifetime.
func scopeFromClaim(claim string) tenancy.Scope {
	if claim == tenancy.Global().String() {
		return tenancy.Global()
	}

	return tenancy.FromOwner(claim)
}

// boolClaim reads a boolean claim, reporting whether it was present and a
// boolean. A JSON-decoded token hands it back as a bool; a claim set that
// stringifies its values hands it back as "true" or "false".
func boolClaim(claims tokens.Claims, key string) (value, ok bool) {
	raw, present := claims.Get(key)
	if !present {
		return false, false
	}

	switch v := raw.(type) {
	case bool:
		return v, true
	case string:
		switch v {
		case "true":
			return true, true
		case "false":
			return false, true
		}
	}

	return false, false
}

// resolved is what an interceptor or middleware put on a request: the caller
// it found, or the fact that it looked and found nobody. The second half is
// what stops Extract from resolving a second time a request the interceptor
// already judged anonymous.
type resolved struct {
	principal callers.Principal
}

type resolvedKey struct{}

// withResolved records what was found for this request.
func withResolved(ctx context.Context, principal callers.Principal) context.Context {
	return context.WithValue(ctx, resolvedKey{}, &resolved{principal: principal})
}

// Extract is the callers.PrincipalExtractor: who is calling this request, if
// anybody.
//
// It answers from what the interceptor or HTTPMiddleware resolved, and from
// nothing else. A request neither saw names nobody, whatever token it carries:
// Extract reads no credential of its own. Resolving one here would verify the
// token and read the directory again on every call, and would have to report a
// directory outage as nobody, since an extractor cannot return an error — the
// two things the interceptor exists to get right. A server that installed
// neither therefore sees every caller as anonymous, and a method that needs a
// caller refuses them all.
func (e *PrincipalExtractor) Extract(ctx context.Context) (callers.Principal, bool) {
	r, ok := ctx.Value(resolvedKey{}).(*resolved)
	if !ok || r.principal == nil {
		return nil, false
	}

	return r.principal, true
}

// Grants is the authorization.GrantsExtractor: what the caller on this request
// may do, by the policy WithGrants named.
//
// It reports nothing for a request with nobody on it, for an extractor built
// without a policy, and for a policy that failed — a denial, in every case, by
// the rule every grant-reading surface and primitives-go's enforcer apply.
func (e *PrincipalExtractor) Grants(ctx context.Context) (authorization.Grants, bool) {
	if e.grants == nil {
		return authorization.Grants{}, false
	}

	principal, ok := e.Extract(ctx)
	if !ok {
		return authorization.Grants{}, false
	}

	grants, err := e.grants(ctx, principal)
	if err != nil {
		e.o11y.Logger().Error("resolving a caller's grants", err)

		return authorization.Grants{}, false
	}

	return grants, true
}

// resolve is one request's answer: the caller the token names, or the
// fallback's answer where the token is absent or not this module's.
//
// It returns a nil principal and a nil error for a request that carries nobody,
// an error wrapping ErrUnauthenticated for a credential this module minted and
// refuses, and any other error for a directory that could not be read.
func (e *PrincipalExtractor) resolve(ctx context.Context, token string) (callers.Principal, error) {
	if token == "" {
		return e.fallBack(ctx), nil
	}

	caller, err := e.Authenticate(ctx, token)
	if err == nil {
		return caller, nil
	}

	if !errors.Is(err, ErrUnauthenticated) {
		return nil, err
	}

	// A token that is not ours goes to the fallback. A token that is ours,
	// naming somebody the directory refuses, does not.
	if e.fallback != nil && !refusesTheCaller(err) {
		if principal := e.fallBack(ctx); principal != nil {
			return principal, nil
		}
	}

	return nil, err
}

// fallBack is the fallback's answer, or nobody.
func (e *PrincipalExtractor) fallBack(ctx context.Context) callers.Principal {
	if e.fallback == nil {
		return nil
	}

	principal, ok := e.fallback(ctx)
	if !ok {
		return nil
	}

	return principal
}

// HTTPMiddleware resolves the caller of an HTTP request from its Authorization
// header and puts them on the request context, where Extract reads them.
//
// It treats every request as the gRPC interceptor treats a method declared
// optional. Whether a request with nobody on it may proceed is each HTTP
// surface's decision, so a missing credential, and one that names nobody,
// proceed as nobody. It answers two failures itself, because proceeding would
// misreport them: a token naming somebody whose account status admits no
// sign-in is a 403, and a directory that cannot be read is a 503.
func (e *PrincipalExtractor) HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()

		principal, err := e.resolve(ctx, bearerToken(r.Header.Get(authorizationHeader)))

		switch {
		case err != nil && !errors.Is(err, ErrUnauthenticated):
			http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)

			return
		case errors.Is(err, identity.ErrSignInNotAdmitted):
			http.Error(w, http.StatusText(http.StatusForbidden), http.StatusForbidden)

			return
		}

		next.ServeHTTP(w, r.WithContext(withResolved(ctx, principal)))
	})
}

// authorizationHeader is where a bearer token travels: the HTTP header, and
// the gRPC metadata key it becomes, which gRPC lower-cases.
const authorizationHeader = "authorization"

// bearerFromMetadata reads the bearer token off a gRPC request's incoming
// metadata, or returns empty.
func bearerFromMetadata(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}

	for _, value := range md.Get(authorizationHeader) {
		if token := bearerToken(value); token != "" {
			return token
		}
	}

	return ""
}

// bearerToken strips the scheme from an Authorization value. The scheme is
// matched case-insensitively, as RFC 9110 says it is; anything that is not a
// bearer credential is no token at all rather than a malformed one, so a basic
// credential a consumer's own interceptor reads goes to the fallback.
func bearerToken(value string) string {
	scheme, token, ok := strings.Cut(strings.TrimSpace(value), " ")
	if !ok || !strings.EqualFold(scheme, "bearer") {
		return ""
	}

	return strings.TrimSpace(token)
}

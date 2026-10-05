package service

import (
	"context"

	"github.com/primandproper/platform-go/v15/audit"
	auditgrpc "github.com/primandproper/platform-go/v15/audit/grpc"
	"github.com/primandproper/platform-go/v15/authentication/oauth2clients"
	oauth2clientsgrpc "github.com/primandproper/platform-go/v15/authentication/oauth2clients/grpc"
	"github.com/primandproper/platform-go/v15/authentication/passkeys"
	passkeysgrpc "github.com/primandproper/platform-go/v15/authentication/passkeys/grpc"
	"github.com/primandproper/platform-go/v15/authentication/passwordreset"
	passwordresetgrpc "github.com/primandproper/platform-go/v15/authentication/passwordreset/grpc"
	"github.com/primandproper/platform-go/v15/authentication/signin"
	signincfg "github.com/primandproper/platform-go/v15/authentication/signin/config"
	signingrpc "github.com/primandproper/platform-go/v15/authentication/signin/grpc"
	"github.com/primandproper/platform-go/v15/billing"
	billinggrpc "github.com/primandproper/platform-go/v15/billing/grpc"
	"github.com/primandproper/platform-go/v15/callers"
	"github.com/primandproper/platform-go/v15/comments"
	commentsgrpc "github.com/primandproper/platform-go/v15/comments/grpc"
	"github.com/primandproper/platform-go/v15/dataprivacy"
	dataprivacyhttp "github.com/primandproper/platform-go/v15/dataprivacy/http"
	"github.com/primandproper/platform-go/v15/identity"
	identitycfg "github.com/primandproper/platform-go/v15/identity/config"
	identitygrpc "github.com/primandproper/platform-go/v15/identity/grpc"
	"github.com/primandproper/platform-go/v15/issuereports"
	issuereportsgrpc "github.com/primandproper/platform-go/v15/issuereports/grpc"
	"github.com/primandproper/platform-go/v15/links"
	"github.com/primandproper/platform-go/v15/mediaregistry"
	mediaregistrygrpc "github.com/primandproper/platform-go/v15/mediaregistry/grpc"
	mediaregistryhttp "github.com/primandproper/platform-go/v15/mediaregistry/http"
	"github.com/primandproper/platform-go/v15/notifications"
	notificationsgrpc "github.com/primandproper/platform-go/v15/notifications/grpc"
	"github.com/primandproper/platform-go/v15/operations"
	operationshttp "github.com/primandproper/platform-go/v15/operations/http"
	"github.com/primandproper/platform-go/v15/settings"
	settingsgrpc "github.com/primandproper/platform-go/v15/settings/grpc"
	"github.com/primandproper/platform-go/v15/waitlists"
	waitlistsgrpc "github.com/primandproper/platform-go/v15/waitlists/grpc"
	"github.com/primandproper/platform-go/v15/webhooks"
	webhooksgrpc "github.com/primandproper/platform-go/v15/webhooks/grpc"

	"github.com/primandproper/primitives-go/v2/authentication/oauth2server"
	"github.com/primandproper/primitives-go/v2/authorization"
	authzhttp "github.com/primandproper/primitives-go/v2/authorization/http"
	"github.com/primandproper/primitives-go/v2/config/injection"
	"github.com/primandproper/primitives-go/v2/database"
	platformerrors "github.com/primandproper/primitives-go/v2/errors"
	"github.com/primandproper/primitives-go/v2/observability"
	"github.com/primandproper/primitives-go/v2/routing"
	grpcserver "github.com/primandproper/primitives-go/v2/server/grpc"
	"github.com/primandproper/primitives-go/v2/tenancy"
	"github.com/primandproper/primitives-go/v2/uploads"

	"github.com/samber/do/v2"
)

// The refusals RegisterTransports raises for itself, as opposed to the ones the
// surfaces raise for themselves.
//
// There are only a handful, and that is the measure of how little this file
// decides: a surface that cannot be built refuses in its own words, under its
// own sentinel, and these are the failures no surface is in a position to see.
var (
	// ErrNilPrincipalExtractor is a Transports with a surface to mount and no
	// way to tell who is calling.
	//
	// It is refused here rather than passed along, unlike a nil authorizer,
	// because some of the surfaces take a narrower seam than an extractor and
	// this is where those are derived. Handing them a derivation over no
	// extractor would mount surfaces that refuse every request, which is
	// the shape of hole this whole registration exists to close.
	ErrNilPrincipalExtractor = platformerrors.Wrap(
		platformerrors.ErrNilInputParameter,
		"nil principal extractor for the mounted transport surfaces",
	)

	// ErrNilTenantOf is a Transports mounting one of the surfaces that mean
	// the tenant — audit, operations and the media registry's two — with no
	// Transports.TenantOf to read the tenant with.
	//
	// It is refused rather than read as Principal.Scope(), because that reading
	// is right only for a deployment whose directory is its tenant, and a
	// deployment where it is wrong files its rows under one scope and reads
	// them back under another, which sees none of them and says nothing. A
	// deployment for which the directory is the tenant says so by naming
	// DirectoryTenant.
	ErrNilTenantOf = platformerrors.Wrap(
		platformerrors.ErrNilInputParameter,
		"nil tenant reader for a mounted transport surface that reads the tenant",
	)

	// ErrNoPrincipal is a request to one of the surfaces whose seam is derived
	// from the extractor, arriving with nobody on it.
	//
	// The surfaces that take the extractor directly answer this themselves, and
	// each words it for the RPC it refused. This one is for the derivation:
	// audit's scope, operations' owner, dataprivacy's subject and
	// mediaregistry's caller are each a reading of a principal, and there is no
	// reading of nobody.
	//
	// It wraps callers.ErrNoPrincipal, whose mappers answer it Unauthenticated
	// and 401. Without that, each of them answered with the code it falls
	// back to for a resolver that failed — InvalidArgument from audit, a 500 from
	// mediaregistry — which is the right answer to a request a consumer's
	// resolver could not place and the wrong one to a request with nobody on it.
	ErrNoPrincipal = platformerrors.Wrap(
		callers.ErrNoPrincipal,
		"no principal on the request context a derived transport seam was reading",
	)

	// ErrRouterAlreadyFailed is a routing.Router that arrived at the HTTP lane
	// already carrying a registration failure.
	//
	// routing.Router accumulates its failures rather than returning them, and
	// Err joins them, so a router handed over dirty cannot afterwards be asked
	// which of its failures a surface here caused. Rather than blame the next
	// surface to mount — which would send the reader to a file that did nothing
	// wrong — the lane refuses before mounting any of them and reports what it
	// found underneath.
	//
	// The failure it names is the application's: nothing in this package has
	// touched the router yet when this is raised. Its usual cause is a route
	// registered twice, which is a route quietly not on the server, and the
	// reason the lane does not simply proceed is that nothing between here and
	// Serve would ever look again.
	ErrRouterAlreadyFailed = platformerrors.New(
		"the router already carried a route registration failure before any transport surface mounted",
	)

	// ErrGRPCRegistrationsAlreadyProvided is an injector that already holds the
	// key RegisterTransports mounts the gRPC surfaces through.
	//
	// See RegisterTransports for why that key is this call's to own, and
	// Transports.Registrations for the door an application's own services come
	// through instead.
	ErrGRPCRegistrationsAlreadyProvided = platformerrors.New(
		"the gRPC registration functions are already registered; RegisterTransports owns them",
	)

	// ErrWaitlistConfirmationNeedsLinks is an application that registered a
	// waitlistsgrpc.ConfirmationMailer in a service whose Config names no
	// Links block, so there is no minter to mint the links the mailer would
	// send.
	//
	// It is this file's rather than the surface's because the surface is never
	// asked: WithConfirmation is handed a minter, and the absence of one is a
	// fact about the injector. Mounting without the loop instead would be the
	// quiet version — a mailer registered and never called, and a form whose
	// signups go straight to waiting on a deployment that meant to confirm them.
	ErrWaitlistConfirmationNeedsLinks = platformerrors.New(
		"a waitlist confirmation mailer is registered and no links minter is configured to mint its links",
	)

	// ErrPasskeysNeedSignIn is a service with a *passkeys.Service and no
	// *signin.Service, so there is nothing to mint the token a finished
	// passkey login answers with.
	//
	// It is refused rather than read as an absence for the reason
	// ErrWaitlistConfirmationNeedsLinks is: the passkeys block is the
	// deployment saying it wants passkeys, and leaving the surface unmounted
	// would be a configured feature that quietly is not there.
	ErrPasskeysNeedSignIn = platformerrors.New(
		"a passkey service is configured and no sign-in service is configured to issue its tokens",
	)

	// ErrUnknownSurface is a Transports.Skip naming a surface this package does
	// not mount.
	//
	// It is refused rather than ignored because a skip is how an application
	// replaces a surface, and a misspelled one leaves the platform's surface
	// mounted beside the application's replacement — which the gRPC server
	// reports as a duplicate service somewhere far from the typo, and the
	// router as a collision on a route the application thought it owned.
	ErrUnknownSurface = platformerrors.New("Transports.Skip names a surface RegisterTransports does not mount")
)

// Surface names one of the surfaces RegisterTransports mounts, for
// Transports.Skip. Its value is the name the surface reports under in a
// startup failure.
type Surface string

// The surfaces RegisterTransports mounts, gRPC then HTTP.
const (
	SurfaceAudit         Surface = "audit"
	SurfaceBilling       Surface = "billing"
	SurfaceComments      Surface = "comments"
	SurfaceIdentity      Surface = "identity"
	SurfaceIssueReports  Surface = "issue reports"
	SurfaceMediaUploads  Surface = "media uploads"
	SurfaceNotifications Surface = "notifications"
	SurfaceOAuth2Clients Surface = "oauth2 clients"
	SurfacePasskeys      Surface = "passkeys"
	SurfacePasswordReset Surface = "password reset"
	SurfaceSettings      Surface = "settings"
	SurfaceSignIn        Surface = "sign-in"
	SurfaceWaitlists     Surface = "waitlists"
	SurfaceWebhooks      Surface = "webhooks"

	SurfaceDataPrivacy   Surface = "data privacy"
	SurfaceMediaRegistry Surface = "media registry"
	SurfaceOAuth2Server  Surface = "oauth2 server"
	SurfaceOperations    Surface = "operations"
)

// surfaces is every Surface, which is what a Skip entry is checked against.
var surfaces = map[Surface]bool{
	SurfaceAudit:         true,
	SurfaceBilling:       true,
	SurfaceComments:      true,
	SurfaceIdentity:      true,
	SurfaceIssueReports:  true,
	SurfaceMediaUploads:  true,
	SurfaceNotifications: true,
	SurfaceOAuth2Clients: true,
	SurfacePasskeys:      true,
	SurfacePasswordReset: true,
	SurfaceSettings:      true,
	SurfaceSignIn:        true,
	SurfaceWaitlists:     true,
	SurfaceWebhooks:      true,
	SurfaceDataPrivacy:   true,
	SurfaceMediaRegistry: true,
	SurfaceOAuth2Server:  true,
	SurfaceOperations:    true,
}

// SurfaceOptions are an application's own options for each mounted surface,
// passed through to that surface's constructor.
//
// They are the one door for configuring a mounted surface beyond the seams
// Transports names, and the reason Transports stops at the seams it has: a
// field per seam made every surface's configuration something to learn twice,
// once on the surface and once here, and a seam this struct had not yet
// mirrored was a seam the automatic mount could not set. Here the surface's
// own Option is the only spelling.
//
// Each slice is applied after everything RegisterTransports supplies — the
// observability pillars, the seams derived from the extractor, the
// authorizers, the grants extractor and whatever a config block contributes —
// so an option here overrides the platform's for the same setting, which is
// the reading every surface's Options already take of a later option.
//
// The surfaces that read the tenant, and dataprivacy, derive a resolver from
// Transports.Extractor and, for the former, Transports.TenantOf. A surface
// given options here is not refused for lacking either: the derivation is
// skipped, and the surface is built from what the application passed. If that
// names no resolver either, the surface refuses in its own words, so leaving
// the seam out opens nothing.
type SurfaceOptions struct {
	Audit         []auditgrpc.Option
	Billing       []billinggrpc.Option
	Comments      []commentsgrpc.Option
	Identity      []identitygrpc.Option
	IssueReports  []issuereportsgrpc.Option
	MediaUploads  []mediaregistrygrpc.Option
	Notifications []notificationsgrpc.Option
	OAuth2Clients []oauth2clientsgrpc.Option
	Passkeys      []passkeysgrpc.Option
	PasswordReset []passwordresetgrpc.Option
	Settings      []settingsgrpc.Option
	SignIn        []signingrpc.Option
	Waitlists     []waitlistsgrpc.Option
	Webhooks      []webhooksgrpc.Option

	DataPrivacy   []dataprivacyhttp.Option
	MediaRegistry []mediaregistryhttp.Option
	Operations    []operationshttp.Option
}

// Transports is what a mounted surface needs and a Config cannot carry.
//
// Two required seams and three optional ones, and they are the whole of the
// "you keep the policy" bargain. Every surface this module ships is otherwise
// deterministic from the config: the store it reads, the client it reads on,
// the observability it reports through. What is not deterministic is who is
// calling, which rows they may act on, and — for a deployment whose directory
// and whose tenant are not the same thing — which tenant a request is against.
// Those are values a caller constructs rather than anything an environment
// variable can express, so they arrive here, as arguments, and nothing about
// them is decided by this package.
//
// It is one struct rather than a variadic because the option slot on these
// constructors already belongs to WithLogger, WithTracerProvider and
// WithMetricsProvider, and because absent-means-noop is the wrong reading for
// an authorizer: a surface with no rule about which rows a caller may touch is
// a surface that mounts open, so a missing one is a startup error rather than a
// default.
//
// The fields naming a seam are the ones the surfaces mounted when this struct
// was written, and the list is closed. A surface joins the automatic mount only
// if every seam it has carries a default; a required seam of a surface added
// since — or any seam of an existing one that no field names — arrives through
// Options, in the surface's own Option type, and a surface configured without
// it fails the startup in its own words. A surface an application would rather
// build itself is named in Skip.
//
// HTTPEnforcer is the one field added since, and it is not one surface's seam:
// it is the HTTP half of the authorization every surface answers to, shared by
// the three HTTP surfaces that declare route permissions the way Grants is
// shared by the gRPC ones, so a consumer names it once rather than three times
// in Options.
type Transports struct {
	// Extractor is how every mounted surface tells who is calling.
	//
	// One extractor for all of them, which is the argument callers' own
	// documentation makes: a deployment has one authentication interceptor and
	// one notion of a caller, so a surface that reads a narrower seam has that
	// seam derived from this rather than asking for a second adapter that reads
	// the same three facts.
	//
	// The one fact that derivation cannot always get right is the tenant — see
	// TenantOf.
	Extractor callers.PrincipalExtractor

	// TenantOf reads the tenant a caller's rows belong to off the caller, for
	// the surfaces that mean the tenant rather than the directory: audit's
	// ScopeResolver, operations' OwnerResolver and the caller scope of
	// mediaregistry's two surfaces. A Transports mounting any of them without one is
	// ErrNilTenantOf at startup, and a deployment whose tenant is its directory
	// names DirectoryTenant here.
	//
	// It exists because a principal has two scopes and callers.Principal names
	// one. Principal.Scope() is the directory the caller is in — identity's
	// gRPC surface reads it exactly that way — and for a deployment with one
	// directory it is tenancy.Global(). A deployment whose tenant is the
	// account rather than the directory therefore files its audit entries under
	// tenancy.Of(accountID) and, without this field, reads them back under
	// Global(), which sees none of them. Making Principal.Scope() answer with
	// the account instead would break identity.
	//
	// It is handed the principal Extractor already found, and not the request
	// context, and that is the point of its shape. For these surfaces
	// the resolved scope is the authorization — no comparison follows it — so
	// a resolver that could read the context could read a header, and a
	// tenant named by the client is a cross-tenant read. Taking the principal
	// means this package still refuses a request with nobody on it before the
	// application is asked anything, and the application's only question is
	// which of an authenticated caller's facts is their tenant. A scope it
	// answers that names nothing is refused with tenancy.ErrNoScope rather
	// than carried to the store.
	//
	// It is a field rather than a method on callers.Principal because that
	// interface is primitives-go's, and which of a principal's facts is the
	// tenant is a question only the application can answer. There is no
	// default: a reading that is right for one deployment and silently wrong
	// for another is the failure this field exists to remove.
	//
	// It does not touch dataprivacy's subject resolver, which reads a user and
	// not a scope.
	TenantOf func(principal callers.Principal) (tenancy.Scope, error)

	// Authorizers are the per-surface rules about which rows a caller who may
	// make this call may make it against.
	Authorizers Authorizers

	// Grants is what the caller may do, for every gRPC surface that asks it
	// inside a handler rather than at the method.
	//
	// Each of them decides something off it that no method grant can reach,
	// because it depends on the request rather than on the RPC — whether a
	// read that sent include_archived receives the archived rows, on each
	// surface whose paged reads have any; on
	// settings, whether a write names a setting the catalog reserved to
	// administrators; and on identity, whether a caller the row check
	// refused holds the operator permission that lets them past it. That
	// last is armed only where an audit.Recorder resolves as well, since
	// every operator admission is recorded and one nobody can see is none.
	// Audit's operator read is not among them: it is a service of its own,
	// AuditAdministrationService, gated at the method like any other. It is the same authorization.GrantsExtractor a consumer
	// hands primitives-go's authorization/grpc enforcer, so the interceptor
	// that decides whether a method may be called and the handler that decides
	// which rows the answer may hold read one authority and cannot disagree.
	//
	// Nil is deliberately today's behavior rather than a startup error. Each
	// surface's own absence rule is the fail-closed one — include_archived is
	// cleared and every reserved write is refused — which is a server that
	// withholds features rather than one that mounts open, so a deployment
	// that has not wired grants loses nothing it was relying on. It is passed
	// to those surfaces only when it is set, for the reason an optional
	// authorizer is: the absence rule is theirs.
	Grants authorization.GrantsExtractor

	// HTTPEnforcer checks the permission each route of three of the HTTP
	// surfaces requires — dataprivacy, mediaregistry and operations, each of
	// which declares its routes' permissions in a Permissions map beside them.
	// The authorization server declares none: it authenticates its own
	// callers, a client by its secret and a person at /authorize. It is
	// the HTTP counterpart of the authorization interceptor a consumer installs
	// on the gRPC server, and is built the same way: over the same grants
	// extractor, by the consumer, with primitives-go's authorization/http.
	//
	// Nil does not mount the surfaces open. Each refuses every route its
	// Permissions names, as 403, and serves only the routes it exports as
	// reached on the caller's own standing — a privacy subject following their
	// own export, the confirmation link in their mail. That is the reading a
	// fail-closed gRPC enforcer gives a method nobody declared, and it is why
	// this is a separate field from Grants rather than something this package
	// builds from it: nil Grants withholds features and serves the rest, and
	// one field meaning both would mean opposite things on the two transports.
	HTTPEnforcer *authzhttp.Enforcer

	// Registrations are the application's own gRPC services, mounted on the
	// same server as the platform's.
	//
	// They arrive here rather than through the injector because
	// RegisterTransports owns the []grpcserver.RegistrationFunc key — see the
	// function. It is the same door WithRunners is for an application's own
	// background loops, and for the same reason: a composition root that mounts
	// the platform's surfaces has to leave somewhere for the surfaces it will
	// never know about.
	//
	// They are appended after the platform's, so a service name declared on
	// both fails on this one, which is the half the application can move.
	Registrations []grpcserver.RegistrationFunc

	// Options are the application's own options for each surface, passed
	// through to its constructor after the platform's. They are how a mounted
	// surface is configured past the seams above, and the reason no further
	// seam is mirrored here — see SurfaceOptions.
	Options SurfaceOptions

	// Skip names surfaces left unmounted even when everything they are built
	// from resolves, so an application can replace one: it mounts its own
	// through Registrations or on the router, over the same store. A name this
	// package does not mount is ErrUnknownSurface at startup.
	Skip []Surface
}

// Authorizers is the second seam, one field per surface that takes one.
//
// Some are required: a surface configured without them does not mount open, it
// fails the startup that configured it, under the surface's own sentinel rather
// than one invented here. The rest have a default their own package documents and
// a nil field leaves that default in place, because the default is a decision
// that package already made and this one has no standing to overrule.
//
// They are separate fields rather than one interface because the surfaces
// declare them separately, and they declare them separately because they ask
// different questions: whether a caller may act on an account is not the
// question of whether they may act on a comment. What they share is the
// currency — every method here takes a callers.Principal.
type Authorizers struct {
	// BillingAccounts decides which accounts a caller may read and write the
	// ledger of. Required wherever billing.Store is registered.
	BillingAccounts billinggrpc.AccountAuthorizer

	// CommentAuthors decides which authors a caller may write as. Optional;
	// comments/grpc defaults it to OwnCommentsOnly.
	CommentAuthors commentsgrpc.AuthorAuthorizer

	// IdentityTargets decides which users, accounts and invitations a caller
	// may act on. Optional; identity/grpc defaults it to a membership
	// authorizer built over the store it was given.
	IdentityTargets identitygrpc.TargetAuthorizer

	// IssueReports decides which reports and which reporters a caller may act
	// on. Required wherever issuereports.Store is registered.
	IssueReports issuereportsgrpc.ReportAuthorizer

	// MediaObjects decides which stored objects a caller may read, on both
	// media registry surfaces: the bytes from mediaregistry/http's serve route,
	// and the rows from mediaregistry/grpc's reads. Optional; both default it
	// to OwnerOnly.
	MediaObjects mediaregistryhttp.Entitlement

	// SettingsSubjects decides which subjects a caller may resolve and set
	// values for. Required wherever settings.Store is registered.
	SettingsSubjects settingsgrpc.SubjectAuthorizer

	// WaitlistSignups decides which signups a caller may withdraw. Required
	// wherever waitlists.Store is registered.
	WaitlistSignups waitlistsgrpc.SignupAuthorizer
}

// RegisterTransports mounts every gRPC and HTTP surface this module ships whose
// dependencies the injector can supply.
//
// It is a separate call from Register rather than a step inside it, for two
// reasons that point the same way. A consumer wiring stores only — a worker, a
// migration, a batch job — must not be made to supply authorizers it has no use
// for. And the seams are values the caller constructs, which is exactly what a
// Config is not: Register is a pure function of the configuration, and staying
// that way is what makes a service's composition readable off the config it
// booted with.
//
// # What mounts
//
// The gRPC surfaces — audit, oauth2clients, passkeys, passwordreset, signin,
// billing, comments, identity, issuereports, mediaregistry, notifications,
// settings, waitlists and webhooks — and four HTTP ones — dataprivacy,
// mediaregistry, the OAuth 2.1 authorization server and operations.
// sessions/http is not among them; see the package documentation for why.
//
// A surface mounts when everything it is built from resolves, and the reading
// of "resolves" is the one the rest of this package already uses: nobody
// registered one is an absence and contributes nothing, while one that was
// registered and cannot be built is an error naming the surface. That single
// rule is what makes a Config naming no billing mount no billing surface, and
// it is also what makes identity behave sensibly without a special case — its
// server is built over a service Register does not register, so it mounts for
// an application that registered one and stays absent for an application that
// did not. oauth2clients, passkeys, passwordreset and signin mount over
// services Register does register, from Config.OAuth2Clients,
// Config.Passkeys, Config.PasswordReset and Config.SignIn.
//
// # What it owns
//
// The []grpcserver.RegistrationFunc key, which grpcserver.RegisterGRPCServer
// builds the server from. There is no way to mount a gRPC surface without it
// and samber/do refuses a second provider for one type by panicking, so this
// call takes the key rather than racing a consumer for it. An injector that
// already holds one is reported as ErrGRPCRegistrationsAlreadyProvided at
// startup rather than as a panic in a composition root, and an application's
// own services join through Transports.Registrations.
//
// # What it does not do
//
// It registers no error mappers. errormappers.Register is the one door for
// those and Register already calls it, which is why nothing here is conditional
// on a surface having mounted. operations/http.New installs its own HTTP mapper
// as it has always done — that is the module's single standing exception,
// tripped by mounting the surface rather than added here, and nothing follows
// it.
//
// It declares no authorization requirements. Every gRPC surface ships a
// Require(*authzgrpc.RequirementsBuilder) naming the permission each of its
// methods needs, and those are still the consumer's to install, beside the
// interceptor that enforces them. The HTTP surfaces install their own
// requirements, route by route, and are handed the consumer's enforcer to check
// them with through Transports.HTTPEnforcer. This is the same thing
// identitycfg.RegisterServer already says about the one surface it builds: a
// mount is not a policy.
//
// Registration is lazy, as Register's is. Nothing here is built until something
// invokes it, which for a service built through New is at startup.
func RegisterTransports(i do.Injector, t *Transports) {
	// A nil Transports is a caller who has named no seams, which is exactly
	// what a zero one is. It is not an error on the way in: whether it is a
	// mistake depends on whether anything was configured to mount, and that is
	// not knowable until something invokes.
	if t == nil {
		t = &Transports{}
	}

	// Read before anything is registered, because what this reports on is the
	// state of the injector as the caller handed it over. A consumer's own
	// []grpcserver.RegistrationFunc registered afterwards panics on its own
	// call, which is the right place for it to.
	taken := providedNames(i)
	_, contested := taken[do.NameOf[[]grpcserver.RegistrationFunc]()]

	do.Provide(i, func(i do.Injector) (*mountedTransports, error) {
		if contested {
			return nil, ErrGRPCRegistrationsAlreadyProvided
		}

		return mountTransports(i, t)
	})

	if contested {
		return
	}

	do.Provide(i, func(i do.Injector) ([]grpcserver.RegistrationFunc, error) {
		mounted, err := do.Invoke[*mountedTransports](i)
		if err != nil {
			return nil, err
		}

		return mounted.registrations, nil
	})
}

// mountedTransports is what one RegisterTransports call mounted.
//
// It is a value rather than a slot on Service because the gRPC half has to be
// available to grpcserver.RegisterGRPCServer's own provider, which resolves a
// []grpcserver.RegistrationFunc and knows nothing about this package. The HTTP
// half is already on the router by the time this exists — mounting is what
// building it did — so the names are all there is left to hold.
type mountedTransports struct {
	// registrations is the platform's surfaces followed by the application's,
	// in the order the gRPC server will register them.
	registrations []grpcserver.RegistrationFunc

	// names is what mounted, in mount order, for a startup log and for the
	// tests that pin which surfaces a configuration produces.
	names []string
}

// mountTransports builds every surface whose dependencies i can supply.
//
// The order is the two lanes, gRPC then HTTP, alphabetical within each. It
// decides nothing — no surface here reads another — and it is fixed so that the
// OpenAPI document the HTTP lane accumulates and the names a test asserts are
// the same on every boot.
func mountTransports(i do.Injector, t *Transports) (*mountedTransports, error) {
	pillars, err := observability.InvokePillars(i)
	if err != nil {
		return nil, platformerrors.Wrap(err, "invoking the observability pillars for the transport surfaces")
	}

	skip := make(map[Surface]bool, len(t.Skip))
	for _, surface := range t.Skip {
		if !surfaces[surface] {
			return nil, platformerrors.Wrapf(ErrUnknownSurface, "%q", surface)
		}

		skip[surface] = true
	}

	m := &mount{i: i, pillars: pillars, t: t, skip: skip}

	m.audit()
	m.billing()
	m.comments()
	m.identity()
	m.issueReports()
	m.mediaUploads()
	m.notifications()
	m.oauth2Clients()
	m.passkeys()
	m.passwordReset()
	m.settings()
	m.signIn()
	m.waitlists()
	m.webhooks()

	m.httpLane()

	if m.err != nil {
		return nil, m.err
	}

	// After the platform's, so that a service name declared on both ends fails
	// on the application's — which is the one of the two its author can move.
	m.registrations = append(m.registrations, t.Registrations...)

	return &mountedTransports{registrations: m.registrations, names: m.names}, nil
}

// mount is one pass over the surfaces, carrying what they are all built from
// and remembering the first failure — so each surface below reads as the list
// of what it needs rather than as a stack of identical error checks. It is the
// same shape resolver has, for the same reason.
type mount struct {
	i   do.Injector
	err error

	pillars *observability.Pillars

	t *Transports

	// skip is Transports.Skip as a set, already checked against surfaces.
	skip map[Surface]bool

	registrations []grpcserver.RegistrationFunc
	names         []string
}

// mounting reports whether surface is to be mounted at all: false for a
// surface the application named in Skip, and for every surface once one has
// failed.
func (m *mount) mounting(surface Surface) bool {
	return m.err == nil && !m.skip[surface]
}

// need resolves T, reporting absence as false rather than as a failure.
//
// It is resolve's body against a value rather than a callback, because a
// surface needs several of these before it can be built and a chain of
// callbacks nested five deep is not a list of dependencies anybody can read.
// The distinction it draws is the same one: nobody registered one is an
// absence, and one that was registered and cannot be built is an error.
func need[T any](m *mount) (T, bool) {
	var zero T

	if m.err != nil {
		return zero, false
	}

	v, err := injection.InvokeOptional[T](m.i)
	if err != nil {
		m.err = platformerrors.Wrapf(err, "invoking %s", do.NameOf[T]())

		return zero, false
	}

	if isAbsent(v) {
		return zero, false
	}

	return v, true
}

// caller returns the extractor, refusing a surface that has arrived at the
// point of needing one and has none.
//
// The check is here rather than at the top of mountTransports so that a
// Transports with no extractor is only a failure for a service that configured
// something to mount. A consumer who calls this and configures no surfaces has
// said nothing wrong.
func (m *mount) caller(surface Surface) (callers.PrincipalExtractor, bool) {
	if m.t.Extractor == nil {
		m.err = platformerrors.Wrapf(ErrNilPrincipalExtractor, "mounting the %s surface", surface)

		return nil, false
	}

	return m.t.Extractor, true
}

// tenantOf returns Transports.TenantOf, refusing a surface that reads the
// tenant and has no reader for it.
//
// Like caller, the check is made where a surface needs it, so that only a
// service that mounts one of those surfaces is asked for one.
func (m *mount) tenantOf(surface Surface) (func(callers.Principal) (tenancy.Scope, error), bool) {
	if m.t.TenantOf == nil {
		m.err = platformerrors.Wrapf(ErrNilTenantOf, "mounting the %s surface", surface)

		return nil, false
	}

	return m.t.TenantOf, true
}

// derivation returns the extractor, and for a surface that reads the tenant
// Transports.TenantOf, that a surface's derived resolver is built from, and
// whether one is to be derived at all.
//
// own is whether the application passed the surface options of its own. With
// none, the derivation is the surface's only resolver, so a missing seam is
// refused here as it always was. With some, a missing seam means no
// derivation: the surface is built from the application's options, and if
// those name no resolver either the surface refuses in its own words. A
// derivation that is made goes first, so the application's resolver, if it
// names one, overrides it.
func (m *mount) derivation(surface Surface, own, readsTenant bool) (
	extract callers.PrincipalExtractor,
	tenantOf func(callers.Principal) (tenancy.Scope, error),
	derive bool,
) {
	if own && (m.t.Extractor == nil || (readsTenant && m.t.TenantOf == nil)) {
		return nil, nil, false
	}

	extract, ok := m.caller(surface)
	if !ok {
		return nil, nil, false
	}

	if !readsTenant {
		return extract, nil, true
	}

	tenantOf, ok = m.tenantOf(surface)
	if !ok {
		return nil, nil, false
	}

	return extract, tenantOf, true
}

// fail records a surface that could not be built, naming it.
//
// The surface's own sentinel is underneath, which is the whole intent of
// passing a nil authorizer through rather than checking it here: a service that
// configured billing and supplied no AccountAuthorizer is told so by
// billing/grpc, in billing's words.
func (m *mount) fail(surface Surface, err error) {
	m.err = platformerrors.Wrapf(err, "building the %s transport surface", surface)
}

// mountedGRPC records a built gRPC surface and the registration that will put
// it on the server.
func (m *mount) mountedGRPC(surface Surface, register grpcserver.RegistrationFunc) {
	m.registrations = append(m.registrations, register)
	m.names = append(m.names, string(surface)+" gRPC")
}

// mountedHTTP records a surface that has put its own routes on the router.
func (m *mount) mountedHTTP(surface Surface) {
	m.names = append(m.names, string(surface)+" HTTP")
}

// httpLane mounts the HTTP surfaces, having first established that the
// router they share is not already carrying somebody else's failure.
//
// The check is the lane's rather than each surface's because it is answerable
// only once: routing.Router accumulates registration failures and Err joins
// them, so after the first surface mounts there is no telling an application's
// duplicate route from a platform surface's. Asking before any of them mount is
// the only moment the answer is attributable, and refusing on it is what keeps
// routesLanded below able to name the surface that caused what it finds.
func (m *mount) httpLane() {
	if !m.routerClean() {
		return
	}

	m.dataPrivacy()
	m.mediaRegistry()
	m.oauth2Server()
	m.operations()
}

// routerClean reports whether the router the HTTP surfaces will mount onto
// arrived without a failure already recorded on it.
//
// A router nobody registered is not a failure: it is an absence, and each
// surface below reports it as its own by resolving nothing. What is a failure
// is a router that exists and is already broken, because routing.Router's own
// documentation says to check Err before serving and nothing between here and
// Serve does — so a lane that mounted over it would leave the process serving a
// route that is quietly not there, with the reason recorded on a value nobody
// reads again.
func (m *mount) routerClean() bool {
	router, ok := need[*routing.Router](m)
	if !ok {
		return m.err == nil
	}

	if err := router.Err(); err != nil {
		m.err = platformerrors.Join(ErrRouterAlreadyFailed, err)

		return false
	}

	return true
}

// routesLanded reports whether the routes a surface just put on the router were
// accepted, and records the failure if they were not.
//
// routing.Router accumulates its registration failures rather than returning
// them — a pattern that collides with one already there is a route that is
// quietly not on the server — and its own documentation says to check before
// serving. Nothing between here and Serve does, so this is that check, drawn
// per surface so the failure names the one that caused it.
//
// It can name one because routerClean has already refused a router that arrived
// dirty, and because the first surface to fail stops the lane: whatever Err
// reports here was put there by the surface that just mounted.
func (m *mount) routesLanded(surface Surface, router *routing.Router) bool {
	if err := router.Err(); err != nil {
		m.fail(surface, err)

		return false
	}

	return true
}

// deriveScope reads the tenant a request is against off the principal on the
// context.
//
// It serves audit's ScopeResolver and operations' OwnerResolver, which are the
// same function type under two names because they ask the same question of the
// same value. Neither package may say so — they are siblings, not a hierarchy —
// so this is where the one answer is written.
//
// tenantOf is Transports.TenantOf. The principal is found here first, so a
// request with nobody on it is refused before an application's resolver is
// asked anything. See that field for why the directory and the tenant are
// different questions.
func deriveScope(
	extract callers.PrincipalExtractor,
	tenantOf func(callers.Principal) (tenancy.Scope, error),
) func(context.Context) (tenancy.Scope, error) {
	return func(ctx context.Context) (tenancy.Scope, error) {
		principal, ok := extract(ctx)
		if !ok {
			return tenancy.Global(), ErrNoPrincipal
		}

		return tenantScope(principal, tenantOf)
	}
}

// DirectoryTenant is the Transports.TenantOf of a deployment whose tenant is
// the directory its callers are in: it reads Principal.Scope().
//
// It is exported so that the choice is spelled at the composition root rather
// than made by leaving a field nil. For a deployment with one directory it
// answers tenancy.Global() for everybody, which is right when the rows the
// tenant-reading surfaces serve were filed under it.
func DirectoryTenant(principal callers.Principal) (tenancy.Scope, error) {
	return principal.Scope(), nil
}

// tenantScope is the application's reading of the tenant of a principal
// already found, validated.
func tenantScope(principal callers.Principal, tenantOf func(callers.Principal) (tenancy.Scope, error)) (tenancy.Scope, error) {
	scope, err := tenantOf(principal)
	if err != nil {
		return tenancy.Scope{}, err
	}

	if err = scope.Validate(); err != nil {
		return tenancy.Scope{}, platformerrors.Wrap(err, "the application's tenant for this principal")
	}

	return scope, nil
}

// deriveOwners reads the owners whose operations a caller may follow: the
// tenant the request is against, and the person making it.
//
// Two because operations are started under both. An application's own work is
// owned by its tenant — the reading deriveScope gives, and the one this mount
// used alone until a privacy request's progress link answered its own subject
// 404 — and dataprivacy starts its operations owned by the person the request
// is about, the same identifier deriveSubject reads. A caller holds both and
// nothing else, so a colleague in the same tenant still cannot follow
// somebody's export.
func deriveOwners(
	extract callers.PrincipalExtractor,
	tenantOf func(callers.Principal) (tenancy.Scope, error),
) func(context.Context) ([]tenancy.Scope, error) {
	return func(ctx context.Context) ([]tenancy.Scope, error) {
		principal, ok := extract(ctx)
		if !ok {
			return nil, ErrNoPrincipal
		}

		tenant, err := tenantScope(principal, tenantOf)
		if err != nil {
			return nil, err
		}

		owners := []tenancy.Scope{tenant}
		if person := principal.UserID(); person != "" {
			owners = append(owners, tenancy.Of(person))
		}

		return owners, nil
	}
}

// deriveSubject reads the person a privacy request is about off the principal.
//
// The type is SubjectUser because a principal is a person: every other
// SubjectType names a thing this extractor has no way to be holding. An
// application whose requests are about a third kind of subject resolves them
// itself, which is what dataprivacy/http's own option is for.
func deriveSubject(extract callers.PrincipalExtractor) func(context.Context) (dataprivacy.Subject, error) {
	return func(ctx context.Context) (dataprivacy.Subject, error) {
		principal, ok := extract(ctx)
		if !ok {
			return dataprivacy.Subject{}, ErrNoPrincipal
		}

		return dataprivacy.Subject{ID: principal.UserID(), Type: dataprivacy.SubjectUser}, nil
	}
}

// deriveMediaCaller reads mediaregistry's two-field caller off the principal.
//
// The identifier is the principal's and the scope is the tenant's, which is why
// this takes Transports.TenantOf rather than reading Principal.Scope() itself:
// mediaregistry documents Caller.Scope as "the tenant the request is being made
// in", and an object filed under an account is not found under a directory.
func deriveMediaCaller(
	extract callers.PrincipalExtractor,
	tenantOf func(callers.Principal) (tenancy.Scope, error),
) func(context.Context) (mediaregistryhttp.Caller, error) {
	return func(ctx context.Context) (mediaregistryhttp.Caller, error) {
		principal, ok := extract(ctx)
		if !ok {
			return mediaregistryhttp.Caller{}, ErrNoPrincipal
		}

		scope, err := tenantScope(principal, tenantOf)
		if err != nil {
			return mediaregistryhttp.Caller{}, err
		}

		return mediaregistryhttp.Caller{PrincipalID: principal.UserID(), Scope: scope}, nil
	}
}

// audit mounts the audit log's read surface.
//
// It is the one gRPC surface that takes no extractor: it reads a scope and
// nothing else about a caller, so what it declares is a ScopeResolver, and that
// resolver is derived from the extractor here.
func (m *mount) audit() {
	if !m.mounting(SurfaceAudit) {
		return
	}

	reader, ok := need[audit.Reader](m)
	if !ok {
		return
	}

	client, ok := need[database.Client](m)
	if !ok {
		return
	}

	opts := []auditgrpc.Option{auditgrpc.WithPillars(m.pillars)}

	extract, tenantOf, derive := m.derivation(SurfaceAudit, len(m.t.Options.Audit) > 0, true)
	if m.err != nil {
		return
	}

	if derive {
		opts = append(opts, auditgrpc.WithScopeResolver(deriveScope(extract, tenantOf)))
	}

	// AuditAdministrationService, the operator's read of every tenant's log,
	// is armed by the recorder each such read is filed through. Who may call
	// it is the authorization interceptor's question, asked of the method
	// against audit/grpc's Permissions; without a recorder the server answers
	// those methods Unimplemented.
	recorder, found := need[audit.Recorder](m)
	if m.err != nil {
		return
	}

	if found && extract != nil {
		opts = append(opts, auditgrpc.WithOperatorRecorder(recorder, extract))
	}

	srv, err := auditgrpc.NewServer(reader, client, append(opts, m.t.Options.Audit...)...)
	if err != nil {
		m.fail(SurfaceAudit, err)

		return
	}

	m.mountedGRPC(SurfaceAudit, srv.RegisterOn)
}

// billing mounts the ledger surface. Its authorizer is required, and a nil one
// travels to the constructor so the refusal is billing's own.
func (m *mount) billing() {
	if !m.mounting(SurfaceBilling) {
		return
	}

	store, ok := need[billing.Store](m)
	if !ok {
		return
	}

	client, ok := need[database.Client](m)
	if !ok {
		return
	}

	extract, ok := m.caller(SurfaceBilling)
	if !ok {
		return
	}

	opts := []billinggrpc.Option{billinggrpc.WithPillars(m.pillars)}
	if m.t.Grants != nil {
		opts = append(opts, billinggrpc.WithGrantsExtractor(m.t.Grants))
	}

	srv, err := billinggrpc.NewServer(store, client, extract, m.t.Authorizers.BillingAccounts, append(opts, m.t.Options.Billing...)...)
	if err != nil {
		m.fail(SurfaceBilling, err)

		return
	}

	m.mountedGRPC(SurfaceBilling, srv.RegisterOn)
}

// comments mounts the comment surface. Its authorizer is optional, so a nil one
// is left out rather than passed, and comments/grpc's own default stands.
func (m *mount) comments() {
	if !m.mounting(SurfaceComments) {
		return
	}

	store, ok := need[comments.Store](m)
	if !ok {
		return
	}

	client, ok := need[database.Client](m)
	if !ok {
		return
	}

	extract, ok := m.caller(SurfaceComments)
	if !ok {
		return
	}

	opts := []commentsgrpc.Option{commentsgrpc.WithPillars(m.pillars)}
	if m.t.Authorizers.CommentAuthors != nil {
		opts = append(opts, commentsgrpc.WithAuthorAuthorizer(m.t.Authorizers.CommentAuthors))
	}

	if m.t.Grants != nil {
		opts = append(opts, commentsgrpc.WithGrantsExtractor(m.t.Grants))
	}

	srv, err := commentsgrpc.NewServer(store, client, extract, append(opts, m.t.Options.Comments...)...)
	if err != nil {
		m.fail(SurfaceComments, err)

		return
	}

	m.mountedGRPC(SurfaceComments, srv.RegisterOn)
}

// identity mounts the directory surface.
//
// Its service is a dependency like any other here, and Register does not
// register one — so a Config naming Identity mounts this surface only for an
// application that built the service itself. That is the absence rule doing its
// job rather than a gap in it: a surface over half a directory is not a surface.
func (m *mount) identity() {
	if !m.mounting(SurfaceIdentity) {
		return
	}

	svc, ok := need[*identity.Service](m)
	if !ok {
		return
	}

	store, ok := need[identity.Store](m)
	if !ok {
		return
	}

	client, ok := need[database.Client](m)
	if !ok {
		return
	}

	extract, ok := m.caller(SurfaceIdentity)
	if !ok {
		return
	}

	opts := []identitygrpc.Option{identitygrpc.WithPillars(m.pillars)}

	// The config block is what Register provided for Config.Identity, and the
	// server half of it — the invitation lifetimes and whether a sender gets
	// the token back — is read here, where the server is built, through the
	// same ServerOptions identitycfg.NewServer reads. Absent, the server's own
	// defaults stand.
	if cfg, found := need[*identitycfg.Config](m); found {
		opts = append(opts, cfg.ServerOptions()...)
	} else if m.err != nil {
		return
	}

	if m.t.Authorizers.IdentityTargets != nil {
		opts = append(opts, identitygrpc.WithTargetAuthorizer(m.t.Authorizers.IdentityTargets))
	}

	// The operator bypass past the row check: the grants that say who holds
	// an operator permission, and the recorder every admission is filed
	// through, which is what arms it. Either absent, the row check's refusals
	// stand.
	if m.t.Grants != nil {
		opts = append(opts, identitygrpc.WithGrantsExtractor(m.t.Grants))

		recorder, found := need[audit.Recorder](m)
		if m.err != nil {
			return
		}

		if found {
			opts = append(opts, identitygrpc.WithOperatorRecorder(recorder))
		}
	}

	srv, err := identitygrpc.NewServer(svc, store, client, extract, append(opts, m.t.Options.Identity...)...)
	if err != nil {
		m.fail(SurfaceIdentity, err)

		return
	}

	m.mountedGRPC(SurfaceIdentity, srv.RegisterOn)
}

// issueReports mounts the report surface. Its authorizer is required.
func (m *mount) issueReports() {
	if !m.mounting(SurfaceIssueReports) {
		return
	}

	store, ok := need[issuereports.Store](m)
	if !ok {
		return
	}

	client, ok := need[database.Client](m)
	if !ok {
		return
	}

	extract, ok := m.caller(SurfaceIssueReports)
	if !ok {
		return
	}

	opts := []issuereportsgrpc.Option{issuereportsgrpc.WithPillars(m.pillars)}
	if m.t.Grants != nil {
		opts = append(opts, issuereportsgrpc.WithGrantsExtractor(m.t.Grants))
	}

	srv, err := issuereportsgrpc.NewServer(store, client, extract, m.t.Authorizers.IssueReports, append(opts, m.t.Options.IssueReports...)...)
	if err != nil {
		m.fail(SurfaceIssueReports, err)

		return
	}

	m.mountedGRPC(SurfaceIssueReports, srv.RegisterOn)
}

// mediaUploads mounts the media registry's resource surface, over the store and
// the upload manager the serve route is built from.
//
// It joins the automatic mount because every seam it has carries a default but
// one, and that one is derived here: its caller is mediaregistry/http's Caller,
// read off the principal and Transports.TenantOf exactly as the serve route's
// is, so an object uploaded here is found by the route that serves it. Its
// entitlement is Authorizers.MediaObjects, the serve route's, so the two
// surfaces cannot disagree about who may read an object.
func (m *mount) mediaUploads() {
	if !m.mounting(SurfaceMediaUploads) {
		return
	}

	store, ok := need[mediaregistry.Store](m)
	if !ok {
		return
	}

	client, ok := need[database.Client](m)
	if !ok {
		return
	}

	manager, ok := need[uploads.UploadManager](m)
	if !ok {
		return
	}

	opts := []mediaregistrygrpc.Option{mediaregistrygrpc.WithPillars(m.pillars)}

	extract, tenantOf, derive := m.derivation(SurfaceMediaUploads, len(m.t.Options.MediaUploads) > 0, true)
	if m.err != nil {
		return
	}

	if derive {
		opts = append(opts, mediaregistrygrpc.WithCallerResolver(deriveMediaCaller(extract, tenantOf)))
	}

	if m.t.Authorizers.MediaObjects != nil {
		opts = append(opts, mediaregistrygrpc.WithEntitlement(m.t.Authorizers.MediaObjects))
	}

	srv, err := mediaregistrygrpc.NewServer(store, client, manager, append(opts, m.t.Options.MediaUploads...)...)
	if err != nil {
		m.fail(SurfaceMediaUploads, err)

		return
	}

	m.mountedGRPC(SurfaceMediaUploads, srv.RegisterOn)
}

// notifications mounts the inbox and device surface. It takes two seams, and
// one registered store satisfies both — notificationscfg registers each as a
// narrowing of the same value.
func (m *mount) notifications() {
	if !m.mounting(SurfaceNotifications) {
		return
	}

	inbox, ok := need[notifications.Inbox](m)
	if !ok {
		return
	}

	registry, ok := need[notifications.Registry](m)
	if !ok {
		return
	}

	client, ok := need[database.Client](m)
	if !ok {
		return
	}

	extract, ok := m.caller(SurfaceNotifications)
	if !ok {
		return
	}

	opts := []notificationsgrpc.Option{notificationsgrpc.WithPillars(m.pillars)}
	if m.t.Grants != nil {
		opts = append(opts, notificationsgrpc.WithGrantsExtractor(m.t.Grants))
	}

	srv, err := notificationsgrpc.NewServer(inbox, registry, client, extract, append(opts, m.t.Options.Notifications...)...)
	if err != nil {
		m.fail(SurfaceNotifications, err)

		return
	}

	m.mountedGRPC(SurfaceNotifications, srv.RegisterOn)
}

// oauth2Clients mounts the client registry surface. It needs both the service
// and the store, which Config.OAuth2Clients registers together, and stays absent
// for a service that configured neither.
func (m *mount) oauth2Clients() {
	if !m.mounting(SurfaceOAuth2Clients) {
		return
	}

	svc, ok := need[*oauth2clients.Service](m)
	if !ok {
		return
	}

	store, ok := need[oauth2clients.Store](m)
	if !ok {
		return
	}

	client, ok := need[database.Client](m)
	if !ok {
		return
	}

	extract, ok := m.caller(SurfaceOAuth2Clients)
	if !ok {
		return
	}

	opts := []oauth2clientsgrpc.Option{oauth2clientsgrpc.WithPillars(m.pillars)}
	if m.t.Grants != nil {
		opts = append(opts, oauth2clientsgrpc.WithGrantsExtractor(m.t.Grants))
	}

	srv, err := oauth2clientsgrpc.NewServer(svc, store, client, extract, append(opts, m.t.Options.OAuth2Clients...)...)
	if err != nil {
		m.fail(SurfaceOAuth2Clients, err)

		return
	}

	m.mountedGRPC(SurfaceOAuth2Clients, srv.RegisterOn)
}

// settings mounts the settings surface. Its authorizer is required.
func (m *mount) settings() {
	if !m.mounting(SurfaceSettings) {
		return
	}

	store, ok := need[settings.Store](m)
	if !ok {
		return
	}

	client, ok := need[database.Client](m)
	if !ok {
		return
	}

	extract, ok := m.caller(SurfaceSettings)
	if !ok {
		return
	}

	opts := []settingsgrpc.Option{settingsgrpc.WithPillars(m.pillars)}
	if m.t.Grants != nil {
		opts = append(opts, settingsgrpc.WithGrantsExtractor(m.t.Grants))
	}

	srv, err := settingsgrpc.NewServer(store, client, extract, m.t.Authorizers.SettingsSubjects, append(opts, m.t.Options.Settings...)...)
	if err != nil {
		m.fail(SurfaceSettings, err)

		return
	}

	m.mountedGRPC(SurfaceSettings, srv.RegisterOn)
}

// passwordReset mounts the way back in for somebody who cannot sign in.
//
// It takes no principal extractor, and it is the only surface here that does not:
// every one of its RPCs is for a caller who has not signed in and cannot, so
// there is nobody to extract. Its scope resolver is left at the package's own
// default for sign-in's reason, with no exception to make — every RPC on it
// arrives with nobody on it, not just some of them.
//
// Config.PasswordReset registers the *passwordreset.Service this mounts over,
// and it stays absent for a service that configured none. What that service
// needs beyond a store — a Mailer, and the authenticator that hashes the
// password a reset writes — has no default this module could supply, which is a
// reason for no field rather than for no block: passwordresetcfg resolves both
// from the injector, and a block whose application registered neither fails at
// boot naming the one it wanted.
func (m *mount) passwordReset() {
	if !m.mounting(SurfacePasswordReset) {
		return
	}

	svc, ok := need[*passwordreset.Service](m)
	if !ok {
		return
	}

	opts := append([]passwordresetgrpc.Option{passwordresetgrpc.WithPillars(m.pillars)}, m.t.Options.PasswordReset...)

	srv, err := passwordresetgrpc.NewServer(svc, opts...)
	if err != nil {
		m.fail(SurfacePasswordReset, err)

		return
	}

	m.mountedGRPC(SurfacePasswordReset, srv.RegisterOn)
}

// passkeys mounts the passkey surface.
//
// Its scope resolver is left at passkeys/grpc's own default for the reason
// sign-in's is: the login half is reached before there is anybody to extract.
// A deployment running both names one resolver for the two through
// SurfaceOptions.
//
// Config.Passkeys registers the *passkeys.Service this mounts over, and it is
// built over the *signin.Service Config.SignIn registers, which is what mints
// a finished login's token. A passkey service with no sign-in service beside
// it fails the startup with ErrPasskeysNeedSignIn rather than staying absent.
func (m *mount) passkeys() {
	if !m.mounting(SurfacePasskeys) {
		return
	}

	svc, ok := need[*passkeys.Service](m)
	if !ok {
		return
	}

	issuer, ok := need[*signin.Service](m)
	if m.err != nil {
		return
	}

	if !ok {
		m.fail(SurfacePasskeys, ErrPasskeysNeedSignIn)

		return
	}

	client, ok := need[database.Client](m)
	if !ok {
		return
	}

	extract, ok := m.caller(SurfacePasskeys)
	if !ok {
		return
	}

	opts := append([]passkeysgrpc.Option{passkeysgrpc.WithPillars(m.pillars)}, m.t.Options.Passkeys...)

	srv, err := passkeysgrpc.NewServer(svc, client, issuer, extract, opts...)
	if err != nil {
		m.fail(SurfacePasskeys, err)

		return
	}

	m.mountedGRPC(SurfacePasskeys, srv.RegisterOn)
}

// signIn mounts the sign-in surface.
//
// Its scope resolver is left at signin/grpc's own default rather than derived:
// several of its RPCs are the ones a caller reaches before there is anybody to
// extract, so a resolver that refuses a request with no principal would refuse
// the act of signing in — and, since registration landed here, the act of
// finishing one.
//
// Config.SignIn registers the *signin.Service this mounts over, and it stays
// absent for a service that configured none.
func (m *mount) signIn() {
	if !m.mounting(SurfaceSignIn) {
		return
	}

	svc, ok := need[*signin.Service](m)
	if !ok {
		return
	}

	extract, ok := m.caller(SurfaceSignIn)
	if !ok {
		return
	}

	opts := []signingrpc.Option{signingrpc.WithPillars(m.pillars)}

	// The config block is what Register provided for Config.SignIn, and its
	// server half — whether the sign-up door is closed on the wire — is read
	// here through the ServerOptions any hand-built server reads, as
	// identity's is.
	if cfg, found := need[*signincfg.Config](m); found {
		opts = append(opts, cfg.ServerOptions()...)
	} else if m.err != nil {
		return
	}

	srv, err := signingrpc.NewServer(svc, extract, append(opts, m.t.Options.SignIn...)...)
	if err != nil {
		m.fail(SurfaceSignIn, err)

		return
	}

	m.mountedGRPC(SurfaceSignIn, srv.RegisterOn)
}

// waitlists mounts the signup surface. Its authorizer is required, and its
// scope resolver is left defaulted for the reason sign-in's is: the public
// signup page's RPCs arrive with nobody on them by design.
//
// The confirmation loop mounts when the application registered a
// waitlistsgrpc.ConfirmationMailer, which is presence as the switch for the
// reason every block here uses it: the mailer is the application's, so its
// being registered is the decision. It is built over the *links.Minter
// Config.Links registers, and a mailer with no minter to mint with fails the
// startup rather than mounting a form whose signups nothing could confirm —
// as does a minter that declares neither waitlist action, which is the
// surface's own refusal.
func (m *mount) waitlists() {
	if !m.mounting(SurfaceWaitlists) {
		return
	}

	store, ok := need[waitlists.Store](m)
	if !ok {
		return
	}

	client, ok := need[database.Client](m)
	if !ok {
		return
	}

	extract, ok := m.caller(SurfaceWaitlists)
	if !ok {
		return
	}

	opts := []waitlistsgrpc.Option{waitlistsgrpc.WithPillars(m.pillars)}
	if m.t.Grants != nil {
		opts = append(opts, waitlistsgrpc.WithGrantsExtractor(m.t.Grants))
	}

	// need reports an absence and a failed lookup alike as false, so each is
	// followed by a check of m.err: only an absence goes on to be judged.
	mailer, mailing := need[waitlistsgrpc.ConfirmationMailer](m)
	if m.err != nil {
		return
	}

	if mailing {
		minter, minting := need[*links.Minter](m)
		if m.err != nil {
			return
		}

		if !minting {
			m.fail(SurfaceWaitlists, ErrWaitlistConfirmationNeedsLinks)

			return
		}

		opts = append(opts, waitlistsgrpc.WithConfirmation(minter, mailer))
	}

	srv, err := waitlistsgrpc.NewServer(store, client, extract, m.t.Authorizers.WaitlistSignups, append(opts, m.t.Options.Waitlists...)...)
	if err != nil {
		m.fail(SurfaceWaitlists, err)

		return
	}

	m.mountedGRPC(SurfaceWaitlists, srv.RegisterOn)
}

// webhooks mounts the endpoint and subscription surface. It takes the
// dispatcher and the store beneath it, both of which Register registers
// together.
func (m *mount) webhooks() {
	if !m.mounting(SurfaceWebhooks) {
		return
	}

	dispatcher, ok := need[webhooks.Dispatcher](m)
	if !ok {
		return
	}

	store, ok := need[webhooks.Store](m)
	if !ok {
		return
	}

	client, ok := need[database.Client](m)
	if !ok {
		return
	}

	extract, ok := m.caller(SurfaceWebhooks)
	if !ok {
		return
	}

	opts := []webhooksgrpc.Option{webhooksgrpc.WithPillars(m.pillars)}
	if m.t.Grants != nil {
		opts = append(opts, webhooksgrpc.WithGrantsExtractor(m.t.Grants))
	}

	srv, err := webhooksgrpc.NewServer(dispatcher, store, client, extract, append(opts, m.t.Options.Webhooks...)...)
	if err != nil {
		m.fail(SurfaceWebhooks, err)

		return
	}

	m.mountedGRPC(SurfaceWebhooks, srv.RegisterOn)
}

// dataPrivacy mounts the subject access request surface.
//
// Its scope resolver is deliberately left at dataprivacy/http's default. A nil
// scope there reads as every confinement the subject appears in, which is what
// a person asking after their own data means; deriving one from the principal
// would narrow every privacy request to the tenant the caller happens to be
// acting in, which is a quieter answer than the one that was asked for.
func (m *mount) dataPrivacy() {
	if !m.mounting(SurfaceDataPrivacy) {
		return
	}

	svc, ok := need[dataprivacy.Service](m)
	if !ok {
		return
	}

	router, ok := need[*routing.Router](m)
	if !ok {
		return
	}

	opts := []dataprivacyhttp.Option{
		dataprivacyhttp.WithLogger(m.pillars.Logger),
		dataprivacyhttp.WithTracerProvider(m.pillars.TracerProvider),
		dataprivacyhttp.WithEnforcer(m.t.HTTPEnforcer),
	}

	extract, _, derive := m.derivation(SurfaceDataPrivacy, len(m.t.Options.DataPrivacy) > 0, false)
	if m.err != nil {
		return
	}

	if derive {
		opts = append(opts, dataprivacyhttp.WithSubjectResolver(deriveSubject(extract)))
	}

	handlers, err := dataprivacyhttp.New(svc, append(opts, m.t.Options.DataPrivacy...)...)
	if err != nil {
		m.fail(SurfaceDataPrivacy, err)

		return
	}

	// Mount includes the artifact route, so a subject can collect the export
	// they asked for; see dataprivacy/http's package documentation.
	handlers.Mount(router)

	if !m.routesLanded(SurfaceDataPrivacy, router) {
		return
	}

	m.mountedHTTP(SurfaceDataPrivacy)
}

// mediaRegistry mounts the object download surface. Its entitlement is
// optional, so a nil one leaves mediaregistry/http's OwnerOnly in place.
func (m *mount) mediaRegistry() {
	if !m.mounting(SurfaceMediaRegistry) {
		return
	}

	store, ok := need[mediaregistry.Store](m)
	if !ok {
		return
	}

	client, ok := need[database.Client](m)
	if !ok {
		return
	}

	manager, ok := need[uploads.UploadManager](m)
	if !ok {
		return
	}

	router, ok := need[*routing.Router](m)
	if !ok {
		return
	}

	opts := []mediaregistryhttp.Option{
		mediaregistryhttp.WithLogger(m.pillars.Logger),
		mediaregistryhttp.WithTracerProvider(m.pillars.TracerProvider),
		mediaregistryhttp.WithEnforcer(m.t.HTTPEnforcer),
	}

	extract, tenantOf, derive := m.derivation(SurfaceMediaRegistry, len(m.t.Options.MediaRegistry) > 0, true)
	if m.err != nil {
		return
	}

	if derive {
		opts = append(opts, mediaregistryhttp.WithCallerResolver(deriveMediaCaller(extract, tenantOf)))
	}

	if m.t.Authorizers.MediaObjects != nil {
		opts = append(opts, mediaregistryhttp.WithEntitlement(m.t.Authorizers.MediaObjects))
	}

	handler, err := mediaregistryhttp.New(store, client, manager, append(opts, m.t.Options.MediaRegistry...)...)
	if err != nil {
		m.fail(SurfaceMediaRegistry, err)

		return
	}

	handler.Mount(router)

	if !m.routesLanded(SurfaceMediaRegistry, router) {
		return
	}

	m.mountedHTTP(SurfaceMediaRegistry)
}

// oauth2Server mounts the OAuth 2.1 authorization server: its discovery
// document, /authorize, /token and /revoke, at the paths oauth2server fixes.
//
// It mounts when a *oauth2server.Server resolves, which Config.OAuth2Server
// registers. Building one needs the application's
// oauth2server.SubjectAuthenticator — how a deployment identifies a human is
// not something an environment variable says — and a block with none beside it
// fails the startup on the server that could not be built rather than staying
// absent, for the reason a passkeys block with no sign-in service does: the
// block is the deployment asking for the surface. An application that builds the server
// itself, over the registry's authserver.NewStore say, registers it under the
// same key and is mounted the same way.
//
// It takes no seam from Transports. The server authenticates its own callers —
// a client by its secret at /token and /revoke, a person through the
// authenticator and resolver it was built with at /authorize — so there is no
// extractor to derive anything from, and it declares no route permissions for
// HTTPEnforcer to check. Nor does it take options: everything the server is
// configured with is an option to oauth2server.NewServer, which is the
// registration's to pass, and Mount takes only middleware.
//
// An application that mounts the server itself names SurfaceOAuth2Server in
// Skip rather than putting the same routes on the router a second time.
func (m *mount) oauth2Server() {
	if !m.mounting(SurfaceOAuth2Server) {
		return
	}

	srv, ok := need[*oauth2server.Server](m)
	if !ok {
		return
	}

	router, ok := need[*routing.Router](m)
	if !ok {
		return
	}

	srv.Mount(router)

	if !m.routesLanded(SurfaceOAuth2Server, router) {
		return
	}

	m.mountedHTTP(SurfaceOAuth2Server)
}

// operations mounts the long-running operation surface.
//
// The watcher is resolved optionally and passed when it is there, because it is
// what gates the event stream: without one, operations/http mounts without its
// subscription route rather than mounting a subscription with nothing behind
// it.
func (m *mount) operations() {
	if !m.mounting(SurfaceOperations) {
		return
	}

	svc, ok := need[operations.Service](m)
	if !ok {
		return
	}

	router, ok := need[*routing.Router](m)
	if !ok {
		return
	}

	opts := []operationshttp.Option{
		operationshttp.WithLogger(m.pillars.Logger),
		operationshttp.WithTracerProvider(m.pillars.TracerProvider),
		operationshttp.WithEnforcer(m.t.HTTPEnforcer),
	}

	extract, tenantOf, derive := m.derivation(SurfaceOperations, len(m.t.Options.Operations) > 0, true)
	if m.err != nil {
		return
	}

	if derive {
		opts = append(opts, operationshttp.WithOwnersResolver(deriveOwners(extract, tenantOf)))
	}

	if watcher, watching := need[*operations.Watcher](m); watching {
		opts = append(opts, operationshttp.WithWatcher(watcher))
	}

	if m.err != nil {
		return
	}

	handlers, err := operationshttp.New(svc, append(opts, m.t.Options.Operations...)...)
	if err != nil {
		m.fail(SurfaceOperations, err)

		return
	}

	handlers.Mount(router)

	if !m.routesLanded(SurfaceOperations, router) {
		return
	}

	m.mountedHTTP(SurfaceOperations)
}

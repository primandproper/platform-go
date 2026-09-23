package service

import (
	"context"

	"github.com/primandproper/platform-go/v14/audit"
	auditgrpc "github.com/primandproper/platform-go/v14/audit/grpc"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	oauth2clientsgrpc "github.com/primandproper/platform-go/v14/authentication/oauth2clients/grpc"
	"github.com/primandproper/platform-go/v14/authentication/passwordreset"
	passwordresetgrpc "github.com/primandproper/platform-go/v14/authentication/passwordreset/grpc"
	"github.com/primandproper/platform-go/v14/authentication/signin"
	signingrpc "github.com/primandproper/platform-go/v14/authentication/signin/grpc"
	"github.com/primandproper/platform-go/v14/billing"
	billinggrpc "github.com/primandproper/platform-go/v14/billing/grpc"
	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/comments"
	commentsgrpc "github.com/primandproper/platform-go/v14/comments/grpc"
	"github.com/primandproper/platform-go/v14/dataprivacy"
	dataprivacyhttp "github.com/primandproper/platform-go/v14/dataprivacy/http"
	"github.com/primandproper/platform-go/v14/identity"
	identitygrpc "github.com/primandproper/platform-go/v14/identity/grpc"
	"github.com/primandproper/platform-go/v14/issuereports"
	issuereportsgrpc "github.com/primandproper/platform-go/v14/issuereports/grpc"
	"github.com/primandproper/platform-go/v14/mediaregistry"
	mediaregistryhttp "github.com/primandproper/platform-go/v14/mediaregistry/http"
	"github.com/primandproper/platform-go/v14/notifications"
	notificationsgrpc "github.com/primandproper/platform-go/v14/notifications/grpc"
	"github.com/primandproper/platform-go/v14/operations"
	operationshttp "github.com/primandproper/platform-go/v14/operations/http"
	"github.com/primandproper/platform-go/v14/settings"
	settingsgrpc "github.com/primandproper/platform-go/v14/settings/grpc"
	"github.com/primandproper/platform-go/v14/waitlists"
	waitlistsgrpc "github.com/primandproper/platform-go/v14/waitlists/grpc"
	"github.com/primandproper/platform-go/v14/webhooks"
	webhooksgrpc "github.com/primandproper/platform-go/v14/webhooks/grpc"

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
// There are only three, and that is the measure of how little this file
// decides: a surface that cannot be built refuses in its own words, under its
// own sentinel, and these are the failures no surface is in a position to see.
var (
	// ErrNilPrincipalExtractor is a Transports with a surface to mount and no
	// way to tell who is calling.
	//
	// It is refused here rather than passed along, unlike a nil authorizer,
	// because four of the surfaces take a narrower seam than an extractor and
	// this is where those are derived. Handing them a derivation over no
	// extractor would mount four surfaces that refuse every request, which is
	// the shape of hole this whole registration exists to close.
	ErrNilPrincipalExtractor = platformerrors.Wrap(
		platformerrors.ErrNilInputParameter,
		"nil principal extractor for the mounted transport surfaces",
	)

	// ErrNoPrincipal is a request to one of the four surfaces whose seam is
	// derived from the extractor, arriving with nobody on it.
	//
	// The surfaces that take the extractor directly answer this themselves, and
	// each words it for the RPC it refused. This one is for the derivation:
	// audit's scope, operations' owner, dataprivacy's subject and
	// mediaregistry's caller are each a reading of a principal, and there is no
	// reading of nobody.
	ErrNoPrincipal = platformerrors.New(
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
)

// Transports is what a mounted surface needs and a Config cannot carry.
//
// Two required seams and one optional third, and they are the whole of the "you
// keep the policy" bargain. Every surface this module ships is otherwise
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
	// the three surfaces that mean the tenant rather than the directory:
	// audit's ScopeResolver, operations' OwnerResolver and mediaregistry's
	// caller scope. Nil reads Principal.Scope(), which is what every consumer
	// had before this field existed.
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
	// context, and that is the point of its shape. For these three surfaces
	// the resolved scope is the authorization — no comparison follows it — so
	// a resolver that could read the context could read a header, and a
	// tenant named by the client is a cross-tenant read. Taking the principal
	// means this package still refuses a request with nobody on it before the
	// application is asked anything, and the application's only question is
	// which of an authenticated caller's facts is their tenant. A scope it
	// answers that names nothing is refused with tenancy.ErrNoScope rather
	// than carried to the store.
	//
	// It is a field rather than a method on callers.Principal only because
	// that interface is one consumers implement and v14 is frozen. Nil falls
	// back quietly, which is the failure a method would have made a compile
	// error; the next major version should make it one.
	//
	// It does not touch dataprivacy's subject resolver, which reads a user and
	// not a scope.
	TenantOf func(principal callers.Principal) (tenancy.Scope, error)

	// Authorizers are the per-surface rules about which rows a caller who may
	// make this call may make it against.
	Authorizers Authorizers

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
}

// Authorizers is the second seam, one field per surface that takes one.
//
// Four are required: a surface configured without them does not mount open, it
// fails the startup that configured it, under the surface's own sentinel rather
// than one invented here. Three have a default their own package documents and
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

	// MediaObjects decides which stored objects a caller may fetch. Optional;
	// mediaregistry/http defaults it to OwnerOnly.
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
// Eleven gRPC surfaces — audit, oauth2clients, signin, billing, comments,
// identity, issuereports, notifications, settings, waitlists and webhooks — and
// three HTTP ones — dataprivacy, mediaregistry and operations. sessions/http is
// not among them; see the package documentation for why.
//
// A surface mounts when everything it is built from resolves, and the reading
// of "resolves" is the one the rest of this package already uses: nobody
// registered one is an absence and contributes nothing, while one that was
// registered and cannot be built is an error naming the surface. That single
// rule is what makes a Config naming no billing mount no billing surface, and
// it is also what makes identity, oauth2clients and signin behave sensibly
// without a special case — their servers are built over a service Register does
// not register, so they mount for an application that registered one and stay
// absent for an application that did not.
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
// interceptor that enforces them. This is the same thing
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

	m := &mount{i: i, pillars: pillars, t: t}

	m.audit()
	m.billing()
	m.comments()
	m.identity()
	m.issueReports()
	m.notifications()
	m.oauth2Clients()
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

	registrations []grpcserver.RegistrationFunc
	names         []string
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
func (m *mount) caller(surface string) (callers.PrincipalExtractor, bool) {
	if m.t.Extractor == nil {
		m.err = platformerrors.Wrapf(ErrNilPrincipalExtractor, "mounting the %s surface", surface)

		return nil, false
	}

	return m.t.Extractor, true
}

// fail records a surface that could not be built, naming it.
//
// The surface's own sentinel is underneath, which is the whole intent of
// passing a nil authorizer through rather than checking it here: a service that
// configured billing and supplied no AccountAuthorizer is told so by
// billing/grpc, in billing's words.
func (m *mount) fail(surface string, err error) {
	m.err = platformerrors.Wrapf(err, "building the %s transport surface", surface)
}

// mountedGRPC records a built gRPC surface and the registration that will put
// it on the server.
func (m *mount) mountedGRPC(surface string, register grpcserver.RegistrationFunc) {
	m.registrations = append(m.registrations, register)
	m.names = append(m.names, surface+" gRPC")
}

// mountedHTTP records a surface that has put its own routes on the router.
func (m *mount) mountedHTTP(surface string) {
	m.names = append(m.names, surface+" HTTP")
}

// httpLane mounts the three HTTP surfaces, having first established that the
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
func (m *mount) routesLanded(surface string, router *routing.Router) bool {
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
// tenantOf is Transports.TenantOf, and nil reads Principal.Scope(). Either way
// the principal is found here first, so a request with nobody on it is refused
// before an application's resolver is asked anything. See that field for why
// the directory and the tenant are different questions.
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

// tenantScope is the tenant of a principal already found: the application's
// reading when it supplied one, and Principal.Scope() when it did not.
//
// Only the application's reading is validated. Principal.Scope() is returned as
// it always was, so a consumer that leaves Transports.TenantOf nil sees no
// change at all.
func tenantScope(principal callers.Principal, tenantOf func(callers.Principal) (tenancy.Scope, error)) (tenancy.Scope, error) {
	if tenantOf == nil {
		return principal.Scope(), nil
	}

	scope, err := tenantOf(principal)
	if err != nil {
		return tenancy.Scope{}, err
	}

	if err = scope.Validate(); err != nil {
		return tenancy.Scope{}, platformerrors.Wrap(err, "the application's tenant for this principal")
	}

	return scope, nil
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
	reader, ok := need[audit.Reader](m)
	if !ok {
		return
	}

	client, ok := need[database.Client](m)
	if !ok {
		return
	}

	extract, ok := m.caller("audit")
	if !ok {
		return
	}

	srv, err := auditgrpc.NewServer(reader, client,
		auditgrpc.WithPillars(m.pillars),
		auditgrpc.WithScopeResolver(deriveScope(extract, m.t.TenantOf)),
	)
	if err != nil {
		m.fail("audit", err)

		return
	}

	m.mountedGRPC("audit", srv.RegisterOn)
}

// billing mounts the ledger surface. Its authorizer is required, and a nil one
// travels to the constructor so the refusal is billing's own.
func (m *mount) billing() {
	store, ok := need[billing.Store](m)
	if !ok {
		return
	}

	client, ok := need[database.Client](m)
	if !ok {
		return
	}

	extract, ok := m.caller("billing")
	if !ok {
		return
	}

	srv, err := billinggrpc.NewServer(store, client, extract, m.t.Authorizers.BillingAccounts,
		billinggrpc.WithPillars(m.pillars),
	)
	if err != nil {
		m.fail("billing", err)

		return
	}

	m.mountedGRPC("billing", srv.RegisterOn)
}

// comments mounts the comment surface. Its authorizer is optional, so a nil one
// is left out rather than passed, and comments/grpc's own default stands.
func (m *mount) comments() {
	store, ok := need[comments.Store](m)
	if !ok {
		return
	}

	client, ok := need[database.Client](m)
	if !ok {
		return
	}

	extract, ok := m.caller("comments")
	if !ok {
		return
	}

	opts := []commentsgrpc.Option{commentsgrpc.WithPillars(m.pillars)}
	if m.t.Authorizers.CommentAuthors != nil {
		opts = append(opts, commentsgrpc.WithAuthorAuthorizer(m.t.Authorizers.CommentAuthors))
	}

	srv, err := commentsgrpc.NewServer(store, client, extract, opts...)
	if err != nil {
		m.fail("comments", err)

		return
	}

	m.mountedGRPC("comments", srv.RegisterOn)
}

// identity mounts the directory surface.
//
// Its service is a dependency like any other here, and Register does not
// register one — so a Config naming Identity mounts this surface only for an
// application that built the service itself. That is the absence rule doing its
// job rather than a gap in it: a surface over half a directory is not a surface.
func (m *mount) identity() {
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

	extract, ok := m.caller("identity")
	if !ok {
		return
	}

	opts := []identitygrpc.Option{identitygrpc.WithPillars(m.pillars)}
	if m.t.Authorizers.IdentityTargets != nil {
		opts = append(opts, identitygrpc.WithTargetAuthorizer(m.t.Authorizers.IdentityTargets))
	}

	srv, err := identitygrpc.NewServer(svc, store, client, extract, opts...)
	if err != nil {
		m.fail("identity", err)

		return
	}

	m.mountedGRPC("identity", srv.RegisterOn)
}

// issueReports mounts the report surface. Its authorizer is required.
func (m *mount) issueReports() {
	store, ok := need[issuereports.Store](m)
	if !ok {
		return
	}

	client, ok := need[database.Client](m)
	if !ok {
		return
	}

	extract, ok := m.caller("issue reports")
	if !ok {
		return
	}

	srv, err := issuereportsgrpc.NewServer(store, client, extract, m.t.Authorizers.IssueReports,
		issuereportsgrpc.WithPillars(m.pillars),
	)
	if err != nil {
		m.fail("issue reports", err)

		return
	}

	m.mountedGRPC("issue reports", srv.RegisterOn)
}

// notifications mounts the inbox and device surface. It takes two seams, and
// one registered store satisfies both — notificationscfg registers each as a
// narrowing of the same value.
func (m *mount) notifications() {
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

	extract, ok := m.caller("notifications")
	if !ok {
		return
	}

	srv, err := notificationsgrpc.NewServer(inbox, registry, client, extract,
		notificationsgrpc.WithPillars(m.pillars),
	)
	if err != nil {
		m.fail("notifications", err)

		return
	}

	m.mountedGRPC("notifications", srv.RegisterOn)
}

// oauth2Clients mounts the client registry surface.
//
// Neither its service nor its store is reachable from a Config — the package
// ships no config subpackage — so this mounts for an application that
// registered both and stays absent otherwise.
func (m *mount) oauth2Clients() {
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

	extract, ok := m.caller("oauth2 clients")
	if !ok {
		return
	}

	srv, err := oauth2clientsgrpc.NewServer(svc, store, client, extract,
		oauth2clientsgrpc.WithPillars(m.pillars),
	)
	if err != nil {
		m.fail("oauth2 clients", err)

		return
	}

	m.mountedGRPC("oauth2 clients", srv.RegisterOn)
}

// settings mounts the settings surface. Its authorizer is required.
func (m *mount) settings() {
	store, ok := need[settings.Store](m)
	if !ok {
		return
	}

	client, ok := need[database.Client](m)
	if !ok {
		return
	}

	extract, ok := m.caller("settings")
	if !ok {
		return
	}

	srv, err := settingsgrpc.NewServer(store, client, extract, m.t.Authorizers.SettingsSubjects,
		settingsgrpc.WithPillars(m.pillars),
	)
	if err != nil {
		m.fail("settings", err)

		return
	}

	m.mountedGRPC("settings", srv.RegisterOn)
}

// passwordReset mounts the way back in for somebody who cannot sign in.
//
// It takes no principal extractor, and it is the only surface here that does not:
// all three of its RPCs are for a caller who has not signed in and cannot, so
// there is nobody to extract. Its scope resolver is left at the package's own
// default for sign-in's reason, with no exception to make — every RPC on it
// arrives with nobody on it, not just six of them.
//
// A consumer provides the *passwordreset.Service the way they provide
// *signin.Service, and this mounts the surface over it if they did. What that
// service needs beyond a store — a Mailer, and the authenticator that hashes the
// password a reset writes — has no default this module could supply, which is
// why neither is a config field here.
func (m *mount) passwordReset() {
	svc, ok := need[*passwordreset.Service](m)
	if !ok {
		return
	}

	srv, err := passwordresetgrpc.NewServer(svc, passwordresetgrpc.WithPillars(m.pillars))
	if err != nil {
		m.fail("password reset", err)

		return
	}

	m.mountedGRPC("password reset", srv.RegisterOn)
}

// signIn mounts the sign-in surface.
//
// Its scope resolver is left at signin/grpc's own default rather than derived:
// six of its RPCs are the ones a caller reaches before there is anybody to
// extract, so a resolver that refuses a request with no principal would refuse
// the act of signing in — and, since registration landed here, the act of
// finishing one.
func (m *mount) signIn() {
	svc, ok := need[*signin.Service](m)
	if !ok {
		return
	}

	extract, ok := m.caller("sign-in")
	if !ok {
		return
	}

	srv, err := signingrpc.NewServer(svc, extract, signingrpc.WithPillars(m.pillars))
	if err != nil {
		m.fail("sign-in", err)

		return
	}

	m.mountedGRPC("sign-in", srv.RegisterOn)
}

// waitlists mounts the signup surface. Its authorizer is required, and its
// scope resolver is left defaulted for the reason sign-in's is: the public
// signup page is three RPCs that arrive with nobody on them by design.
func (m *mount) waitlists() {
	store, ok := need[waitlists.Store](m)
	if !ok {
		return
	}

	client, ok := need[database.Client](m)
	if !ok {
		return
	}

	extract, ok := m.caller("waitlists")
	if !ok {
		return
	}

	srv, err := waitlistsgrpc.NewServer(store, client, extract, m.t.Authorizers.WaitlistSignups,
		waitlistsgrpc.WithPillars(m.pillars),
	)
	if err != nil {
		m.fail("waitlists", err)

		return
	}

	m.mountedGRPC("waitlists", srv.RegisterOn)
}

// webhooks mounts the endpoint and subscription surface. It takes the
// dispatcher and the store beneath it, both of which Register registers
// together.
func (m *mount) webhooks() {
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

	extract, ok := m.caller("webhooks")
	if !ok {
		return
	}

	srv, err := webhooksgrpc.NewServer(dispatcher, store, client, extract,
		webhooksgrpc.WithPillars(m.pillars),
	)
	if err != nil {
		m.fail("webhooks", err)

		return
	}

	m.mountedGRPC("webhooks", srv.RegisterOn)
}

// dataPrivacy mounts the subject access request surface.
//
// Its scope resolver is deliberately left at dataprivacy/http's default. A nil
// scope there reads as every confinement the subject appears in, which is what
// a person asking after their own data means; deriving one from the principal
// would narrow every privacy request to the tenant the caller happens to be
// acting in, which is a quieter answer than the one that was asked for.
func (m *mount) dataPrivacy() {
	svc, ok := need[dataprivacy.Service](m)
	if !ok {
		return
	}

	router, ok := need[*routing.Router](m)
	if !ok {
		return
	}

	extract, ok := m.caller("data privacy")
	if !ok {
		return
	}

	handlers, err := dataprivacyhttp.New(svc,
		dataprivacyhttp.WithLogger(m.pillars.Logger),
		dataprivacyhttp.WithTracerProvider(m.pillars.TracerProvider),
		dataprivacyhttp.WithSubjectResolver(deriveSubject(extract)),
	)
	if err != nil {
		m.fail("data privacy", err)

		return
	}

	handlers.Mount(router)

	if !m.routesLanded("data privacy", router) {
		return
	}

	m.mountedHTTP("data privacy")
}

// mediaRegistry mounts the object download surface. Its entitlement is
// optional, so a nil one leaves mediaregistry/http's OwnerOnly in place.
func (m *mount) mediaRegistry() {
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

	extract, ok := m.caller("media registry")
	if !ok {
		return
	}

	opts := []mediaregistryhttp.Option{
		mediaregistryhttp.WithLogger(m.pillars.Logger),
		mediaregistryhttp.WithTracerProvider(m.pillars.TracerProvider),
		mediaregistryhttp.WithCallerResolver(deriveMediaCaller(extract, m.t.TenantOf)),
	}
	if m.t.Authorizers.MediaObjects != nil {
		opts = append(opts, mediaregistryhttp.WithEntitlement(m.t.Authorizers.MediaObjects))
	}

	handler, err := mediaregistryhttp.New(store, client, manager, opts...)
	if err != nil {
		m.fail("media registry", err)

		return
	}

	handler.Mount(router)

	if !m.routesLanded("media registry", router) {
		return
	}

	m.mountedHTTP("media registry")
}

// operations mounts the long-running operation surface.
//
// The watcher is resolved optionally and passed when it is there, because it is
// what gates the event stream: without one, operations/http mounts three routes
// rather than four rather than mounting a subscription with nothing behind it.
func (m *mount) operations() {
	svc, ok := need[operations.Service](m)
	if !ok {
		return
	}

	router, ok := need[*routing.Router](m)
	if !ok {
		return
	}

	extract, ok := m.caller("operations")
	if !ok {
		return
	}

	opts := []operationshttp.Option{
		operationshttp.WithLogger(m.pillars.Logger),
		operationshttp.WithTracerProvider(m.pillars.TracerProvider),
		operationshttp.WithOwnerResolver(deriveScope(extract, m.t.TenantOf)),
	}

	if watcher, watching := need[*operations.Watcher](m); watching {
		opts = append(opts, operationshttp.WithWatcher(watcher))
	}

	if m.err != nil {
		return
	}

	handlers, err := operationshttp.New(svc, opts...)
	if err != nil {
		m.fail("operations", err)

		return
	}

	handlers.Mount(router)

	if !m.routesLanded("operations", router) {
		return
	}

	m.mountedHTTP("operations")
}

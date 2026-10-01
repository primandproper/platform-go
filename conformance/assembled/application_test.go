package assembled_test

import (
	"context"
	"net/http"
	"slices"
	"sync"

	"github.com/primandproper/platform-go/v14/audit/auditpb"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
	"github.com/primandproper/platform-go/v14/authentication/passwordreset"
	signingrpc "github.com/primandproper/platform-go/v14/authentication/signin/grpc"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/billing/billingpb"
	billinggrpc "github.com/primandproper/platform-go/v14/billing/grpc"
	"github.com/primandproper/platform-go/v14/callers"
	"github.com/primandproper/platform-go/v14/comments"
	"github.com/primandproper/platform-go/v14/comments/commentspb"
	commentsgrpc "github.com/primandproper/platform-go/v14/comments/grpc"
	"github.com/primandproper/platform-go/v14/dataprivacy"
	dataprivacyhttp "github.com/primandproper/platform-go/v14/dataprivacy/http"
	"github.com/primandproper/platform-go/v14/identity"
	identitycfg "github.com/primandproper/platform-go/v14/identity/config"
	"github.com/primandproper/platform-go/v14/identity/identitypb"
	"github.com/primandproper/platform-go/v14/issuereports"
	issuereportsgrpc "github.com/primandproper/platform-go/v14/issuereports/grpc"
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"
	mediaregistrygrpc "github.com/primandproper/platform-go/v14/mediaregistry/grpc"
	mediaregistryhttp "github.com/primandproper/platform-go/v14/mediaregistry/http"
	notificationsgrpc "github.com/primandproper/platform-go/v14/notifications/grpc"
	"github.com/primandproper/platform-go/v14/operations"
	operationscfg "github.com/primandproper/platform-go/v14/operations/config"
	operationshttp "github.com/primandproper/platform-go/v14/operations/http"
	"github.com/primandproper/platform-go/v14/privacyadapters"
	"github.com/primandproper/platform-go/v14/service"
	"github.com/primandproper/platform-go/v14/settings"
	settingsgrpc "github.com/primandproper/platform-go/v14/settings/grpc"
	"github.com/primandproper/platform-go/v14/settings/settingspb"
	"github.com/primandproper/platform-go/v14/waitlists"
	waitlistsgrpc "github.com/primandproper/platform-go/v14/waitlists/grpc"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"
	"github.com/primandproper/platform-go/v14/webhooks"
	webhooksgrpc "github.com/primandproper/platform-go/v14/webhooks/grpc"

	"github.com/primandproper/primitives-go/v2/authentication"
	"github.com/primandproper/primitives-go/v2/authentication/argon2"
	"github.com/primandproper/primitives-go/v2/authorization"
	"github.com/primandproper/primitives-go/v2/database"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"github.com/samber/do/v2"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// registerApplication is what a consumer's main registers beyond its config:
// the declarations no environment variable can express, and the services this
// module's composition root deliberately does not build.
//
// Each surface below mounts only because something here made its dependency
// resolvable, which is service.RegisterTransports' absence rule doing its job —
// a surface over half a service is not a surface.
func registerApplication(i do.Injector, prefix string, commentable *things, people *directories) {
	// The declarations. Which kinds of thing accept comments, and which events
	// an application publishes, are the application's to say.
	do.ProvideValue(i, comments.Targets{thingType: commentable.definition()})
	// Two event types rather than one, because the webhooks suite's assertions
	// about a subscription set — reconciling it, retiring one of it — need a
	// set with more than one member to be observable.
	do.ProvideValue(i, webhooks.Catalog{
		"conformance.happened": {Description: "something the conformance suite made happen"},
		"conformance.followed": {Description: "something that followed from what the suite made happen"},
	})

	// What kinds of long-running work the application runs. Empty is a real
	// answer: dataprivacy registers its own operation kinds into it as it is
	// built, and the application here runs none of its own.
	do.ProvideValue(i, operations.NewRegistry())

	// Which collectors and erasers answer a privacy request. dataprivacy refuses
	// an empty registry — a subject access request nothing can answer is not a
	// request — so this registers identity's adapter through privacyadapters,
	// the call a consumer makes, over the directory the composition root built.
	do.Provide(i, func(i do.Injector) (*dataprivacy.Registry, error) {
		registry := dataprivacy.NewRegistry()

		if _, err := privacyadapters.Register(registry, &privacyadapters.Adapters{
			Reader: do.MustInvoke[database.Client](i).Reader(),
			Identity: &privacyadapters.IdentityAdapter{
				Store:   do.MustInvoke[identity.Store](i),
				Resolve: people.resolve,
			},
		}); err != nil {
			return nil, err
		}

		return registry, nil
	})

	// identity's service, which identity/config ships a registration for and
	// service.Register does not call. The sign-in block's registration door
	// registers people through it.
	identitycfg.RegisterService(i)

	// The one password engine. The sign-in block resolves it, and so does the
	// reset flow below, so a reset writes a password sign-in can check.
	do.ProvideValue[authentication.Authenticator](i, argon2.NewArgon2Authenticator())

	// Two services built by hand, the way each package's documentation says a
	// consumer builds it, from what the composition root already registered.
	// oauth2clients and passwordreset have a config block each, left unset here
	// so that neither is registered twice. signin is not here: its block in
	// assemble's config mounts it.
	do.Provide(i, func(i do.Injector) (oauth2clients.Store, error) {
		store, err := oauth2clients.NewSQLStore(do.MustInvoke[database.Client](i), oauth2clients.WithTablePrefix(prefix))
		if err != nil {
			return nil, err
		}

		return store, nil
	})

	do.Provide(i, func(i do.Injector) (*oauth2clients.Service, error) {
		return oauth2clients.NewService(do.MustInvoke[database.Client](i), do.MustInvoke[oauth2clients.Store](i))
	})

	do.Provide(i, func(i do.Injector) (*passwordreset.Service, error) {
		db := do.MustInvoke[database.Client](i)

		resetTokens, err := passwordreset.NewSQLStore(&passwordreset.Config{TablePrefix: prefix}, db)
		if err != nil {
			return nil, err
		}

		// The mailbox assemble registered, which is where the reset suite reads
		// the link a person would have been sent.
		return passwordreset.NewService(db, resetTokens, do.MustInvoke[identity.Store](i),
			do.MustInvoke[authentication.Authenticator](i), do.MustInvoke[*resetMailbox](i))
	})
}

// directories is the harness's answer to which tenants a person's data lives
// in: the one it registered them into.
//
// A request's own confinement is not that answer here. service mounts the
// privacy surface with dataprivacy/http's default, UnconfinedRequests, so every
// request a caller submits names no scope — a person's request, not a
// tenant's — and the resolver is what says where that person is. Every caller
// NewSubject mints is registered into one directory of its own, so the
// directory it was registered into is the whole of the truth rather than a
// narrowing of it. A person the harness never registered has nothing in any
// directory it knows of, which is the answer a resolver gives for them.
type directories struct {
	byUser sync.Map
}

// remember records the directory a caller was registered into.
func (d *directories) remember(userID string, scope tenancy.Scope) {
	d.byUser.Store(userID, scope)
}

// resolve is the dataprivacy.ScopeResolver: the request's own confinement
// where it names one, and the subject's directory where it does not.
func (d *directories) resolve(_ context.Context, requestScope tenancy.Scope, subject dataprivacy.Subject) ([]tenancy.Scope, error) {
	if requestScope.Validate() == nil {
		return []tenancy.Scope{requestScope}, nil
	}

	scope, ok := d.byUser.Load(subject.ID)
	if !ok {
		return nil, nil
	}

	return []tenancy.Scope{scope.(tenancy.Scope)}, nil
}

// operationsConfig puts both of the operations block's tables under the run's
// prefix: the operations themselves and the work queue that runs them.
func operationsConfig(prefix string) *operationscfg.Config {
	cfg := &operationscfg.Config{}
	cfg.Operations.TablePrefix = prefix
	cfg.Queue.TablePrefix = prefix

	return cfg
}

// authorizers are this harness's rules about which rows a caller has standing
// in, and there is one rule: a caller's own user and their active account, and
// nothing else.
//
// A rule rather than a yes. A permissive authorizer would make every confinement
// assertion a later suite writes pass on the strength of the rule being absent,
// which is the failure the positive-control ruling exists to catch — so each of
// these refuses with callers.ErrTargetNotPermitted, the refusal every surface
// already answers.
func authorizers() service.Authorizers {
	return service.Authorizers{
		BillingAccounts:  standing{},
		IssueReports:     standing{},
		SettingsSubjects: standing{},
		WaitlistSignups:  standing{},
	}
}

type standing struct{}

func (standing) own(caller callers.Principal, subjectType, id string) error {
	switch {
	case subjectType == "user" && id != "" && id == caller.UserID():
		return nil
	case subjectType == "account" && id != "" && id == caller.ActiveAccountID():
		return nil
	default:
		return callers.ErrTargetNotPermitted
	}
}

func (s standing) AuthorizeAccount(_ context.Context, caller callers.Principal, accountID string) error {
	return s.own(caller, "account", accountID)
}

func (s standing) AuthorizeReport(_ context.Context, caller callers.Principal, report *issuereports.Report) error {
	if report == nil {
		return callers.ErrTargetNotPermitted
	}

	return s.own(caller, "user", report.Reporter)
}

func (s standing) AuthorizeReporter(_ context.Context, caller callers.Principal, reporter string) error {
	return s.own(caller, "user", reporter)
}

func (s standing) AuthorizeSubject(_ context.Context, caller callers.Principal, subject settings.Subject) error {
	return s.own(caller, string(subject.Type), subject.ID)
}

func (s standing) AuthorizeSubjectRead(
	_ context.Context,
	caller callers.Principal,
	_ tenancy.Scope,
	subject waitlists.Subject,
) error {
	return s.own(caller, string(subject.Type), subject.ID)
}

// AuthorizeWithdrawal refuses. A withdrawal names a signup rather than a person,
// and whose signup it is lives in the store — so this harness cannot answer the
// question without reading a table, and a rule that cannot answer refuses
// rather than permits.
func (standing) AuthorizeWithdrawal(context.Context, callers.Principal, tenancy.Scope, string, string) error {
	return callers.ErrTargetNotPermitted
}

// administrative are the grants this harness reserves to an administrator, and
// they are exactly the ones the seven grant-reading surfaces ask inside a
// handler: every archive grant, which decides whether include_archived is
// honored, and settings' reserved-write grant.
//
// The harness installs one piece of method enforcement, reserveStaffCalls,
// and it reads the run's reservation rather than these grants — service mounts
// no authorization interceptor and a consumer's main adds its own — so these
// are the only grants a request here is ever asked about inside a handler.
// That is why they are the line drawn: a member holds every other permission
// the surfaces' Permissions maps name, the way a consumer's self-service role
// would, and none of the ones that would make the refused half of each rule
// and the granted half indistinguishable. A consumer whose members dismiss their own notifications
// and so hold notifications' archive grant is right to, and sees their own
// dismissed rows; the suites assert only what an administrator receives.
var administrative = []authorization.Permission{
	billinggrpc.PermissionArchiveProducts,
	billinggrpc.PermissionArchiveSubscriptions,
	billinggrpc.PermissionArchivePurchases,
	billinggrpc.PermissionArchiveTransactions,
	commentsgrpc.PermissionArchiveComments,
	issuereportsgrpc.PermissionArchiveReports,
	notificationsgrpc.PermissionArchiveInbox,
	settingsgrpc.PermissionArchiveDefinitions,
	settingsgrpc.PermissionWriteAdminValues,
	waitlistsgrpc.PermissionArchiveLists,
	waitlistsgrpc.PermissionArchiveSignups,
	webhooksgrpc.PermissionArchiveEndpoints,
	webhooksgrpc.PermissionArchiveSubscriptions,
}

// memberRole and adminRole are the two permission sets a stand-in principal can
// hold.
var memberRole, adminRole = roles()

// roles builds the two sets from the gRPC surfaces' own Permissions maps, and
// the HTTP surfaces', so that a permission a surface adds later is a member's
// without an edit here.
func roles() (member, admin *authorization.PermissionSet) {
	var every []authorization.Permission

	for _, surface := range []map[string][]authorization.Permission{
		dataprivacyhttp.Permissions(),
		mediaregistryhttp.Permissions(),
		operationshttp.Permissions(),
		billinggrpc.Permissions(),
		commentsgrpc.Permissions(),
		issuereportsgrpc.Permissions(),
		mediaregistrygrpc.Permissions(),
		notificationsgrpc.Permissions(),
		settingsgrpc.Permissions(),
		waitlistsgrpc.Permissions(),
		webhooksgrpc.Permissions(),
	} {
		for _, required := range surface {
			every = append(every, required...)
		}
	}

	var ordinary []authorization.Permission

	for _, p := range every {
		if !slices.Contains(administrative, p) {
			ordinary = append(ordinary, p)
		}
	}

	return authorization.NewPermissionSet(ordinary...), authorization.NewPermissionSet(append(every, administrative...)...)
}

// grantsOf is this harness's role policy, handed to the sign-in extractor the
// way a consumer hands theirs: it reads the roles on the caller the extractor
// resolved and answers with the matching set. The extractor asks it only for a
// request somebody is on, so a request with nobody on it has no authority,
// which every surface reads as a denial.
func grantsOf(_ context.Context, principal callers.Principal) (authorization.Grants, error) {
	if isAdministrator(principal) {
		return authorization.NewGrants(adminRole), nil
	}

	return authorization.NewGrants(memberRole), nil
}

// isAdministrator reports whether a caller holds adminServiceRole on this
// request. The directory holds it for every administrator subject; the
// extractor leaves it on the request only for a token the administrative door
// minted, so reading it here is reading the rule the extractor exists to apply.
func isAdministrator(principal callers.Principal) bool {
	caller, ok := principal.(*signingrpc.Caller)

	return ok && slices.Contains(caller.Identity().ServiceRoles(), adminServiceRole)
}

// staffOnly is the reservation this harness's second run makes: a deployment
// that keeps its console to its staff, the way a product with a back office
// does. The directory's administration, the catalog's writes, the scope-wide
// ledgers and their corrections, the chain's verification and every tenant's
// log, the moderation read,
// the report queue across tenants, the settings catalog, somebody else's logins,
// the client registry and the waitlist console are an operator's; everything a
// person does to their own rows, and every door reached with nobody on the
// call, is left to members.
//
// A list rather than a rule, and not the whole surface, because what it
// exercises is the path a consumer's reservation takes: each call named here
// is made by an administrator the subject minted for it, and each call not
// named by a member. Which calls a consumer names is its own to decide.
var staffOnly = []string{
	auditpb.AuditService_VerifyChain_FullMethodName,
	auditpb.AuditAdministrationService_GetAnyEntry_FullMethodName,
	auditpb.AuditAdministrationService_ListAnyEntries_FullMethodName,

	billingpb.BillingService_CreateProduct_FullMethodName,
	billingpb.BillingService_UpdateProduct_FullMethodName,
	billingpb.BillingService_ArchiveProduct_FullMethodName,
	billingpb.BillingService_ListSubscriptions_FullMethodName,
	billingpb.BillingService_ArchiveSubscription_FullMethodName,
	billingpb.BillingService_ListPurchases_FullMethodName,
	billingpb.BillingService_ArchivePurchase_FullMethodName,
	billingpb.BillingService_ListTransactions_FullMethodName,
	billingpb.BillingService_ArchiveTransaction_FullMethodName,

	commentspb.CommentsService_ListCommentsByTargetType_FullMethodName,

	issuereportspb.IssueReportsService_ListReportsAcrossScopes_FullMethodName,
	issuereportspb.IssueReportsService_ListReportsByStatusAcrossScopes_FullMethodName,

	identitypb.IdentityService_GetUser_FullMethodName,
	identitypb.IdentityService_ListUsers_FullMethodName,
	identitypb.IdentityService_SearchUsersByUsername_FullMethodName,
	identitypb.IdentityService_ListAccounts_FullMethodName,
	identitypb.IdentityService_ArchiveUser_FullMethodName,
	identitypb.IdentityService_UpdateUserAccountStatus_FullMethodName,
	identitypb.IdentityService_SetUserServiceRoles_FullMethodName,
	identitypb.IdentityService_SetUserRequiresPasswordChange_FullMethodName,

	oauth2clientspb.OAuth2ClientsService_CreateOAuth2Client_FullMethodName,
	oauth2clientspb.OAuth2ClientsService_GetOAuth2Client_FullMethodName,
	oauth2clientspb.OAuth2ClientsService_ListOAuth2Clients_FullMethodName,
	oauth2clientspb.OAuth2ClientsService_ArchiveOAuth2Client_FullMethodName,

	settingspb.SettingsService_CreateDefinition_FullMethodName,
	settingspb.SettingsService_UpdateDefinition_FullMethodName,
	settingspb.SettingsService_ArchiveDefinition_FullMethodName,
	settingspb.SettingsService_ListValuesForDefinition_FullMethodName,

	signinpb.SignInAdministrationService_ListSignInsForUser_FullMethodName,
	signinpb.SignInAdministrationService_EndSignInForUser_FullMethodName,
	signinpb.SignInAdministrationService_EndAllSignInsForUser_FullMethodName,

	waitlistspb.WaitlistsService_CreateList_FullMethodName,
	waitlistspb.WaitlistsService_UpdateList_FullMethodName,
	waitlistspb.WaitlistsService_ArchiveList_FullMethodName,
	waitlistspb.WaitlistsService_GetSignup_FullMethodName,
	waitlistspb.WaitlistsService_GetSignupByContact_FullMethodName,
	waitlistspb.WaitlistsService_ListSignups_FullMethodName,
	waitlistspb.WaitlistsService_UpdateSignupNotes_FullMethodName,
	waitlistspb.WaitlistsService_Invite_FullMethodName,
	waitlistspb.WaitlistsService_Convert_FullMethodName,
	waitlistspb.WaitlistsService_ArchiveSignup_FullMethodName,
	waitlistspb.WaitlistsService_WithdrawSignupsForSubject_FullMethodName,
}

// reserveStaffCalls refuses a call staffOnly names to a caller who is not an
// administrator, in the run that reserves them, the way a consumer's
// authorization interceptor refuses a call its caller's role does not cover.
//
// What it checks that the suites' own check on each caller cannot is the other
// half of a reservation: that a caller declaring a reserved call really is one
// the deployment treats as its staff. The suites decide from Seams which
// caller to mint, and a caller minted as a member for a reserved call — the
// subject forgetting a reservation, or a suite asking AsMember and not
// skipping — is refused here rather than in a consumer's deployment. A request
// with nobody on it is left to the handler, since no door is reserved.
//
// It runs after the extractor's interceptor, so the caller it reads is the one
// that interceptor resolved.
func reserveStaffCalls(extractor *signingrpc.PrincipalExtractor) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		principal, ok := extractor.Extract(ctx)
		if !ok || !reserving(ctx) || isAdministrator(principal) || !slices.Contains(staffOnly, info.FullMethod) {
			return handler(ctx, req)
		}

		return nil, status.Errorf(codes.PermissionDenied, "%s is reserved to an administrator", info.FullMethod)
	}
}

// reserving reports whether a request was made in the run that reserves
// staffOnly.
func reserving(ctx context.Context) bool {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return false
	}

	values := md.Get(mdReserving)

	return len(values) > 0 && values[0] == "true"
}

// staffOnlyRoutes is the HTTP half of staffOnly: one route on each HTTP
// surface, kept to the back office in the run that reserves.
//
// The console view of every operation running in a tenant, the withdrawal of
// somebody's privacy request — which that deployment routes through its support
// desk — and the stored objects, which its product serves to staff alone. None
// is one of the surfaces' OwnStandingRoutes, which no deployment can reserve:
// a person following their own erasure and the confirmation link in their mail
// stay reachable to a member in both runs.
var staffOnlyRoutes = []string{
	dataprivacyhttp.RouteCancel,
	mediaregistryhttp.RouteServe,
	operationshttp.RouteList,
}

// memberWithoutStaffRoutes is what a member holds in the run that reserves
// staffOnlyRoutes: every permission memberRole does, but the ones those routes
// require.
//
// Withholding a route's permission is how a deployment reserves it, and it
// reserves exactly that route here because no permission a reserved route
// requires is one an unreserved route requires too — which is the property a
// consumer checks of its own policy before relying on the same move.
var memberWithoutStaffRoutes = func() *authorization.PermissionSet {
	var withheld []authorization.Permission

	for _, surface := range []map[string][]authorization.Permission{
		dataprivacyhttp.Permissions(),
		mediaregistryhttp.Permissions(),
		operationshttp.Permissions(),
	} {
		for route, required := range surface {
			if slices.Contains(staffOnlyRoutes, route) {
				withheld = append(withheld, required...)
			}
		}
	}

	var kept []authorization.Permission

	for p := range memberRole.All() {
		if !slices.Contains(withheld, p) {
			kept = append(kept, p)
		}
	}

	return authorization.NewPermissionSet(kept...)
}()

// httpGrants is the grants extractor this harness's HTTP enforcer reads: the
// role policy the extractor applies, except that in the run reserving
// staffOnlyRoutes a member holds memberWithoutStaffRoutes instead.
//
// It is the HTTP counterpart of reserveStaffCalls, and it reserves by the
// means a consumer's policy would — a member simply does not hold the
// permission — because on HTTP that is the only means there is: each surface
// checks its own routes' permissions with the enforcer it was handed, and
// nothing sits in front of them keyed by route.
func httpGrants(extractor *signingrpc.PrincipalExtractor) authorization.GrantsExtractor {
	return func(ctx context.Context) (authorization.Grants, bool) {
		grants, ok := extractor.Grants(ctx)
		if !ok || !reservingRequest(ctx) {
			return grants, ok
		}

		if principal, found := extractor.Extract(ctx); found && isAdministrator(principal) {
			return grants, ok
		}

		return authorization.NewGrants(memberWithoutStaffRoutes), true
	}
}

// headerReserving is mdReserving's HTTP spelling: which of the harness's two
// runs the caller making a request was minted in.
const headerReserving = "Conformance-Reserving"

// reservingKey is where markReserving leaves the run on a request's context.
type reservingKey struct{}

// markReserving reads headerReserving onto the request's context, where
// httpGrants reads it back.
func markReserving(next http.Handler) http.Handler {
	return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		if req.Header.Get(headerReserving) == "true" {
			req = req.WithContext(context.WithValue(req.Context(), reservingKey{}, true))
		}

		next.ServeHTTP(res, req)
	})
}

// reservingRequest reports whether an HTTP request was made in the run that
// reserves staffOnlyRoutes.
func reservingRequest(ctx context.Context) bool {
	reserving, ok := ctx.Value(reservingKey{}).(bool)

	return ok && reserving
}

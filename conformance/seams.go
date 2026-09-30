package conformance

import (
	"context"
	"net/http"
	"time"

	"github.com/primandproper/platform-go/v14/audit/auditpb"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
	"github.com/primandproper/platform-go/v14/authentication/passkeys/passkeyspb"
	"github.com/primandproper/platform-go/v14/authentication/passwordreset/passwordresetpb"
	"github.com/primandproper/platform-go/v14/authentication/signin/signinpb"
	"github.com/primandproper/platform-go/v14/billing"
	"github.com/primandproper/platform-go/v14/billing/billingpb"
	"github.com/primandproper/platform-go/v14/comments/commentspb"
	"github.com/primandproper/platform-go/v14/identity/identitypb"
	"github.com/primandproper/platform-go/v14/issuereports/issuereportspb"
	"github.com/primandproper/platform-go/v14/notifications/notificationspb"
	"github.com/primandproper/platform-go/v14/settings/settingspb"
	"github.com/primandproper/platform-go/v14/waitlists/waitlistspb"
	"github.com/primandproper/platform-go/v14/webhooks/webhookspb"

	"github.com/primandproper/primitives-go/v2/database/dialect"
	"github.com/primandproper/primitives-go/v2/tenancy"

	"google.golang.org/grpc"
)

// Seams is everything a subject supplies, and the only thing Run takes besides
// the suites to run.
//
// Every field but NewSubject is optional, and every absence is a skip with its
// reason printed rather than a failure. That is service.Config's rule one level
// down: presence is the switch, and a subsystem nobody configured is absent
// rather than broken.
type Seams struct {

	// Actions are the states no client can bring about on its own, brought
	// about however this subject's deployment brings them about. Absent fields
	// skip their assertions.
	Actions Actions

	// NewSubject mints a caller. Required.
	//
	// The default is a caller in a tenant nothing else in this run shares,
	// which is what makes an assertion's rows findable in a database it does
	// not own. InTenant and AsAdmin narrow it; both are declined by returning
	// ErrSubjectUnsupported, and the assertions that asked skip.
	NewSubject func(ctx context.Context, opts ...SubjectOption) (*Subject, error)

	// Anonymous returns a connection carrying no caller — what a request looks
	// like once the consumer's authentication interceptor has declined to add
	// one.
	//
	// It is a connection rather than a context decorator because "no caller" is
	// not always the absence of a decoration. A deployment may carry
	// credentials on the connection itself, in which case an unauthenticated
	// call needs a connection of its own rather than a call made without
	// metadata.
	//
	// The assertions it unlocks are the ones that enumerate a service's RPCs
	// from its descriptor rather than naming them, so they cover a method added
	// later without being edited. A subject that supplies none skips them.
	Anonymous func(ctx context.Context) (grpc.ClientConnInterface, error)

	// AnonymousHTTP returns an HTTP client carrying no caller — Anonymous's
	// counterpart for the surfaces this module serves over HTTP.
	//
	// A client rather than a request decorator for Anonymous's reason: a
	// deployment may carry credentials in the client itself — a cookie jar, a
	// transport that signs — and a request made without headers through that
	// client is not a request with nobody on it. Where the routes are is the
	// probe subject's HTTP.BaseURL; a subject that supplies no client, or whose
	// subjects carry no HTTP, skips the HTTP half.
	AnonymousHTTP func(ctx context.Context) (*http.Client, error)

	// SignedIn turns a token the sign-in surface issued into a caller: a
	// connection carrying that token the way the deployment's clients carry
	// one. Nil skips the assertions that call as somebody the suite signed in
	// itself, with the reason printed.
	//
	// It is the conformance face of the Authorizer seam docs/client-contract.md
	// describes, and a seam for that seam's reason: nothing in this module
	// fixes how an access token reaches a server, so no suite can attach one.
	// A deployment whose clients send the contract's default dials with
	// "authorization: Bearer <token>" on every call and is done.
	//
	// It is what lets an assertion be about a person whose every credential
	// the suite chose — a registrant, with a password it typed and no second
	// factor until it enrolls one — rather than a caller NewSubject minted,
	// whose credentials are the deployment's. The connection is held to the
	// calls declared for it exactly as a minted caller's is; see
	// Session.SignedIn.
	SignedIn func(ctx context.Context, token *signinpb.IssuedToken) (grpc.ClientConnInterface, error)

	// VisitorScope is the tenant the deployment's waitlists surface places a
	// request with nobody on it in — what its scope resolver answers for the
	// Anonymous connection. Nil skips the assertions about the public half made
	// without a caller, with the reason printed.
	//
	// A fact about the deployment rather than an action, and it is needed
	// because a visitor's tenant is the one thing about a signup page no
	// client can learn: the resolver reads the connection, and a deployment
	// may place visitors by hostname, by port or nowhere but the global
	// directory. An assertion that a visitor joined a list has to open that
	// list somewhere the visitor lands, and then read it back as an operator
	// in the same place.
	//
	// A pointer for SubjectRequest.Scope's reason: tenancy.Global() is a real
	// answer here — it is the default resolver's — and must not read as
	// "unknown".
	VisitorScope *tenancy.Scope

	// WebAuthn is the relying party the deployment's passkeys surface verifies
	// ceremonies against, which is what a virtual authenticator has to answer
	// as. Nil skips the passkeys suite, with the reason printed.
	//
	// A fact about the deployment rather than an action, and one no client can
	// learn whole: the relying party's ID is in the options a registration
	// hands out, but the origin a browser signs is not, and it has to match
	// exactly — scheme and port included.
	WebAuthn *WebAuthnDeployment

	// Roles is the deployment's role vocabulary, for the assertions that grant
	// a role. The zero value is this package's own literals, which a deployment
	// whose roles are open accepts; one whose roles are a closed vocabulary
	// names the ones it declares. See Roles.
	Roles Roles

	// CommentTargetType is a target type the deployment's comments.Targets
	// declares, for the reads that name a comment target. Which kinds of thing
	// accept comments is the application's vocabulary, so no suite can guess
	// one; empty skips the reads that need it, with the reason printed.
	//
	// It is also what the comments suite writes against when
	// Actions.CommentTarget is nil, on an identifier it mints — which only a
	// type declared without an existence check accepts. A deployment whose
	// types are checked supplies the action as well, and the action wins.
	CommentTargetType string

	// WebhookURL is an address the deployment's webhooks surface accepts an
	// endpoint at, for the assertions that register one. Empty is not an
	// absence: it is https://192.0.2.1/conformance/hook, in the block RFC 5737
	// reserves for documentation, which this module's default URL check
	// accepts and which routes nowhere.
	//
	// A deployment that has replaced the URL check with an allowlist of its own
	// hosts names one of them here — ideally one that accepts and discards,
	// since each endpoint is archived when its test ends but a worker may
	// deliver to it before then. What it cannot do is leave the field empty and
	// have its refusals skip: a refused registration fails, because "this
	// deployment refuses the address" and "this deployment refuses every
	// endpoint" answer the same code, and only the subject knows which it is.
	WebhookURL string

	// Dialect is what the subject's database is, for the assertions that must
	// narrow to it. The zero value means unknown, and an assertion that needs
	// to know skips.
	//
	// It exists for precision rather than for behavior: an assertion that
	// branches on the dialect to expect different results is asserting that
	// this module's surfaces behave differently per dialect, which is the
	// opposite of what the matrix promises. What it legitimately decides is how
	// closely a timestamp may be compared — see the package documentation.
	Dialect dialect.Dialect

	// OperatorMethods are the calls the deployment reserves to an operator, as
	// full method names. Nil reserves nothing, which is a deployment whose
	// members may make every call.
	//
	// Any call may be named, on any service. Which calls a deployment keeps
	// from its members is its product's decision — a dispute desk that keeps
	// commenting to its staff is as legitimate as a household app that keeps
	// nothing — and this module draws no line of its own. The suites make each
	// call named here as an operator and every other call as a member: each
	// caller declares the calls it goes on to make (see Making), is minted an
	// administrator where it declares a reserved one, and is held to its
	// declaration on its own connection. An assertion that is only about a
	// member making a call named here skips, with the reservation named,
	// because the deployment has promised its members nothing about that call.
	//
	// A deployment's own list is the one to hand over — the one its
	// authorization interceptor reads. Run checks only that each entry is
	// spelled as a full method name; conformance/reservations checks that the
	// deployment refuses a member each entry on one of this module's surfaces.
	OperatorMethods []string

	// OperatorRoutes are the routes on this module's HTTP surfaces the
	// deployment reserves to an operator, keyed as each surface's own route
	// constants key them — the method, a space, and the path at the surface's
	// default base path with its parameters braced, as in
	// operationshttp.RouteCancel, "POST /operations/{operationID}/cancel". Nil
	// reserves nothing.
	//
	// They are OperatorMethods for the HTTP half, read the same way: a caller
	// that declares a reserved route with Making is minted an administrator,
	// and its client is held to the routes it declared. A deployment reserves a
	// route by granting its members none of the permissions the surface's
	// Permissions map says it requires, which is the list to hand over here.
	//
	// Run checks that each entry is a route one of this module's HTTP surfaces
	// mounts, and that none is a route its surface exports among its
	// OwnStandingRoutes: those ask for no grant, so no deployment can keep them
	// from its members, and a list naming one has contradicted itself.
	// conformance/reservations checks that the deployment refuses a member
	// each entry as 403, and before it reads the row the route names.
	OperatorRoutes []string

	// FulfillmentBudget is how long this deployment may take to pick queued
	// work up and finish it — a privacy request submitted to its worker, say.
	// Zero is DefaultFulfillmentBudget. Session.Await waits this long, or until
	// the test's own deadline if that comes first.
	//
	// A fact about the deployment rather than an action, and the deployment's
	// because nothing else knows it: one worker is woken the moment work is
	// queued, another sleeps a whole poll interval first, and a suite that
	// guessed would either flake on the slow one or wait out a minute's
	// silence on a fast one that had stopped.
	FulfillmentBudget time.Duration

	// PasswordChangeGateDisabled says the deployment installs no gate holding a
	// caller who owes a forced password change at the form — it built
	// signin/grpc's PrincipalExtractor WithoutPasswordChangeGate, or
	// authenticates through its own interceptor and installed no
	// PasswordChangeGate behind it. True skips the assertion that
	// such a caller's ordinary call is refused, with that printed; false, the
	// zero value, asserts it, because the gate is on by default.
	//
	// It is a fact about the deployment rather than an action, and the only one
	// the suite cannot find out for itself: a call that succeeds for a flagged
	// caller is either a gate that is off or a gate that is broken, and only the
	// deployment knows which it meant.
	PasswordChangeGateDisabled bool

	// ErrorReasonsStripped says the deployment's edge drops a refusal's
	// client-safe reason before it reaches a client. True skips the reason half
	// of each assertion that reads one, with that printed; the code half runs
	// regardless.
	//
	// A refusal is asserted by its status code everywhere, and never by a Go
	// sentinel decoded off the wire: the encoded error chain is this module's
	// client talking to this module's server, and a deployment that strips it
	// before a response leaves — so internal wording never reaches a client —
	// is doing what docs/client-contract.md tells a client-facing edge to do.
	// The reason is different. Where the contract lists one (sign-in's
	// refusals) it is a promise, and R11 says it survives exactly that edge:
	// errors/grpc.StripEncodedErrorDetail removes the chain and leaves the
	// google.rpc.ErrorInfo alone. So it is asserted unless the subject says
	// otherwise, and false is the zero value. A deployment that sets this has
	// written down that it breaks R11 for its clients, which is the point of
	// making it say so rather than making everybody else opt in.
	ErrorReasonsStripped bool

	// ImmediateRevocation says the deployment checks an access token's login on
	// every request — signin.Service.CheckSignIn, which the sign-in extractor
	// makes through its WithSignInCheck — so a login that ends stops its access
	// token working at once rather than when it expires. True runs the
	// assertion that it does; false skips it, with the reason printed.
	//
	// It is a declaration rather than something a suite could find out,
	// because the default is the other answer and a legitimate one: an access
	// token is a signed statement that stands until it expires, and a sign-out
	// takes effect within one access-token lifetime. A deployment that bought
	// the per-request read has promised its clients more than that, and this is
	// where it says so and is held to it.
	ImmediateRevocation bool

	// MediaObjectsShared says the deployment's mediaregistry Entitlement lets
	// somebody other than an object's owner read it — the attachments on a
	// ticket everybody assigned to it may open. True skips the assertion that
	// a colleague is refused, with the reason printed.
	//
	// False is mediaregistry/http's default, OwnerOnly, and so it is the zero
	// value: a deployment that never supplied an Entitlement has nothing to
	// say here, and one that supplied a wider rule says so rather than having
	// the suite guess which rule it wrote.
	MediaObjectsShared bool

	// InvitationTokenReturned says the deployment's identity server was built
	// with identitygrpc.WithInvitationTokenReturned, so Invite answers the
	// sender with the token beside the invitation and the sender can copy the
	// link. True asserts that reading — the token comes back, it is the one the
	// deployment delivered, and a copied link registers the addressed person
	// into the inviting account and nobody else — in place of the default's,
	// that it does not come back at all.
	//
	// False is the server's default, and so it is the zero value: a deployment
	// that never opted in has nothing to say here, and one that did says so
	// rather than having the suite accept either answer.
	InvitationTokenReturned bool

	// PrincipalPermissions says the deployment's identity server was built with
	// identitygrpc.WithPermissionResolver, so GetPrincipal answers what the
	// caller may do beside who they are. True asserts the field is present and
	// follows the account GetPrincipal resolved; false asserts it is absent.
	//
	// What a role permits is the deployment's, so no assertion names a
	// permission. What the suite holds a deployment to is that the answer is a
	// function of the roles held where the read was asked about: two callers
	// holding the same role in one account are told the same thing there, and
	// a caller who names an account they are a member of is told what that
	// membership permits rather than what their own account does.
	//
	// False is the server's default, and so it is the zero value: a server with
	// no role policy to consult serves no field rather than an empty one.
	PrincipalPermissions bool

	// AccountSettingsUnresolved says the deployment's settings
	// SubjectAuthorizer resolves no account subjects at all, so a caller asking
	// for their own account's setting is refused as a stranger asking for
	// somebody else's would be. True skips the assertion that a stranger's
	// account is refused, with that printed, because the refusal says nothing
	// about membership there.
	//
	// It is a declaration rather than something a suite could find out: a
	// refusal of the caller's own account is either a deployment that resolves
	// none or an authorizer that is broken, and only the deployment knows which
	// it meant. False asserts the caller's own account is answered first.
	AccountSettingsUnresolved bool

	// RefreshTokensUnissued says the deployment's sign-in service was built
	// with no refresh token store, so a sign-in answers an access token alone.
	// True skips the assertions about rotating and ending a login, with that
	// printed.
	//
	// docs/client-contract.md calls a sign-in with no refresh token a valid
	// shape, so a deployment may have it — but a sign-in that answers none is
	// also what a service that stopped wiring its store answers, and a suite
	// that read the absence off the answer would pass every rotation assertion
	// by skipping it. False, the zero value, fails a sign-in that carries none.
	RefreshTokensUnissued bool
}

// Subject is one caller, and the clients it calls through.
//
// The clients are per subject rather than per run because the tenant travels on
// the connection — docs/client-contract.md states that as the rule a client
// obeys, and a suite that shared one connection between two tenants would be
// asserting against a seam this module does not offer. direct mode returns the
// same clients for every subject and distinguishes them on Decorate; a deployed
// subject typically dials its own.
type Subject struct {

	// Surfaces are the clients this caller reaches the service through. A nil
	// field is a surface the subject did not mount.
	Surfaces Surfaces

	// Decorate adds to a call context whatever this subject's connection is not
	// already carrying — a principal in direct mode, credentials metadata over
	// a wire. Nil means the connection carries everything.
	Decorate func(context.Context) context.Context

	// Conn is this caller's connection: what its surfaces are reached
	// through, and what the assertions that enumerate a service's RPCs from its
	// descriptor invoke them on.
	//
	// Required of a subject that mounts any gRPC surface. Every caller a suite
	// mints is held to the calls it declared with Making, and that check is a
	// connection in front of this one: the surfaces the subject hands over are
	// read for which of them it mounts, and rebuilt over the checked
	// connection. A subject whose typed clients were dialed over Conn — every
	// subject built from this module's client wrappers — loses nothing by it.
	Conn grpc.ClientConnInterface

	// UserID is the calling user's identifier, where the subject knows it.
	// Empty is legal: a subject that mints credentials without surfacing an
	// identifier leaves it empty, and the assertions that need one skip.
	UserID string

	// AccountID is the account this caller's requests are against — the
	// principal's active account — where the subject knows it. Empty is legal,
	// on the same terms as UserID.
	AccountID string

	// HTTP is how this caller reaches the surfaces served over HTTP rather than
	// gRPC. Nil is a subject that serves none of them, and the assertions that
	// need one skip.
	HTTP *HTTPSurfaces

	// Scopes is the tenant this caller is in on each surface whose tenancy
	// differs from Scope, keyed by the surface's Suite.Name. Nil is a
	// deployment whose surfaces all share one tenancy, which is every one this
	// module assembles.
	//
	// It exists because a consumer's surfaces need not agree on what a tenant
	// is. One deployment serves its directory, its settings and its catalog
	// from the global scope and confines its issue reports, its webhooks and
	// its audit chain to the caller's account; no single value of Scope
	// describes both, and a suite that read one surface's tenancy off another's
	// would assert a confinement the deployment never promised — or miss one it
	// did. Read it through ScopeFor rather than directly.
	Scopes map[string]tenancy.Scope

	// Scope is whose directory this caller is in, on every surface Scopes does
	// not name. Assertions use it to name the rows they seeded and to prove a
	// neighbor's are absent.
	Scope tenancy.Scope
}

// WebAuthnDeployment is the relying party a deployment's passkey ceremonies
// are verified against.
type WebAuthnDeployment struct {
	// RPID is the relying party's ID — a registrable domain, "example.com".
	RPID string

	// Origin is the origin a browser reports signing from, exactly as the
	// relying party accepts it: "https://example.com", not the RP ID.
	Origin string
}

// ScopeFor is the tenant this caller is in on the named surface: its entry in
// Scopes where there is one, and Scope where there is not.
func (s *Subject) ScopeFor(surface string) tenancy.Scope {
	if scope, ok := s.Scopes[surface]; ok {
		return scope
	}

	return s.Scope
}

// HTTPSurfaces are the three surfaces this module serves over HTTP, as one
// caller reaches them.
//
// A client and a base URL rather than a client per surface, because there is
// no generated client for these and the three share one router: what differs
// between them is the path, and each is mounted at its own package's default
// base path under BaseURL. A deployment that mounted one elsewhere is not
// describable here yet, and says so by leaving that surface's flag false.
type HTTPSurfaces struct {
	// Client makes this caller's requests, carrying whatever the deployment
	// authenticates an HTTP request by.
	Client *http.Client

	// BaseURL is where the router is served, with no trailing slash.
	BaseURL string

	// Each flag is a surface the subject mounted at its default base path.
	DataPrivacy   bool
	MediaRegistry bool
	Operations    bool
}

// Context applies Decorate, or returns ctx when there is nothing to add.
func (s *Subject) Context(ctx context.Context) context.Context {
	if s == nil || s.Decorate == nil {
		return ctx
	}

	return s.Decorate(ctx)
}

// Surfaces are the gRPC surfaces this module mounts, as the generated client
// interface each one is reached through.
//
// The generated interface rather than this module's <pkg>/grpc/client wrapper,
// because the wrapper embeds the interface and a subject that dialed its own
// connection has one of those already. A subject holding a wrapper assigns it
// directly. What a suite calls through is the generated client over the
// subject's Conn, rebuilt for each caller so that it makes only the calls it
// declared; a field here says which surfaces the subject mounts.
type Surfaces struct {
	Audit         auditpb.AuditServiceClient
	Billing       billingpb.BillingServiceClient
	Comments      commentspb.CommentsServiceClient
	Identity      identitypb.IdentityServiceClient
	IssueReports  issuereportspb.IssueReportsServiceClient
	Notifications notificationspb.NotificationsServiceClient
	OAuth2Clients oauth2clientspb.OAuth2ClientsServiceClient
	Passkeys      passkeyspb.PasskeysServiceClient
	PasswordReset passwordresetpb.PasswordResetServiceClient
	Settings      settingspb.SettingsServiceClient
	SignIn        signinpb.SignInServiceClient
	Waitlists     waitlistspb.WaitlistsServiceClient

	// SignInAdministration is the operator half of sign-in, which
	// authentication/signin/grpc's Server registers beside SignIn. A subject
	// that mounts it sets it, and one that mounts sign-in without it leaves it
	// nil and the assertions that need an operator's view of somebody's
	// logins skip.
	SignInAdministration signinpb.SignInAdministrationServiceClient
	Webhooks             webhookspb.WebhooksServiceClient
}

// Actions are the states no client can bring about on its own.
//
// An action rather than a row, and that is the whole of the design. The first
// shape of this seam handed over an audit.Entry for the suite to write, which
// is a backdoor: it puts the suite in the business of manufacturing state, and
// it asserts against a row that may not resemble the ones the deployment
// actually produces.
//
// What a subject is asked for instead is the thing it already does. "Do
// something in this tenant that your deployment records an audit entry for,
// and tell me what that entry will name." A consumer satisfies it by creating
// a webhook or registering a user, exercising their handler, their recorder
// and their transaction; this module's own harnesses satisfy it by calling the
// recorder, which is what a consumer's handler does at the end of that path
// anyway. Same seam, and in a deployed subject it is the real path rather than
// a shortcut past it.
//
// That asymmetry is not incidental. No gRPC surface in this module records an
// audit entry — settings and identity only name audit.Recorder in comments
// showing a consumer how to wire one — because recording belongs inside the
// transaction of the change it describes, and this module does not own that
// transaction. So "call the surface and read the log afterwards" works in a
// consumer's deployment and writes nothing at all in this module's, which is
// exactly why the seam has to describe the action rather than assume it.
type Actions struct {
	// Auditable does something in this tenant the deployment records an audit
	// entry for, and reports what that entry names.
	Auditable func(ctx context.Context, scope tenancy.Scope) (*Audited, error)

	// Credentialed gives a caller a stored secret, the way the deployment's own
	// password or credential path does, and reports a fragment of it that must
	// never appear in a response.
	//
	// The fragment comes back rather than going in for the same reason Audited
	// reports what it touched: the deployment chooses how it stores a secret,
	// and an assertion that supplied one would be searching responses for a
	// string nothing ever wrote. What is asserted is that whatever the
	// deployment did store is not rendered — so the fragment has to be the
	// deployment's own.
	Credentialed func(ctx context.Context, scope tenancy.Scope, userID string) (string, error)

	// InvitationToken reports the token the deployment delivered to an
	// invitation's recipient — the secret in the link a real invitee clicks.
	//
	// By default no RPC returns it, and that is the point of the design:
	// identity's Invite answers the sender with a redacted invitation, and the
	// token reaches the recipient through whatever the deployment's AfterInvite
	// hook queues. A deployment that returns it to the sender (see
	// Seams.InvitationTokenReturned) still delivers it this way, and the suite
	// checks the two agree. A consumer implements this by reading the mail their
	// deployment sent; this module's harnesses by a hook that remembers what it
	// was handed. Either way the token is the deployment's, which is what makes
	// accepting with it a real acceptance.
	InvitationToken func(ctx context.Context, scope tenancy.Scope, invitationID string) (string, error)

	// EmailVerified does for a user what the deployment's email verification
	// link does: marks the address they registered with as theirs.
	//
	// An action rather than an RPC because proving you hold an address is a
	// round trip through a mailbox, and the suite cannot hold one. The reads
	// that answer only a verified caller are asserted through it.
	EmailVerified func(ctx context.Context, scope tenancy.Scope, userID string) error

	// Subscribed makes a paid subscription exist for one account in this
	// tenant, the way the payment provider's webhook handler does, with a paid
	// period that covers the moment it was made.
	//
	// There is no CreateSubscription RPC and that is a ruling rather than a
	// gap: a subscription mirrors what a payment provider says is paid for, so
	// one created over the wire would grant paid features with nothing behind
	// them. A consumer's own suite documented this as an assertion it could not
	// write — "needs data seeding the harness does not currently provide" — and
	// omitted it rather than write it flaky. This is the seam that closes it.
	//
	// It names the account because a subscription belongs to one, and the reads
	// that matter most about it are the ones confining an account to its own:
	// a tenant-wide subscription would be a row no account-keyed read could be
	// asserted against.
	Subscribed func(ctx context.Context, scope tenancy.Scope, accountID string) (*billing.Subscription, error)

	// PasswordResetToken reports the secret the deployment most recently mailed
	// to an address as a password reset link — the secret a person who cannot
	// sign in clicks through with.
	//
	// There is no RPC that returns it, deliberately: RequestPasswordReset
	// answers a known address and an unknown one identically, and the secret
	// reaches the person through the deployment's passwordreset.Mailer. A
	// consumer implements this by reading the mail their deployment sent; this
	// module's harnesses by a mailer that remembers what it was handed. Either
	// way the secret is the deployment's, which is what makes completing a reset
	// with it a real reset rather than one the suite forged.
	PasswordResetToken func(ctx context.Context, scope tenancy.Scope, emailAddress string) (string, error)

	// Notified does something in this tenant the deployment tells a user
	// about, and reports the identifier of the notification that landed in
	// their inbox.
	//
	// There is no RPC that files a notification, and that is the design rather
	// than a gap: a notification is the application telling somebody something
	// happened, so a client that could file one could put words in the
	// application's mouth on anybody's lock screen. Every inbox therefore fills
	// the same way in every deployment — the application's own code, inside the
	// transaction of whatever it is announcing. A consumer implements this by
	// doing one of those things; this module's harnesses by calling the inbox
	// the composition root built, which is where that path ends anyway.
	//
	// The identifier comes back rather than going in for Audited's reason: the
	// deployment decides what a notification says, and the suite finds it by
	// what the deployment reports rather than by words it guessed.
	Notified func(ctx context.Context, scope tenancy.Scope, userID string) (string, error)

	// VerificationToken reports the secret the deployment most recently mailed
	// to an address as a verification link — the one sent when somebody
	// registered with it, or the one a later RequestVerificationEmail sent in
	// its place. It is the link that proves the address and finishes the
	// registration.
	//
	// There is no RPC that returns it: sign-in's Register answers whoever
	// called it with the registrant and never with the link, because the
	// person who clicks it is not the client that registered them, and a
	// resend answers with nothing at all. The secret reaches the registrant
	// through whatever the deployment queues from its identity registration
	// hook, and a resend's through its sign-in VerificationMailer. A consumer
	// implements this by reading the newest such mail their deployment sent;
	// this module's harnesses by a hook and a mailer that remember what they
	// were handed. "Most recently" matters: the resend assertions compare the
	// link read after a resend with the one read before it, and an action that
	// kept answering with the first would fail them.
	VerificationToken func(ctx context.Context, scope tenancy.Scope, emailAddress string) (string, error)

	// MagicLinkToken reports the secret the deployment most recently mailed to
	// an address as a sign-in link — the secret a person with no password
	// clicks through with.
	//
	// There is no RPC that returns it, deliberately: RequestMagicLink answers a
	// known address and an unknown one identically, and the secret reaches the
	// person through the deployment's signin.MagicLinkMailer. It is
	// PasswordResetToken's shape for PasswordResetToken's reason, and a subject
	// whose deployment mails no sign-in links leaves it nil.
	MagicLinkToken func(ctx context.Context, scope tenancy.Scope, emailAddress string) (string, error)

	// HandleReminder reports the handle the deployment most recently mailed to
	// an address that asked what it signs in with.
	//
	// There is no RPC that returns it, for MagicLinkToken's reason:
	// RequestHandleReminder answers a known address and an unknown one
	// identically, and the handle reaches the person through the deployment's
	// signin.HandleReminderMailer. An address that was mailed nothing is an
	// error, which is how the suite tells a reminder that was sent from one that
	// was not. A subject whose deployment reminds nobody leaves it nil.
	HandleReminder func(ctx context.Context, scope tenancy.Scope, emailAddress string) (string, error)

	// WaitlistLinks reports the links the deployment most recently mailed to
	// an address that joined a list — the confirmation link a person follows
	// to make their signup count, and the unsubscribe link beside it.
	//
	// Its presence is also the statement that this deployment confirms: that
	// its waitlists surface was built with waitlistsgrpc.WithConfirmation, so
	// a join is held pending until its link is followed. The waitlists suite
	// follows the link after every join it makes when this is set, and asserts
	// the loop itself; left nil, it asserts a deployment whose joins wait at
	// once. A confirming deployment that leaves it nil fails the assertions
	// that read a signup's status, because every signup they make is still
	// pending.
	//
	// There is no RPC that returns either token, for PasswordResetToken's
	// reason: Join answers every address identically, and the links reach the
	// person through the deployment's waitlistsgrpc.ConfirmationMailer. A
	// consumer implements this by reading the mail their deployment sent; this
	// module's harnesses by a mailer that remembers what it was handed. The
	// scope is the tenant the signup was made in, and the contact is as it was
	// typed.
	WaitlistLinks func(ctx context.Context, scope tenancy.Scope, listID, contact string) (*WaitlistLinks, error)

	// Registered stores an object's bytes and registers them in this tenant
	// as the user's, the way the deployment's own upload path does, and
	// reports what it registered.
	//
	// There is no route or RPC that creates one, and that is mediaregistry's
	// design rather than a gap: an object comes to exist through whatever
	// upload the application offers — a form, a signed URL, a migration from
	// an existing bucket — and the registry is the row the application writes
	// once the bytes are somewhere. Only the read is served. A consumer
	// implements this by uploading through their own path; this module's
	// harnesses by mediaregistry.StoreAndRecord over the manager and store the
	// composition root built, which is where that path ends anyway.
	//
	// The bytes come back rather than going in for Audited's reason: what an
	// object is belongs to the deployment, and what the suite asserts is which
	// caller gets it back, not what it contains.
	Registered func(ctx context.Context, scope tenancy.Scope, userID string) (*RegisteredObject, error)

	// CommentTarget brings a thing that accepts comments into being in this
	// tenant, the way the deployment does, and reports its target type and
	// identifier — a recipe created, a ticket opened. The tenant is the one
	// the commenting caller is in on the comments surface, as
	// Subject.ScopeFor reads it.
	//
	// It is needed wherever a target type's comments.TargetDefinition carries
	// an existence check, which refuses a comment on a thing the application
	// does not have, and rightly: an identifier the suite minted names nothing,
	// and no client of this module's surfaces can make one name something,
	// because the thing lives in a table the application owns. Nil falls back
	// to Seams.CommentTargetType and a minted identifier, which is right for a
	// deployment that checks nothing.
	//
	// Each call must report a thing no earlier call reported: a listing by
	// target reads everything said about it, and the suite finds its own rows
	// there by being the only one who has spoken. Every call must report the
	// same target type too, since the moderation read is asserted across two
	// targets of one type.
	CommentTarget func(ctx context.Context, scope tenancy.Scope) (targetType, targetID string, err error)

	// ArtifactExpired brings a completed export's artifact window to an end,
	// and runs the deployment's sweep over it: the artifact is gone and the
	// request reads expired, the way it does once the days a deployment keeps
	// an export for have passed and its scheduled sweep has come round.
	//
	// An action because no client can bring either about. The window is
	// stamped onto the row by the worker that completed the export, so a
	// shorter one in configuration reaches only exports not yet made, and the
	// sweep is a job the deployment schedules rather than a call anybody
	// makes. How the subject gets there — a sweep run at a clock past the
	// request's expiry, most likely — is its own business, and the suite only
	// reads the row afterwards.
	//
	// A sweep is usually deployment-wide, so a subject whose sweep would expire
	// other exports as well confines it however it can — to this request, or
	// behind a lock its own tests share — rather than let the suite's
	// assertions about a live artifact race it. It returns an error when the
	// request's artifact was not expired, rather than reporting success on a
	// sweep that found nothing.
	ArtifactExpired func(ctx context.Context, scope tenancy.Scope, requestID string) error
}

// WaitlistLinks are the two secrets a confirming deployment mails to an address
// that joined a list, as bare tokens.
type WaitlistLinks struct {
	// Confirm is the token the confirmation link carries, which the waitlists
	// surface's Confirm spends.
	Confirm string

	// Unsubscribe is the token the unsubscribe link beside it carries, which
	// the surface's Unsubscribe spends.
	Unsubscribe string
}

// RegisteredObject is what a registration action stored, as the guarded read
// will serve it.
type RegisteredObject struct {
	// ID is the registered object's identifier, which is what its route is
	// addressed by.
	ID string

	// Content is the bytes that were stored. The read that serves them must
	// answer with exactly these, so it must not be empty: an empty object is
	// indistinguishable from a read that served nothing.
	Content []byte
}

// Audited is what an auditable action touched, as the entry recording it will
// name the same thing.
//
// It is what the action reports rather than what the suite supplied, because a
// deployment chooses its own resource types and actors and an assertion that
// dictated them would be asserting against a log nobody keeps.
type Audited struct {
	// ResourceType and ResourceID are the row the action touched. The suite
	// finds the entry by them, so they have to match what the deployment
	// recorded.
	ResourceType string
	ResourceID   string

	// ActorID is who the deployment recorded as having acted. Empty is legal,
	// and the assertions that query by actor skip.
	ActorID string
}

// SubjectOption narrows what NewSubject mints.
type SubjectOption func(*SubjectRequest)

// SubjectRequest is what the options accumulate into, and what a subject
// factory reads.
type SubjectRequest struct {
	// Scope, when non-nil, is the tenant the caller must be in — the two-
	// members-of-one-account case. Nil asks for a tenant of this caller's own.
	//
	// A pointer because the zero tenancy.Scope is undecided rather than global,
	// and those are two different requests: nil is "any fresh tenant", and a
	// non-nil tenancy.Global() is a caller in the scope belonging to nobody.
	// Reading presence off the zero value would collapse them, which is the
	// conflation tenancy's own documentation exists to prevent.
	Scope *tenancy.Scope

	// Surface is the surface whose tenancy Scope was read from, as its
	// Suite.Name, and empty where Scope is nil. A factory whose surfaces differ
	// in what a tenant is reads the two together: an account's scope named for
	// issuereports asks for a second member of that account, and the global
	// scope named for billing asks for a caller in the same directory with an
	// account of their own.
	Surface string

	// Methods are the calls the caller goes on to make, as full method names
	// and route keys, which a suite names on every caller it mints with
	// Making. A factory may
	// ignore them: Session.Subject has already read them against
	// Seams.OperatorMethods and Seams.OperatorRoutes and asked for an
	// administrator where the subject reserves one. They are there for a harness that wants to refuse a caller
	// every reserved call it was not minted for, which is how this module's own
	// keeps each suite honest about naming every call it makes.
	Methods []string

	// attempting are the calls among Methods named with Attempting, which
	// Session.Subject leaves out when it reads Methods against
	// Seams.OperatorMethods.
	attempting []string

	// Admin asks for a caller holding whatever service role the deployment
	// treats as administrative.
	Admin bool

	// member is AsMember or Attempting: the suite asked for a caller with no administrative
	// standing, and has already skipped where Methods names a reserved call.
	// A factory has nothing to do with it, so it is not exported.
	member bool
}

// InTenant asks for a caller in an existing tenant rather than a fresh one: the
// tenant scope names on surface. A suite passes its own Suite.Name, and reads
// scope off the caller it wants company for with Subject.ScopeFor.
func InTenant(surface string, scope tenancy.Scope) SubjectOption {
	return func(r *SubjectRequest) {
		r.Scope = &scope
		r.Surface = surface
	}
}

// AsAdmin asks for an administrative caller.
func AsAdmin() SubjectOption {
	return func(r *SubjectRequest) { r.Admin = true }
}

// NewSubjectRequest applies opts, for a subject factory to read.
func NewSubjectRequest(opts ...SubjectOption) *SubjectRequest {
	req := &SubjectRequest{}

	for _, opt := range opts {
		if opt != nil {
			opt(req)
		}
	}

	return req
}

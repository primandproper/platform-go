package conformance

import (
	"context"
	"net/http"

	"github.com/primandproper/platform-go/v14/audit/auditpb"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients/oauth2clientspb"
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

	// CommentTargetType is a target type the deployment's comments.Targets
	// declares, for the reads that name a comment target. Which kinds of thing
	// accept comments is the application's vocabulary, so no suite can guess
	// one; empty skips the reads that need it, with the reason printed.
	CommentTargetType string

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

	// ExclusiveDatabase says the suite is the only writer against this
	// subject's database for the length of the run.
	//
	// It unlocks the handful of assertions that cannot be phrased without it —
	// a sweep's survivors, an empty-result read — and it is false by default
	// because a consumer running this against a shared environment is the case
	// that must be safe when nobody thought about it.
	ExclusiveDatabase bool

	// ControlledTime says the suite may move the clock the subject's service
	// reads, through whatever mechanism the subject arranged.
	//
	// direct mode sets it where it runs inside a testing/synctest bubble.
	// A deployed service cannot offer it, so expiry and pacing assertions skip
	// there — which is honest: nothing a consumer can do makes their production
	// clock movable, and a suite that waited for real time is a suite nobody
	// runs.
	ControlledTime bool
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

	// Conn is this caller's connection, for the assertions that enumerate a
	// service's RPCs from its descriptor and invoke them dynamically rather
	// than through a typed client.
	//
	// Optional, and separate from Surfaces because a typed client is not
	// required to expose the connection underneath it. A subject that supplies
	// none skips those assertions; everything written against a typed client
	// runs regardless.
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

	// Scope is whose directory this caller is in. Assertions use it to name
	// the rows they seeded and to prove a neighbor's are absent.
	Scope tenancy.Scope
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

// Surfaces are the twelve gRPC surfaces this module mounts, as the generated
// client interface each one is reached through.
//
// The generated interface rather than this module's <pkg>/grpc/client wrapper,
// because the wrapper embeds the interface and a subject that dialed its own
// connection has one of those already. A subject holding a wrapper assigns it
// directly.
type Surfaces struct {
	Audit         auditpb.AuditServiceClient
	Billing       billingpb.BillingServiceClient
	Comments      commentspb.CommentsServiceClient
	Identity      identitypb.IdentityServiceClient
	IssueReports  issuereportspb.IssueReportsServiceClient
	Notifications notificationspb.NotificationsServiceClient
	OAuth2Clients oauth2clientspb.OAuth2ClientsServiceClient
	PasswordReset passwordresetpb.PasswordResetServiceClient
	Settings      settingspb.SettingsServiceClient
	SignIn        signinpb.SignInServiceClient
	Waitlists     waitlistspb.WaitlistsServiceClient
	Webhooks      webhookspb.WebhooksServiceClient
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
	// There is no RPC that returns it, and that is the point of the design:
	// identity's Invite answers the sender with a redacted invitation, and the
	// token reaches the recipient through whatever the deployment's AfterInvite
	// hook queues. A consumer implements this by reading the mail their
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

	// VerificationToken reports the secret the deployment mailed to an address
	// when somebody registered with it — the link that proves the address and
	// finishes the registration.
	//
	// There is no RPC that returns it: sign-in's Register answers whoever
	// called it with the registrant and never with the link, because the
	// person who clicks it is not the client that registered them. The secret
	// reaches the registrant through whatever the deployment queues from its
	// identity registration hook. A consumer implements this by reading the
	// mail their deployment sent; this module's harnesses by a hook that
	// remembers what it was handed.
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

	// Admin asks for a caller holding whatever service role the deployment
	// treats as administrative.
	Admin bool
}

// InTenant asks for a caller in an existing tenant rather than a fresh one.
func InTenant(scope tenancy.Scope) SubjectOption {
	return func(r *SubjectRequest) { r.Scope = &scope }
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

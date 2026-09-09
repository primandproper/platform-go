package sentinelmatrix

import (
	"slices"

	"github.com/primandproper/platform-go/v14/audit"
	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/billing"
	"github.com/primandproper/platform-go/v14/comments"
	"github.com/primandproper/platform-go/v14/dataprivacy"
	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/issuereports"
	"github.com/primandproper/platform-go/v14/links"
	"github.com/primandproper/platform-go/v14/notifications"
	"github.com/primandproper/platform-go/v14/operations"
	"github.com/primandproper/platform-go/v14/sessions"
	"github.com/primandproper/platform-go/v14/settings"
	"github.com/primandproper/platform-go/v14/waitlists"
	"github.com/primandproper/platform-go/v14/webhooks"

	grpcerrors "github.com/primandproper/primitives-go/errors/grpc"
	httperrors "github.com/primandproper/primitives-go/errors/http"

	"google.golang.org/grpc/codes"
)

// Disposition is what this module has decided one sentinel means on the wire.
// The three are exhaustive by construction: a sentinel either has a case in its
// own package's mappers, or wraps something the platform mappers answer, or
// resolves to a 500.
type Disposition int

const (
	// Mapped: the package's own HTTPMapper and GRPCMapper both answer.
	Mapped Disposition = iota
	// Platform: those two are silent and errors/http and errors/grpc answer,
	// because the sentinel wraps a platform one.
	Platform
	// Unhandled: nobody answers, and a 500 is the honest reply.
	Unhandled
)

func (d Disposition) String() string {
	switch d {
	case Mapped:
		return "mapped by its own package"
	case Platform:
		return "mapped by the platform mappers"
	case Unhandled:
		return "deliberately unmapped"
	default:
		return "unknown"
	}
}

// The packages that map their own sentinels, spelled once each; there are fifteen
// today. Each name is three things — a key in Matrix, an entry in Packages and a
// case in Mappers — and a package that declares a pair later is added in all
// three together.
//
// Each is a path relative to the module root rather than a package name,
// because that is what the roster's own test reads the rows out of.
const (
	auditPkg         = "audit"
	dataPrivacyPkg   = "dataprivacy"
	identityPkg      = "identity"
	linksPkg         = "links"
	operationsPkg    = "operations"
	sessionsPkg      = "sessions"
	signInPkg        = "authentication/signin"
	oauth2ClientsPkg = "authentication/oauth2clients"
	notificationsPkg = "notifications"
	commentsPkg      = "comments"
	webhooksPkg      = "webhooks"
	billingPkg       = "billing"
	issueReportsPkg  = "issuereports"
	settingsPkg      = "settings"
	waitlistsPkg     = "waitlists"
)

// Decision is one sentinel and what this module decided it means on the wire.
type Decision struct {
	Err error
	Is  Disposition
}

// Matrix is the decision made about every exported sentinel in the fifteen
// packages that map their own errors. Its keys are checked against those
// packages' source in both directions, so it is a roster that cannot quietly
// stop describing the tree.
//
//nolint:goconst // The keys are sentinel identifiers in four other packages, and three of those packages have an ErrNilStore. That they collide is a fact the roster records, not a constant this one should extract.
var Matrix = map[string]map[string]Decision{
	auditPkg: {
		// The one thing a reader of the log can be told about a request rather
		// than about the process serving it. An entry never written, one
		// retention has pruned, one that was deleted and one in another
		// tenant's log all arrive as this, and audit/grpc answers all four the
		// same way on purpose — see that package's GetEntry.
		"ErrEntryNotFound": {Err: audit.ErrEntryNotFound, Is: Mapped},

		// The nil-argument sentinels, which wrap errors.ErrNilInputParameter
		// and are answered by the platform mapper for that reason.
		"ErrNilDatabaseClient": {Err: audit.ErrNilDatabaseClient, Is: Platform},
		"ErrNilEntry":          {Err: audit.ErrNilEntry, Is: Platform},
		"ErrNilExecutor":       {Err: audit.ErrNilExecutor, Is: Platform},

		// Everything raised while the consumer's own code assembles an entry or
		// wires this package up. None of them is anything a client sent: an
		// entry with no resource type, no event type or no actor was built by
		// the process recording it, a diff of two different types is a call in
		// that process, and a table prefix is configuration. A 500 is the
		// honest answer to a request that failed because the service was built
		// wrong.
		"ErrDiffTypeMismatch":   {Err: audit.ErrDiffTypeMismatch, Is: Unhandled},
		"ErrEmptyActor":         {Err: audit.ErrEmptyActor, Is: Unhandled},
		"ErrEmptyEventType":     {Err: audit.ErrEmptyEventType, Is: Unhandled},
		"ErrEmptyResourceType":  {Err: audit.ErrEmptyResourceType, Is: Unhandled},
		"ErrInvalidTablePrefix": {Err: audit.ErrInvalidTablePrefix, Is: Unhandled},
		"ErrMalformedHash":      {Err: audit.ErrMalformedHash, Is: Unhandled},
		"ErrNotAStruct":         {Err: audit.ErrNotAStruct, Is: Unhandled},
		"ErrNothingToDiff":      {Err: audit.ErrNothingToDiff, Is: Unhandled},

		// Not returned by Verify at all: a break is a finding, carried in the
		// VerificationResult, and audit/grpc answers one with an ordinary
		// response. It reaches a transport only through a consumer who chose to
		// escalate it, at which point what it means is theirs to decide.
		"ErrChainBroken": {Err: audit.ErrChainBroken, Is: Unhandled},
	},

	billingPkg: {
		// The four absences, one per table. Each is what a client naming a row
		// that is gone, archived, or in another scope is told, and the three
		// cases are one answer so that a read cannot be used to enumerate
		// another tenant's catalog or ledger.
		"ErrProductNotFound":      {Err: billing.ErrProductNotFound, Is: Mapped},
		"ErrPurchaseNotFound":     {Err: billing.ErrPurchaseNotFound, Is: Mapped},
		"ErrSubscriptionNotFound": {Err: billing.ErrSubscriptionNotFound, Is: Mapped},
		"ErrTransactionNotFound":  {Err: billing.ErrTransactionNotFound, Is: Mapped},

		// The five collisions on a unique key. Four are a payment provider's
		// identifier arriving twice, which is the ordinary redelivery this
		// schema is shaped around, and ErrIDTaken is an application handing out
		// an id it has already used. All five are a conflict rather than a
		// server fault, which is what lets a webhook receiver acknowledge the
		// delivery instead of retrying it forever.
		"ErrIDTaken":            {Err: billing.ErrIDTaken, Is: Mapped},
		"ErrProductExists":      {Err: billing.ErrProductExists, Is: Mapped},
		"ErrPurchaseExists":     {Err: billing.ErrPurchaseExists, Is: Mapped},
		"ErrSubscriptionExists": {Err: billing.ErrSubscriptionExists, Is: Mapped},
		"ErrTransactionExists":  {Err: billing.ErrTransactionExists, Is: Mapped},

		// The two answers a guarded write gives a replay: the status was
		// already where the event would have put it, and the money had already
		// arrived. Neither is a failure, and a 500 would tell a caller to retry
		// work that has been done.
		"ErrAlreadyCompleted": {Err: billing.ErrAlreadyCompleted, Is: Mapped},
		"ErrStatusUnchanged":  {Err: billing.ErrStatusUnchanged, Is: Mapped},

		// The seven a caller can correct, each naming a field rather than the
		// call. They are mapped rather than left to the platform because none of
		// them wraps a platform sentinel: they are judgements about a value that
		// was supplied, not about one that was missing.
		"ErrAmbiguousTransaction":      {Err: billing.ErrAmbiguousTransaction, Is: Mapped},
		"ErrBackwardsPeriod":           {Err: billing.ErrBackwardsPeriod, Is: Mapped},
		"ErrInvalidCurrency":           {Err: billing.ErrInvalidCurrency, Is: Mapped},
		"ErrInvalidKind":               {Err: billing.ErrInvalidKind, Is: Mapped},
		"ErrInvalidStatus":             {Err: billing.ErrInvalidStatus, Is: Mapped},
		"ErrNegativeAmount":            {Err: billing.ErrNegativeAmount, Is: Mapped},
		"ErrUnexpectedBillingInterval": {Err: billing.ErrUnexpectedBillingInterval, Is: Mapped},

		// Wrap errors.ErrNilInputParameter, so the platform mappers answer them.
		// Every one is a caller that passed no executor or no entity, which is a
		// wiring failure rather than anything a client sent.
		"ErrNilDatabaseClient": {Err: billing.ErrNilDatabaseClient, Is: Platform},
		"ErrNilExecutor":       {Err: billing.ErrNilExecutor, Is: Platform},
		"ErrNilProduct":        {Err: billing.ErrNilProduct, Is: Platform},
		"ErrNilPurchase":       {Err: billing.ErrNilPurchase, Is: Platform},
		"ErrNilSubscription":   {Err: billing.ErrNilSubscription, Is: Platform},
		"ErrNilTransaction":    {Err: billing.ErrNilTransaction, Is: Platform},

		// Wrap errors.ErrEmptyInputParameter, which the platform mappers already
		// answer as a bad request. A case of this package's own would be a
		// second copy of that decision, free to drift from it.
		"ErrEmptyAccount":         {Err: billing.ErrEmptyAccount, Is: Platform},
		"ErrEmptyBillingInterval": {Err: billing.ErrEmptyBillingInterval, Is: Platform},
		"ErrEmptyExternalID":      {Err: billing.ErrEmptyExternalID, Is: Platform},
		"ErrEmptyPeriod":          {Err: billing.ErrEmptyPeriod, Is: Platform},
		"ErrEmptyProduct":         {Err: billing.ErrEmptyProduct, Is: Platform},
		"ErrEmptyProductName":     {Err: billing.ErrEmptyProductName, Is: Platform},
	},
	dataPrivacyPkg: {
		// A subject asking after their own export or erasure is a client. These five
		// are the answers they can act on: the ID is not one of theirs, the request
		// is not in the state the call needs, or the request they sent is malformed.
		"ErrArtifactUnavailable":     {Err: dataprivacy.ErrArtifactUnavailable, Is: Mapped},
		"ErrEmptySubjectID":          {Err: dataprivacy.ErrEmptySubjectID, Is: Mapped},
		"ErrNotAwaitingConfirmation": {Err: dataprivacy.ErrNotAwaitingConfirmation, Is: Mapped},
		"ErrRequestNotFound":         {Err: dataprivacy.ErrRequestNotFound, Is: Mapped},
		"ErrUnknownRequestType":      {Err: dataprivacy.ErrUnknownRequestType, Is: Mapped},

		// The nil-argument sentinels, which wrap errors.ErrNilInputParameter and are
		// answered by the platform mapper for that reason. They are wiring failures
		// and the 400 they resolve to is generous; the mapping predates this package
		// and is not this package's to change.
		"ErrNilDatabaseClient": {Err: dataprivacy.ErrNilDatabaseClient, Is: Platform},
		"ErrNilExecutor":       {Err: dataprivacy.ErrNilExecutor, Is: Platform},
		"ErrNilFetch":          {Err: dataprivacy.ErrNilFetch, Is: Platform},
		"ErrNilOperations":     {Err: dataprivacy.ErrNilOperations, Is: Platform},
		"ErrNilRequest":        {Err: dataprivacy.ErrNilRequest, Is: Platform},
		"ErrNilStore":          {Err: dataprivacy.ErrNilStore, Is: Platform},

		// Fulfillment-side outcomes and construction failures. A collector that
		// panicked, an export document too large to store, an upload manager that
		// cannot sign a URL, a registry with a duplicate key: none is a request a
		// subject could have sent differently, and a 500 is the honest answer.
		// ErrNilPage and ErrCursorStalled are a Store misbehaving toward its own
		// caller and never reach a handler at all.
		"ErrArtifactEncrypted":  {Err: dataprivacy.ErrArtifactEncrypted, Is: Unhandled},
		"ErrCollectorPanicked":  {Err: dataprivacy.ErrCollectorPanicked, Is: Unhandled},
		"ErrCursorStalled":      {Err: dataprivacy.ErrCursorStalled, Is: Unhandled},
		"ErrDocumentTooLarge":   {Err: dataprivacy.ErrDocumentTooLarge, Is: Unhandled},
		"ErrDuplicateKey":       {Err: dataprivacy.ErrDuplicateKey, Is: Unhandled},
		"ErrEraserPanicked":     {Err: dataprivacy.ErrEraserPanicked, Is: Unhandled},
		"ErrEverySectionFailed": {Err: dataprivacy.ErrEverySectionFailed, Is: Unhandled},
		"ErrInvalidFragment":    {Err: dataprivacy.ErrInvalidFragment, Is: Unhandled},
		"ErrInvalidKey":         {Err: dataprivacy.ErrInvalidKey, Is: Unhandled},
		"ErrNilPage":            {Err: dataprivacy.ErrNilPage, Is: Unhandled},
		"ErrNoCollectors":       {Err: dataprivacy.ErrNoCollectors, Is: Unhandled},
		"ErrNoErasers":          {Err: dataprivacy.ErrNoErasers, Is: Unhandled},
		"ErrNoURLSigner":        {Err: dataprivacy.ErrNoURLSigner, Is: Unhandled},
		"ErrNoUploadManager":    {Err: dataprivacy.ErrNoUploadManager, Is: Unhandled},
		"ErrNotInProgress":      {Err: dataprivacy.ErrNotInProgress, Is: Unhandled},
		"ErrUnexpiringArtifact": {Err: dataprivacy.ErrUnexpiringArtifact, Is: Unhandled},
		"ErrUnknownStatus":      {Err: dataprivacy.ErrUnknownStatus, Is: Unhandled},
	},
	linksPkg: {
		// The four redemption outcomes and the malformed token. These are the whole
		// of what a person holding a link can be told.
		"ErrInvalidToken":        {Err: links.ErrInvalidToken, Is: Mapped},
		"ErrLinkAlreadyRedeemed": {Err: links.ErrLinkAlreadyRedeemed, Is: Mapped},
		"ErrLinkExpired":         {Err: links.ErrLinkExpired, Is: Mapped},
		"ErrLinkNotFound":        {Err: links.ErrLinkNotFound, Is: Mapped},
		"ErrLinkRevoked":         {Err: links.ErrLinkRevoked, Is: Mapped},

		// Wraps errors.ErrNilInputParameter, and the platform mapper answers it.
		"ErrNilStore": {Err: links.ErrNilStore, Is: Platform},

		// Minter construction — an unregistered action, an unusable URL template, a
		// non-positive TTL — and the store reporting itself. ErrStoreUnavailable is
		// the one worth pausing on: redemption fails closed on it, and it is a 500
		// deliberately, because a link this package cannot prove is unused is not a
		// link the bearer should be told anything specific about.
		"ErrEmptySubject":      {Err: links.ErrEmptySubject, Is: Unhandled},
		"ErrInsecureActionURL": {Err: links.ErrInsecureActionURL, Is: Unhandled},
		"ErrInvalidActionURL":  {Err: links.ErrInvalidActionURL, Is: Unhandled},
		"ErrInvalidID":         {Err: links.ErrInvalidID, Is: Unhandled},
		"ErrInvalidTTL":        {Err: links.ErrInvalidTTL, Is: Unhandled},
		"ErrNoActions":         {Err: links.ErrNoActions, Is: Unhandled},
		"ErrStaleRecord":       {Err: links.ErrStaleRecord, Is: Unhandled},
		"ErrStoreUnavailable":  {Err: links.ErrStoreUnavailable, Is: Unhandled},
		"ErrUnknownAction":     {Err: links.ErrUnknownAction, Is: Unhandled},
	},
	operationsPkg: {
		// A missing operation, which is also what an operation belonging to somebody
		// else reads as, and a subscription refused for capacity.
		"ErrOperationNotFound": {Err: operations.ErrOperationNotFound, Is: Mapped},
		"ErrTooManyWatchers":   {Err: operations.ErrTooManyWatchers, Is: Mapped},

		// The nil-argument sentinels, which wrap errors.ErrNilInputParameter.
		"ErrNilConfig":         {Err: operations.ErrNilConfig, Is: Platform},
		"ErrNilDatabaseClient": {Err: operations.ErrNilDatabaseClient, Is: Platform},
		"ErrNilExecutor":       {Err: operations.ErrNilExecutor, Is: Platform},
		"ErrNilOperation":      {Err: operations.ErrNilOperation, Is: Platform},
		"ErrNilQueue":          {Err: operations.ErrNilQueue, Is: Platform},
		"ErrNilRegistry":       {Err: operations.ErrNilRegistry, Is: Platform},
		"ErrNilService":        {Err: operations.ErrNilService, Is: Platform},
		"ErrNilStore":          {Err: operations.ErrNilStore, Is: Platform},

		// Registry and worker outcomes: a kind registered twice, a runner that
		// panicked, a result too large to record, a watcher used after close. They
		// describe the service rather than the request, and a 500 is the honest
		// answer. ErrRequestTooLarge is the near miss — it is about something a
		// caller sent — but it is raised by the service enqueuing work rather than
		// by a handler decoding a request, and nothing today puts it on a response.
		"ErrDuplicateKind":       {Err: operations.ErrDuplicateKind, Is: Unhandled},
		"ErrDuplicateOperation":  {Err: operations.ErrDuplicateOperation, Is: Unhandled},
		"ErrInvalidDefinition":   {Err: operations.ErrInvalidDefinition, Is: Unhandled},
		"ErrRequestTooLarge":     {Err: operations.ErrRequestTooLarge, Is: Unhandled},
		"ErrRequestTypeMismatch": {Err: operations.ErrRequestTypeMismatch, Is: Unhandled},
		"ErrResultTooLarge":      {Err: operations.ErrResultTooLarge, Is: Unhandled},
		"ErrRunnerPanicked":      {Err: operations.ErrRunnerPanicked, Is: Unhandled},
		"ErrUnknownKind":         {Err: operations.ErrUnknownKind, Is: Unhandled},
		"ErrWatcherClosed":       {Err: operations.ErrWatcherClosed, Is: Unhandled},
	},
	identityPkg: {
		// The four absences and the two collisions: what a client sent that
		// names nothing, and what a client sent that names something already
		// taken. These are the answers a registration form and a directory UI
		// act on, and the reason this package acquired mappers at all.
		"ErrAccountNotFound":    {Err: identity.ErrAccountNotFound, Is: Mapped},
		"ErrEmailAddressTaken":  {Err: identity.ErrEmailAddressTaken, Is: Mapped},
		"ErrInvitationNotFound": {Err: identity.ErrInvitationNotFound, Is: Mapped},
		"ErrMembershipNotFound": {Err: identity.ErrMembershipNotFound, Is: Mapped},
		"ErrUserNotFound":       {Err: identity.ErrUserNotFound, Is: Mapped},
		"ErrUsernameTaken":      {Err: identity.ErrUsernameTaken, Is: Mapped},

		// The three states an act is refused from rather than forbidden. Each is
		// fixable by the caller in a specific order, and a 500 would tell them to
		// do nothing. ErrInvitationExpired is the one row in this file whose two
		// transports differ on purpose — see identity's own mappers.
		"ErrInvitationExpired": {Err: identity.ErrInvitationExpired, Is: Mapped},
		"ErrLastAccountOwner":  {Err: identity.ErrLastAccountOwner, Is: Mapped},
		"ErrNoDefaultAccount":  {Err: identity.ErrNoDefaultAccount, Is: Mapped},

		// A write whose entity names a different tenant than the call did. The
		// two halves of the request disagreed, which is a bad request rather than
		// a refusal on authority.
		"ErrScopeMismatch": {Err: identity.ErrScopeMismatch, Is: Mapped},

		// Wrap errors.ErrNilInputParameter, so the platform mappers answer them.
		// They are wiring failures rather than anything a client sent.
		"ErrNilAccount":        {Err: identity.ErrNilAccount, Is: Platform},
		"ErrNilAccountUpdate":  {Err: identity.ErrNilAccountUpdate, Is: Platform},
		"ErrNilDatabaseClient": {Err: identity.ErrNilDatabaseClient, Is: Platform},
		"ErrNilExecutor":       {Err: identity.ErrNilExecutor, Is: Platform},
		"ErrNilInvitation":     {Err: identity.ErrNilInvitation, Is: Platform},
		"ErrNilMembership":     {Err: identity.ErrNilMembership, Is: Platform},
		"ErrNilProfileUpdate":  {Err: identity.ErrNilProfileUpdate, Is: Platform},
		"ErrNilStore":          {Err: identity.ErrNilStore, Is: Platform},
		"ErrNilUser":           {Err: identity.ErrNilUser, Is: Platform},

		// Wrap errors.ErrUnrecognizedInputValue, which the platform mappers
		// already answer as a bad request. A case of this package's own would be
		// a second copy of that decision, free to drift from it.
		"ErrInvalidEmailAddress":     {Err: identity.ErrInvalidEmailAddress, Is: Platform},
		"ErrInvalidInvitationStatus": {Err: identity.ErrInvalidInvitationStatus, Is: Platform},
		"ErrInvalidTimeZone":         {Err: identity.ErrInvalidTimeZone, Is: Platform},
	},

	sessionsPkg: {
		// Every unusable session. The two timeouts wrap ErrExpired, which wraps
		// ErrNotFound, so all four resolve; they are listed because a sentinel that
		// stops wrapping is exactly the kind of change this roster is here to catch.
		"ErrAbsoluteTimeout": {Err: sessions.ErrAbsoluteTimeout, Is: Mapped},
		"ErrExpired":         {Err: sessions.ErrExpired, Is: Mapped},
		"ErrIdleTimeout":     {Err: sessions.ErrIdleTimeout, Is: Mapped},
		"ErrNotFound":        {Err: sessions.ErrNotFound, Is: Mapped},

		// Wrap errors.ErrEmptyInputParameter and errors.ErrNilInputParameter.
		"ErrIDRequired":        {Err: sessions.ErrIDRequired, Is: Platform},
		"ErrNilBackend":        {Err: sessions.ErrNilBackend, Is: Platform},
		"ErrPrincipalRequired": {Err: sessions.ErrPrincipalRequired, Is: Platform},

		// A backend handed an identifier it did not mint, a backend that keeps no
		// principal index, and the three Policy validation failures. None is
		// something a client sent.
		"ErrIDConflict":              {Err: sessions.ErrIDConflict, Is: Unhandled},
		"ErrNegativeTouchInterval":   {Err: sessions.ErrNegativeTouchInterval, Is: Unhandled},
		"ErrNoPrincipalIndex":        {Err: sessions.ErrNoPrincipalIndex, Is: Unhandled},
		"ErrNoTimeout":               {Err: sessions.ErrNoTimeout, Is: Unhandled},
		"ErrTouchExceedsIdleTimeout": {Err: sessions.ErrTouchExceedsIdleTimeout, Is: Unhandled},
	},

	oauth2ClientsPkg: {
		// The read's one answer. Absent, archived and in another registry are
		// deliberately the same 404, which is what keeps the read from being an
		// enumeration oracle over other tenants' registrations.
		"ErrClientNotFound": {Err: oauth2clients.ErrClientNotFound, Is: Mapped},

		// The one refusal that means "try again". The identifier was minted
		// from crypto/rand and the caller never chose it, so a 409 naming the
		// remedy is the only useful thing to say.
		"ErrClientIDTaken": {Err: oauth2clients.ErrClientIDTaken, Is: Mapped},

		// The four a caller can correct, each naming its field. ErrScopeMismatch
		// is among them because a registration whose own scope disagrees with
		// the call's is refused rather than corrected, and the caller is the one
		// holding both halves.
		"ErrEmptyName":          {Err: oauth2clients.ErrEmptyName, Is: Mapped},
		"ErrInvalidRedirectURI": {Err: oauth2clients.ErrInvalidRedirectURI, Is: Mapped},
		"ErrNoRedirectURIs":     {Err: oauth2clients.ErrNoRedirectURIs, Is: Mapped},
		"ErrScopeMismatch":      {Err: oauth2clients.ErrScopeMismatch, Is: Mapped},

		// The two refusals on authority a person meets in a browser, both
		// PermissionDenied. The caller is who they say they are; the
		// registration is not theirs to authorize through. Both are also
		// ClientSafeSentinels, because their remedies differ and the code cannot
		// say which applies.
		"ErrClientOwnerMismatch": {Err: oauth2clients.ErrClientOwnerMismatch, Is: Mapped},
		"ErrClientScopeMismatch": {Err: oauth2clients.ErrClientScopeMismatch, Is: Mapped},

		// The wiring failures. Each wraps a platform sentinel that errors/http
		// and errors/grpc already answer, so this package's mappers say nothing
		// about them.
		"ErrEmptyClientID":     {Err: oauth2clients.ErrEmptyClientID, Is: Platform},
		"ErrEmptyID":           {Err: oauth2clients.ErrEmptyID, Is: Platform},
		"ErrEmptyUserID":       {Err: oauth2clients.ErrEmptyUserID, Is: Platform},
		"ErrNilClient":         {Err: oauth2clients.ErrNilClient, Is: Platform},
		"ErrNilDatabaseClient": {Err: oauth2clients.ErrNilDatabaseClient, Is: Platform},
		"ErrNilExecutor":       {Err: oauth2clients.ErrNilExecutor, Is: Platform},
		"ErrNilInput":          {Err: oauth2clients.ErrNilInput, Is: Platform},
		"ErrNilService":        {Err: oauth2clients.ErrNilService, Is: Platform},
		"ErrNilStore":          {Err: oauth2clients.ErrNilStore, Is: Platform},
		"ErrNilTransaction":    {Err: oauth2clients.ErrNilTransaction, Is: Platform},

		// This process's own randomness failing. There is nothing the caller did
		// and nothing they can change, so a 500 is the honest answer and the
		// useful signal is in this process's logs.
		"ErrSecretGeneration": {Err: oauth2clients.ErrSecretGeneration, Is: Unhandled},
	},

	waitlistsPkg: {
		// The two absences. Archived and in another tenant's scope read the
		// same way, which is what keeps a keyed read from being an enumeration
		// oracle over other tenants' rows.
		"ErrListNotFound":   {Err: waitlists.ErrListNotFound, Is: Mapped},
		"ErrSignupNotFound": {Err: waitlists.ErrSignupNotFound, Is: Mapped},

		// The five refusals a person on a signup page meets, and the only rows
		// in this file whose reader has not signed in. Each is also a
		// ClientSafeSentinel: four of the five are FailedPrecondition, so the
		// code cannot say which of them applies and each has a different
		// remedy. ErrAlreadySignedUp is AlreadyExists rather than a fifth
		// FailedPrecondition, because its collision is on a unique key.
		"ErrAlreadySignedUp":  {Err: waitlists.ErrAlreadySignedUp, Is: Mapped},
		"ErrAlreadyWithdrawn": {Err: waitlists.ErrAlreadyWithdrawn, Is: Mapped},
		"ErrContactWithdrawn": {Err: waitlists.ErrContactWithdrawn, Is: Mapped},
		"ErrListClosed":       {Err: waitlists.ErrListClosed, Is: Mapped},
		"ErrWrongStatus":      {Err: waitlists.ErrWrongStatus, Is: Mapped},

		// A write whose list or signup names a different tenant than the call
		// did. The two halves of the request disagreed, which is a bad request
		// rather than a refusal on authority.
		"ErrScopeMismatch": {Err: waitlists.ErrScopeMismatch, Is: Mapped},

		// Wrap errors.ErrNilInputParameter, so the platform mappers answer
		// them. They are wiring failures rather than anything a client sent.
		"ErrNilDatabaseClient": {Err: waitlists.ErrNilDatabaseClient, Is: Platform},
		"ErrNilExecutor":       {Err: waitlists.ErrNilExecutor, Is: Platform},
		"ErrNilList":           {Err: waitlists.ErrNilList, Is: Platform},
		"ErrNilSignup":         {Err: waitlists.ErrNilSignup, Is: Platform},

		// Wrap errors.ErrEmptyInputParameter, which the platform mappers
		// already answer as a bad request. A case of this package's own would
		// be a second copy of that decision, free to drift from it — which is
		// the reading identity takes of its own missing-field sentinels.
		"ErrEmptyClosesAt":    {Err: waitlists.ErrEmptyClosesAt, Is: Platform},
		"ErrEmptyContact":     {Err: waitlists.ErrEmptyContact, Is: Platform},
		"ErrEmptyListName":    {Err: waitlists.ErrEmptyListName, Is: Platform},
		"ErrEmptySubjectID":   {Err: waitlists.ErrEmptySubjectID, Is: Platform},
		"ErrEmptySubjectType": {Err: waitlists.ErrEmptySubjectType, Is: Platform},
	},

	signInPkg: {
		// The two refusals a caller gets before they hold anything. Both are
		// Unauthenticated and both are 401, and they differ only in the message,
		// which is the one distinction a client needs and the only one that is
		// not an oracle.
		"ErrInvalidCredentials":   {Err: signin.ErrInvalidCredentials, Is: Mapped},
		"ErrSecondFactorRequired": {Err: signin.ErrSecondFactorRequired, Is: Mapped},

		// Proven, and refused anyway. The four PermissionDenials: two statuses
		// an operator set, and the two halves of the administrative door.
		"ErrAdminLoginDisabled": {Err: signin.ErrAdminLoginDisabled, Is: Mapped},
		"ErrNotAnAdministrator": {Err: signin.ErrNotAnAdministrator, Is: Mapped},
		"ErrUserBanned":         {Err: signin.ErrUserBanned, Is: Mapped},
		"ErrUserTerminated":     {Err: signin.ErrUserTerminated, Is: Mapped},

		// The three states an act is refused from rather than forbidden. Each is
		// fixable in a specific order, and these are the rows where the two
		// transports read differently on purpose — FailedPrecondition on one
		// side, a conflict with a specific message on the other.
		"ErrNoPasswordCredential":    {Err: signin.ErrNoPasswordCredential, Is: Mapped},
		"ErrSecondFactorNotEnrolled": {Err: signin.ErrSecondFactorNotEnrolled, Is: Mapped},
		"ErrUserUnverified":          {Err: signin.ErrUserUnverified, Is: Mapped},

		// Wrap errors.ErrNilInputParameter and errors.ErrEmptyInputParameter, so
		// the platform mappers answer them. Four are wiring failures and four are
		// a request that arrived incomplete.
		"ErrEmptyHandle":       {Err: signin.ErrEmptyHandle, Is: Platform},
		"ErrEmptyPassword":     {Err: signin.ErrEmptyPassword, Is: Platform},
		"ErrEmptyUserID":       {Err: signin.ErrEmptyUserID, Is: Platform},
		"ErrNilAuthenticator":  {Err: signin.ErrNilAuthenticator, Is: Platform},
		"ErrNilCredentials":    {Err: signin.ErrNilCredentials, Is: Platform},
		"ErrNilDatabaseClient": {Err: signin.ErrNilDatabaseClient, Is: Platform},
		"ErrNilDirectory":      {Err: signin.ErrNilDirectory, Is: Platform},
		"ErrNilPasswordUpdate": {Err: signin.ErrNilPasswordUpdate, Is: Platform},
		"ErrNilSecretRefresh":  {Err: signin.ErrNilSecretRefresh, Is: Platform},
		"ErrNilTokenIssuer":    {Err: signin.ErrNilTokenIssuer, Is: Platform},

		// Wraps errors.ErrUnrecognizedInputValue, which the platform mappers
		// already answer as a bad request.
		"ErrAmbiguousHandle": {Err: signin.ErrAmbiguousHandle, Is: Platform},

		// A consumer who never named the label an authenticator app shows. It is
		// wiring rather than anything a caller sent, so a 500 is the honest
		// answer and no mapper claims it.
		"ErrTOTPIssuerNotConfigured": {Err: signin.ErrTOTPIssuerNotConfigured, Is: Unhandled},
	},
	notificationsPkg: {
		// The two reads' one answer. Absent, archived, and belonging to somebody
		// else are deliberately the same 404 on both seams, which is what keeps a
		// read by id from telling a caller what other people have been told and
		// which handsets they hold.
		"ErrNotificationNotFound": {Err: notifications.ErrNotificationNotFound, Is: Mapped},
		"ErrDeviceNotFound":       {Err: notifications.ErrDeviceNotFound, Is: Mapped},

		// The four a caller can correct, each naming its field. Three of them are
		// reachable from notifications/grpc — a registration with no token, one
		// naming no platform, one naming a platform this module does not serve —
		// and ErrEmptyTopic is reachable from an HTTP handler a consumer writes
		// over CreateNotification, which is the write that has no RPC.
		"ErrEmptyPrincipal":  {Err: notifications.ErrEmptyPrincipal, Is: Mapped},
		"ErrEmptyTopic":      {Err: notifications.ErrEmptyTopic, Is: Mapped},
		"ErrEmptyToken":      {Err: notifications.ErrEmptyToken, Is: Mapped},
		"ErrUnknownPlatform": {Err: notifications.ErrUnknownPlatform, Is: Mapped},

		// A write whose entity names a different scope than the write does,
		// refused rather than corrected. It is the same row oauth2clients carries
		// and for the same reason: the caller holds both halves.
		"ErrScopeMismatch": {Err: notifications.ErrScopeMismatch, Is: Mapped},

		// The wiring failures. Each wraps a platform sentinel that errors/http
		// and errors/grpc already answer, so this package's mappers say nothing
		// about them.
		"ErrNilDatabaseClient": {Err: notifications.ErrNilDatabaseClient, Is: Platform},
		"ErrNilDevice":         {Err: notifications.ErrNilDevice, Is: Platform},
		"ErrNilExecutor":       {Err: notifications.ErrNilExecutor, Is: Platform},
		"ErrNilNotification":   {Err: notifications.ErrNilNotification, Is: Platform},
	},

	commentsPkg: {
		// The twelve a person writing or moderating a comment can act on. Nine
		// are something about the request they just sent — a target type outside
		// the consumer's catalog, a reply to a reply, a reply filed under a
		// different discussion than its parent, an empty body, a read of replies
		// that named no parent, a target missing one of its two halves, a comment
		// attributed to nobody, and a comment written into a scope it does not
		// name — and three are a row they named and cannot reach.
		//
		// The three not-found answers are three different absences and stay
		// separate on purpose: the comment they named, the comment they were
		// replying to, and the thing being discussed. A client shown the second
		// has a discussion that moved under them; one shown the third has a stale
		// list.
		//
		// ErrCommentNotFound is one answer for absent, archived and in another
		// tenant's scope, which is what keeps a read from being an enumeration
		// oracle over other tenants' discussions — and is why it is the one
		// not-found here that is not client-safe.
		"ErrCommentNotFound":   {Err: comments.ErrCommentNotFound, Is: Mapped},
		"ErrEmptyAuthor":       {Err: comments.ErrEmptyAuthor, Is: Mapped},
		"ErrEmptyBody":         {Err: comments.ErrEmptyBody, Is: Mapped},
		"ErrEmptyParent":       {Err: comments.ErrEmptyParent, Is: Mapped},
		"ErrEmptyTargetID":     {Err: comments.ErrEmptyTargetID, Is: Mapped},
		"ErrEmptyTargetType":   {Err: comments.ErrEmptyTargetType, Is: Mapped},
		"ErrNestedReply":       {Err: comments.ErrNestedReply, Is: Mapped},
		"ErrParentNotFound":    {Err: comments.ErrParentNotFound, Is: Mapped},
		"ErrScopeMismatch":     {Err: comments.ErrScopeMismatch, Is: Mapped},
		"ErrTargetMismatch":    {Err: comments.ErrTargetMismatch, Is: Mapped},
		"ErrTargetNotFound":    {Err: comments.ErrTargetNotFound, Is: Mapped},
		"ErrUnknownTargetType": {Err: comments.ErrUnknownTargetType, Is: Mapped},

		// The three that are somebody else's sentinel, answered by the platform
		// mappers because that is the tier those sentinels belong to. All three
		// wrap errors.ErrNilInputParameter, and all three are a nil argument
		// inside the process rather than anything a request can express: no
		// executor, no comment, no client. comments/grpc refuses a request whose
		// comment field was never set with a sentinel of its own instead, where
		// the answer is about that request rather than about the argument.
		"ErrNilComment":        {Err: comments.ErrNilComment, Is: Platform},
		"ErrNilDatabaseClient": {Err: comments.ErrNilDatabaseClient, Is: Platform},
		"ErrNilExecutor":       {Err: comments.ErrNilExecutor, Is: Platform},
	},

	webhooksPkg: {
		// The eight an operator managing endpoints can act on. Six are a field in
		// the request they just sent — a URL that is not https, a host that is not
		// reachable from the internet, a header this module reserves, an endpoint
		// subscribing to nothing, an event type outside the consumer's catalog, an
		// endpoint written into a scope it does not name — and two are the state
		// they are writing against: an identifier another tenant already holds, and
		// an endpoint disabled while somebody replays a delivery to it.
		//
		// The two not-found answers are one answer for absent, archived and in
		// another tenant's scope, which is what keeps a read from being an
		// enumeration oracle over somebody else's endpoints.
		"ErrDeliveryNotFound":       {Err: webhooks.ErrDeliveryNotFound, Is: Mapped},
		"ErrDisallowedEndpointHost": {Err: webhooks.ErrDisallowedEndpointHost, Is: Mapped},
		"ErrEndpointDisabled":       {Err: webhooks.ErrEndpointDisabled, Is: Mapped},
		"ErrEndpointOutOfScope":     {Err: webhooks.ErrEndpointOutOfScope, Is: Mapped},
		"ErrInvalidEndpointURL":     {Err: webhooks.ErrInvalidEndpointURL, Is: Mapped},
		"ErrNoEvents":               {Err: webhooks.ErrNoEvents, Is: Mapped},
		"ErrReservedHeader":         {Err: webhooks.ErrReservedHeader, Is: Mapped},
		"ErrScopeMismatch":          {Err: webhooks.ErrScopeMismatch, Is: Mapped},
		"ErrUnknownEventType":       {Err: webhooks.ErrUnknownEventType, Is: Mapped},
		"ErrUnknownSubscription":    {Err: webhooks.ErrUnknownSubscription, Is: Mapped},

		// The ones that are somebody else's sentinel, answered by the platform
		// mappers because that is the tier those sentinels belong to.
		//
		// Four wrap errors.ErrNilInputParameter and one wraps
		// errors.ErrEmptyInputParameter. ErrNoScope is tenancy's own and wraps the
		// empty-parameter sentinel too, which is why a scopeless call resolves the
		// same way whether it was caught at registration, at dispatch, or by the
		// driver. ErrCircuitOpen wraps circuitbreaking.ErrCircuitBroken and gets
		// that sentinel's 503 and Unavailable — a webhooks case would be this
		// package deciding what a primitive's sentinel means everywhere in the
		// process.
		"ErrCircuitOpen":       {Err: webhooks.ErrCircuitOpen, Is: Platform},
		"ErrEmptyEventType":    {Err: webhooks.ErrEmptyEventType, Is: Platform},
		"ErrNilDatabaseClient": {Err: webhooks.ErrNilDatabaseClient, Is: Platform},
		"ErrNilDelivery":       {Err: webhooks.ErrNilDelivery, Is: Platform},
		"ErrNilEndpoint":       {Err: webhooks.ErrNilEndpoint, Is: Platform},
		"ErrNilExecutor":       {Err: webhooks.ErrNilExecutor, Is: Platform},
		"ErrNilStore":          {Err: webhooks.ErrNilStore, Is: Platform},
		"ErrNoScope":           {Err: webhooks.ErrNoScope, Is: Platform},

		// The three nobody answers. ErrLeaseTooShort is a worker configured with a
		// lease that does not outlast its own request timeout, which is a process
		// that should not have started. ErrNonSuccessStatus is a subscriber
		// answering 4xx or 5xx, which is the delivery worker's own business and
		// reaches no client of this module at all.
		//
		// ErrNoSigningSecret is the one that looks mappable. It is
		// requestsigning.ErrNoSigningKey rather than a sentinel of this package's,
		// deliberately, so that an endpoint refused at registration and a delivery
		// that failed to sign report the same condition — and a case for it here
		// would install that answer for every other caller of requestsigning in the
		// process, where a keyring with no key is a wiring failure and a 500 is
		// honest. webhooks/grpc refuses a keyless save at the request instead, with
		// codes.InvalidArgument as that one call site's default.
		"ErrLeaseTooShort":    {Err: webhooks.ErrLeaseTooShort, Is: Unhandled},
		"ErrNoSigningSecret":  {Err: webhooks.ErrNoSigningSecret, Is: Unhandled},
		"ErrNonSuccessStatus": {Err: webhooks.ErrNonSuccessStatus, Is: Unhandled},
	},

	issueReportsPkg: {
		// The eight a caller working a report queue can act on. Four are the
		// lifecycle's: a report that is not in the scope that asked, a guard that
		// matched nothing because somebody else moved the row first, a move the
		// lifecycle does not admit, and a status this package does not serve. Three
		// are the fields a report is unreachable without — filed by nobody, under
		// no category, saying nothing — and the eighth is a write whose entity
		// names a different tenant than the call did.
		//
		// ErrReportNotFound is one answer for absent, archived and in another
		// tenant's scope, which is what keeps a read from being an oracle for what
		// other tenants have been told.
		"ErrEmptyDetails":            {Err: issuereports.ErrEmptyDetails, Is: Mapped},
		"ErrEmptyKind":               {Err: issuereports.ErrEmptyKind, Is: Mapped},
		"ErrEmptyReporter":           {Err: issuereports.ErrEmptyReporter, Is: Mapped},
		"ErrInvalidStatusTransition": {Err: issuereports.ErrInvalidStatusTransition, Is: Mapped},
		"ErrReportNotFound":          {Err: issuereports.ErrReportNotFound, Is: Mapped},
		"ErrScopeMismatch":           {Err: issuereports.ErrScopeMismatch, Is: Mapped},
		"ErrStatusConflict":          {Err: issuereports.ErrStatusConflict, Is: Mapped},
		"ErrUnknownStatus":           {Err: issuereports.ErrUnknownStatus, Is: Mapped},

		// The three nil-argument sentinels, which wrap errors.ErrNilInputParameter
		// and are answered by the platform mapper for that reason. Two of them
		// cannot reach a client through this module's own surface at all: every
		// write there is handed a transaction the handler opened, and every read an
		// executor it holds.
		"ErrNilDatabaseClient": {Err: issuereports.ErrNilDatabaseClient, Is: Platform},
		"ErrNilExecutor":       {Err: issuereports.ErrNilExecutor, Is: Platform},
		"ErrNilReport":         {Err: issuereports.ErrNilReport, Is: Platform},
	},

	settingsPkg: {
		// The seven a caller can act on. Three are absences and are all
		// codes.NotFound — no setting by that name, no value stored against it,
		// and a resolution with neither a value nor a default — which is the
		// clearest case in this roster for why the wording matters as much as the
		// code, and why all three are client-safe. Two are a request to correct.
		// Two are state the caller is writing against: a name already defined, and
		// an edit some stored value no longer satisfies.
		//
		// ErrSettingUnset is mapped despite settings/grpc never returning it: a
		// resolution carries the unset state in its source rather than as a
		// refusal, and the sentinel is what a consumer's own handler gets from
		// Resolution.Int. A mapping that covered only the RPCs this module ships
		// would make the answer depend on which transport asked.
		"ErrDefinitionNameTaken":       {Err: settings.ErrDefinitionNameTaken, Is: Mapped},
		"ErrDefinitionNotFound":        {Err: settings.ErrDefinitionNotFound, Is: Mapped},
		"ErrDuplicateEnumerationValue": {Err: settings.ErrDuplicateEnumerationValue, Is: Mapped},
		"ErrKindMismatch":              {Err: settings.ErrKindMismatch, Is: Mapped},
		"ErrSettingUnset":              {Err: settings.ErrSettingUnset, Is: Mapped},
		"ErrStrandedValues":            {Err: settings.ErrStrandedValues, Is: Mapped},
		"ErrValueNotFound":             {Err: settings.ErrValueNotFound, Is: Mapped},

		// The ten that are somebody else's sentinel, answered by the platform
		// mappers because that is the tier those sentinels belong to.
		//
		// The last three are the ones worth pausing on, because they are refusals
		// a client reads and are still not this package's to map. A value that is
		// not of its setting's kind, a value outside the enumeration, and a kind
		// nothing implements all wrap errors.ErrUnrecognizedInputValue, which
		// errors/http already answers as a bad request and errors/grpc as
		// InvalidArgument — and the platform mapper is asked first, so a case here
		// would be unreachable. Two of them are on
		// settings.ClientSafeSentinels anyway, which is the other half of the
		// question and a separate registry: what the status *says* is decided
		// there, and what code it carries here.
		"ErrEmptyDefinitionName":   {Err: settings.ErrEmptyDefinitionName, Is: Platform},
		"ErrEmptyEnumerationValue": {Err: settings.ErrEmptyEnumerationValue, Is: Platform},
		"ErrEmptySubjectID":        {Err: settings.ErrEmptySubjectID, Is: Platform},
		"ErrEmptySubjectType":      {Err: settings.ErrEmptySubjectType, Is: Platform},
		"ErrNilDatabaseClient":     {Err: settings.ErrNilDatabaseClient, Is: Platform},
		"ErrNilDefinition":         {Err: settings.ErrNilDefinition, Is: Platform},
		"ErrNilExecutor":           {Err: settings.ErrNilExecutor, Is: Platform},
		"ErrMalformedValue":        {Err: settings.ErrMalformedValue, Is: Platform},
		"ErrNotEnumerated":         {Err: settings.ErrNotEnumerated, Is: Platform},
		"ErrUnknownKind":           {Err: settings.ErrUnknownKind, Is: Platform},

		// The one nobody answers. A paged read that answered with the cursor it
		// was handed is a store misbehaving toward its own caller — it reaches a
		// handler only through a service that shipped broken, and a 500 is the
		// honest reply.
		"ErrCursorStalled": {Err: settings.ErrCursorStalled, Is: Unhandled},
	},
}

// Packages are the directories Matrix's rows are read out of, relative to the
// module root. They are the fifteen that export mappers of their own; a package
// that declares a pair later is added here, in Matrix and in Mappers together.
var Packages = []string{
	auditPkg, dataPrivacyPkg, identityPkg, linksPkg, operationsPkg,
	sessionsPkg, signInPkg, oauth2ClientsPkg, notificationsPkg, commentsPkg,
	webhooksPkg, billingPkg, issueReportsPkg, settingsPkg, waitlistsPkg,
}

// Mappers is the pair of mappers a package exports. The switch is the one place
// this package spells the fifteen out; everywhere else they are the strings in
// Packages.
func Mappers(pkg string) (httperrors.HTTPErrorMapper, grpcerrors.GRPCErrorMapper) {
	switch pkg {
	case auditPkg:
		return audit.HTTPMapper, audit.GRPCMapper
	case dataPrivacyPkg:
		return dataprivacy.HTTPMapper, dataprivacy.GRPCMapper
	case identityPkg:
		return identity.HTTPMapper, identity.GRPCMapper
	case linksPkg:
		return links.HTTPMapper, links.GRPCMapper
	case operationsPkg:
		return operations.HTTPMapper, operations.GRPCMapper
	case sessionsPkg:
		return sessions.HTTPMapper, sessions.GRPCMapper
	case signInPkg:
		return signin.HTTPMapper, signin.GRPCMapper
	case oauth2ClientsPkg:
		return oauth2clients.HTTPMapper, oauth2clients.GRPCMapper
	case notificationsPkg:
		return notifications.HTTPMapper, notifications.GRPCMapper
	case commentsPkg:
		return comments.HTTPMapper, comments.GRPCMapper
	case webhooksPkg:
		return webhooks.HTTPMapper, webhooks.GRPCMapper
	case billingPkg:
		return billing.HTTPMapper, billing.GRPCMapper
	case issueReportsPkg:
		return issuereports.HTTPMapper, issuereports.GRPCMapper
	case settingsPkg:
		return settings.HTTPMapper, settings.GRPCMapper
	case waitlistsPkg:
		return waitlists.HTTPMapper, waitlists.GRPCMapper
	default:
		panic("no mappers for " + pkg)
	}
}

// Resolution is what one sentinel resolves to on both transports, asked of the
// mapper that owns it rather than of a process-global registry.
type Resolution struct {
	Err      error
	Package  string
	Name     string
	HTTPMsg  string
	HTTPCode httperrors.ErrorCode
	GRPCCode codes.Code
}

// MappedResolutions is every sentinel this roster records as Mapped, together
// with the answer its own package's mappers give.
//
// It exists for the callers that register those mappers — errormappers.Register
// and, through it, service.Register. Each asserts that what its registration
// makes ToAPIError and MapToGRPC say matches this, so the two cannot answer one
// sentinel differently and neither can drift from the mapper that owns it. Both
// are separate test binaries and could not otherwise share an expectation.
//
// The order is the order of Packages and then of name, so a failure names the
// same row twice in a row rather than a different one each run.
func MappedResolutions() []Resolution {
	var out []Resolution

	for _, pkg := range Packages {
		httpMapper, grpcMapper := Mappers(pkg)

		names := make([]string, 0, len(Matrix[pkg]))
		for name, row := range Matrix[pkg] {
			if row.Is == Mapped {
				names = append(names, name)
			}
		}

		slices.Sort(names)

		for _, name := range names {
			row := Matrix[pkg][name]

			httpCode, httpMsg, _ := httpMapper.Map(row.Err)
			grpcCode, _ := grpcMapper.Map(row.Err)

			out = append(out, Resolution{
				Err:      row.Err,
				Package:  pkg,
				Name:     name,
				HTTPCode: httpCode,
				HTTPMsg:  httpMsg,
				GRPCCode: grpcCode,
			})
		}
	}

	return out
}

package sentinelmatrix

import (
	"slices"

	"github.com/primandproper/platform-go/v14/authentication/oauth2clients"
	"github.com/primandproper/platform-go/v14/authentication/signin"
	"github.com/primandproper/platform-go/v14/dataprivacy"
	"github.com/primandproper/platform-go/v14/identity"
	"github.com/primandproper/platform-go/v14/links"
	"github.com/primandproper/platform-go/v14/operations"
	"github.com/primandproper/platform-go/v14/sessions"
	"github.com/primandproper/platform-go/v14/settings"

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

// The packages that map their own sentinels, spelled once each; there are eight
// today. Each name is three things — a key in Matrix, an entry in Packages and a
// case in Mappers — and a package that declares a pair later is added in all
// three together.
//
// Each is a path relative to the module root rather than a package name,
// because that is what the roster's own test reads the rows out of.
const (
	dataPrivacyPkg   = "dataprivacy"
	identityPkg      = "identity"
	linksPkg         = "links"
	operationsPkg    = "operations"
	sessionsPkg      = "sessions"
	signInPkg        = "authentication/signin"
	oauth2ClientsPkg = "authentication/oauth2clients"
	settingsPkg      = "settings"
)

// Decision is one sentinel and what this module decided it means on the wire.
type Decision struct {
	Err error
	Is  Disposition
}

// Matrix is the decision made about every exported sentinel in the eight
// packages that map their own errors. Its keys are checked against those
// packages' source in both directions, so it is a roster that cannot quietly
// stop describing the tree.
//
//nolint:goconst // The keys are sentinel identifiers in four other packages, and three of those packages have an ErrNilStore. That they collide is a fact the roster records, not a constant this one should extract.
var Matrix = map[string]map[string]Decision{
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
// module root. They are the eight that export mappers of their own; a package
// that declares a pair later is added here, in Matrix and in Mappers together.
var Packages = []string{
	dataPrivacyPkg, identityPkg, linksPkg, operationsPkg, sessionsPkg, signInPkg,
	oauth2ClientsPkg, settingsPkg,
}

// Mappers is the pair of mappers a package exports. The switch is the one place
// this package spells the eight out; everywhere else they are the strings in
// Packages.
func Mappers(pkg string) (httperrors.HTTPErrorMapper, grpcerrors.GRPCErrorMapper) {
	switch pkg {
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
	case settingsPkg:
		return settings.HTTPMapper, settings.GRPCMapper
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
